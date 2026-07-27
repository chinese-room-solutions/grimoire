// Go kernel runner — the host side of Grimoire's NDJSON kernel protocol, backed
// by the yaegi Go interpreter so blocks share interpreter state (variables,
// funcs, imports) across runs like notebook cells.
//
// Loop: read one request (an id line, then a base64 line of the block's code)
// from stdin, evaluate it in a long-lived interpreter whose stdout and stderr
// are merged into one buffer in write order, then emit the captured output and
// an exit status as NDJSON events on stdout. The host reads those events until
// the terminal "exit".
//
// Output model matches the bash kernel: stdout and stderr are one chronological
// stream (no separate "error" colour — that's the exit footer's job). A failed
// Eval (compile or runtime error) is reported as a non-zero exit with the error
// text appended to the output, not as a protocol-level "error" event, which is
// reserved for the runner itself failing.
package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/traefik/yaegi/interp"
	"github.com/traefik/yaegi/stdlib"
)

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "go kernel runner:", err)
		os.Exit(1)
	}
}

// session is one kernel's live interpreter plus the state the protocol needs
// across blocks: the merged stdout/stderr buffer and the set of package paths
// already imported, so re-running an import (which yaegi rejects as a
// redeclaration) is a harmless no-op like a notebook cell.
type session struct {
	interp *interp.Interpreter
	out    *bytes.Buffer
	seen   map[string]bool // import paths already brought into the session.
}

// run drives the read/eval/emit loop until stdin reaches EOF (the host closing
// the kernel) or an unrecoverable I/O error.
func run(stdin io.Reader, stdout io.Writer) error {
	var out bytes.Buffer
	i := interp.New(interp.Options{
		Stdout:       &out,
		Stderr:       &out, // merge with stdout so output stays chronological.
		Unrestricted: true, // local single-user app: no sandbox, like the bash kernel.
	})
	if err := i.Use(stdlib.Symbols); err != nil {
		return fmt.Errorf("loading stdlib symbols: %w", err)
	}
	s := &session{interp: i, out: &out, seen: map[string]bool{}}

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
			events = runBlock(s, id, string(code))
		}
		if _, err := stdout.Write(events); err != nil {
			return err
		}
	}
}

// runBlock evaluates one block and returns its output and exit events as NDJSON
// lines. A failed Eval is reported as a non-zero exit with the error text
// appended to the output (same shape as a shell command printing to stderr and
// returning 1), not as a protocol "error" event, which is reserved for the
// runner failing.
func runBlock(s *session, id, code string) []byte {
	out := s.out
	out.Reset()
	start := time.Now()
	evalErr := s.evalBlock(code)
	dur := int(time.Since(start).Milliseconds())

	if evalErr != nil {
		if out.Len() > 0 && out.Bytes()[out.Len()-1] != '\n' {
			out.WriteByte('\n')
		}
		out.WriteString(evalErr.Error())
		out.WriteByte('\n')
	}

	code0 := 0
	if evalErr != nil {
		code0 = 1
	}
	return append(outputEvent(id, out.String()), exitEvent(id, code0, dur)...)
}

// evalBlock evaluates one block. The block is split into the chunks yaegi
// accepts per Eval (a single declaration, or a run of statements) and they run
// in order through the same interpreter so state is shared; the first failing
// chunk stops the block. A yaegi runtime panic is turned into an error so a bad
// block can't take the runner down (interpreter state survives for the next
// block).
func (s *session) evalBlock(code string) error {
	for _, chunk := range splitChunks(code) {
		if err := s.evalChunk(chunk); err != nil {
			return err
		}
	}
	return nil
}

// evalChunk evaluates one chunk, recovering a panic into an error. An import
// chunk is rewritten to drop packages already imported in this session, so
// re-running an import cell is a no-op rather than a "redeclared" error.
func (s *session) evalChunk(chunk string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	chunk, skip := s.dedupImports(chunk)
	if skip {
		return nil // every path was already imported; nothing to do.
	}
	_, err = s.interp.Eval(chunk)
	return err
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

// outputEvent renders an output event line, or nothing when there's no output.
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
