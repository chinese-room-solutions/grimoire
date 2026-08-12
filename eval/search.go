package main

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/store"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"
)

// searcher runs one query in either mode against the eval index. It owns a
// second, read-only connection to the same database file, opened lazily: the
// experimental keyword variants are SQL the store doesn't offer, and the eval
// index is never written while queries run.
type searcher struct {
	st *store.Store
	o  options
	ro *sql.DB
}

func (s *searcher) close() error {
	if s.ro == nil {
		return nil
	}
	return s.ro.Close()
}

// prod is the baseline: exactly the search the GUI and CLI run against a single
// vault, with the app's own relevance policy.
func (s *searcher) prod(query string, qvec []float32) ([]store.Hit, error) {
	return s.st.Search(query, qvec, app.SearchOptions(resultK, 0))
}

// legs is the tuning playground: the raw retrieval legs, re-fused here under
// the harness's knobs. With every knob at its default it reproduces prod — the
// store skips the band in SearchLegs, so the harness applies it (see parity).
func (s *searcher) legs(query string, qvec []float32) ([]store.Hit, error) {
	opts := store.SearchOptions{K: s.o.poolK, MinSim: s.o.minSim, TopRatio: s.o.topRatio}
	vec, fts, err := s.st.SearchLegs(query, qvec, opts)
	if err != nil {
		return nil, err
	}
	if s.o.band {
		vec = store.Band(vec, func(h store.Hit) float64 { return h.Similarity }, opts)
	}
	if s.o.ownKeywordLeg() {
		if fts, err = s.keywordLeg(query, searchPool(s.o.poolK)); err != nil {
			return nil, err
		}
	}
	return fuse(vec, fts, s.o), nil
}

// searchPool mirrors the store's per-leg candidate pool for a K-result search.
func searchPool(k int) int {
	if k <= 0 {
		k = resultK
	}
	return max(4*k, 40)
}

// fuse merges the two legs with weighted Reciprocal Rank Fusion, mirroring
// store.fuse: 1/(rrfK+rank) per leg, ties broken towards the keyword side
// (an exact term match is a precise signal, the top of a compressed similarity
// band is not), then adjacent-window duplicates dropped and the top K kept.
func fuse(vec, fts []store.Hit, o options) []store.Hit {
	type entry struct {
		hit     store.Hit
		score   float64
		fts     float64 // the keyword leg's share of score, for tie-breaking.
		vecRank int
		ftsRank int
	}
	byID := make(map[int64]*entry, len(vec)+len(fts))
	at := func(h store.Hit) *entry {
		e := byID[h.ID]
		if e == nil {
			e = &entry{hit: h}
			byID[h.ID] = e
		}
		return e
	}
	for rank, h := range vec {
		e := at(h)
		e.score += o.vecWeight / (o.rrfK + float64(rank) + 1)
		e.hit.Similarity = h.Similarity
		e.vecRank = rank + 1
	}
	for rank, h := range fts {
		e := at(h)
		contrib := o.ftsWeight / (o.rrfK + float64(rank) + 1)
		e.score += contrib
		e.fts = contrib
		e.ftsRank = rank + 1
	}

	hits := make([]store.Hit, 0, len(byID))
	for _, e := range byID {
		h := e.hit
		h.VecRank, h.FTSRank = e.vecRank, e.ftsRank
		hits = append(hits, h)
	}
	sort.Slice(hits, func(i, j int) bool {
		ei, ej := byID[hits[i].ID], byID[hits[j].ID]
		if ei.score != ej.score {
			return ei.score > ej.score
		}
		if ei.fts != ej.fts {
			return ei.fts > ej.fts
		}
		if hits[i].Similarity != hits[j].Similarity {
			return hits[i].Similarity > hits[j].Similarity
		}
		if hits[i].Path != hits[j].Path {
			return hits[i].Path < hits[j].Path
		}
		return hits[i].Index < hits[j].Index
	})
	hits = dedupeAdjacent(hits)
	if len(hits) > resultK {
		hits = hits[:resultK]
	}
	return hits
}

// dedupeAdjacent drops a hit when a better-ranked hit from the same note is an
// adjacent or identical window, as the store does.
func dedupeAdjacent(hits []store.Hit) []store.Hit {
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

// keywordLeg is the harness's own BM25 leg, run as read-only SQL against the
// same FTS index the store queries, so a variant can change the MATCH
// expression and the column weights without touching production code.
func (s *searcher) keywordLeg(query string, pool int) ([]store.Hit, error) {
	terms := ftsTerms(query)
	if s.o.ftsVariant == variantStopdrop {
		terms = dropStopwords(terms)
	}
	if len(terms) == 0 {
		return nil, nil
	}
	db, err := s.readOnly()
	if err != nil {
		return nil, err
	}

	var ids []int64
	if s.o.ftsVariant == variantAndOr {
		// Every term present is the precise reading of the query; take those
		// first and only then relax to OR for the remaining pool slots.
		if ids, err = s.matchIDs(db, join(terms, " AND "), pool); err != nil {
			return nil, err
		}
	}
	if len(ids) < pool {
		rest, err := s.matchIDs(db, join(terms, " OR "), pool)
		if err != nil {
			return nil, err
		}
		seen := make(map[int64]bool, len(ids))
		for _, id := range ids {
			seen[id] = true
		}
		for _, id := range rest {
			if len(ids) == pool {
				break
			}
			if !seen[id] {
				ids = append(ids, id)
			}
		}
	}
	return s.loadHits(db, ids)
}

// matchIDs runs one FTS5 MATCH in BM25 order under the configured column
// weights (path, heading, text).
func (s *searcher) matchIDs(db *sql.DB, expr string, pool int) ([]int64, error) {
	rows, err := db.Query(`
SELECT rowid FROM chunks_fts
WHERE chunks_fts MATCH ?
ORDER BY bm25(chunks_fts, ?, ?, ?)
LIMIT ?`, expr, s.o.bm25[0], s.o.bm25[1], s.o.bm25[2], pool)
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

// loadHits loads the chunk metadata for the given ids, preserving their rank
// order and stamping the 1-based keyword rank fusion needs.
func (s *searcher) loadHits(db *sql.DB, ids []int64) ([]store.Hit, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.Query(
		"SELECT id, path, idx, heading, text FROM chunks WHERE id IN (?"+
			strings.Repeat(",?", len(ids)-1)+")", args...)
	if err != nil {
		return nil, fmt.Errorf("loading chunks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	byID := make(map[int64]store.Chunk, len(ids))
	for rows.Next() {
		var c store.Chunk
		if err := rows.Scan(&c.ID, &c.Path, &c.Index, &c.Heading, &c.Text); err != nil {
			return nil, fmt.Errorf("scanning chunk: %w", err)
		}
		byID[c.ID] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating chunks: %w", err)
	}

	out := make([]store.Hit, 0, len(ids))
	for _, id := range ids {
		c, ok := byID[id]
		if !ok {
			continue
		}
		out = append(out, store.Hit{Chunk: c, FTSRank: len(out) + 1})
	}
	return out, nil
}

// readOnly opens (once) a read-only connection to the index, with the same
// driver and DSN shape the store uses.
func (s *searcher) readOnly() (*sql.DB, error) {
	if s.ro != nil {
		return s.ro, nil
	}
	dsn := "file:" + filepath.ToSlash(s.o.db) + "?mode=ro&_pragma=busy_timeout(5000)"
	db, err := driver.Open(dsn, fts5.Register)
	if err != nil {
		return nil, fmt.Errorf("opening %s read-only: %w", s.o.db, err)
	}
	s.ro = db
	return db, nil
}

// ftsTerms splits free text into the searchable tokens, mirroring the store's
// sanitizer: whitespace-separated, tokens with no letter or digit dropped.
// Quoting happens at join time so a variant can filter on the raw token.
func ftsTerms(query string) []string {
	var terms []string
	for _, tok := range strings.Fields(query) {
		if !strings.ContainsFunc(tok, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
			continue
		}
		terms = append(terms, tok)
	}
	return terms
}

// join quotes each term as an FTS5 phrase (immune to the MATCH grammar's
// operators and punctuation) and joins them with the given operator.
func join(terms []string, op string) string {
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
	}
	return strings.Join(quoted, op)
}

// stopwords are the high-frequency English words the stopdrop variant removes:
// they match half the corpus, so BM25 spends the pool on them instead of on the
// terms that carry the query. Kept deliberately small and hand-picked. The
// store's own leg now drops stopwords too (store.ftsStopwords); this list stays
// so the variant can be re-tuned independently of it.
var stopwords = map[string]bool{
	"a": true, "about": true, "an": true, "and": true, "any": true, "are": true,
	"as": true, "at": true, "be": true, "by": true, "can": true, "do": true,
	"does": true, "for": true, "from": true, "how": true, "i": true, "if": true,
	"in": true, "is": true, "it": true, "me": true, "my": true, "of": true,
	"on": true, "or": true, "should": true, "than": true, "that": true,
	"the": true, "there": true, "this": true, "to": true, "vs": true,
	"was": true, "what": true, "when": true, "which": true, "why": true,
	"with": true, "you": true, "your": true,
}

// dropStopwords removes stopwords, unless that would leave nothing searchable —
// a query made only of them still has to run.
func dropStopwords(terms []string) []string {
	kept := make([]string, 0, len(terms))
	for _, t := range terms {
		if !stopwords[strings.ToLower(strings.Trim(t, `.,;:!?"'()[]`))] {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		return terms
	}
	return kept
}
