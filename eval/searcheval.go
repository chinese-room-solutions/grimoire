// Command searcheval measures Grimoire's search quality against a labelled
// query set. It indexes an eval vault with the real indexer (so chunking and
// the embed-text recipe are byte-identical to production), runs each query in
// one of two modes, and scores the results against the qrels.
//
// Mode prod is the baseline: store.Search under the app's own relevance policy,
// exactly what the GUI and CLI do for a single vault. Mode legs pulls the raw
// retrieval legs and re-fuses them here, so RRF, leg weights, the relevance
// band, pool size and the keyword leg's MATCH expression are all knobs. With
// every knob at its default the two modes must agree; the harness checks that
// and warns per query where they don't.
//
// Nothing here talks to the daemon or to the user's own index: it is a tool for
// tuning retrieval, pointed at its own corpus and its own database file.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/KernelPryanic/golog"
	"github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/chunk"
	"github.com/chinese-room-solutions/grimoire/internal/embed"
	"github.com/chinese-room-solutions/grimoire/internal/index"
	"github.com/chinese-room-solutions/grimoire/internal/store"
	openai "github.com/chinese-room-solutions/llama-cpp-openai-client-go"
	"github.com/rs/zerolog"
)

// resultK is how many results a search returns and how deep the metrics look.
const resultK = 10

const (
	modeProd = "prod"
	modeLegs = "legs"

	variantOr       = "or"
	variantAndOr    = "and-or"
	variantStopdrop = "stopdrop"
	variantPrefix   = "prefix"
)

// defaultBM25 are the store's own FTS5 column weights (path, heading, text):
// the note title lives in the filename, so path outranks heading outranks body.
// Keep in step with keywordLeg's bm25() call — legs only reproduces prod while
// they match.
var defaultBM25 = [3]float64{2, 1.5, 1}

// options is the whole command line.
type options struct {
	vault   string
	queries string
	db      string
	gateway string
	model   string
	mode    string

	rrfK       float64
	vecWeight  float64
	ftsWeight  float64
	band       bool
	topRatio   float64
	minSim     float64
	poolK      int
	ftsVariant string
	bm25       [3]float64
	chunkMax   int
	chunkOver  int

	jsonOut bool
	dump    string
	locate  string
	passage bool
	parity  bool
}

// tuned reports whether any fusion knob was moved off its default. Only an
// untuned legs run is expected to reproduce prod, so only then is parity
// checked automatically.
func (o options) tuned() bool {
	return o.rrfK != 40 || o.vecWeight != 1 || o.ftsWeight != 3 ||
		!o.band || o.topRatio != app.SearchTopRatio || o.minSim != app.SearchFloor ||
		o.poolK != resultK || o.ftsVariant != variantOr || o.bm25 != defaultBM25
}

// ownKeywordLeg reports whether the keyword leg must come from the harness's own
// SQL rather than from the store — any variant but plain OR, or reweighted BM25.
// prefix mirrors the store's leg (stopwords dropped, OR join) with a trailing *
// on every term, so it needs its own MATCH expression.
func (o options) ownKeywordLeg() bool {
	return o.ftsVariant != variantOr || o.bm25 != defaultBM25
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "searcheval:", err)
		os.Exit(1)
	}
}

func parseFlags() (options, error) {
	var o options
	flag.StringVar(&o.vault, "vault", "eval/corpus", "vault directory to index and search")
	flag.StringVar(&o.queries, "queries", "eval/queries.json", "labelled query set")
	flag.StringVar(&o.db, "db", "eval/index.db", "index database file")
	flag.StringVar(&o.gateway, "gateway", "http://127.0.0.1:39456", "OpenAI-compatible embeddings endpoint")
	flag.StringVar(&o.model, "model", "qwen3-embedding-0-6b/Qwen3-Embedding-0.6B-Q8_0.gguf", "embedding model id")
	flag.StringVar(&o.mode, "mode", modeProd, "search mode: prod (baseline) or legs (re-fused here)")
	flag.Float64Var(&o.rrfK, "rrf-k", 40, "legs: RRF constant in 1/(k+rank)")
	flag.Float64Var(&o.vecWeight, "vec-weight", 1, "legs: multiplier on the vector leg's RRF contribution")
	flag.Float64Var(&o.ftsWeight, "fts-weight", 3, "legs: multiplier on the keyword leg's RRF contribution")
	flag.BoolVar(&o.band, "band", true, "legs: apply the relevance band to the vector leg")
	flag.Float64Var(&o.topRatio, "top-ratio", app.SearchTopRatio, "legs: band width as a ratio of the best vector hit")
	flag.Float64Var(&o.minSim, "min-sim", app.SearchFloor, "legs: absolute similarity floor for the vector leg")
	flag.IntVar(&o.poolK, "pool-k", resultK, "legs: K passed to SearchLegs; pool per leg is max(4K,40)")
	flag.StringVar(&o.ftsVariant, "fts-variant", variantOr,
		"legs: keyword leg — or (the store's own), and-or, or stopdrop")
	bm25 := flag.String("bm25-weights", "2,1.5,1", "legs: BM25 column weights path,heading,text")
	flag.IntVar(&o.chunkMax, "chunk-max", 0, "chunk window size override (0 = store default); forces a rebuild")
	flag.IntVar(&o.chunkOver, "chunk-overlap", 0, "chunk overlap override (0 = keep default)")
	flag.BoolVar(&o.jsonOut, "json", false, "emit the whole report as JSON")
	flag.StringVar(&o.dump, "dump", "", "print the top-10 detail for a query id, or all")
	flag.StringVar(&o.locate, "locate", "", "print where each graded note of a query id sits in each leg")
	flag.BoolVar(&o.passage, "passage", false, "score the returned passages against the query by cosine")
	flag.BoolVar(&o.parity, "parity", false, "only check that untuned legs reproduces prod")
	flag.Parse()

	if o.mode != modeProd && o.mode != modeLegs {
		return o, fmt.Errorf("unknown mode %q, want %s or %s", o.mode, modeProd, modeLegs)
	}
	switch o.ftsVariant {
	case variantOr, variantAndOr, variantStopdrop, variantPrefix:
	default:
		return o, fmt.Errorf("unknown fts-variant %q, want %s, %s, %s or %s",
			o.ftsVariant, variantOr, variantAndOr, variantStopdrop, variantPrefix)
	}
	parts := strings.Split(*bm25, ",")
	if len(parts) != 3 {
		return o, fmt.Errorf("bm25-weights wants three comma-separated numbers, got %q", *bm25)
	}
	for i, p := range parts {
		w, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return o, fmt.Errorf("bm25-weights %q: %w", *bm25, err)
		}
		o.bm25[i] = w
	}
	return o, nil
}

func run() error {
	o, err := parseFlags()
	if err != nil {
		return err
	}
	ctx := context.Background()
	logger := golog.New(true, os.Stderr).Level(zerolog.WarnLevel)

	emb := embed.New(openai.New(openai.Options{BaseURL: o.gateway}), o.model)
	dim, err := emb.Dimension(ctx)
	if err != nil {
		return fmt.Errorf("probing embedding dimension at %s: %w", o.gateway, err)
	}
	// A chunk-size override changes every chunk, so the content-hash shortcut
	// would skip re-embedding: drop the index and build it fresh.
	if o.chunkMax > 0 {
		for _, p := range []string{o.db, o.db + "-wal", o.db + "-shm"} {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("removing index for chunk override: %w", err)
			}
		}
	}
	st, err := openStore(o.db, dim, emb.DocPrefix())
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	ix := index.New(o.vault, st, emb, logger)
	if o.chunkMax > 0 {
		ix.SetChunkOptions(chunk.Options{MaxChars: o.chunkMax, Overlap: o.chunkOver})
	}
	stats, err := ix.Sync(ctx, nil, false)
	if err != nil {
		return fmt.Errorf("indexing %s: %w", o.vault, err)
	}
	fmt.Fprintf(os.Stderr, "index: %d indexed, %d skipped, %d pruned, %d chunks (dim %d)\n",
		stats.Indexed, stats.Skipped, stats.Pruned, stats.Chunks, dim)

	queries, err := loadQueries(o.queries)
	if err != nil {
		return err
	}

	s := &searcher{st: st, o: o}
	defer func() { _ = s.close() }()

	results, err := search(ctx, s, emb, queries, o)
	if err != nil {
		return err
	}
	if o.parity {
		return reportParity(results)
	}
	if o.locate != "" {
		return reportLocate(ctx, s, emb, queries, o)
	}
	if o.passage {
		return reportPassage(ctx, results, emb, o)
	}
	for i := range results {
		results[i].notes = collapse(results[i].hits(o.mode))
		results[i].scores = score(results[i].query, results[i].notes)
	}
	return report(results, o)
}

// openStore opens the eval index, rebuilding it when the on-disk fingerprint no
// longer matches the embedding configuration. The index is a derived cache, so
// a stale one is deleted rather than repaired — the same call the app makes.
func openStore(path string, dim int, docPrefix string) (*store.Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("creating index directory: %w", err)
		}
	}
	st, err := store.Open(path, dim, docPrefix)
	if err == nil {
		return st, nil
	}
	if !errors.Is(err, store.ErrIncompatible) {
		return nil, fmt.Errorf("opening index: %w", err)
	}
	fmt.Fprintf(os.Stderr, "index %s is incompatible with the current embedding configuration; rebuilding\n", path)
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("removing stale index: %w", err)
		}
	}
	st, err = store.Open(path, dim, docPrefix)
	if err != nil {
		return nil, fmt.Errorf("recreating index: %w", err)
	}
	return st, nil
}

// result is one query's outcome: the hit list from each mode that ran, and the
// note-level scoring of the mode under evaluation.
type result struct {
	query  query
	prod   []store.Hit
	legs   []store.Hit
	notes  []noteHit
	scores scores
	qvec   []float32 // the query's embedding, kept for the passage check.
}

func (r result) hits(mode string) []store.Hit {
	if mode == modeLegs {
		return r.legs
	}
	return r.prod
}

// search embeds and runs every query. Both modes run whenever parity is being
// checked — explicitly, or implicitly because an untuned legs run should
// reproduce the baseline.
func search(
	ctx context.Context, s *searcher, emb *embed.Embedder, queries []query, o options,
) ([]result, error) {
	both := o.parity || (o.mode == modeLegs && !o.tuned())
	results := make([]result, 0, len(queries))
	for _, q := range queries {
		qvec, err := emb.EmbedQuery(ctx, q.Query)
		if err != nil {
			return nil, fmt.Errorf("embedding query %q: %w", q.ID, err)
		}
		r := result{query: q, qvec: qvec}
		if both || o.mode == modeProd {
			if r.prod, err = s.prod(q.Query, qvec); err != nil {
				return nil, fmt.Errorf("prod search %q: %w", q.ID, err)
			}
		}
		if both || o.mode == modeLegs {
			if r.legs, err = s.legs(q.Query, qvec); err != nil {
				return nil, fmt.Errorf("legs search %q: %w", q.ID, err)
			}
		}
		results = append(results, r)
	}
	return results, nil
}

// notePaths is a hit list collapsed to the note paths it surfaced, in order —
// the granularity both scoring and the parity check work at.
func notePaths(hits []store.Hit) []string {
	notes := collapse(hits)
	paths := make([]string, len(notes))
	for i, n := range notes {
		paths[i] = n.Path
	}
	return paths
}

// parityBreaks lists the query ids whose two modes disagree on note ordering.
func parityBreaks(results []result) []string {
	var broken []string
	for _, r := range results {
		if !slicesEqual(notePaths(r.prod), notePaths(r.legs)) {
			broken = append(broken, r.query.ID)
		}
	}
	return broken
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// reportLocate answers "where was the target, actually?" for one query: for
// every graded note it prints the note's best (lowest) rank in each leg over
// the whole corpus — pool far beyond the fused top-10, so a note missing from
// the results shows whether a leg lost it or fusion did.
func reportLocate(ctx context.Context, s *searcher, emb *embed.Embedder, queries []query, o options) error {
	var q query
	found := false
	for _, cand := range queries {
		if cand.ID == o.locate {
			q, found = cand, true
			break
		}
	}
	if !found {
		return fmt.Errorf("no query id %q", o.locate)
	}
	qvec, err := emb.EmbedQuery(ctx, q.Query)
	if err != nil {
		return fmt.Errorf("embedding query %q: %w", q.ID, err)
	}
	// A pool covering every chunk: ranks are then corpus-true. SearchLegs
	// returns the raw legs unfused and skips the band, which is what locating
	// needs.
	opts := store.SearchOptions{K: 500, MinSim: -1, TopRatio: s.o.topRatio}
	vec, fts, err := s.st.SearchLegs(q.Query, qvec, opts)
	if err != nil {
		return fmt.Errorf("legs for %q: %w", q.ID, err)
	}
	best := map[string][2]int{} // path → {bestVecRank, bestFTSRank}, 0 = absent.
	rank := func(hits []store.Hit, which int) {
		for _, h := range hits {
			r := h.VecRank
			if which == 1 {
				r = h.FTSRank
			}
			b := best[h.Path]
			if which == 0 && (b[0] == 0 || r < b[0]) {
				b[0] = r
			}
			if which == 1 && (b[1] == 0 || r < b[1]) {
				b[1] = r
			}
			best[h.Path] = b
		}
	}
	rank(vec, 0)
	rank(fts, 1)

	paths := make([]string, 0, len(q.Relevant))
	for p := range q.Relevant {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool { return q.Relevant[paths[i]] > q.Relevant[paths[j]] })
	t := newTable("GRADE", "PATH", "VEC", "FTS")
	for _, p := range paths {
		b := best[p]
		t.row(strconv.Itoa(q.Relevant[p]), p, strconv.Itoa(b[0]), strconv.Itoa(b[1]))
	}
	fmt.Printf("locate %s: %q\n", q.ID, q.Query)
	return t.flush()
}

// reportPassage is the prompt-vs-result check: it re-embeds each query's
// returned passages as bare text (no title/heading prefix) and scores them by
// cosine against the query vector. The ranking metrics judge "the right note
// surfaced"; this judges "the passage itself is about the query" — a result can
// rank the right note via an exact keyword match while the shown chunk is the
// wrong section of it. Hit-1 cosine below app.SearchFloor is flagged, the same
// floor the vector leg applies to its own candidates.
func reportPassage(ctx context.Context, results []result, emb *embed.Embedder, o options) error {
	t := newTable("QUERY", "HIT1-COS", "MEAN5-COS", "HIT1")
	var sum1, sum5 float64
	var n int
	for _, r := range results {
		hits := r.hits(o.mode)
		if len(hits) == 0 {
			t.row(r.query.ID, "-", "-", "")
			continue
		}
		texts := make([]string, len(hits))
		for i, h := range hits {
			texts[i] = h.Text
		}
		vecs, err := emb.Embed(ctx, texts)
		if err != nil {
			return fmt.Errorf("embedding passages for %q: %w", r.query.ID, err)
		}
		cos1 := cosine(r.qvec, vecs[0])
		top := min(len(hits), 5)
		var sum float64
		for _, v := range vecs[:top] {
			sum += cosine(r.qvec, v)
		}
		mean5 := sum / float64(top)
		sum1 += cos1
		sum5 += mean5
		n++
		flag := ""
		if cos1 < app.SearchFloor {
			flag = "  <floor"
		}
		t.row(r.query.ID, strconv.FormatFloat(cos1, 'f', 4, 64),
			strconv.FormatFloat(mean5, 'f', 4, 64), hits[0].Path+flag)
	}
	if err := t.flush(); err != nil {
		return err
	}
	fmt.Printf("\npassage check over %d queries (mode %s): mean hit-1 cosine %.4f, mean top-5 cosine %.4f\n",
		n, o.mode, sum1/float64(n), sum5/float64(n))
	return nil
}

// cosine is the cosine similarity of two raw vectors (zero when either has no
// direction).
func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// reportParity is the --parity run: it prints the disagreements and fails when
// there are any, so the harness can be used as a regression check on the
// fusion this tool reimplements.
func reportParity(results []result) error {
	broken := parityBreaks(results)
	for _, id := range broken {
		for _, r := range results {
			if r.query.ID != id {
				continue
			}
			fmt.Printf("PARITY BREAK %s\n  prod: %s\n  legs: %s\n",
				id, strings.Join(notePaths(r.prod), ", "), strings.Join(notePaths(r.legs), ", "))
		}
	}
	fmt.Printf("parity: %d/%d queries agree\n", len(results)-len(broken), len(results))
	if len(broken) > 0 {
		return fmt.Errorf("%d of %d queries differ between prod and untuned legs", len(broken), len(results))
	}
	return nil
}

// dumpHit is one line of a --dump listing.
type dumpHit struct {
	Rank       int     `json:"rank"`
	Path       string  `json:"path"`
	Heading    string  `json:"heading"`
	Similarity float64 `json:"similarity"`
	VecRank    int     `json:"vec_rank"`
	FTSRank    int     `json:"fts_rank"`
	Grade      int     `json:"grade"`
}

func dumpHits(r result, mode string) []dumpHit {
	hits := r.hits(mode)
	out := make([]dumpHit, len(hits))
	for i, h := range hits {
		out[i] = dumpHit{
			Rank: i + 1, Path: h.Path, Heading: h.Heading, Similarity: h.Similarity,
			VecRank: h.VecRank, FTSRank: h.FTSRank, Grade: r.query.Relevant[h.Path],
		}
	}
	return out
}

// queryReport is one query's block of the report.
type queryReport struct {
	ID     string    `json:"id"`
	Kind   string    `json:"kind"`
	Query  string    `json:"query"`
	Scores scores    `json:"scores"`
	Hit1   string    `json:"hit1"`
	Top    []dumpHit `json:"top,omitempty"`
}

// kindReport aggregates the queries of one kind.
type kindReport struct {
	Kind    string `json:"kind"`
	Queries int    `json:"queries"`
	Scores  scores `json:"scores"`
}

type fullReport struct {
	Mode         string        `json:"mode"`
	Vault        string        `json:"vault"`
	Model        string        `json:"model"`
	Queries      []queryReport `json:"queries"`
	Aggregate    scores        `json:"aggregate"`
	ByKind       []kindReport  `json:"by_kind"`
	ParityBroken []string      `json:"parity_broken,omitempty"`
}

func buildReport(results []result, o options) fullReport {
	rep := fullReport{Mode: o.mode, Vault: o.vault, Model: o.model}
	all := make([]scores, 0, len(results))
	byKind := make(map[string][]scores)
	for _, r := range results {
		hit1 := ""
		if paths := notePaths(r.hits(o.mode)); len(paths) > 0 {
			hit1 = paths[0]
		}
		qr := queryReport{
			ID: r.query.ID, Kind: r.query.Kind, Query: r.query.Query,
			Scores: r.scores, Hit1: hit1,
		}
		if o.dump == "all" || (o.dump != "" && o.dump == r.query.ID) {
			qr.Top = dumpHits(r, o.mode)
		}
		rep.Queries = append(rep.Queries, qr)
		all = append(all, r.scores)
		byKind[r.query.Kind] = append(byKind[r.query.Kind], r.scores)
	}
	rep.Aggregate = aggregate(all)

	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		rep.ByKind = append(rep.ByKind, kindReport{
			Kind: k, Queries: len(byKind[k]), Scores: aggregate(byKind[k]),
		})
	}
	if o.mode == modeLegs && !o.tuned() {
		rep.ParityBroken = parityBreaks(results)
	}
	return rep
}

func report(results []result, o options) error {
	rep := buildReport(results, o)
	if o.jsonOut {
		data, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return fmt.Errorf("encoding report: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}

	for _, id := range rep.ParityBroken {
		fmt.Fprintf(os.Stderr, "warning: %s: untuned legs differs from prod\n", id)
	}

	t := newTable("QUERY", "KIND", "NDCG@10", "MRR@10", "R@5", "R@10", "HIT-1")
	for _, q := range rep.Queries {
		t.row(q.ID, q.Kind, fmtScore(q.Scores.NDCG10), fmtScore(q.Scores.MRR10),
			fmtScore(q.Scores.R5), fmtScore(q.Scores.R10), q.Hit1)
	}
	if err := t.flush(); err != nil {
		return fmt.Errorf("writing report: %w", err)
	}

	fmt.Printf("\naggregate over %d queries (mode %s)\n", len(rep.Queries), rep.Mode)
	fmt.Printf("  nDCG@10   %s\n  MRR@10    %s\n  Recall@5  %s\n  Recall@10 %s\n\n",
		fmtScore(rep.Aggregate.NDCG10), fmtScore(rep.Aggregate.MRR10),
		fmtScore(rep.Aggregate.R5), fmtScore(rep.Aggregate.R10))

	t = newTable("KIND", "N", "NDCG@10", "MRR@10", "R@5", "R@10")
	for _, kr := range rep.ByKind {
		t.row(kr.Kind, strconv.Itoa(kr.Queries), fmtScore(kr.Scores.NDCG10),
			fmtScore(kr.Scores.MRR10), fmtScore(kr.Scores.R5), fmtScore(kr.Scores.R10))
	}
	if err := t.flush(); err != nil {
		return fmt.Errorf("writing per-kind report: %w", err)
	}

	for _, q := range rep.Queries {
		if len(q.Top) == 0 {
			continue
		}
		fmt.Printf("\ndump %s: %q\n", q.ID, q.Query)
		t = newTable("RANK", "PATH", "HEADING", "SIM", "VEC", "FTS", "GRADE")
		for _, h := range q.Top {
			t.row(strconv.Itoa(h.Rank), h.Path, h.Heading,
				strconv.FormatFloat(h.Similarity, 'f', 4, 64),
				strconv.Itoa(h.VecRank), strconv.Itoa(h.FTSRank), strconv.Itoa(h.Grade))
		}
		if err := t.flush(); err != nil {
			return fmt.Errorf("writing dump: %w", err)
		}
	}
	return nil
}

// table is an aligned stdout table that remembers the first write failure, so
// rows are written plainly and the error is checked once, at the flush.
type table struct {
	w   *tabwriter.Writer
	err error
}

func newTable(header ...string) *table {
	t := &table{w: tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)}
	t.row(header...)
	return t
}

func (t *table) row(cells ...string) {
	if t.err != nil {
		return
	}
	_, t.err = fmt.Fprintln(t.w, strings.Join(cells, "\t"))
}

func (t *table) flush() error {
	if t.err != nil {
		return t.err
	}
	return t.w.Flush()
}

// fmtScore renders a metric, or "-" when the qrels leave it undefined.
func fmtScore(v *float64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatFloat(*v, 'f', 4, 64)
}
