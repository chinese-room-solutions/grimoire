package index

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/chunk"
	"github.com/chinese-room-solutions/grimoire/internal/store"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// fakeEmbedder returns a fixed-dimension vector per text, counting calls so
// tests can assert incremental behavior (unchanged notes aren't re-embedded).
type fakeEmbedder struct {
	dim   int
	calls atomic.Int64 // texts embedded across all Embed calls; atomic since Sync embeds concurrently.
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		f.calls.Add(1)
		v := make([]float32, f.dim)
		v[0] = float32(len(texts[i])) // arbitrary, deterministic.
		out[i] = v
	}
	return out, nil
}

func writeNote(t *testing.T, vault, rel, body string) {
	t.Helper()
	p := filepath.Join(vault, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
}

// testDim is the embedding dimension used across indexer tests.
const testDim = 4

func newTestIndexer(t *testing.T) (*Indexer, string, *store.Store, *fakeEmbedder) {
	t.Helper()
	emb := &fakeEmbedder{dim: testDim}
	ix, vault, st := newTestIndexerWith(t, emb)
	return ix, vault, st, emb
}

// newTestIndexerWith is newTestIndexer over a caller-supplied embedder, for tests
// that need one that fails.
func newTestIndexerWith(t *testing.T, emb EmbedderInterface) (*Indexer, string, *store.Store) {
	t.Helper()
	vault := t.TempDir()
	st, err := store.Open(filepath.Join(t.TempDir(), "index.db"), testDim, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return New(vault, st, emb, zerolog.Nop()), vault, st
}

// poisonEmbedder fails any batch whose text contains poison and embeds everything
// else, so a test can fail chosen notes out of many.
type poisonEmbedder struct {
	dim    int
	poison string
}

func (p *poisonEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		if strings.Contains(text, p.poison) {
			return nil, errors.New("gateway rejected the batch")
		}
		v := make([]float32, p.dim)
		v[0] = float32(len(text))
		out[i] = v
	}
	return out, nil
}

// TestSync_Concurrent indexes many notes with concurrency on, checking every
// note lands and the stats (counted across parallel workers) are exact.
func TestSync_Concurrent(t *testing.T) {
	ix, vault, st, emb := newTestIndexer(t)
	ix.SetConcurrency(4)
	const n = 50
	for i := 0; i < n; i++ {
		writeNote(t, vault, fmt.Sprintf("n%02d.md", i), fmt.Sprintf("# N%d\nbody %d", i, i))
	}

	stats, err := ix.Sync(context.Background(), nil, false)
	require.NoError(t, err)
	require.Equal(t, n, stats.Indexed)
	require.Equal(t, n, stats.Chunks)
	require.EqualValues(t, n, emb.calls.Load())

	count, err := st.Count()
	require.NoError(t, err)
	require.Equal(t, n, count)
}

// Progress counts completed notes, so it runs from 1 to total and never reports
// 0 — the SSE handler prints it verbatim.
func TestSync_ProgressCountsCompletedNotes(t *testing.T) {
	ix, vault, _, _ := newTestIndexer(t)
	ix.SetConcurrency(4)
	const n = 10
	for i := range n {
		writeNote(t, vault, fmt.Sprintf("n%02d.md", i), fmt.Sprintf("# N%d", i))
	}

	var mu sync.Mutex
	var seen []int
	paths := map[string]struct{}{}
	_, err := ix.Sync(context.Background(), func(done, total int, path string) {
		require.Equal(t, n, total)
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, done)
		paths[path] = struct{}{}
	}, false)
	require.NoError(t, err)

	sort.Ints(seen)
	want := make([]int, n)
	for i := range want {
		want[i] = i + 1
	}
	require.Equal(t, want, seen, "every note reports once, counting 1..total")
	require.Len(t, paths, n, "each report names a distinct note")
}

func TestSync_IndexesNotes(t *testing.T) {
	ix, vault, st, _ := newTestIndexer(t)
	writeNote(t, vault, "a.md", "# A\nalpha")
	writeNote(t, vault, "sub/b.md", "# B\nbeta")

	stats, err := ix.Sync(context.Background(), nil, false)
	require.NoError(t, err)
	require.Equal(t, 2, stats.Indexed)
	require.Equal(t, 0, stats.Skipped)
	require.Equal(t, 2, stats.Chunks)

	n, err := st.Count()
	require.NoError(t, err)
	require.Equal(t, 2, n)

	// Stable, slash-separated keys regardless of OS.
	paths, err := st.Paths()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"a.md", "sub/b.md"}, paths)
}

func TestSyncNote(t *testing.T) {
	ix, vault, st, _ := newTestIndexer(t)
	writeNote(t, vault, "a.md", "# A\nalpha")

	// Index just the one note, no full walk.
	require.NoError(t, ix.SyncNote(context.Background(), "a.md", false))
	n, err := st.Count()
	require.NoError(t, err)
	require.Equal(t, 1, n)

	// Re-syncing after an edit replaces its chunks.
	writeNote(t, vault, "a.md", "# A\nalpha\n\n## More\nbeta gamma")
	require.NoError(t, ix.SyncNote(context.Background(), "a.md", false))
	paths, err := st.Paths()
	require.NoError(t, err)
	require.Equal(t, []string{"a.md"}, paths)

	// A note removed from disk is pruned from the store.
	require.NoError(t, os.Remove(filepath.Join(vault, "a.md")))
	require.NoError(t, ix.SyncNote(context.Background(), "a.md", false))
	n, err = st.Count()
	require.NoError(t, err)
	require.Equal(t, 0, n)
}

// TestSyncNote_SkipsUnchanged verifies SyncNote short-circuits by doc hash like
// Sync: re-syncing an unchanged note (an in-app save's fsnotify echo) must not
// call the embedder again.
func TestSyncNote_SkipsUnchanged(t *testing.T) {
	ix, vault, _, emb := newTestIndexer(t)
	writeNote(t, vault, "a.md", "# A\nalpha")

	require.NoError(t, ix.SyncNote(context.Background(), "a.md", false))
	first := emb.calls.Load()
	require.Positive(t, first)

	// Same content on disk: no re-embed.
	require.NoError(t, ix.SyncNote(context.Background(), "a.md", false))
	require.Equal(t, first, emb.calls.Load(), "unchanged note must not be re-embedded")

	// Changed content: embeds again.
	writeNote(t, vault, "a.md", "# A\nalpha changed")
	require.NoError(t, ix.SyncNote(context.Background(), "a.md", false))
	require.Greater(t, emb.calls.Load(), first)
}

// TestSyncNote_Force covers the one case the hash gate would otherwise block:
// re-embedding a byte-identical note, which is what a model or chunker change
// needs.
func TestSyncNote_Force(t *testing.T) {
	ix, vault, _, emb := newTestIndexer(t)
	writeNote(t, vault, "a.md", "# A\nalpha")

	require.NoError(t, ix.SyncNote(context.Background(), "a.md", false))
	first := emb.calls.Load()
	require.Positive(t, first)

	require.NoError(t, ix.SyncNote(context.Background(), "a.md", true))
	require.Greater(t, emb.calls.Load(), first, "force must re-embed an unchanged note")
}

// TestSyncNotes covers the targeted pass: only the named notes are touched, the
// stats total per note, and an unnamed note keeps whatever the store already
// held for it.
func TestSyncNotes(t *testing.T) {
	tests := []struct {
		name    string
		paths   []string
		force   bool
		indexed int
		skipped int
		pruned  int
	}{
		{name: "one changed note", paths: []string{"a.md"}, indexed: 1},
		{name: "unchanged note skips", paths: []string{"b.md"}, skipped: 1},
		{name: "forced unchanged note re-embeds", paths: []string{"b.md"}, force: true, indexed: 1},
		{name: "missing note is pruned", paths: []string{"gone.md"}, pruned: 1},
		{name: "mixed batch totals", paths: []string{"a.md", "b.md", "gone.md"}, indexed: 1, skipped: 1, pruned: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ix, vault, st, _ := newTestIndexer(t)
			writeNote(t, vault, "a.md", "# A\nalpha")
			writeNote(t, vault, "b.md", "# B\nbeta")
			writeNote(t, vault, "gone.md", "# Gone\ndelta")
			_, err := ix.Sync(context.Background(), nil, false)
			require.NoError(t, err)

			// a.md changes on disk, gone.md leaves it; b.md stays as indexed.
			writeNote(t, vault, "a.md", "# A\nalpha and more")
			require.NoError(t, os.Remove(filepath.Join(vault, "gone.md")))

			stats, err := ix.SyncNotes(context.Background(), tt.paths, tt.force)
			require.NoError(t, err)
			require.Equal(t, tt.indexed, stats.Indexed)
			require.Equal(t, tt.skipped, stats.Skipped)
			require.Equal(t, tt.pruned, stats.Pruned)

			// Notes outside the batch keep their chunks — a targeted pass prunes
			// only what it was pointed at.
			paths, err := st.Paths()
			require.NoError(t, err)
			require.Contains(t, paths, "b.md")
		})
	}
}

// TestSyncNotes_PartialFailure pins Sync's contract for the targeted pass: one
// note's failure is counted, not fatal, and the healthy notes still land.
func TestSyncNotes_PartialFailure(t *testing.T) {
	ix, vault, st := newTestIndexerWith(t, &poisonEmbedder{dim: testDim, poison: "boom"})
	writeNote(t, vault, "a.md", "# A\nalpha")
	writeNote(t, vault, "bad.md", "# Bad\nboom")

	stats, err := ix.SyncNotes(context.Background(), []string{"a.md", "bad.md"}, false)
	var partial *SyncError
	require.ErrorAs(t, err, &partial)
	require.Equal(t, 1, partial.Failed)
	require.Equal(t, 1, stats.Indexed)

	paths, err := st.Paths()
	require.NoError(t, err)
	require.Equal(t, []string{"a.md"}, paths)
}

func TestSync_IsIncremental(t *testing.T) {
	ix, vault, _, emb := newTestIndexer(t)
	writeNote(t, vault, "a.md", "# A\nalpha")

	_, err := ix.Sync(context.Background(), nil, false)
	require.NoError(t, err)
	first := emb.calls.Load()
	require.Positive(t, first)

	// Second sync with no changes embeds nothing.
	stats, err := ix.Sync(context.Background(), nil, false)
	require.NoError(t, err)
	require.Equal(t, 0, stats.Indexed)
	require.Equal(t, 1, stats.Skipped)
	require.Equal(t, first, emb.calls.Load(), "unchanged note must not be re-embedded")
}

func TestSync_ForceReindexesUnchanged(t *testing.T) {
	ix, vault, _, emb := newTestIndexer(t)
	writeNote(t, vault, "a.md", "# A\nalpha")

	_, err := ix.Sync(context.Background(), nil, false)
	require.NoError(t, err)
	first := emb.calls.Load()

	// Force re-embeds the unchanged note (nothing skipped).
	stats, err := ix.Sync(context.Background(), nil, true)
	require.NoError(t, err)
	require.Equal(t, 1, stats.Indexed)
	require.Equal(t, 0, stats.Skipped)
	require.Greater(t, emb.calls.Load(), first)
}

func TestSync_ReindexesChangedNote(t *testing.T) {
	ix, vault, _, emb := newTestIndexer(t)
	writeNote(t, vault, "a.md", "# A\nalpha")
	_, err := ix.Sync(context.Background(), nil, false)
	require.NoError(t, err)
	before := emb.calls.Load()

	writeNote(t, vault, "a.md", "# A\nalpha changed and longer")
	stats, err := ix.Sync(context.Background(), nil, false)
	require.NoError(t, err)
	require.Equal(t, 1, stats.Indexed)
	require.Greater(t, emb.calls.Load(), before)
}

func TestSync_PrunesDeletedNotes(t *testing.T) {
	ix, vault, st, _ := newTestIndexer(t)
	writeNote(t, vault, "a.md", "# A\nalpha")
	writeNote(t, vault, "b.md", "# B\nbeta")
	_, err := ix.Sync(context.Background(), nil, false)
	require.NoError(t, err)

	require.NoError(t, os.Remove(filepath.Join(vault, "b.md")))
	stats, err := ix.Sync(context.Background(), nil, false)
	require.NoError(t, err)
	require.Equal(t, 1, stats.Pruned)

	paths, err := st.Paths()
	require.NoError(t, err)
	require.Equal(t, []string{"a.md"}, paths)
}

func TestSync_SkipsHiddenDirs(t *testing.T) {
	ix, vault, st, _ := newTestIndexer(t)
	writeNote(t, vault, "a.md", "# A\nalpha")
	writeNote(t, vault, ".obsidian/config.md", "# hidden\nshould be skipped")

	_, err := ix.Sync(context.Background(), nil, false)
	require.NoError(t, err)
	paths, err := st.Paths()
	require.NoError(t, err)
	require.Equal(t, []string{"a.md"}, paths)
}

func TestSync_NonMarkdownIgnored(t *testing.T) {
	ix, vault, st, _ := newTestIndexer(t)
	writeNote(t, vault, "a.md", "# A\nalpha")
	require.NoError(t, os.WriteFile(filepath.Join(vault, "note.txt"), []byte("not markdown"), 0o644))

	_, err := ix.Sync(context.Background(), nil, false)
	require.NoError(t, err)
	paths, err := st.Paths()
	require.NoError(t, err)
	require.Equal(t, []string{"a.md"}, paths)
}

func TestEmbedText_TitleAndBreadcrumb(t *testing.T) {
	tests := []struct {
		name  string
		title string
		chunk chunk.Chunk
		want  string
	}{
		{
			name:  "title and breadcrumb prefix the text",
			title: "Auth notes",
			chunk: chunk.Chunk{Heading: "Project › Tokens", Text: "rotate the key"},
			want:  "Auth notes\nProject › Tokens\n\nrotate the key",
		},
		{
			name:  "no heading keeps the title",
			title: "Auth notes",
			chunk: chunk.Chunk{Text: "intro line"},
			want:  "Auth notes\n\nintro line",
		},
		{
			name:  "nothing to prefix",
			chunk: chunk.Chunk{Text: "bare"},
			want:  "bare",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, embedText(tc.title, tc.chunk))
		})
	}
}

func TestNoteTitle(t *testing.T) {
	require.Equal(t, "Go QA", noteTitle("Prep/Go QA.md"))
	require.Equal(t, "readme", noteTitle("readme.markdown"))
}

// One note that trips a non-retryable gateway error must not discard the pass: on
// a big vault that would throw away thousands of notes' work. Its siblings index,
// the stats count them, and the aggregate error names the casualty.
func TestSync_FailingNoteDoesNotAbortSiblings(t *testing.T) {
	ix, vault, st := newTestIndexerWith(t, &poisonEmbedder{dim: testDim, poison: "POISON"})
	ix.SetConcurrency(4)
	const n = 20
	for i := range n {
		writeNote(t, vault, fmt.Sprintf("n%02d.md", i), fmt.Sprintf("# N%d\nbody %d", i, i))
	}
	writeNote(t, vault, "bad.md", "# Bad\nPOISON")

	stats, err := ix.Sync(context.Background(), nil, false)

	var syncErr *SyncError
	require.ErrorAs(t, err, &syncErr, "a partial pass reports a *SyncError")
	require.Equal(t, 1, syncErr.Failed)
	require.ErrorContains(t, err, `indexing "bad.md"`)
	require.Equal(t, n, stats.Indexed, "every other note still indexed")

	paths, err := st.Paths()
	require.NoError(t, err)
	require.Len(t, paths, n, "the store holds the siblings")
	require.NotContains(t, paths, "bad.md")
}

// The retained sample is capped so a vault-wide outage can't build an
// unbounded error string, while the count stays exact.
func TestSync_ManyFailuresAreCappedButCounted(t *testing.T) {
	ix, vault, _ := newTestIndexerWith(t, &poisonEmbedder{dim: testDim, poison: "POISON"})
	ix.SetConcurrency(4)
	const bad = maxRetainedNoteErrors + 5
	for i := range bad {
		writeNote(t, vault, fmt.Sprintf("bad%02d.md", i), "POISON")
	}
	writeNote(t, vault, "good.md", "# Good\nfine")

	stats, err := ix.Sync(context.Background(), nil, false)

	var syncErr *SyncError
	require.ErrorAs(t, err, &syncErr)
	require.Equal(t, bad, syncErr.Failed)
	require.Len(t, syncErr.Errs, maxRetainedNoteErrors, "the sample is bounded")
	require.ErrorContains(t, err, "and 5 more")
	require.Equal(t, 1, stats.Indexed)
}

// Cancellation is not a note failure: the caller asked to stop, so the pass ends
// with ctx.Err() rather than a list of per-note errors.
func TestSync_CancelledContextAborts(t *testing.T) {
	ix, vault, _, _ := newTestIndexer(t)
	for i := range 5 {
		writeNote(t, vault, fmt.Sprintf("n%d.md", i), "# N\nbody")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stats, err := ix.Sync(ctx, nil, false)

	require.ErrorIs(t, err, context.Canceled)
	var syncErr *SyncError
	require.False(t, errors.As(err, &syncErr), "a cancelled pass is not a partial one")
	require.Zero(t, stats.Indexed)
}
