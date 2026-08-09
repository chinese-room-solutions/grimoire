package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	shared, err := app.NewShared(client, t.TempDir(), "", "", "", zerolog.Nop())
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

// The regression guard: re-fusing one vault's hits must reproduce that vault's
// own ranking exactly. Cross-vault fusion runs on every search, so a search of a
// single vault must rank precisely as it did before there was such a thing.
func TestFuse_SingleVaultReproducesTheStoreOrder(t *testing.T) {
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

	want, err := st.Search("ULID", []float32{1, 0}, store.SearchOptions{K: 10, MinSim: 0.5, TopRatio: 0.88})
	require.NoError(t, err)
	require.NotEmpty(t, want)

	got := fuse(map[string][]store.Hit{"/vaults/notes": want}, 10)
	require.Len(t, got, len(want))
	for i := range want {
		require.Equal(t, want[i].Path, got[i].Path, "position %d", i)
		require.Equal(t, want[i].Index, got[i].Index, "position %d", i)
		require.Equal(t, "/vaults/notes", got[i].Vault)
	}
}

func TestFuse_CrossVault(t *testing.T) {
	hit := func(path string, idx, vecRank, ftsRank int) store.Hit {
		return store.Hit{
			Chunk:   store.Chunk{Path: path, Index: idx},
			VecRank: vecRank,
			FTSRank: ftsRank,
		}
	}

	tests := []struct {
		name    string
		results map[string][]store.Hit
		k       int
		want    []string
	}{
		{
			name: "the same note path in two vaults is two notes",
			results: map[string][]store.Hit{
				"/vaults/work": {hit("notes.md", 0, 1, 0)},
				"/vaults/home": {hit("notes.md", 0, 1, 0)},
			},
			k:    10,
			want: []string{"home/notes.md", "work/notes.md"}, // tie broken by vault.
		},
		{
			name: "adjacent windows of one note collapse, per vault",
			results: map[string][]store.Hit{
				"/vaults/work": {hit("a.md", 0, 1, 0), hit("a.md", 1, 2, 0), hit("a.md", 5, 3, 0)},
			},
			k:    10,
			want: []string{"work/a.md", "work/a.md"}, // windows 0 and 5; 1 is adjacent to 0.
		},
		{
			name: "ranks fuse across vaults, best first",
			results: map[string][]store.Hit{
				"/vaults/work": {hit("weak.md", 0, 3, 0)},
				"/vaults/home": {hit("strong.md", 0, 1, 1), hit("mid.md", 0, 2, 0)},
			},
			k:    10,
			want: []string{"home/strong.md", "home/mid.md", "work/weak.md"},
		},
		{
			name: "an exact keyword match outranks a vector-only hit at the same score",
			results: map[string][]store.Hit{
				"/vaults/work": {hit("vector.md", 0, 1, 0)},
				"/vaults/home": {hit("keyword.md", 0, 0, 1)},
			},
			k:    10,
			want: []string{"home/keyword.md", "work/vector.md"},
		},
		{
			name: "k truncates the fused ranking, not each vault's",
			results: map[string][]store.Hit{
				"/vaults/work": {hit("w1.md", 0, 1, 0), hit("w2.md", 0, 4, 0)},
				"/vaults/home": {hit("h1.md", 0, 2, 0), hit("h2.md", 0, 3, 0)},
			},
			k:    2,
			want: []string{"work/w1.md", "home/h1.md"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hitPaths(fuse(tt.results, tt.k))
			require.Equal(t, tt.want, got)
			// Map iteration order must not reach the result: repeat it.
			for i := 0; i < 5; i++ {
				require.Equal(t, tt.want, hitPaths(fuse(tt.results, tt.k)), "run %d", i)
			}
		})
	}
}

// ── coordination ─────────────────────────────────────────────────────

// The default: one query, every vault, one ranking — with each hit saying which
// vault it came from.
func TestMultiSearch_CoversEveryVault(t *testing.T) {
	gw := newEmbedServer(t)
	reg := newEmbedRegistry(t, gw)
	work := openIndexedVault(t, reg, "model-x", map[string]string{"specs/alpha.md": "# Alpha\n\nthe alpha protocol\n"})
	home := openIndexedVault(t, reg, "model-x", map[string]string{"alpha-diary.md": "# Diary\n\nalpha again\n"})

	hits, warnings, err := multiSearch(context.Background(), reg, "alpha", 10, 0)
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Len(t, hits, 2)

	byVault := map[string]string{}
	for _, h := range hits {
		byVault[h.Vault] = h.Path
	}
	require.Equal(t, map[string]string{work: "specs/alpha.md", home: "alpha-diary.md"}, byVault)
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

	hits, warnings, err := multiSearch(context.Background(), reg, "alpha", 10, 0)
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Len(t, hits, 3, "all three vaults answered")
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

	hits, warnings, err := multiSearch(context.Background(), reg, "alpha", 10, 0)
	require.NoError(t, err)
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
