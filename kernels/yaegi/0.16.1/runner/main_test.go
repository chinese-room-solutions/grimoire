package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// drive feeds a sequence of code blocks through the runner and returns the
// events grouped by request id, in arrival order. run reads to EOF and writes
// every event before returning, so a plain reader/buffer pair is enough — no
// pipes or goroutines needed.
func drive(t *testing.T, blocks []string) map[string][]event {
	t.Helper()

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

// outputOf concatenates the output-event data for one request.
func outputOf(evs []event) string {
	var b strings.Builder
	for _, e := range evs {
		if e.Type == "output" {
			b.WriteString(e.Data)
		}
	}
	return b.String()
}

// exitOf returns the exit code for one request, or -1 if it never exited.
func exitOf(evs []event) int {
	for _, e := range evs {
		if e.Type == "exit" {
			return e.Code
		}
	}
	return -1
}

func TestRunnerSharedState(t *testing.T) {
	got := drive(t, []string{
		`import "fmt"`,
		"x := 21\nfmt.Println(x)",
		"y := x * 2\nfmt.Println(y)", // sees x from the previous block.
	})

	check := func(id string, wantExit int, wantOut string) {
		t.Helper()
		evs := got[id]
		if c := exitOf(evs); c != wantExit {
			t.Errorf("%s: exit = %d, want %d", id, c, wantExit)
		}
		if o := outputOf(evs); o != wantOut {
			t.Errorf("%s: output = %q, want %q", id, o, wantOut)
		}
	}
	check("r1", 0, "")
	check("r2", 0, "21\n")
	check("r3", 0, "42\n")
}

func TestRunnerDeclThenStatement(t *testing.T) {
	// A block that defines a func and then calls it is one cell to the user;
	// yaegi rejects the mix, so the runner splits it. Same for a var + use.
	got := drive(t, []string{
		`import "fmt"`,
		"func double(n int) int { return n * 2 }\nfmt.Println(double(21))",
		"var base = 100\nfmt.Println(base + double(1))",
	})
	if got, want := outputOf(got["r2"]), "42\n"; got != want {
		t.Errorf("func+call output = %q, want %q", got, want)
	}
	if got, want := outputOf(got["r3"]), "102\n"; got != want {
		t.Errorf("var+use output = %q, want %q", got, want)
	}
}

func TestRunnerReimportIsIdempotent(t *testing.T) {
	// Re-running an import cell must not error (yaegi would otherwise reject the
	// duplicate as "redeclared"). The package stays usable after the re-run.
	got := drive(t, []string{
		`import "fmt"`,
		`import "fmt"`, // re-run of the same cell.
		`fmt.Println("ok")`,
	})
	if exitOf(got["r2"]) != 0 {
		t.Errorf("re-import should exit 0, got %d (%q)", exitOf(got["r2"]), outputOf(got["r2"]))
	}
	if got, want := outputOf(got["r3"]), "ok\n"; got != want {
		t.Errorf("package unusable after re-import: output = %q, want %q", got, want)
	}
}

func TestRunnerErrorsAndRecovery(t *testing.T) {
	got := drive(t, []string{
		`import "fmt"`,
		`fmt.Println(undefinedSymbol)`,     // compile error -> exit 1.
		"s := []int{1}\nfmt.Println(s[9])", // runtime panic -> recovered, exit 1.
		`fmt.Println("alive")`,             // interpreter survived both.
	})

	if exitOf(got["r2"]) != 1 {
		t.Errorf("compile error should exit 1, got %d", exitOf(got["r2"]))
	}
	if !strings.Contains(outputOf(got["r2"]), "undefined") {
		t.Errorf("compile error output missing diagnostic: %q", outputOf(got["r2"]))
	}
	if exitOf(got["r3"]) != 1 {
		t.Errorf("runtime panic should exit 1, got %d", exitOf(got["r3"]))
	}
	if got, want := outputOf(got["r4"]), "alive\n"; got != want {
		t.Errorf("post-error block output = %q, want %q (state should survive)", got, want)
	}
}

func TestRunnerBadBase64(t *testing.T) {
	// A malformed request body yields a runner-level error event, not a crash.
	var out bytes.Buffer
	if err := run(strings.NewReader("r1\n!!!not base64!!!\n"), &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), `"type":"error"`) {
		t.Errorf("expected an error event for bad base64, got: %s", out.String())
	}
}
