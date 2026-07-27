// Default Go kernel runner (vanilla "go run") — the host side of Grimoire's
// NDJSON kernel protocol, backed by the real Go toolchain. Each block is compiled
// and run as a complete, self-contained program: there is NO shared state between
// blocks (unlike the yaegi kernel), which suits self-contained examples.
//
// Loop: read one request (an id line, then a base64 line of the block's code)
// from stdin, turn it into a full Go program (run as-is if it already declares a
// package, else auto-wrapped), write it to a temp file, `go run` it with stdout
// and stderr merged in write order, then emit the captured output and exit
// status as NDJSON events on stdout. The host reads those events until the
// terminal "exit".
package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "go kernel runner:", err)
		os.Exit(1)
	}
}

// run drives the read/compile-run/emit loop until stdin reaches EOF (the host
// closing the kernel) or an unrecoverable I/O error.
func run(stdin io.Reader, stdout io.Writer) error {
	r := bufio.NewScanner(stdin)
	r.Buffer(make([]byte, 0, 64*1024), 16<<20) // allow large code blocks.

	for {
		id, ok := readLine(r)
		if !ok {
			return r.Err() // nil at clean EOF.
		}
		b64, ok := readLine(r)
		if !ok {
			return r.Err()
		}
		var events []byte
		if code, err := base64.StdEncoding.DecodeString(b64); err != nil {
			events = errorEvent(id, "decoding block: "+err.Error())
		} else {
			events = runBlock(id, string(code))
		}
		if _, err := stdout.Write(events); err != nil {
			return err
		}
	}
}

// runBlock compiles and runs one block as a complete program, returning its
// output and exit events. A compile or run failure is reported as a non-zero
// exit with the toolchain's diagnostics in the output (same shape as a shell
// command printing to stderr and returning non-zero), not as a protocol "error"
// event, which is reserved for the runner itself failing.
func runBlock(id, code string) []byte {
	start := time.Now()
	out, exit, runnerErr := compileAndRun(wrapProgram(code))
	dur := int(time.Since(start).Milliseconds())

	if runnerErr != "" {
		return errorEvent(id, runnerErr)
	}
	return append(outputEvent(id, out), exitEvent(id, exit, dur)...)
}

// compileAndRun writes prog as a throwaway Go module in a temp dir, resolves any
// third-party imports with `go mod tidy`, then runs it with `go run .`, returning
// the merged output and the program's exit code. The throwaway module is what lets
// a block import third-party packages: `go mod tidy` resolves them through the
// host's module cache / GOPROXY (Grimoire never fetches them itself — the host
// owns the dependency surface). A dependency the host can't resolve (offline and
// uncached, or a typo'd import path) makes tidy fail, which surfaces as a non-zero
// exit with the toolchain's diagnostics in the output — the same shape as a
// compile error. A non-empty runnerErr means the runner itself failed (couldn't
// make a temp dir, the go tool isn't on PATH), distinct from the block failing.
func compileAndRun(prog string) (out string, exit int, runnerErr string) {
	dir, err := os.MkdirTemp("", "grimoire-go-run-")
	if err != nil {
		return "", 0, "creating temp dir: " + err.Error()
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintln(os.Stderr, "go kernel: cleaning temp dir:", err)
		}
	}()

	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(prog), 0o600); err != nil {
		return "", 0, "writing program: " + err.Error()
	}
	// A minimal module so `go run .` and `go mod tidy` operate in module mode,
	// which is what makes third-party imports resolvable.
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goModFile), 0o600); err != nil {
		return "", 0, "writing go.mod: " + err.Error()
	}

	// Resolve imports first. If the block uses only the standard library, tidy is
	// a quick no-op; if it imports third-party packages, tidy fetches/records them
	// (from the host's cache or GOPROXY). A tidy failure is the block failing to
	// build — report it as output + non-zero exit, not a runner error.
	if out, exit, runnerErr := goCommand(dir, "mod", "tidy"); runnerErr != "" || exit != 0 {
		return out, exit, runnerErr
	}
	return goCommand(dir, "run", ".")
}

// goModFile is the throwaway module's go.mod. The module name is arbitrary (the
// block isn't imported by anything); the Go version matches the runner's module.
const goModFile = "module grimoire-block\n\ngo 1.26\n"

// goCommand runs a `go` subcommand in dir with stdout and stderr merged in write
// order, returning the combined output and the command's exit code. A non-empty
// runnerErr means the go tool couldn't be launched at all (not on PATH).
func goCommand(dir string, args ...string) (out string, exit int, runnerErr string) {
	var buf bytes.Buffer
	cmd := exec.Command("go", args...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf // merge so output (and compile errors) stay chronological.
	cmd.Stdin = nil
	cmd.Dir = dir

	err := cmd.Run()
	if err == nil {
		return buf.String(), 0, ""
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return buf.String(), ee.ExitCode(), ""
	}
	return buf.String(), 0, "running go: " + err.Error()
}

// readLine returns the next line and whether one was read (false at EOF/error).
func readLine(r *bufio.Scanner) (string, bool) {
	if !r.Scan() {
		return "", false
	}
	return r.Text(), true
}

// event mirrors the host's kernel.Event; only the fields a runner emits are set.
type event struct {
	Id    string `json:"id"`
	Type  string `json:"type"`
	Data  string `json:"data,omitempty"`
	Code  int    `json:"code,omitempty"`
	DurMS int    `json:"dur_ms,omitempty"`
}

func outputEvent(id, data string) []byte {
	if data == "" {
		return nil
	}
	return marshalEvent(event{Id: id, Type: "output", Data: data})
}

func exitEvent(id string, code, durMS int) []byte {
	return marshalEvent(event{Id: id, Type: "exit", Code: code, DurMS: durMS})
}

func errorEvent(id, msg string) []byte {
	return marshalEvent(event{Id: id, Type: "error", Data: msg})
}

// marshalEvent renders one event as an NDJSON line (with trailing newline). The
// event is a fixed struct of basic types, so marshalling can't fail.
func marshalEvent(e event) []byte {
	line, err := json.Marshal(e)
	if err != nil {
		return nil
	}
	return append(line, '\n')
}
