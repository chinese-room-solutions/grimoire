package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/appconfig"
	"github.com/chinese-room-solutions/grimoire/internal/store"
	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
	openai "github.com/chinese-room-solutions/llama-cpp-openai-client-go"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// ── fixtures ─────────────────────────────────────────────────────────

// embedServer is a stand-in MASS gateway: it embeds text into a 3-dimensional
// "topic" vector (alpha / beta / neither), so similarity is exact and a search's
// ranking is decided by the fixture, not by a model. It counts requests per
// model, which is how the tests see that one query is embedded once per model
// however many vaults share it.
type embedServer struct {
	*httptest.Server
	mu    sync.Mutex
	calls map[string]int
}

func newEmbedServer(t *testing.T) *embedServer {
	t.Helper()
	e := &embedServer{calls: map[string]int{}}
	e.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
			Input any    `json:"input"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		e.mu.Lock()
		e.calls[req.Model]++
		e.mu.Unlock()

		var resp openai.EmbedResponse
		for i, text := range embedInputs(req.Input) {
			resp.Data = append(resp.Data, openai.EmbedItem{Index: i, Embedding: topicVector(text)})
		}
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	t.Cleanup(e.Close)
	return e
}

// resetCalls forgets the requests made while the fixtures were being indexed, so
// a test counts only what its search did.
func (e *embedServer) resetCalls() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = map[string]int{}
}

func (e *embedServer) callsFor(model string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls[model]
}

// embedInputs normalizes the request's input, which is one string or a list.
func embedInputs(input any) []string {
	switch v := input.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, _ := item.(string)
			out = append(out, s)
		}
		return out
	}
	return []string{""}
}

// topicVector maps text to one of three orthogonal directions, so a query about
// "alpha" matches alpha text exactly (1.0) and everything else not at all (0.0).
func topicVector(text string) []float64 {
	switch {
	case strings.Contains(strings.ToLower(text), "alpha"):
		return []float64{1, 0, 0}
	case strings.Contains(strings.ToLower(text), "beta"):
		return []float64{0, 1, 0}
	default:
		return []float64{0, 0, 1}
	}
}

// newEmbedRegistry builds a registry whose shared state talks to the stub
// gateway, so its vaults can open real indexes.
func newEmbedRegistry(t *testing.T, gw *embedServer) *vaultRegistry {
	t.Helper()
	isolateVaultDirs(t)
	client := app.NewGatewayClient(openai.New(openai.Options{BaseURL: gw.URL}))
	shared, err := app.NewShared(client, t.TempDir(), "", "", "", testCoreVersion, zerolog.Nop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, shared.Close()) })
	reg := newVaultRegistry(shared, zerolog.Nop())
	t.Cleanup(reg.closeAll)
	return reg
}

// openIndexedVault seeds a vault, makes it one Grimoire knows about, and brings
// its runtime up with the given embedding model ("" leaves it without one, so it
// can't answer a search). The model is persisted before the runtime starts, so
// the vault indexes itself the way the daemon does — the watcher opens the index
// and syncs — rather than through a second, racing openStore.
func openIndexedVault(t *testing.T, reg *vaultRegistry, model string, notes map[string]string) string {
	t.Helper()
	vault := seedVault(t, notes)
	require.NoError(t, vaultdir.SetLastVault(vault)) // also records it as known.
	if model != "" {
		dir, err := vaultdir.For(vault)
		require.NoError(t, err)
		require.NoError(t, appconfig.Save(dir, appconfig.Config{Vault: vault, EmbedModel: model}))
	}
	svc, err := reg.runtime(context.Background(), vault)
	require.NoError(t, err)
	if model != "" {
		require.Eventually(t, func() bool {
			n, err := svc.Count()
			return err == nil && n >= len(notes)
		}, 10*time.Second, 10*time.Millisecond, "the vault never finished indexing")
	}
	return svc.Vault()
}

// hitPaths renders a result set as "vault/path" lines, the shape a test asserts
// order on.
func hitPaths(hits []vaultHit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = vaultdir.Name(h.Vault) + "/" + h.Path
	}
	return out
}

// ── fusion ───────────────────────────────────────────────────────────

// fuseOpts are the relevance knobs the fusion tests run under, the app's own.
var fuseOpts = store.SearchOptions{K: 10, MinSim: 0.5, TopRatio: 0.88}

// vaultsOf lists a fixture's vaults the way fuseGroup takes them.
func vaultsOf(results map[string]legs) []string {
	out := make([]string, 0, len(results))
	for vault := range results {
		out = append(out, vault)
	}
	sort.Strings(out)
	return out
}

// fuseOne fuses a fixture as one model group and returns its ranking.
func fuseOne(results map[string]legs, opts store.SearchOptions) []vaultHit {
	hits, _ := fuseGroup(results, vaultsOf(results), "model-x", opts)
	return hits
}

// The regression guard: fusing one vault's legs must reproduce that vault's own
// ranking exactly. Cross-vault fusion runs on every search, so a search of a
// single vault must rank precisely as it did before there was such a thing.
func TestFuseGroup_SingleVaultReproducesTheStoreOrder(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "index.db"), 2, "")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	// A spread over both legs: keyword-only, vector-only, both, and a pair of
	// adjacent windows in one note — every branch of the store's ordering.
	notes := map[string][]store.Chunk{
		"ulid-spec.md": {{Index: 0, Text: "ULID identifiers", Vector: []float32{0, 1}}},
		"both.md":      {{Index: 0, Text: "we picked ULID", Vector: []float32{1, 0}}},
		"vec-only.md":  {{Index: 0, Text: "generic prose", Vector: []float32{0.99, 0.14106736}}},
		"windows.md": {
			{Index: 0, Text: "ULID window one", Vector: []float32{0.98, 0.19899749}},
			{Index: 1, Text: "ULID window two", Vector: []float32{0.97, 0.24310492}},
		},
	}
	for path, chunks := range notes {
		for i := range chunks {
			chunks[i].Path = path
			chunks[i].DocHash = "h"
		}
		require.NoError(t, st.ReplaceNote(path, chunks))
	}

	want, err := st.Search("ULID", []float32{1, 0}, fuseOpts)
	require.NoError(t, err)
	require.NotEmpty(t, want)

	vec, fts, err := st.SearchLegs("ULID", []float32{1, 0}, fuseOpts)
	require.NoError(t, err)
	got := fuseOne(map[string]legs{"/vaults/notes": {vec: vec, fts: fts}}, fuseOpts)
	require.Len(t, got, len(want))
	for i := range want {
		require.Equal(t, want[i].Path, got[i].Path, "position %d", i)
		require.Equal(t, want[i].Index, got[i].Index, "position %d", i)
		require.Equal(t, "/vaults/notes", got[i].Vault)
		require.Equal(t, "model-x", got[i].Model)
	}
}

// The point of the rework: vaults that share a model are one corpus, so their
// vector legs rank against each other by similarity. Under the old positional
// fusion every vault's best hit outranked every vault's second-best, however
// much weaker it was.
func TestFuseGroup_RanksBySimilarityNotVaultInterleave(t *testing.T) {
	vec := func(path string, id int64, sim float64) store.Hit {
		return store.Hit{Chunk: store.Chunk{ID: id, Path: path}, Similarity: sim}
	}
	results := map[string]legs{
		"/vaults/prep": {vec: []store.Hit{vec("prep-1.md", 1, 0.691), vec("prep-2.md", 2, 0.648)}},
		"/vaults/blog": {vec: []store.Hit{vec("blog-1.md", 1, 0.641), vec("blog-2.md", 2, 0.602)}},
	}
	require.Equal(t,
		[]string{"prep/prep-1.md", "prep/prep-2.md", "blog/blog-1.md", "blog/blog-2.md"},
		hitPaths(fuseOne(results, store.SearchOptions{K: 10, MinSim: 0.5, TopRatio: 0.8})))
}

func TestFuseGroup(t *testing.T) {
	vec := func(path string, id int64, sim float64) store.Hit {
		return store.Hit{Chunk: store.Chunk{ID: id, Path: path}, Similarity: sim}
	}
	win := func(path string, id int64, idx int, sim float64) store.Hit {
		return store.Hit{Chunk: store.Chunk{ID: id, Path: path, Index: idx}, Similarity: sim}
	}
	fts := func(path string, id int64) store.Hit {
		return store.Hit{Chunk: store.Chunk{ID: id, Path: path}}
	}

	tests := []struct {
		name    string
		results map[string]legs
		opts    store.SearchOptions
		want    []string
	}{
		{
			name: "the same note path in two vaults is two notes",
			results: map[string]legs{
				"/vaults/work": {vec: []store.Hit{vec("notes.md", 1, 0.7)}},
				"/vaults/home": {vec: []store.Hit{vec("notes.md", 1, 0.7)}},
			},
			want: []string{"home/notes.md", "work/notes.md"}, // tie broken by vault.
		},
		{
			name: "adjacent windows of one note collapse, per vault",
			results: map[string]legs{
				"/vaults/work": {vec: []store.Hit{
					win("a.md", 1, 0, 0.70), win("a.md", 2, 1, 0.69), win("a.md", 3, 5, 0.68),
				}},
			},
			want: []string{"work/a.md", "work/a.md"}, // windows 0 and 5; 1 is adjacent to 0.
		},
		{
			name: "the band is measured against the best hit in the group",
			results: map[string]legs{
				"/vaults/strong": {vec: []store.Hit{
					vec("s1.md", 1, 0.90), vec("s2.md", 2, 0.85), vec("s3.md", 3, 0.84),
				}},
				"/vaults/weak": {vec: []store.Hit{vec("w1.md", 1, 0.60), vec("w2.md", 2, 0.55)}},
			},
			// 0.90·0.88 = 0.792: the weak vault's own best is not the group's.
			// The strong vault fills the band's floor, so no weak hit rides in.
			want: []string{"strong/s1.md", "strong/s2.md", "strong/s3.md"},
		},
		{
			name: "the keyword legs interleave by position",
			results: map[string]legs{
				"/vaults/work": {fts: []store.Hit{fts("w1.md", 1), fts("w2.md", 2), fts("w3.md", 3)}},
				"/vaults/home": {fts: []store.Hit{fts("h1.md", 1)}},
			},
			want: []string{"home/h1.md", "work/w1.md", "work/w2.md", "work/w3.md"},
		},
		{
			name: "an exact keyword match outranks a vector-only hit at the same score",
			results: map[string]legs{
				"/vaults/work": {vec: []store.Hit{vec("vector.md", 1, 0.7)}},
				"/vaults/home": {fts: []store.Hit{fts("keyword.md", 1)}},
			},
			want: []string{"home/keyword.md", "work/vector.md"},
		},
		{
			name: "a hit in both legs fuses once, with both contributions",
			results: map[string]legs{
				"/vaults/work": {
					vec: []store.Hit{vec("solo.md", 1, 0.80), vec("both.md", 2, 0.75)},
					fts: []store.Hit{fts("both.md", 2)},
				},
			},
			want: []string{"work/both.md", "work/solo.md"},
		},
		{
			name: "k truncates the group's ranking, not each vault's",
			results: map[string]legs{
				"/vaults/work": {vec: []store.Hit{vec("w1.md", 1, 0.90), vec("w2.md", 2, 0.80)}},
				"/vaults/home": {vec: []store.Hit{vec("h1.md", 1, 0.85), vec("h2.md", 2, 0.82)}},
			},
			opts: store.SearchOptions{K: 2, MinSim: 0.5, TopRatio: 0.88},
			want: []string{"work/w1.md", "home/h1.md"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := tt.opts
			if opts.K == 0 {
				opts = fuseOpts
			}
			// Map iteration order must not reach the result: repeat it.
			for i := 0; i < 5; i++ {
				require.Equal(t, tt.want, hitPaths(fuseOne(tt.results, opts)), "run %d", i)
			}
		})
	}
}

// Vaults on different models can't be ranked against each other, so each model
// keeps its own group; the groups are ordered by their best fused score.
func TestFuseGroups_OneGroupPerModel(t *testing.T) {
	vec := func(path string, id int64, sim float64) store.Hit {
		return store.Hit{Chunk: store.Chunk{ID: id, Path: path}, Similarity: sim}
	}
	fts := func(path string, id int64) store.Hit {
		return store.Hit{Chunk: store.Chunk{ID: id, Path: path}}
	}
	results := map[string]legs{
		// Vector-only: one RRF leg.
		"/vaults/prep": {vec: []store.Hit{vec("prep-1.md", 1, 0.70), vec("prep-2.md", 2, 0.66)}},
		"/vaults/blog": {vec: []store.Hit{vec("blog-1.md", 1, 0.69)}},
		// Both legs: a higher best score, so this group comes first.
		"/vaults/code": {vec: []store.Hit{vec("code-1.md", 1, 0.80)}, fts: []store.Hit{fts("code-1.md", 1)}},
	}
	models := map[string]string{
		"/vaults/prep": "model-x",
		"/vaults/blog": "model-x",
		"/vaults/code": "model-y",
	}

	groups := fuseGroups(results, models, fuseOpts)
	require.Len(t, groups, 2)
	require.Equal(t, "model-y", groups[0].Model)
	require.Equal(t, []string{"/vaults/code"}, groups[0].Vaults)
	require.Equal(t, []string{"code/code-1.md"}, hitPaths(groups[0].Hits))
	require.Equal(t, "model-x", groups[1].Model)
	require.Equal(t, []string{"/vaults/blog", "/vaults/prep"}, groups[1].Vaults)
	require.Equal(t,
		[]string{"prep/prep-1.md", "blog/blog-1.md", "prep/prep-2.md"},
		hitPaths(groups[1].Hits))
	require.Equal(t, []string{"code/code-1.md", "prep/prep-1.md", "blog/blog-1.md", "prep/prep-2.md"},
		hitPaths(flatten(groups)))
}

// A model whose embedding failed still answers from its keyword leg: the group
// is there, ranked on positions alone, and its hits carry no similarity.
func TestFuseGroups_KeywordOnlyGroup(t *testing.T) {
	fts := func(path string, id int64) store.Hit {
		return store.Hit{Chunk: store.Chunk{ID: id, Path: path}}
	}
	groups := fuseGroups(
		map[string]legs{"/vaults/work": {fts: []store.Hit{fts("a.md", 1), fts("b.md", 2)}}},
		map[string]string{"/vaults/work": "model-x"},
		fuseOpts)
	require.Len(t, groups, 1)
	require.Equal(t, []string{"work/a.md", "work/b.md"}, hitPaths(groups[0].Hits))
	require.Zero(t, groups[0].Hits[0].Similarity)
	require.Equal(t, 1, groups[0].Hits[0].FTSRank)
}

// k caps every group, so a two-model search returns up to k per model rather
// than k split between rankings that can't be compared.
func TestFuseGroups_KCapsEachGroup(t *testing.T) {
	vec := func(path string, id int64, sim float64) store.Hit {
		return store.Hit{Chunk: store.Chunk{ID: id, Path: path}, Similarity: sim}
	}
	results := map[string]legs{
		"/vaults/prep": {vec: []store.Hit{vec("p1.md", 1, 0.70), vec("p2.md", 2, 0.69)}},
		"/vaults/blog": {vec: []store.Hit{vec("b1.md", 1, 0.70), vec("b2.md", 2, 0.69)}},
	}
	models := map[string]string{"/vaults/prep": "model-x", "/vaults/blog": "model-y"}

	groups := fuseGroups(results, models, store.SearchOptions{K: 1, MinSim: 0.5, TopRatio: 0.88})
	require.Len(t, groups, 2)
	for _, g := range groups {
		require.Len(t, g.Hits, 1, "group %s", g.Model)
	}
}

// ── coordination ─────────────────────────────────────────────────────

// The default: one query, every vault, one ranking — with each hit saying which
// vault it came from. Vaults sharing a model rank as one group.
func TestMultiSearch_CoversEveryVault(t *testing.T) {
	gw := newEmbedServer(t)
	reg := newEmbedRegistry(t, gw)
	work := openIndexedVault(t, reg, "model-x", map[string]string{"specs/alpha.md": "# Alpha\n\nthe alpha protocol\n"})
	home := openIndexedVault(t, reg, "model-x", map[string]string{"alpha-diary.md": "# Diary\n\nalpha again\n"})

	groups, warnings, err := multiSearch(context.Background(), reg, "alpha", 10, 0)
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Len(t, groups, 1, "one model, one group")
	require.Equal(t, "model-x", groups[0].Model)
	require.ElementsMatch(t, []string{work, home}, groups[0].Vaults)

	hits := flatten(groups)
	require.Len(t, hits, 2)
	byVault := map[string]string{}
	for _, h := range hits {
		byVault[h.Vault] = h.Path
		require.Equal(t, "model-x", h.Model)
	}
	require.Equal(t, map[string]string{work: "specs/alpha.md", home: "alpha-diary.md"}, byVault)
}

// Two models are two corpora: each keeps its own ranking, so nothing is ordered
// against a similarity it can't be compared with.
func TestMultiSearch_GroupsByModel(t *testing.T) {
	gw := newEmbedServer(t)
	reg := newEmbedRegistry(t, gw)
	openIndexedVault(t, reg, "model-x", map[string]string{"a.md": "alpha one\n"})
	openIndexedVault(t, reg, "model-y", map[string]string{"b.md": "alpha two\n"})

	groups, warnings, err := multiSearch(context.Background(), reg, "alpha", 10, 0)
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Len(t, groups, 2)

	models := []string{groups[0].Model, groups[1].Model}
	require.ElementsMatch(t, []string{"model-x", "model-y"}, models)
	for _, g := range groups {
		require.Len(t, g.Vaults, 1)
		require.Len(t, g.Hits, 1)
		require.Equal(t, g.Model, g.Hits[0].Model)
	}
}

// The query is embedded once per distinct model, not once per vault: vaults that
// share a model share the round trip.
func TestMultiSearch_EmbedsOncePerModel(t *testing.T) {
	gw := newEmbedServer(t)
	reg := newEmbedRegistry(t, gw)
	openIndexedVault(t, reg, "model-x", map[string]string{"a.md": "alpha one\n"})
	openIndexedVault(t, reg, "model-x", map[string]string{"b.md": "alpha two\n"})
	openIndexedVault(t, reg, "model-y", map[string]string{"c.md": "alpha three\n"})
	gw.resetCalls()

	groups, warnings, err := multiSearch(context.Background(), reg, "alpha", 10, 0)
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Len(t, flatten(groups), 3, "all three vaults answered")
	require.Equal(t, 1, gw.callsFor("model-x"), "two vaults, one model, one embedding")
	require.Equal(t, 1, gw.callsFor("model-y"))
}

// A vault that can't answer is named in a warning and skipped; the rest of the
// search still returns.
func TestMultiSearch_SkipsVaultsThatCannotAnswer(t *testing.T) {
	gw := newEmbedServer(t)
	reg := newEmbedRegistry(t, gw)
	good := openIndexedVault(t, reg, "model-x", map[string]string{"alpha.md": "alpha here\n"})
	openIndexedVault(t, reg, "", map[string]string{"alpha.md": "alpha there\n"}) // no model: no index.

	groups, warnings, err := multiSearch(context.Background(), reg, "alpha", 10, 0)
	require.NoError(t, err)
	hits := flatten(groups)
	require.Len(t, hits, 1)
	require.Equal(t, good, hits[0].Vault)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "no embedding model")
}

// With nothing able to answer, the search reports why instead of passing an
// empty result off as "no matches".
func TestMultiSearch_ErrorsWhenNoVaultAnswers(t *testing.T) {
	reg := newTestRegistry(t)
	openIndexedVault(t, reg, "", map[string]string{"a.md": "alpha\n"})
	openIndexedVault(t, reg, "", map[string]string{"b.md": "alpha\n"})

	_, warnings, err := multiSearch(context.Background(), reg, "alpha", 10, 0)
	require.ErrorIs(t, err, app.ErrNoModel)
	require.Len(t, warnings, 2)
}

// With no vault at all there is nothing to search, and the caller is told so
// rather than handed an empty list.
func TestMultiSearch_NoVaultsAtAll(t *testing.T) {
	reg := newTestRegistry(t)
	_, _, err := multiSearch(context.Background(), reg, "alpha", 10, 0)
	require.ErrorIs(t, err, app.ErrNoVault)
}
