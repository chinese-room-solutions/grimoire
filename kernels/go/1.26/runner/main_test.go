//go:build integration

package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// drive feeds blocks through the runner and returns events grouped by id. It
// shells out to `go run`, so it's integration-tagged and skipped without a
// toolchain.
func drive(t *testing.T, blocks []string) map[string][]event {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}

	var in strings.Builder
	for i, code := range blocks {
		id := "r" + string(rune('1'+i))
		in.WriteString(id + "\n")
		in.WriteString(base64.StdEncoding.EncodeToString([]byte(code)) + "\n")
	}

	var out bytes.Buffer
	if err := run(strings.NewReader(in.String()), &out); err != nil {
		t.Fatalf("run: %v", err)
	}

	byID := map[string][]event{}
	sc := bufio.NewScanner(&out)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e event
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("bad event line %q: %v", line, err)
		}
		byID[e.Id] = append(byID[e.Id], e)
	}
	return byID
}

func outputOf(evs []event) string {
	var b strings.Builder
	for _, e := range evs {
		if e.Type == "output" {
			b.WriteString(e.Data)
		}
	}
	return b.String()
}

func exitOf(evs []event) int {
	for _, e := range evs {
		if e.Type == "exit" {
			return e.Code
		}
	}
	return -1
}

func TestRunFullAndWrapped(t *testing.T) {
	got := drive(t, []string{
		"package main\nimport \"fmt\"\nfunc main() { fmt.Println(\"full\") }",
		"import \"fmt\"\nfmt.Println(\"wrapped\")",
	})
	if o := outputOf(got["r1"]); o != "full\n" {
		t.Errorf("full program output = %q, want %q", o, "full\n")
	}
	if o := outputOf(got["r2"]); o != "wrapped\n" {
		t.Errorf("wrapped snippet output = %q, want %q", o, "wrapped\n")
	}
}

func TestRunNoSharedState(t *testing.T) {
	// A var defined in one block is NOT visible in the next — each block is its
	// own program. The second block must fail to compile.
	got := drive(t, []string{
		"x := 5\n_ = x",
		"import \"fmt\"\nfmt.Println(x)", // x is undefined here.
	})
	if exitOf(got["r2"]) == 0 {
		t.Errorf("second block should fail (no shared state), got exit 0: %q", outputOf(got["r2"]))
	}
}

func TestRunModernGoFeatures(t *testing.T) {
	// Unlike the yaegi kernel, the real toolchain supports the max builtin (1.21)
	// and per-iteration loop variables (1.22).
	got := drive(t, []string{
		"import \"fmt\"\nfmt.Println(max(3, 7))",
	})
	if o, c := outputOf(got["r1"]), exitOf(got["r1"]); o != "7\n" || c != 0 {
		t.Errorf("max builtin: output=%q exit=%d, want \"7\\n\"/0", o, c)
	}
}

func TestRunCompileError(t *testing.T) {
	got := drive(t, []string{"import \"fmt\"\nfmt.Println(nope)"})
	if exitOf(got["r1"]) == 0 {
		t.Error("compile error should be a non-zero exit")
	}
	if !strings.Contains(outputOf(got["r1"]), "undefined") {
		t.Errorf("missing compiler diagnostic: %q", outputOf(got["r1"]))
	}
}

func TestRunThirdPartyImport(t *testing.T) {
	// A block can import a third-party package; `go mod tidy` resolves it through
	// the host's module cache / GOPROXY. rsc.io/quote is the canonical tiny example
	// module. If the host is offline and it isn't cached, tidy fails — skip rather
	// than fail, since resolving deps is the host's responsibility, not the runner's.
	got := drive(t, []string{
		"package main\nimport (\n\t\"fmt\"\n\t\"rsc.io/quote\"\n)\nfunc main() { fmt.Println(quote.Hello()) }",
	})
	out, exit := outputOf(got["r1"]), exitOf(got["r1"])
	if exit != 0 {
		if isDepUnreachable(out) {
			t.Skipf("third-party module not resolvable offline: %q", out)
		}
		t.Fatalf("third-party import failed unexpectedly: exit=%d output=%q", exit, out)
	}
	if !strings.Contains(out, "Hello") {
		t.Errorf("expected greeting from rsc.io/quote, got %q", out)
	}
}

func TestRunUnresolvableImportFailsCleanly(t *testing.T) {
	// An import the host can't resolve must fail the block — a non-zero exit with
	// the toolchain's diagnostic in the output — not crash or hang the runner. The
	// import path is bogus, so `go mod tidy` fails fast without needing the network.
	// A follow-up stdlib block proves the runner keeps serving after a failure.
	got := drive(t, []string{
		"package main\nimport \"example.invalid/nope/v999\"\nfunc main() { _ = nope }",
		"import \"fmt\"\nfmt.Println(\"still alive\")",
	})

	out, exit := outputOf(got["r1"]), exitOf(got["r1"])
	if exit == 0 {
		t.Errorf("unresolvable import should be a non-zero exit, got 0: %q", out)
	}
	if exitOf(got["r1"]) == -1 {
		t.Error("a failed block must still emit a terminal exit event (no hang)")
	}
	if strings.TrimSpace(out) == "" {
		t.Error("a failed dependency resolution should surface a diagnostic in the output")
	}

	// The runner survived the failure and ran the next block.
	if o, c := outputOf(got["r2"]), exitOf(got["r2"]); o != "still alive\n" || c != 0 {
		t.Errorf("runner should keep serving after a failed block: output=%q exit=%d", o, c)
	}
}

func TestRunStdlibStillWorksWithTidyStep(t *testing.T) {
	// Adding the `go mod tidy` step (for third-party deps) must not break the common
	// stdlib-only case: tidy is a quick no-op when there are no external imports.
	got := drive(t, []string{
		"package main\nimport (\n\t\"fmt\"\n\t\"strings\"\n)\nfunc main() { fmt.Println(strings.ToUpper(\"ok\")) }",
	})
	if o, c := outputOf(got["r1"]), exitOf(got["r1"]); o != "OK\n" || c != 0 {
		t.Errorf("stdlib block with tidy step: output=%q exit=%d, want \"OK\\n\"/0", o, c)
	}
}

// isDepUnreachable reports whether tidy output indicates the host couldn't reach a
// module source (offline / proxy down), as opposed to the block being wrong.
func isDepUnreachable(out string) bool {
	for _, s := range []string{"dial tcp", "no such host", "cannot find module", "connection refused", "timeout"} {
		if strings.Contains(out, s) {
			return true
		}
	}
	return false
}
