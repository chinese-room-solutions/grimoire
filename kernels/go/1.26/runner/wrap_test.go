package main

import (
	"strings"
	"testing"
)

func TestWrapProgramPassesThroughFullProgram(t *testing.T) {
	full := "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(1) }\n"
	if got := wrapProgram(full); got != full {
		t.Errorf("a full program should pass through unchanged, got:\n%s", got)
	}
}

func TestWrapProgramWrapsSnippets(t *testing.T) {
	tests := []struct {
		name string
		code string
		// substrings the wrapped program must contain, in order of appearance.
		want []string
	}{
		{
			name: "import hoisted, statement in main",
			code: "import \"fmt\"\nfmt.Println(1)",
			want: []string{"package main", "import \"fmt\"", "func main() {", "fmt.Println(1)"},
		},
		{
			name: "func declaration hoisted above main",
			code: "import \"fmt\"\nfunc sq(n int) int { return n * n }\nfmt.Println(sq(3))",
			want: []string{"func sq(n int) int", "func main() {", "fmt.Println(sq(3))"},
		},
		{
			name: "bare statements only",
			code: "x := 2\nprintln(x)",
			want: []string{"package main", "func main() {", "x := 2", "println(x)"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := wrapProgram(tc.code)
			last := 0
			for _, w := range tc.want {
				idx := strings.Index(out[last:], w)
				if idx < 0 {
					t.Fatalf("wrapped program missing %q (in order). Full output:\n%s", w, out)
				}
				last += idx + len(w)
			}
			// A hoisted func declaration must sit OUTSIDE main, i.e. before it.
			if strings.Contains(tc.code, "func sq") {
				if strings.Index(out, "func sq") > strings.Index(out, "func main") {
					t.Errorf("func sq should be hoisted above main:\n%s", out)
				}
			}
		})
	}
}

func TestSplitTopLevel(t *testing.T) {
	decls, stmts := splitTopLevel("import \"fmt\"\nx := 1\nfunc f() {}\nfmt.Println(x)")
	// import and func are declarations; the two statements group separately
	// because the func declaration breaks the run.
	if len(decls) != 2 {
		t.Errorf("want 2 decls (import, func), got %d: %v", len(decls), decls)
	}
	if len(stmts) != 2 {
		t.Errorf("want 2 stmt groups, got %d: %v", len(stmts), stmts)
	}
}
