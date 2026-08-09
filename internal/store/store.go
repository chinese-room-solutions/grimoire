// Package store is Grimoire's persistent index: note chunks, their embeddings,
// and an FTS5 keyword index in a single SQLite file (running CGO-free on
// ncruces' WASM SQLite).
//
// Search is hybrid: a vector leg (an in-memory cosine scan over the chunk
// embeddings — exact, and fast at vault scale) and a keyword leg (FTS5 BM25),
// fused with Reciprocal Rank Fusion. The embeddings live as raw blobs on the
// chunks table; the store keeps unit-normalized copies in memory so similarity
// is a bare dot product.
//
// The store is bound to one embedding configuration via a fingerprint (format
// version + dimension + document prefix) recorded in meta; opening with a
// different configuration is ErrIncompatible — the caller must rebuild. The
// whole database is a derived cache, safe to delete.
package store

import (
	"bytes"
	"database/sql"
	"embed"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/KernelPryanic/ctxerr"
	"github.com/chinese-room-solutions/grimoire/internal/sqlmigrate"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"
)

//go:embed migrations/*.up.sql
var migrationsFS embed.FS

// formatVersion is bumped whenever the schema or the embed-text recipe (see
// index.embedText) changes, so stale indexes rebuild instead of mixing old and
// new embeddings.
const formatVersion = 2

// ErrIncompatible is returned by Open when the store on disk was built with a
// different fingerprint (format version, embedding dimension, or document
// prefix) than requested. The index is a derived cache: delete and rebuild.
var ErrIncompatible = errors.New("index incompatible; rebuild required")

// Chunk is one indexed slice of a note, with enough provenance to cite it.
type Chunk struct {
	ID      int64  // rowid, primary key.
	Path    string // vault-relative path of the source note.
	Index   int    // 0-based position of this chunk within the note.
	Heading string // breadcrumb of enclosing headings, for display/citation.
	Text    string // the chunk text that was embedded.
	DocHash string // content hash of the whole note, for incremental reindex.
	Vector  []float32
}

// Hit is a search result: a chunk plus its cosine similarity to the query in
// [-1, 1] (1 == identical direction). Similarity is 0 for hits surfaced only
// by the keyword leg — they matched exact terms, not the embedding.
//
// The per-leg ranks are what a caller needs to re-fuse hits from several
// stores: similarity scores from different embedding models are not
// comparable, but rank positions are, so a cross-vault search fuses on
// 1/(60+rank) per leg rather than on Similarity.
type Hit struct {
	Chunk
	Similarity float64
	VecRank    int // 1-based rank in the vector leg; 0 = absent from that leg.
	FTSRank    int // 1-based rank in the keyword (FTS) leg; 0 = absent.
}

// SearchOptions are the relevance knobs the caller passes into a hybrid
// search; the policy values live with the caller, the mechanics here. The
// vector leg keeps candidates at or above max(best·TopRatio, MinSim), so
// TopRatio 0 is not "no band" — it still drops everything below MinSim and
// below zero similarity, and a negative best hit bands out every candidate.
type SearchOptions struct {
	K        int     // results to return (≤0 → 10).
	MinSim   float64 // absolute similarity floor for the vector leg.
	TopRatio float64 // relative band vs the best vector hit; see above.
}

// rrfK is the standard Reciprocal Rank Fusion constant: score contributions
// are 1/(rrfK+rank), damping the gap between neighboring ranks.
const rrfK = 60

// cacheRow is one chunk's entry in the in-memory vector cache.
type cacheRow struct {
	path string
	vec  []float32 // unit-normalized copy; cosine == dot product.
}

// Store is the SQLite-backed chunk + vector index. Safe for concurrent use:
// writes serialize on an internal mutex (SQLite allows one writer at a time,
// and callers construct indexers freely), reads go to the database and the
// vector cache under its own RWMutex.
type Store struct {
	db  *sql.DB
	dim int

	// writeMu serializes the writing methods (ReplaceNote, DeleteNote) across
	// every concurrent caller — the invariant lives here, not in any one indexer.
	writeMu sync.Mutex

	cacheMu sync.RWMutex
	cache   map[int64]cacheRow
}

// Open opens (creating if needed) the index at path for embeddings of the
// given dimension and document prefix. A store built with any other
// fingerprint is ErrIncompatible.
func Open(path string, dim int, docPrefix string) (*Store, error) {
	if dim <= 0 {
		return nil, fmt.Errorf("embedding dimension must be positive, got %d", dim)
	}
	db, err := driver.Open(sqlmigrate.FileDSN(path), fts5.Register)
	if err != nil {
		return nil, fmt.Errorf("opening index: %w", err)
	}
	s := &Store{db: db, dim: dim, cache: make(map[int64]cacheRow)}
	if err := s.init(dim, docPrefix); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.loadCache(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) init(dim int, docPrefix string) error {
	if err := sqlmigrate.Run(s.db, migrationsFS, "migrations"); err != nil {
		return fmt.Errorf("migrating index: %w", err)
	}

	fp := fmt.Sprintf("v%d|dim=%d|doc=%s", formatVersion, dim, docPrefix)
	stored, ok, err := s.metaString("fingerprint")
	if err != nil {
		return err
	}
	if ok {
		if stored != fp {
			return fmt.Errorf("%w: store=%q requested=%q", ErrIncompatible, stored, fp)
		}
		return nil
	}
	// No fingerprint: a fresh store, or one from before fingerprinting. A
	// pre-fingerprint database recorded its migration version already, so the
	// edited DDL never ran on it — probe the embedding column to tell them
	// apart instead of corrupting on the first insert.
	var n int
	if err := s.db.QueryRow("SELECT count(embedding) FROM chunks").Scan(&n); err != nil {
		return fmt.Errorf("%w: pre-fingerprint database", ErrIncompatible)
	}
	if _, err := s.db.Exec("INSERT INTO meta(key, value) VALUES('fingerprint', ?)", fp); err != nil {
		return fmt.Errorf("recording fingerprint: %w", err)
	}
	return nil
}

func (s *Store) metaString(key string) (string, bool, error) {
	var v string
	err := s.db.QueryRow("SELECT value FROM meta WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("reading meta %q: %w", key, err)
	}
	return v, true, nil
}

// loadCache fills the in-memory vector cache from the chunks table. Chunks
// whose embedding has no direction (zero vector) are left out of the vector
// leg; they remain searchable by keyword.
func (s *Store) loadCache() error {
	rows, err := s.db.Query("SELECT id, path, embedding FROM chunks")
	if err != nil {
		return fmt.Errorf("loading vector cache: %w", err)
	}
	defer func() { _ = rows.Close() }()

	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	for rows.Next() {
		var id int64
		var path string
		var blob []byte
		if err := rows.Scan(&id, &path, &blob); err != nil {
			return fmt.Errorf("scanning vector cache row: %w", err)
		}
		vec, err := deserializeFloat32(blob, s.dim)
		if err != nil {
			return ctxerr.With(err, map[string]any{"path": path})
		}
		if normalize(vec) {
			s.cache[id] = cacheRow{path: path, vec: vec}
		}
	}
	return rows.Err()
}

// ReplaceNote atomically replaces all chunks for a note path with the given
// set: it deletes the note's existing chunks, then inserts the new ones (the
// FTS index follows via triggers, the vector cache is updated after commit).
// Passing an empty slice just removes the note from the index.
func (s *Store) ReplaceNote(path string, chunks []Chunk) (err error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if err = deleteNoteTx(tx, path); err != nil {
		return err
	}
	fresh := make(map[int64]cacheRow, len(chunks))
	for _, c := range chunks {
		if len(c.Vector) != s.dim {
			return ctxerr.With(ErrIncompatible, map[string]any{
				"path": path, "chunk": c.Index, "got": len(c.Vector), "want": s.dim,
			})
		}
		res, ierr := tx.Exec(
			"INSERT INTO chunks(path, idx, heading, text, doc_hash, embedding) VALUES(?,?,?,?,?,?)",
			path, c.Index, c.Heading, c.Text, c.DocHash, serializeFloat32(c.Vector),
		)
		if ierr != nil {
			return fmt.Errorf("insert chunk: %w", ierr)
		}
		id, ierr := res.LastInsertId()
		if ierr != nil {
			return fmt.Errorf("chunk id: %w", ierr)
		}
		vec := append([]float32(nil), c.Vector...) // the cache owns its copy.
		if normalize(vec) {
			fresh[id] = cacheRow{path: path, vec: vec}
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}

	// Still under writeMu: swap the note's cache entries to match the commit.
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.dropFromCacheLocked(path)
	for id, row := range fresh {
		s.cache[id] = row
	}
	return nil
}

// DeleteNote removes a note's chunks from the index.
func (s *Store) DeleteNote(path string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := deleteNoteTx(tx, path); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.dropFromCacheLocked(path)
	return nil
}

// dropFromCacheLocked removes a note's entries from the vector cache. Caller
// holds cacheMu. A full sweep is fine at vault scale and avoids maintaining a
// second by-path map.
func (s *Store) dropFromCacheLocked(path string) {
	for id, row := range s.cache {
		if row.path == path {
			delete(s.cache, id)
		}
	}
}

func deleteNoteTx(tx *sql.Tx, path string) error {
	if _, err := tx.Exec("DELETE FROM chunks WHERE path = ?", path); err != nil {
		return fmt.Errorf("delete chunks: %w", err)
	}
	return nil
}

// DocHash returns the stored content hash for a note path and whether it is
// indexed, so the indexer can skip unchanged notes.
func (s *Store) DocHash(path string) (hash string, indexed bool, err error) {
	err = s.db.QueryRow("SELECT doc_hash FROM chunks WHERE path = ? LIMIT 1", path).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("doc hash for %q: %w", path, err)
	}
	return hash, true, nil
}

// Paths returns every note path currently in the index, so the indexer can
// prune notes deleted from the vault.
func (s *Store) Paths() ([]string, error) {
	rows, err := s.db.Query("SELECT DISTINCT path FROM chunks")
	if err != nil {
		return nil, fmt.Errorf("listing paths: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("scanning path: %w", err)
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// Search runs the hybrid query: the vector leg scans the in-memory embeddings
// and keeps candidates inside the relevance band; the keyword leg matches the
// sanitized query against FTS5, ranked by BM25. The legs are fused with
// Reciprocal Rank Fusion, adjacent-window duplicates from the same note are
// dropped, and the top K hits are returned in fused order.
func (s *Store) Search(query string, qvec []float32, opts SearchOptions) ([]Hit, error) {
	if len(qvec) != s.dim {
		return nil, fmt.Errorf("query dimension %d, want %d", len(qvec), s.dim)
	}
	k := opts.K
	if k <= 0 {
		k = 10
	}
	// Both legs fetch well beyond K so fusion sees enough candidates; the
	// floor keeps small-K searches from starving it. Untuned default.
	pool := max(4*k, 40)

	vecHits := s.vectorLeg(qvec, pool, opts)
	ftsHits, err := s.keywordLeg(query, pool)
	if err != nil {
		return nil, err
	}

	hits, err := s.fuse(vecHits, ftsHits)
	if err != nil {
		return nil, err
	}
	hits = dedupeAdjacent(hits)
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

// scored is a chunk id with its per-leg relevance.
type scored struct {
	id  int64
	sim float64
}

// vectorLeg returns up to pool chunk ids by descending cosine similarity,
// band-filtered: only candidates within TopRatio of the best hit and above
// MinSim survive. Embedding models compress all similarities into a narrow,
// model-specific band, so relevance is judged relative to the query's own
// best match rather than by a universal cutoff.
func (s *Store) vectorLeg(qvec []float32, pool int, opts SearchOptions) []scored {
	q := append([]float32(nil), qvec...)
	if !normalize(q) {
		return nil // a zero query has no direction; keyword leg only.
	}

	s.cacheMu.RLock()
	candidates := make([]scored, 0, len(s.cache))
	for id, row := range s.cache {
		var dot float64
		for i, x := range row.vec {
			dot += float64(x) * float64(q[i])
		}
		candidates = append(candidates, scored{id: id, sim: dot})
	}
	s.cacheMu.RUnlock()

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].sim != candidates[j].sim {
			return candidates[i].sim > candidates[j].sim
		}
		return candidates[i].id < candidates[j].id
	})
	if len(candidates) > pool {
		candidates = candidates[:pool]
	}
	if len(candidates) == 0 {
		return nil
	}
	cutoff := candidates[0].sim * opts.TopRatio
	if cutoff < opts.MinSim {
		cutoff = opts.MinSim
	}
	kept := candidates[:0]
	for _, c := range candidates {
		if c.sim >= cutoff {
			kept = append(kept, c)
		}
	}
	return kept
}

// keywordLeg returns up to pool chunk ids by ascending BM25 rank for the
// sanitized query, or nil when the query has no searchable terms. Column
// weights favor the note path (the title lives in the filename) and heading
// over body text. Untuned defaults.
func (s *Store) keywordLeg(query string, pool int) ([]int64, error) {
	expr := sanitizeFTSQuery(query)
	if expr == "" {
		return nil, nil
	}
	rows, err := s.db.Query(`
SELECT rowid FROM chunks_fts
WHERE chunks_fts MATCH ?
ORDER BY bm25(chunks_fts, 4.0, 2.0, 1.0)
LIMIT ?`, expr, pool)
	if err != nil {
		return nil, fmt.Errorf("keyword query %q: %w", expr, err)
	}
	defer func() { _ = rows.Close() }()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning keyword hit: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// sanitizeFTSQuery turns free text into a safe FTS5 MATCH expression: each
// whitespace token becomes a quoted phrase (immune to the MATCH grammar's
// operators and punctuation), tokens with no letter or digit are dropped, and
// terms are joined with OR — natural-language queries would match nothing
// under implicit AND, and BM25 already ranks multi-term matches higher.
// Returns "" when nothing searchable remains.
func sanitizeFTSQuery(query string) string {
	var terms []string
	for _, tok := range strings.Fields(query) {
		if !strings.ContainsFunc(tok, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
			continue
		}
		terms = append(terms, `"`+strings.ReplaceAll(tok, `"`, `""`)+`"`)
	}
	return strings.Join(terms, " OR ")
}

// fuse merges the two legs with Reciprocal Rank Fusion and loads the chunk
// metadata for every fused id in one query. Hits present in the vector leg
// carry their cosine similarity; keyword-only hits report 0. Each hit also
// reports the 1-based rank it held in either leg, so callers can re-fuse
// results from several stores (see Hit).
func (s *Store) fuse(vecHits []scored, ftsIDs []int64) ([]Hit, error) {
	type fused struct {
		score   float64
		fts     float64 // the keyword leg's share of score, for tie-breaking.
		sim     float64
		vecRank int
		ftsRank int
	}
	byID := make(map[int64]*fused, len(vecHits)+len(ftsIDs))
	at := func(id int64) *fused {
		f := byID[id]
		if f == nil {
			f = &fused{}
			byID[id] = f
		}
		return f
	}
	for rank, h := range vecHits {
		f := at(h.id)
		f.score += 1 / float64(rrfK+rank+1)
		f.sim = h.sim
		f.vecRank = rank + 1
	}
	for rank, id := range ftsIDs {
		f := at(id)
		contrib := 1 / float64(rrfK+rank+1)
		f.score += contrib
		f.fts = contrib
		f.ftsRank = rank + 1
	}
	if len(byID) == 0 {
		return nil, nil
	}

	ids := make([]any, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	rows, err := s.db.Query(
		"SELECT id, path, idx, heading, text, doc_hash FROM chunks WHERE id IN (?"+
			strings.Repeat(",?", len(ids)-1)+")", ids...)
	if err != nil {
		return nil, fmt.Errorf("loading fused hits: %w", err)
	}
	defer func() { _ = rows.Close() }()

	hits := make([]Hit, 0, len(byID))
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.ID, &h.Path, &h.Index, &h.Heading, &h.Text, &h.DocHash); err != nil {
			return nil, fmt.Errorf("scanning fused hit: %w", err)
		}
		f := byID[h.ID]
		h.Similarity = f.sim
		h.VecRank, h.FTSRank = f.vecRank, f.ftsRank
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(hits, func(i, j int) bool {
		fi, fj := byID[hits[i].ID], byID[hits[j].ID]
		if fi.score != fj.score {
			return fi.score > fj.score
		}
		// Single-leg rank-1s tie at the same RRF score; prefer the keyword
		// side — an exact term match is a precise signal, while the top of a
		// compressed similarity band is noisy (the "sync.Once" lookup must
		// beat an unrelated vector-rank-1 chunk).
		if fi.fts != fj.fts {
			return fi.fts > fj.fts
		}
		if hits[i].Similarity != hits[j].Similarity {
			return hits[i].Similarity > hits[j].Similarity
		}
		if hits[i].Path != hits[j].Path {
			return hits[i].Path < hits[j].Path
		}
		return hits[i].Index < hits[j].Index
	})
	return hits, nil
}

// dedupeAdjacent drops a hit when a better-ranked hit from the same note is an
// adjacent or identical window — overlapping windows share content, so both
// matching is usually the same passage twice.
func dedupeAdjacent(hits []Hit) []Hit {
	kept := hits[:0]
	for _, h := range hits {
		dup := false
		for _, k := range kept {
			if k.Path == h.Path && abs(k.Index-h.Index) <= 1 {
				dup = true
				break
			}
		}
		if !dup {
			kept = append(kept, h)
		}
	}
	return kept
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Count returns the number of indexed chunks.
func (s *Store) Count() (int, error) {
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM chunks").Scan(&n); err != nil {
		return 0, fmt.Errorf("counting chunks: %w", err)
	}
	return n, nil
}

// NoteVectors returns one embedding per note: the mean of the note's chunk
// vectors (a centroid), L2-normalized so cosine similarity between two notes is
// just their dot product. This is the per-note representation the similarity
// graph compares — the chunk index already covers the whole note, so the mean
// sees all of it (unlike a single whole-document embedding, which would truncate
// long notes at the model's context limit).
//
// Notes whose chunk vectors sum to a zero vector (no embeddable direction) are
// omitted rather than emitted as NaN. The result maps vault-relative path to its
// unit centroid. It reads the raw blobs, not the normalized cache, so the
// centroid weights chunks exactly as the model emitted them.
func (s *Store) NoteVectors() (map[string][]float32, error) {
	rows, err := s.db.Query("SELECT path, embedding FROM chunks")
	if err != nil {
		return nil, fmt.Errorf("reading note vectors: %w", err)
	}
	defer func() { _ = rows.Close() }()

	sums := make(map[string][]float32)
	counts := make(map[string]int)
	for rows.Next() {
		var path string
		var blob []byte
		if err := rows.Scan(&path, &blob); err != nil {
			return nil, fmt.Errorf("scanning note vector: %w", err)
		}
		vec, err := deserializeFloat32(blob, s.dim)
		if err != nil {
			return nil, ctxerr.With(err, map[string]any{"path": path})
		}
		acc := sums[path]
		if acc == nil {
			acc = make([]float32, s.dim)
			sums[path] = acc
		}
		for i, x := range vec {
			acc[i] += x
		}
		counts[path]++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating note vectors: %w", err)
	}

	out := make(map[string][]float32, len(sums))
	for path, acc := range sums {
		n := float32(counts[path])
		for i := range acc {
			acc[i] /= n // mean of the chunk vectors.
		}
		if normalize(acc) { // skip notes with a zero centroid (no direction).
			out[path] = acc
		}
	}
	return out, nil
}

// serializeFloat32 encodes a vector as the little-endian float32 blob stored
// in the embedding column (the inverse of deserializeFloat32).
func serializeFloat32(vec []float32) []byte {
	buf := make([]byte, len(vec)*4)
	for i, x := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(x))
	}
	return buf
}

// deserializeFloat32 decodes an embedding blob (little-endian float32) into a
// dim-length vector.
func deserializeFloat32(blob []byte, dim int) ([]float32, error) {
	if len(blob) != dim*4 {
		return nil, fmt.Errorf("embedding blob is %d bytes, want %d (dim %d)", len(blob), dim*4, dim)
	}
	vec := make([]float32, dim)
	if err := binary.Read(bytes.NewReader(blob), binary.LittleEndian, vec); err != nil {
		return nil, fmt.Errorf("decoding embedding: %w", err)
	}
	return vec, nil
}

// normalize scales v to unit length in place, reporting false (leaving v
// unchanged) when v is the zero vector and has no direction to normalize.
func normalize(v []float32) bool {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return false
	}
	inv := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
	return true
}
