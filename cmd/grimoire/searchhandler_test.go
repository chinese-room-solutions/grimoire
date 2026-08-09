package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/ui"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// searchEnv is the GUI search route over two indexed vaults on one daemon.
type searchEnv struct {
	mux        *http.ServeMux
	reg        *vaultRegistry
	work, home string
}

func newSearchEnv(t *testing.T) searchEnv {
	t.Helper()
	gw := newEmbedServer(t)
	reg := newEmbedRegistry(t, gw)
	env := searchEnv{
		mux:  http.NewServeMux(),
		reg:  reg,
		work: openIndexedVault(t, reg, "model-x", map[string]string{"specs/alpha.md": "# Spec\n\nthe alpha protocol\n"}),
		home: openIndexedVault(t, reg, "model-x", map[string]string{"diary.md": "# Diary\n\nalpha again today\n"}),
	}
	env.mux.HandleFunc("POST /action/search", searchHandler(reg, zerolog.Nop()))
	return env
}

// search posts the page's signals to the route and returns the SSE body.
func (e searchEnv) search(t *testing.T, pageVault string, thisVaultOnly bool) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"gVault":           pageVault,
		"gQuery":           "alpha",
		"gSeq":             "1",
		"gSearchK":         10,
		"gSearchThisVault": thisVaultOnly,
	})
	require.NoError(t, err)
	rec := postSignals(t, e.mux, "/action/search", string(body))
	require.Equal(t, http.StatusOK, rec.Code)
	return rec.Body.String()
}

// The GUI's default is the same as everywhere else: one query, every vault, each
// hit labelled with the vault it came from. Ticking "this vault only" narrows it
// to the page's.
func TestSearchHandler_SpansVaultsUnlessNarrowed(t *testing.T) {
	env := newSearchEnv(t)
	workName, homeName := filepath.Base(env.work), filepath.Base(env.home)

	body := env.search(t, env.work, false)
	require.Contains(t, body, "specs/alpha.md")
	require.Contains(t, body, "diary.md")
	require.Contains(t, body, workName+" ›", "a hit says which vault it came from")
	require.Contains(t, body, homeName+" ›")
	require.Contains(t, body, `data-vault="`+env.home, "a foreign hit carries its vault, to navigate there")

	narrowed := env.search(t, env.work, true)
	require.Contains(t, narrowed, "specs/alpha.md")
	require.NotContains(t, narrowed, "diary.md", "the other vault is out of scope")
}

// A search spanning two embedding models shows two folded blocks rather than
// one list, records the model with every hit, and replays into the same blocks.
func TestSearchHandler_FoldsAndReplaysModelGroups(t *testing.T) {
	gw := newEmbedServer(t)
	reg := newEmbedRegistry(t, gw)
	work := openIndexedVault(t, reg, "model-x", map[string]string{"specs/alpha.md": "# Spec\n\nthe alpha protocol\n"})
	old := openIndexedVault(t, reg, "model-y", map[string]string{"diary.md": "# Diary\n\nalpha again today\n"})
	env := searchEnv{mux: http.NewServeMux(), reg: reg, work: work, home: old}
	env.mux.HandleFunc("POST /action/search", searchHandler(reg, zerolog.Nop()))

	body := env.search(t, work, false)
	require.Contains(t, body, "sl-details", "two models, two blocks")
	require.Contains(t, body, "model-x")
	require.Contains(t, body, "model-y")

	svc, err := reg.runtime(t.Context(), work)
	require.NoError(t, err)
	turns, err := svc.SessionTurns(svc.ActiveSession())
	require.NoError(t, err)
	require.Len(t, turns, 1)

	models := map[string]string{}
	for _, h := range turns[0].Hits {
		models[h.Vault] = h.Model
	}
	require.Equal(t, map[string]string{work: "model-x", old: "model-y"}, models)

	var buf strings.Builder
	require.NoError(t, ui.Conversation(toUITurns(turns, work)).Render(t.Context(), &buf))
	require.Contains(t, buf.String(), "sl-details", "a replayed turn folds like the live one")
	require.Contains(t, buf.String(), "model-y")
}

// A cross-vault turn goes into the history with each hit's own vault, so
// reopening the session still says where every result lives.
func TestSearchHandler_RecordsEachHitsVault(t *testing.T) {
	env := newSearchEnv(t)
	env.search(t, env.work, false)

	svc, err := env.reg.runtime(t.Context(), env.work)
	require.NoError(t, err)
	turns, err := svc.SessionTurns(svc.ActiveSession())
	require.NoError(t, err)
	require.Len(t, turns, 1)

	vaults := map[string]string{}
	for _, h := range turns[0].Hits {
		vaults[h.Vault] = h.Path
	}
	require.Equal(t, map[string]string{env.work: "specs/alpha.md", env.home: "diary.md"}, vaults)
}
