package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chinese-room-solutions/grimoire/internal/runs"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// serviceWithRuns builds a Service backed by a fresh run-result store, enough to
// exercise the auto-save / pending / explicit-save flow.
func serviceWithRuns(t *testing.T) *Service {
	t.Helper()
	rr, err := runs.Open(filepath.Join(t.TempDir(), "runs.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = rr.Close() })
	return &Service{runs: rr, logger: zerolog.Nop()}
}

func sampleResult(out string) RunResult {
	return RunResult{
		Items:  []RunItem{{MIME: MIMEText, Data: out}},
		DurMS:  5,
		Kernel: "Go",
		RanAt:  time.Unix(1_700_000_000, 0),
	}
}

func TestBlockHashStableAndDistinct(t *testing.T) {
	require.Equal(t, BlockHash("x := 1\n"), BlockHash("x := 1\n"), "same source hashes the same")
	require.NotEqual(t, BlockHash("x := 1\n"), BlockHash("x := 2\n"), "different source hashes differently")
}

// The on-open re-hydration hashes the server's goldmark reconstruction (with the
// fence's trailing newline); the live-run save and the per-block save/delete hash
// the browser's <pre> text, which the JS strips of its trailing newline. They must
// land on the same key or saved output won't re-appear when a note is reopened.
func TestBlockHashIgnoresTrailingNewline(t *testing.T) {
	server := "fmt.Println(1)\n" // extractCodeBlocks keeps the trailing newline.
	browser := "fmt.Println(1)"  // JS does innerText.replace(/\n$/, "").
	require.Equal(t, BlockHash(server), BlockHash(browser),
		"server and browser forms of the same block must hash identically")
	// Interior whitespace still matters — a real edit invalidates the cache.
	require.NotEqual(t, BlockHash("fmt.Println(1)"), BlockHash("fmt.Println( 1 )"))
}

func TestAutoSaveThenPendingThenExplicitSave(t *testing.T) {
	s := serviceWithRuns(t)
	const note, code = "n.md", "fmt.Println(1)\n"

	// First run: auto-saved, no pending entry, re-hydrates with this output.
	saved := s.AutoSaveRunResult(note, code, sampleResult("1\n"))
	require.True(t, saved, "a block's first run is preserved automatically")
	got, ok := s.RunResultFor(note, code)
	require.True(t, ok)
	require.Equal(t, "1\n", got.Items[0].Data)

	// Second run: not auto-saved (a result exists); held as pending, the saved
	// result is unchanged.
	saved = s.AutoSaveRunResult(note, code, sampleResult("2\n"))
	require.False(t, saved, "a later run doesn't overwrite the saved result")
	got, _ = s.RunResultFor(note, code)
	require.Equal(t, "1\n", got.Items[0].Data, "the saved output still stands")

	// Explicit save commits the pending output, then there's nothing left pending.
	committed, err := s.SavePendingRun(note, code)
	require.NoError(t, err)
	require.True(t, committed)
	got, _ = s.RunResultFor(note, code)
	require.Equal(t, "2\n", got.Items[0].Data, "the explicit save commits the latest run")

	committed, err = s.SavePendingRun(note, code)
	require.NoError(t, err)
	require.False(t, committed, "nothing is pending after a save")
}

func TestDeleteRunResult(t *testing.T) {
	s := serviceWithRuns(t)
	const note, code = "n.md", "fmt.Println(1)\n"

	require.True(t, s.AutoSaveRunResult(note, code, sampleResult("1\n")))
	// A pending re-run exists too; delete must clear both saved and pending.
	require.False(t, s.AutoSaveRunResult(note, code, sampleResult("2\n")))

	require.NoError(t, s.DeleteRunResult(note, code))

	_, ok := s.RunResultFor(note, code)
	require.False(t, ok, "the saved result is gone")

	// Nothing pending remains: a save after delete does nothing.
	committed, err := s.SavePendingRun(note, code)
	require.NoError(t, err)
	require.False(t, committed)
}

func TestDeleteNoteRunResults(t *testing.T) {
	s := serviceWithRuns(t)
	const note = "n.md"
	codeA, codeB := "a()\n", "b()\n"
	require.True(t, s.AutoSaveRunResult(note, codeA, sampleResult("a\n")))
	require.True(t, s.AutoSaveRunResult(note, codeB, sampleResult("b\n")))
	require.True(t, s.AutoSaveRunResult("other.md", "c()\n", sampleResult("c\n")))

	require.NoError(t, s.DeleteNoteRunResults(note))

	_, ok := s.RunResultFor(note, codeA)
	require.False(t, ok)
	_, ok = s.RunResultFor(note, codeB)
	require.False(t, ok)
	_, ok = s.RunResultFor("other.md", "c()\n")
	require.True(t, ok, "another note's results survive")
}

func TestPruneRunResults(t *testing.T) {
	s := serviceWithRuns(t)
	const note = "n.md"

	// Two blocks, each with saved output. The block bodies are exactly what
	// extractCodeBlocks reconstructs from the fenced source below.
	codeA := "fmt.Println(\"a\")\n"
	codeB := "fmt.Println(\"b\")\n"
	require.True(t, s.AutoSaveRunResult(note, codeA, sampleResult("a\n")))
	require.True(t, s.AutoSaveRunResult(note, codeB, sampleResult("b\n")))

	// The note is edited: block B's code changed (so its stored output is stale),
	// block A is unchanged. Pruning against the new source must drop only B.
	newSource := "# N\n\n```go\n" + codeA + "```\n\n```go\nfmt.Println(\"B2\")\n```\n"
	s.pruneRunResults(note, newSource)

	_, ok := s.RunResultFor(note, codeA)
	require.True(t, ok, "an unchanged block keeps its saved output")
	_, ok = s.RunResultFor(note, codeB)
	require.False(t, ok, "an edited block's stale output is pruned")
}

func TestDiscardPendingRun(t *testing.T) {
	s := serviceWithRuns(t)
	const note, code = "n.md", "fmt.Println(1)\n"

	// Nothing pending yet — discard is a no-op.
	_, _, ok := s.DiscardPendingRun(note, code)
	require.False(t, ok, "nothing to discard before any run")

	// First run auto-saves; a re-run becomes pending.
	require.True(t, s.AutoSaveRunResult(note, code, sampleResult("1\n")))
	require.False(t, s.AutoSaveRunResult(note, code, sampleResult("2\n")))

	// Discard drops the pending re-run and reports the saved result to revert to.
	saved, hasSaved, ok := s.DiscardPendingRun(note, code)
	require.True(t, ok)
	require.True(t, hasSaved)
	require.Equal(t, "1\n", saved.Items[0].Data, "discard reverts to the saved output")

	// The saved result is untouched, and there's nothing left pending to save.
	got, _ := s.RunResultFor(note, code)
	require.Equal(t, "1\n", got.Items[0].Data)
	committed, err := s.SavePendingRun(note, code)
	require.NoError(t, err)
	require.False(t, committed, "the pending run is gone after discard")
}

func TestDiscardPendingRunWithoutSaved(t *testing.T) {
	// A pending run can exist with nothing saved behind it only by setting it
	// directly (the normal path auto-saves a first run). Discarding it then
	// reports hasSaved=false so the caller collapses the panel.
	s := serviceWithRuns(t)
	const note, code = "n.md", "x()\n"
	s.setPendingRun(note, code, sampleResult("pending\n"))

	saved, hasSaved, ok := s.DiscardPendingRun(note, code)
	require.True(t, ok)
	require.False(t, hasSaved, "no prior saved result to revert to")
	require.Empty(t, saved.Items)
	_, present := s.RunResultFor(note, code)
	require.False(t, present)
}

func TestDiscardAllPendingRuns(t *testing.T) {
	s := serviceWithRuns(t)
	const note = "n.md"
	codeA, codeB := "a()\n", "b()\n"

	// Both blocks saved, then re-run so each has a pending result.
	require.True(t, s.AutoSaveRunResult(note, codeA, sampleResult("a1\n")))
	require.True(t, s.AutoSaveRunResult(note, codeB, sampleResult("b1\n")))
	require.False(t, s.AutoSaveRunResult(note, codeA, sampleResult("a2\n")))
	require.False(t, s.AutoSaveRunResult(note, codeB, sampleResult("b2\n")))

	// A second note's pending run must not be swept up by this note's discard-all.
	require.True(t, s.AutoSaveRunResult("other.md", "c()\n", sampleResult("c1\n")))
	require.False(t, s.AutoSaveRunResult("other.md", "c()\n", sampleResult("c2\n")))

	n := s.DiscardAllPendingRuns(note)
	require.Equal(t, 2, n, "both pending blocks in the note are discarded")

	// Saved results are untouched (still the first run), and nothing is pending.
	gotA, _ := s.RunResultFor(note, codeA)
	gotB, _ := s.RunResultFor(note, codeB)
	require.Equal(t, "a1\n", gotA.Items[0].Data, "discard-all keeps the saved output")
	require.Equal(t, "b1\n", gotB.Items[0].Data)
	committed, err := s.SavePendingRun(note, codeA)
	require.NoError(t, err)
	require.False(t, committed, "nothing pending after discard-all")

	// The other note's pending run survives.
	_, _, ok := s.DiscardPendingRun("other.md", "c()\n")
	require.True(t, ok, "discard-all is scoped to its note")
}

func TestSaveAllPendingRuns(t *testing.T) {
	s := serviceWithRuns(t)
	const note = "n.md"
	codeA, codeB := "a()\n", "b()\n"

	// Seed both blocks with a saved result, then re-run both so each has pending.
	require.True(t, s.AutoSaveRunResult(note, codeA, sampleResult("a1\n")))
	require.True(t, s.AutoSaveRunResult(note, codeB, sampleResult("b1\n")))
	require.False(t, s.AutoSaveRunResult(note, codeA, sampleResult("a2\n")))
	require.False(t, s.AutoSaveRunResult(note, codeB, sampleResult("b2\n")))

	// A second note's pending run must not be swept up by this note's save-all.
	require.True(t, s.AutoSaveRunResult("other.md", "c()\n", sampleResult("c1\n")))
	require.False(t, s.AutoSaveRunResult("other.md", "c()\n", sampleResult("c2\n")))

	n := s.SaveAllPendingRuns(note)
	require.Equal(t, 2, n, "both pending blocks in the note are saved")

	gotA, _ := s.RunResultFor(note, codeA)
	gotB, _ := s.RunResultFor(note, codeB)
	require.Equal(t, "a2\n", gotA.Items[0].Data)
	require.Equal(t, "b2\n", gotB.Items[0].Data)

	// The other note's run is still pending (unsaved).
	other, _ := s.RunResultFor("other.md", "c()\n")
	require.Equal(t, "c1\n", other.Items[0].Data, "save-all is scoped to its note")
}

// TestDropRunsForDeleted covers the watcher's prune path for external deletes:
// a note removed outside Grimoire loses its saved results and pending runs; a
// note still on disk keeps both.
func TestDropRunsForDeleted(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(vault, "kept.md"), []byte("# kept"), 0o644))

	s := serviceWithRuns(t)
	s.cfg.Vault = vault

	const code = "echo hi\n"
	require.NoError(t, s.SaveRunResult("kept.md", code, sampleResult("hi\n")))
	require.NoError(t, s.SaveRunResult("gone.md", code, sampleResult("bye\n")))
	s.setPendingRun("gone.md", code, sampleResult("pending\n"))

	s.dropRunsForDeleted("kept.md")
	s.dropRunsForDeleted("gone.md")

	_, ok := s.RunResultFor("kept.md", code)
	require.True(t, ok, "an existing note keeps its results")
	_, ok = s.RunResultFor("gone.md", code)
	require.False(t, ok, "a deleted note's saved results are dropped")
	_, ok = s.takePendingRun("gone.md", code)
	require.False(t, ok, "a deleted note's pending runs are dropped")
}

// TestSweepOrphanRunResults covers the startup sweep: results whose note vanished
// while Grimoire wasn't running are removed; results for notes still in the vault
// survive, including in subfolders.
func TestSweepOrphanRunResults(t *testing.T) {
	vault := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(vault, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(vault, "kept.md"), []byte("# kept"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(vault, "sub", "nested.md"), []byte("# nested"), 0o644))

	s := serviceWithRuns(t)
	s.cfg.Vault = vault

	const code = "echo hi\n"
	tests := []struct {
		note string
		kept bool
	}{
		{note: "kept.md", kept: true},
		{note: "sub/nested.md", kept: true},
		{note: "gone.md", kept: false},
		{note: "sub/gone.md", kept: false},
	}
	for _, tt := range tests {
		require.NoError(t, s.SaveRunResult(tt.note, code, sampleResult("out\n")))
	}

	s.sweepOrphanRunResults()

	for _, tt := range tests {
		_, ok := s.RunResultFor(tt.note, code)
		require.Equal(t, tt.kept, ok, "note %s", tt.note)
	}
}
