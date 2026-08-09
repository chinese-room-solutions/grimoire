package store

import (
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/sqlmigrate"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/stretchr/testify/require"
)

// openTemp opens a fresh store in a temp dir for the given dimension.
func openTemp(t *testing.T, dim int) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "index.db"), dim, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func vec(vals ...float32) []float32 { return vals }

// vectorSearch runs a hybrid search with no keyword leg (empty query), pure
// vector ranking with the band filter disabled.
func vectorSearch(s *Store, qvec []float32, k int) ([]Hit, error) {
	return s.Search("", qvec, SearchOptions{K: k})
}

func TestOpen_RejectsBadDimension(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "x.db"), 0, "")
	require.Error(t, err)
}

func TestReplaceAndSearch(t *testing.T) {
	s := openTemp(t, 4)

	// Cosine similarity ranks by direction, so the two chunks point different
	// ways: "near" aligns with the query, "far" is orthogonal.
	chunks := []Chunk{
		{Path: "a.md", Index: 0, Heading: "Intro", Text: "near", DocHash: "h1", Vector: vec(1, 0, 0, 0)},
		{Path: "a.md", Index: 3, Heading: "Body", Text: "far", DocHash: "h1", Vector: vec(0, 1, 0, 0)},
	}
	require.NoError(t, s.ReplaceNote("a.md", chunks))

	n, err := s.Count()
	require.NoError(t, err)
	require.Equal(t, 2, n)

	hits, err := vectorSearch(s, vec(1, 0, 0, 0), 2)
	require.NoError(t, err)
	require.Len(t, hits, 2)
	require.Equal(t, "near", hits[0].Text)
	require.Equal(t, "Intro", hits[0].Heading)
	require.InDelta(t, 1.0, hits[0].Similarity, 1e-6)
	require.Greater(t, hits[0].Similarity, hits[1].Similarity)
}

func TestReplaceNote_IsIdempotentPerPath(t *testing.T) {
	s := openTemp(t, 3)

	require.NoError(t, s.ReplaceNote("a.md", []Chunk{
		{Path: "a.md", Index: 0, Text: "v1", DocHash: "h1", Vector: vec(1, 0, 0)},
		{Path: "a.md", Index: 1, Text: "v1b", DocHash: "h1", Vector: vec(0, 1, 0)},
	}))
	// Re-indexing the same note replaces, not appends.
	require.NoError(t, s.ReplaceNote("a.md", []Chunk{
		{Path: "a.md", Index: 0, Text: "v2", DocHash: "h2", Vector: vec(0, 0, 1)},
	}))

	n, err := s.Count()
	require.NoError(t, err)
	require.Equal(t, 1, n)

	hash, indexed, err := s.DocHash("a.md")
	require.NoError(t, err)
	require.True(t, indexed)
	require.Equal(t, "h2", hash)
}

func TestDeleteNote(t *testing.T) {
	s := openTemp(t, 2)
	require.NoError(t, s.ReplaceNote("a.md", []Chunk{{Path: "a.md", Text: "x", DocHash: "h", Vector: vec(1, 0)}}))
	require.NoError(t, s.ReplaceNote("b.md", []Chunk{{Path: "b.md", Text: "y", DocHash: "h", Vector: vec(0, 1)}}))

	require.NoError(t, s.DeleteNote("a.md"))

	paths, err := s.Paths()
	require.NoError(t, err)
	require.Equal(t, []string{"b.md"}, paths)

	_, indexed, err := s.DocHash("a.md")
	require.NoError(t, err)
	require.False(t, indexed)
}

func TestReplaceNote_RejectsWrongDimension(t *testing.T) {
	s := openTemp(t, 4)
	err := s.ReplaceNote("a.md", []Chunk{{Path: "a.md", Text: "x", DocHash: "h", Vector: vec(1, 0)}})
	require.Error(t, err)
}

func TestReopen_IncompatibleFingerprint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.db")

	s, err := Open(path, 4, "passage: ")
	require.NoError(t, err)
	require.NoError(t, s.Close())

	// Reopen with the same configuration: fine.
	s2, err := Open(path, 4, "passage: ")
	require.NoError(t, err)
	require.NoError(t, s2.Close())

	// A different dimension or document prefix changes every stored vector.
	_, err = Open(path, 8, "passage: ")
	require.ErrorIs(t, err, ErrIncompatible)
	_, err = Open(path, 4, "search_document: ")
	require.ErrorIs(t, err, ErrIncompatible)
}

// A database from before fingerprinting has its migration version recorded but
// neither the fingerprint key nor the embedding column — it must be flagged
// incompatible, not silently adopted.
func TestOpen_PreFingerprintDatabaseIsIncompatible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	db, err := driver.Open(sqlmigrate.FileDSN(path))
	require.NoError(t, err)
	_, err = db.Exec(`
CREATE TABLE _migrations (version INTEGER PRIMARY KEY);
INSERT INTO _migrations(version) VALUES (1);
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE chunks (
	id INTEGER PRIMARY KEY, path TEXT NOT NULL, idx INTEGER NOT NULL,
	heading TEXT NOT NULL DEFAULT '', text TEXT NOT NULL, doc_hash TEXT NOT NULL
);`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = Open(path, 4, "")
	require.ErrorIs(t, err, ErrIncompatible)
}

func TestSearch_QueryDimensionValidated(t *testing.T) {
	s := openTemp(t, 4)
	_, err := vectorSearch(s, vec(1, 2, 3), 5)
	require.Error(t, err)
}

// With no query embedding at all (the model is unreachable) the search still
// runs the keyword leg rather than refusing — a degraded search, not a broken
// one. The vector leg contributes nothing, so hits carry no rank and no
// similarity from it.
func TestSearch_NoQueryVectorIsKeywordOnly(t *testing.T) {
	s := openTemp(t, 2)
	require.NoError(t, s.ReplaceNote("hit.md", []Chunk{
		{Path: "hit.md", Index: 0, Text: "the ULID spec", DocHash: "h", Vector: vec(1, 0)},
	}))
	require.NoError(t, s.ReplaceNote("miss.md", []Chunk{
		{Path: "miss.md", Index: 0, Text: "unrelated text", DocHash: "h", Vector: vec(0, 1)},
	}))

	for _, qvec := range [][]float32{nil, {}} {
		hits, err := s.Search("ULID", qvec, SearchOptions{K: 5, MinSim: 0.5, TopRatio: 0.88})
		require.NoError(t, err)
		require.Len(t, hits, 1)
		require.Equal(t, "hit.md", hits[0].Path)
		require.Equal(t, 1, hits[0].FTSRank)
		require.Zero(t, hits[0].VecRank)
		require.Zero(t, hits[0].Similarity)
	}
}

func TestSanitizeFTSQuery(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{"plain words", "rotate api key", `"rotate" OR "api" OR "key"`},
		{"operators are neutralized", `NEAR NOT AND foo*`, `"NEAR" OR "NOT" OR "AND" OR "foo*"`},
		{"punctuation-only tokens dropped", "? - :: !", ""},
		{"embedded quotes doubled", `say "hi" there`, `"say" OR """hi""" OR "there"`},
		{"cyrillic kept", "как дела", `"как" OR "дела"`},
		{"empty", "   ", ""},
		{"colons and paths", "internal/store: bug?", `"internal/store:" OR "bug?"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, sanitizeFTSQuery(tc.query))
		})
	}
}

// The FTS5 canary for the WASM/binding pairing: keyword search must match
// Russian and English text. A zero query vector disables the vector leg.
func TestSearch_KeywordRussianAndEnglish(t *testing.T) {
	s := openTemp(t, 2)
	require.NoError(t, s.ReplaceNote("ru.md", []Chunk{
		{Path: "ru.md", Index: 0, Text: "привет мир из заметки", DocHash: "h", Vector: vec(1, 0)},
	}))
	require.NoError(t, s.ReplaceNote("en.md", []Chunk{
		{Path: "en.md", Index: 0, Text: "hello world from a note", DocHash: "h", Vector: vec(0, 1)},
	}))

	for query, wantPath := range map[string]string{
		"привет": "ru.md",
		"hello":  "en.md",
	} {
		hits, err := s.Search(query, vec(0, 0), SearchOptions{K: 5})
		require.NoError(t, err)
		require.Len(t, hits, 1, "query %q", query)
		require.Equal(t, wantPath, hits[0].Path)
		require.Zero(t, hits[0].Similarity, "keyword-only hits carry no similarity")
	}
}

// A chunk that matches both legs must outrank a chunk that leads only the
// vector leg: RRF sums the reciprocal ranks.
func TestSearch_HybridFusion(t *testing.T) {
	s := openTemp(t, 2)
	require.NoError(t, s.ReplaceNote("a.md", []Chunk{
		{Path: "a.md", Index: 0, Text: "alpha beta", DocHash: "h", Vector: vec(1, 0)},
	}))
	require.NoError(t, s.ReplaceNote("b.md", []Chunk{
		{Path: "b.md", Index: 0, Text: "gamma delta", DocHash: "h", Vector: vec(0.95, 0.31224989)},
	}))

	// Vector leg ranks a.md first (sim 1.0 vs ~0.95); the keyword leg matches
	// only b.md, so fusion flips the order.
	hits, err := s.Search("gamma", vec(1, 0), SearchOptions{K: 5, TopRatio: 0.88})
	require.NoError(t, err)
	require.Len(t, hits, 2)
	require.Equal(t, "b.md", hits[0].Path)
	require.Equal(t, "a.md", hits[1].Path)
	require.InDelta(t, 0.95, hits[0].Similarity, 1e-3)
	require.InDelta(t, 1.0, hits[1].Similarity, 1e-6)
}

// An exact keyword match must survive even when its embedding is unrelated to
// the query (the classic identifier lookup), while unrelated chunks without a
// keyword match stay banded out.
func TestSearch_KeywordOnlyHitPassesBand(t *testing.T) {
	s := openTemp(t, 2)
	require.NoError(t, s.ReplaceNote("target.md", []Chunk{
		{Path: "target.md", Index: 0, Text: "the ULID spec", DocHash: "h", Vector: vec(0, 1)}, // orthogonal to query.
	}))
	require.NoError(t, s.ReplaceNote("noise.md", []Chunk{
		{Path: "noise.md", Index: 0, Text: "unrelated text", DocHash: "h", Vector: vec(0.1, 0.99498744)},
	}))
	require.NoError(t, s.ReplaceNote("about.md", []Chunk{
		{Path: "about.md", Index: 0, Text: "on topic", DocHash: "h", Vector: vec(1, 0)},
	}))

	hits, err := s.Search("ULID", vec(1, 0), SearchOptions{K: 5, MinSim: 0.5, TopRatio: 0.88})
	require.NoError(t, err)
	require.Len(t, hits, 2)
	paths := []string{hits[0].Path, hits[1].Path}
	require.Contains(t, paths, "target.md") // keyword-only, sub-band similarity.
	require.Contains(t, paths, "about.md")  // vector match.
	require.NotContains(t, paths, "noise.md")
}

// A keyword-rank-1-only hit and a vector-rank-1-only hit tie on RRF score;
// the exact term match must win — an identifier lookup ("sync.Once") beats an
// unrelated chunk that merely tops the compressed similarity band.
func TestSearch_ExactKeywordWinsRRFTie(t *testing.T) {
	s := openTemp(t, 2)
	require.NoError(t, s.ReplaceNote("vectop.md", []Chunk{
		{Path: "vectop.md", Index: 0, Text: "generic prose", DocHash: "h", Vector: vec(1, 0)},
	}))
	require.NoError(t, s.ReplaceNote("exact.md", []Chunk{
		{Path: "exact.md", Index: 0, Text: "uses sync.Once for init", DocHash: "h", Vector: vec(0, 1)}, // banded out of the vector leg.
	}))

	hits, err := s.Search("sync.Once", vec(1, 0), SearchOptions{K: 5, TopRatio: 0.88})
	require.NoError(t, err)
	require.Len(t, hits, 2)
	require.Equal(t, "exact.md", hits[0].Path)
	require.Equal(t, "vectop.md", hits[1].Path)
}

// Every hit reports the 1-based rank it held in each leg, 0 where it was
// absent — the input a caller needs to re-fuse hits from several stores.
func TestSearch_ReportsPerLegRanks(t *testing.T) {
	s := openTemp(t, 2)
	// "ulid-spec.md" matches the query in its path (the heaviest BM25 column),
	// so it leads the keyword leg; its vector is orthogonal, banding it out of
	// the vector leg entirely.
	require.NoError(t, s.ReplaceNote("ulid-spec.md", []Chunk{
		{Path: "ulid-spec.md", Index: 0, Text: "ULID identifiers", DocHash: "h", Vector: vec(0, 1)},
	}))
	require.NoError(t, s.ReplaceNote("both.md", []Chunk{
		{Path: "both.md", Index: 0, Text: "we picked ULID", DocHash: "h", Vector: vec(1, 0)},
	}))
	require.NoError(t, s.ReplaceNote("vec-only.md", []Chunk{
		{Path: "vec-only.md", Index: 0, Text: "generic prose", DocHash: "h", Vector: vec(0.99, 0.14106736)},
	}))

	hits, err := s.Search("ULID", vec(1, 0), SearchOptions{K: 5, TopRatio: 0.88})
	require.NoError(t, err)
	byPath := make(map[string]Hit, len(hits))
	for _, h := range hits {
		byPath[h.Path] = h
	}
	require.Len(t, byPath, 3)

	tests := []struct {
		path    string
		vecRank int
		ftsRank int
	}{
		{"both.md", 1, 2},      // both legs: vector rank 1 (sim 1.0), keyword runner-up.
		{"vec-only.md", 2, 0},  // vector only: no keyword match.
		{"ulid-spec.md", 0, 1}, // keyword only: banded out of the vector leg.
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			h := byPath[tt.path]
			require.Equal(t, tt.vecRank, h.VecRank)
			require.Equal(t, tt.ftsRank, h.FTSRank)
		})
	}
}

func TestSearch_BandFiltersVectorLeg(t *testing.T) {
	s := openTemp(t, 2)
	require.NoError(t, s.ReplaceNote("top.md", []Chunk{
		{Path: "top.md", Index: 0, Text: "best", DocHash: "h", Vector: vec(1, 0)},
	}))
	require.NoError(t, s.ReplaceNote("tail.md", []Chunk{
		{Path: "tail.md", Index: 0, Text: "weak", DocHash: "h", Vector: vec(0.6, 0.8)},
	}))

	// tail.md's 0.6 similarity is below 0.88 * 1.0: banded out.
	hits, err := s.Search("", vec(1, 0), SearchOptions{K: 5, TopRatio: 0.88})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, "top.md", hits[0].Path)
}

// TestSearch_BandSemantics ports the app-layer relevance-filter table: hits
// are kept relative to the best vector match (TopRatio band) and above the
// caller's floor. Similarities are realized as unit vectors at the matching
// angle from the query.
func TestSearch_BandSemantics(t *testing.T) {
	simVec := func(sim float64) []float32 {
		return vec(float32(sim), float32(math.Sqrt(1-sim*sim)))
	}
	tests := []struct {
		name   string
		sims   []float64
		minSim float64
		want   int // number of hits kept
	}{
		{
			// Real "daniil trishkin" distribution: 3 CV chunks then the noise tail.
			// top=0.705, cutoff=0.705*0.88=0.620 keeps only the first three.
			name: "keeps cluster near the top, drops the tail",
			sims: []float64{0.705, 0.648, 0.624, 0.597, 0.584, 0.575, 0.573}, minSim: 0.50, want: 3,
		},
		{"all tightly clustered are all kept", []float64{0.80, 0.79, 0.78}, 0.50, 3},
		{"floor drops a lone weak top", []float64{0.40, 0.39}, 0.50, 0},
		{"single strong hit", []float64{0.90}, 0.50, 1},
		{"higher floor tightens the cut", []float64{0.80, 0.74}, 0.75, 1},
		{"low floor still respects the band", []float64{0.90, 0.60}, 0.20, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTemp(t, 2)
			for i, sim := range tt.sims {
				path := fmt.Sprintf("n%d.md", i)
				require.NoError(t, s.ReplaceNote(path, []Chunk{
					{Path: path, Index: 0, Text: "t", DocHash: "h", Vector: simVec(sim)},
				}))
			}
			hits, err := s.Search("", vec(1, 0), SearchOptions{K: 10, MinSim: tt.minSim, TopRatio: 0.88})
			require.NoError(t, err)
			require.Len(t, hits, tt.want)
		})
	}
}

func TestSearch_DedupesAdjacentWindows(t *testing.T) {
	s := openTemp(t, 2)
	require.NoError(t, s.ReplaceNote("a.md", []Chunk{
		{Path: "a.md", Index: 0, Text: "w0", DocHash: "h", Vector: vec(1, 0)},
		{Path: "a.md", Index: 1, Text: "w1 overlaps w0", DocHash: "h", Vector: vec(0.999, 0.0447101)},
		{Path: "a.md", Index: 5, Text: "far window", DocHash: "h", Vector: vec(0.99, 0.14106736)},
	}))

	hits, err := vectorSearch(s, vec(1, 0), 5)
	require.NoError(t, err)
	require.Len(t, hits, 2)
	require.Equal(t, 0, hits[0].Index) // best window survives...
	require.Equal(t, 5, hits[1].Index) // ...its overlap twin is dropped, the far one stays.
}

// The vector cache must follow writes without reopening the store.
func TestVectorCache_FollowsReplaceAndDelete(t *testing.T) {
	s := openTemp(t, 2)
	require.NoError(t, s.ReplaceNote("a.md", []Chunk{
		{Path: "a.md", Index: 0, Text: "old", DocHash: "h1", Vector: vec(1, 0)},
	}))

	require.NoError(t, s.ReplaceNote("a.md", []Chunk{
		{Path: "a.md", Index: 0, Text: "new", DocHash: "h2", Vector: vec(0, 1)},
	}))
	hits, err := vectorSearch(s, vec(0, 1), 5)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, "new", hits[0].Text)
	require.InDelta(t, 1.0, hits[0].Similarity, 1e-6)

	require.NoError(t, s.DeleteNote("a.md"))
	hits, err = vectorSearch(s, vec(0, 1), 5)
	require.NoError(t, err)
	require.Empty(t, hits)
}

func TestVectorCache_LoadsOnReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	s, err := Open(path, 2, "")
	require.NoError(t, err)
	require.NoError(t, s.ReplaceNote("a.md", []Chunk{
		{Path: "a.md", Index: 0, Text: "persisted", DocHash: "h", Vector: vec(3, 4)},
	}))
	require.NoError(t, s.Close())

	s2, err := Open(path, 2, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s2.Close() })
	hits, err := vectorSearch(s2, vec(3, 4), 5)
	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, "persisted", hits[0].Text)
	require.InDelta(t, 1.0, hits[0].Similarity, 1e-6)
}

func TestPaths_Empty(t *testing.T) {
	s := openTemp(t, 4)
	paths, err := s.Paths()
	require.NoError(t, err)
	require.Empty(t, paths)
}

func TestNoteVectors_Empty(t *testing.T) {
	s := openTemp(t, 4)
	got, err := s.NoteVectors()
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestNoteVectors_MeanPoolsAndNormalizes(t *testing.T) {
	s := openTemp(t, 2)
	// a.md: two chunks pointing along +x and +y. Their mean is (0.5,0.5),
	// which normalizes to (√½,√½). b.md: a single +x chunk → (1,0).
	require.NoError(t, s.ReplaceNote("a.md", []Chunk{
		{Path: "a.md", Index: 0, Text: "x", DocHash: "h", Vector: vec(2, 0)},
		{Path: "a.md", Index: 1, Text: "y", DocHash: "h", Vector: vec(0, 2)},
	}))
	require.NoError(t, s.ReplaceNote("b.md", []Chunk{
		{Path: "b.md", Index: 0, Text: "z", DocHash: "h", Vector: vec(5, 0)},
	}))

	got, err := s.NoteVectors()
	require.NoError(t, err)
	require.Len(t, got, 2)

	inv := float32(1 / math.Sqrt(2))
	require.InDeltaSlice(t, []float32{inv, inv}, got["a.md"], 1e-6)
	require.InDeltaSlice(t, []float32{1, 0}, got["b.md"], 1e-6)
}

func TestNoteVectors_SkipsZeroCentroid(t *testing.T) {
	s := openTemp(t, 2)
	// Opposing chunk vectors cancel to a zero centroid: the note has no
	// direction to compare, so it's omitted rather than emitted as NaN.
	require.NoError(t, s.ReplaceNote("a.md", []Chunk{
		{Path: "a.md", Index: 0, Text: "x", DocHash: "h", Vector: vec(1, 0)},
		{Path: "a.md", Index: 1, Text: "y", DocHash: "h", Vector: vec(-1, 0)},
	}))
	require.NoError(t, s.ReplaceNote("b.md", []Chunk{
		{Path: "b.md", Index: 0, Text: "z", DocHash: "h", Vector: vec(0, 3)},
	}))

	got, err := s.NoteVectors()
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Contains(t, got, "b.md")
	require.NotContains(t, got, "a.md")
}

// TestReplaceNoteConcurrent guards the store's own single-writer invariant:
// concurrent writers (e.g. watcher and in-app indexers built independently)
// serialize inside the store rather than corrupting or erroring on each other,
// and hybrid searches run safely alongside them.
func TestReplaceNoteConcurrent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "index.db"), 3, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	const writers = 8
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path := fmt.Sprintf("note-%d.md", i)
			for range 5 {
				errs[i] = s.ReplaceNote(path, []Chunk{
					{Path: path, Index: 0, Text: "x", DocHash: "h", Vector: []float32{1, 0, 0}},
					{Path: path, Index: 1, Text: "y", DocHash: "h", Vector: []float32{0, 1, 0}},
				})
				if errs[i] != nil {
					return
				}
			}
		}()
	}
	searchErrs := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 20 {
			if _, err := s.Search("x y", []float32{1, 1, 0}, SearchOptions{K: 5}); err != nil {
				searchErrs <- err
				return
			}
		}
		searchErrs <- nil
	}()
	wg.Wait()
	require.NoError(t, <-searchErrs)
	for i, err := range errs {
		require.NoError(t, err, "writer %d", i)
	}
	n, err := s.Count()
	require.NoError(t, err)
	require.Equal(t, writers*2, n, "each note ends with exactly its last replacement")
}
