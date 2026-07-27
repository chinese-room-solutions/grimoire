package main

import "testing"

func newSession() *session {
	return &session{seen: map[string]bool{}}
}

func TestDedupImportsRewrites(t *testing.T) {
	tests := []struct {
		name     string
		chunk    string
		wantRW   string
		wantSkip bool
		wantPass bool // expect the chunk returned unchanged (not an import).
	}{
		{name: "single import", chunk: `import "fmt"`, wantRW: `import "fmt"`},
		{
			name:   "grouped import",
			chunk:  "import (\n\t\"fmt\"\n\t\"sort\"\n)",
			wantRW: "import (\n\t\"fmt\"\n\t\"sort\"\n)",
		},
		{name: "aliased import", chunk: `import m "math"`, wantRW: `import m "math"`},
		{name: "blank import", chunk: `import _ "embed"`, wantRW: `import _ "embed"`},
		{name: "not an import passes through", chunk: `x := 1`, wantPass: true},
		{name: "func decl passes through", chunk: `func f() {}`, wantPass: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newSession()
			rw, skip := s.dedupImports(tc.chunk)
			if tc.wantPass {
				if rw != tc.chunk || skip {
					t.Fatalf("non-import should pass through: got rw=%q skip=%v", rw, skip)
				}
				return
			}
			if skip != tc.wantSkip {
				t.Errorf("skip = %v, want %v", skip, tc.wantSkip)
			}
			if rw != tc.wantRW {
				t.Errorf("rewrite =\n%q\nwant\n%q", rw, tc.wantRW)
			}
		})
	}
}

func TestDedupImportsSkipsAlreadySeen(t *testing.T) {
	s := newSession()

	// First import is fresh.
	rw, skip := s.dedupImports(`import "fmt"`)
	if skip || rw != `import "fmt"` {
		t.Fatalf("first import: rw=%q skip=%v", rw, skip)
	}

	// Re-importing the same package is dropped entirely.
	_, skip = s.dedupImports(`import "fmt"`)
	if !skip {
		t.Errorf("re-import of fmt should skip")
	}

	// A group with one new and one seen path keeps only the new one.
	rw, skip = s.dedupImports("import (\n\t\"fmt\"\n\t\"sort\"\n)")
	if skip {
		t.Fatalf("group with a new path should not skip")
	}
	if rw != `import "sort"` {
		t.Errorf("group dedup =\n%q\nwant\n%q", rw, `import "sort"`)
	}
}

func TestDedupImportsAliasDistinctFromPlain(t *testing.T) {
	s := newSession()
	if _, skip := s.dedupImports(`import m "math"`); skip {
		t.Fatal("aliased import should be fresh")
	}
	// A later plain import of the same path is a different binding, not a dup.
	rw, skip := s.dedupImports(`import "math"`)
	if skip {
		t.Errorf("plain math after aliased math should not skip")
	}
	if rw != `import "math"` {
		t.Errorf("rewrite = %q, want %q", rw, `import "math"`)
	}
}
