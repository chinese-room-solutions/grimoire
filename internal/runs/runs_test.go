package runs

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// openTemp opens a fresh run-result store in a temp dir.
func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "runs.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func textResult(out string, code int) Result {
	return Result{
		Items:    []Item{{MIME: MIMEText, Data: out}},
		ExitCode: code,
		DurMS:    12,
		Kernel:   "Go",
		RanAt:    time.Unix(1_700_000_000, 0),
	}
}

func TestSaveAndGet(t *testing.T) {
	s := openTemp(t)

	_, ok, err := s.Get("a.md", "h1")
	require.NoError(t, err)
	require.False(t, ok, "a never-run block has no result")

	want := textResult("hello\n", 0)
	require.NoError(t, s.Save("a.md", "h1", want))

	got, ok, err := s.Get("a.md", "h1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, want.Items, got.Items)
	require.Equal(t, want.ExitCode, got.ExitCode)
	require.Equal(t, want.DurMS, got.DurMS)
	require.Equal(t, want.Kernel, got.Kernel)
	require.Equal(t, want.RanAt.Unix(), got.RanAt.Unix())
}

func TestSaveIfAbsent(t *testing.T) {
	s := openTemp(t)

	// First run of a block: stored, reported saved.
	saved, err := s.SaveIfAbsent("a.md", "h1", textResult("first\n", 0))
	require.NoError(t, err)
	require.True(t, saved, "the first run of a block is saved automatically")

	// A later run of the same block: not stored, the saved result is preserved.
	saved, err = s.SaveIfAbsent("a.md", "h1", textResult("second\n", 1))
	require.NoError(t, err)
	require.False(t, saved, "a block with a saved result isn't auto-overwritten")

	got, ok, err := s.Get("a.md", "h1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "first\n", got.Items[0].Data, "the first run's output stands")
	require.Equal(t, 0, got.ExitCode)

	// An explicit Save does overwrite it.
	require.NoError(t, s.Save("a.md", "h1", textResult("third\n", 2)))
	got, _, err = s.Get("a.md", "h1")
	require.NoError(t, err)
	require.Equal(t, "third\n", got.Items[0].Data, "an explicit save overwrites the result")
}

func TestSaveOverwrites(t *testing.T) {
	s := openTemp(t)
	require.NoError(t, s.Save("a.md", "h1", textResult("first\n", 0)))
	require.NoError(t, s.Save("a.md", "h1", textResult("second\n", 1)))

	got, ok, err := s.Get("a.md", "h1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "second\n", got.Items[0].Data, "re-running a block replaces its result")
	require.Equal(t, 1, got.ExitCode)
}

func TestGetIsScopedByNoteAndHash(t *testing.T) {
	s := openTemp(t)
	require.NoError(t, s.Save("a.md", "h1", textResult("A\n", 0)))
	require.NoError(t, s.Save("b.md", "h1", textResult("B\n", 0)))

	got, ok, err := s.Get("a.md", "h1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "A\n", got.Items[0].Data, "same hash in different notes is a different result")

	// A different hash in the same note (an edited block) is a miss.
	_, ok, err = s.Get("a.md", "h2")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestPruneNote(t *testing.T) {
	tests := []struct {
		name    string
		saved   []string // hashes saved for the note
		keep    []string
		present []string // hashes that must remain
		gone    []string // hashes that must be pruned
	}{
		{
			name:    "drops orphans, keeps the rest",
			saved:   []string{"h1", "h2", "h3"},
			keep:    []string{"h1", "h3"},
			present: []string{"h1", "h3"},
			gone:    []string{"h2"},
		},
		{
			name:    "empty keep clears the note",
			saved:   []string{"h1", "h2"},
			keep:    nil,
			present: nil,
			gone:    []string{"h1", "h2"},
		},
		{
			name:    "keeping all is a no-op",
			saved:   []string{"h1", "h2"},
			keep:    []string{"h1", "h2"},
			present: []string{"h1", "h2"},
			gone:    nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := openTemp(t)
			for _, h := range tc.saved {
				require.NoError(t, s.Save("a.md", h, textResult(h, 0)))
			}
			// A second note must be untouched by pruning the first.
			require.NoError(t, s.Save("other.md", "x", textResult("x", 0)))

			require.NoError(t, s.PruneNote("a.md", tc.keep))

			for _, h := range tc.present {
				_, ok, err := s.Get("a.md", h)
				require.NoError(t, err)
				require.True(t, ok, "hash %s should remain", h)
			}
			for _, h := range tc.gone {
				_, ok, err := s.Get("a.md", h)
				require.NoError(t, err)
				require.False(t, ok, "hash %s should be pruned", h)
			}
			_, ok, err := s.Get("other.md", "x")
			require.NoError(t, err)
			require.True(t, ok, "another note's results are untouched")
		})
	}
}

func TestDelete(t *testing.T) {
	s := openTemp(t)
	require.NoError(t, s.Save("a.md", "h1", textResult("A1\n", 0)))
	require.NoError(t, s.Save("a.md", "h2", textResult("A2\n", 0)))

	require.NoError(t, s.Delete("a.md", "h1"))

	_, ok, err := s.Get("a.md", "h1")
	require.NoError(t, err)
	require.False(t, ok, "the deleted block's result is gone")

	_, ok, err = s.Get("a.md", "h2")
	require.NoError(t, err)
	require.True(t, ok, "a sibling block's result is untouched")

	// Deleting a block that has no result is a no-op, not an error.
	require.NoError(t, s.Delete("a.md", "missing"))
}

func TestDeleteNote(t *testing.T) {
	s := openTemp(t)
	require.NoError(t, s.Save("a.md", "h1", textResult("A\n", 0)))
	require.NoError(t, s.Save("a.md", "h2", textResult("A2\n", 0)))
	require.NoError(t, s.Save("b.md", "h1", textResult("B\n", 0)))

	require.NoError(t, s.DeleteNote("a.md"))

	for _, h := range []string{"h1", "h2"} {
		_, ok, err := s.Get("a.md", h)
		require.NoError(t, err)
		require.False(t, ok)
	}
	_, ok, err := s.Get("b.md", "h1")
	require.NoError(t, err)
	require.True(t, ok, "deleting one note leaves others")
}

func TestDeleteFolder(t *testing.T) {
	s := openTemp(t)
	require.NoError(t, s.Save("Projects/a.md", "h1", textResult("A\n", 0)))
	require.NoError(t, s.Save("Projects/Sub/b.md", "h1", textResult("B\n", 0)))
	require.NoError(t, s.Save("Projects2/c.md", "h1", textResult("C\n", 0)))

	require.NoError(t, s.DeleteFolder("Projects"))

	for _, p := range []string{"Projects/a.md", "Projects/Sub/b.md"} {
		_, ok, err := s.Get(p, "h1")
		require.NoError(t, err)
		require.False(t, ok, "results under the folder are gone")
	}
	_, ok, err := s.Get("Projects2/c.md", "h1")
	require.NoError(t, err)
	require.True(t, ok, "a sibling folder sharing the prefix is untouched")
}

func TestRenameNote(t *testing.T) {
	s := openTemp(t)
	require.NoError(t, s.Save("old.md", "h1", textResult("hi\n", 0)))

	require.NoError(t, s.RenameNote("old.md", "new.md"))

	_, ok, err := s.Get("old.md", "h1")
	require.NoError(t, err)
	require.False(t, ok, "the old path no longer has the result")

	got, ok, err := s.Get("new.md", "h1")
	require.NoError(t, err)
	require.True(t, ok, "the result follows the note to its new path")
	require.Equal(t, "hi\n", got.Items[0].Data)
}

func TestRenameFolder(t *testing.T) {
	tests := []struct {
		name        string
		saved       []string // note paths with a stored result
		from, to    string
		wantMoved   map[string]string // old path -> new path
		wantUnmoved []string
	}{
		{
			name:        "re-keys nested notes",
			saved:       []string{"Projects/a.md", "Projects/Sub/b.md"},
			from:        "Projects",
			to:          "Work",
			wantMoved:   map[string]string{"Projects/a.md": "Work/a.md", "Projects/Sub/b.md": "Work/Sub/b.md"},
			wantUnmoved: nil,
		},
		{
			name:        "leaves other folders alone",
			saved:       []string{"Projects/a.md", "Other/a.md", "top.md"},
			from:        "Projects",
			to:          "Work",
			wantMoved:   map[string]string{"Projects/a.md": "Work/a.md"},
			wantUnmoved: []string{"Other/a.md", "top.md"},
		},
		{
			name:        "a name-prefix sibling is not a child",
			saved:       []string{"a/x.md", "ab/x.md"},
			from:        "a",
			to:          "c",
			wantMoved:   map[string]string{"a/x.md": "c/x.md"},
			wantUnmoved: []string{"ab/x.md"},
		},
		{
			name:        "moves a folder under another",
			saved:       []string{"a/x.md"},
			from:        "a",
			to:          "nest/a",
			wantMoved:   map[string]string{"a/x.md": "nest/a/x.md"},
			wantUnmoved: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := openTemp(t)
			for _, p := range tc.saved {
				require.NoError(t, s.Save(p, "h1", textResult(p, 0)))
			}

			require.NoError(t, s.RenameFolder(tc.from, tc.to))

			for old, moved := range tc.wantMoved {
				_, ok, err := s.Get(old, "h1")
				require.NoError(t, err)
				require.False(t, ok, "%s no longer holds the result", old)
				got, ok, err := s.Get(moved, "h1")
				require.NoError(t, err)
				require.True(t, ok, "the result followed the note to %s", moved)
				require.Equal(t, old, got.Items[0].Data)
			}
			for _, p := range tc.wantUnmoved {
				_, ok, err := s.Get(p, "h1")
				require.NoError(t, err)
				require.True(t, ok, "%s is untouched", p)
			}
		})
	}
}

func TestNotePaths(t *testing.T) {
	s := openTemp(t)

	paths, err := s.NotePaths()
	require.NoError(t, err)
	require.Empty(t, paths, "an empty store has no note paths")

	require.NoError(t, s.Save("a.md", "h1", textResult("1\n", 0)))
	require.NoError(t, s.Save("a.md", "h2", textResult("2\n", 0)))
	require.NoError(t, s.Save("sub/b.md", "h1", textResult("3\n", 0)))

	paths, err = s.NotePaths()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"a.md", "sub/b.md"}, paths,
		"each note appears once regardless of how many blocks it stores")
}
