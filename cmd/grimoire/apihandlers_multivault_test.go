package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// newMultiVaultMux mounts the JSON API over the real registry — one daemon, many
// vaults — so the ?vault= routing is exercised end to end rather than stubbed.
func newMultiVaultMux(t *testing.T) *http.ServeMux {
	t.Helper()
	reg := newTestRegistry(t)
	mux := http.NewServeMux()
	mountAPI(mux, grimoireapi.New(reg.runtimeOrLast, reg.open), testControl(), zerolog.Nop())
	return mux
}

// seedVault makes a vault folder holding the given notes ({relPath: content}).
func seedVault(t *testing.T, notes map[string]string) string {
	t.Helper()
	vault := tempVault(t)
	for rel, content := range notes {
		full := filepath.Join(vault, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}
	return vault
}

// One daemon, two vaults: each request's ?vault= decides which one it reads and
// writes, and nothing leaks between them.
func TestAPIRoutesEachRequestToItsVault(t *testing.T) {
	mux := newMultiVaultMux(t)
	alpha := seedVault(t, map[string]string{"only-alpha.md": "# Alpha\n"})
	beta := seedVault(t, map[string]string{"only-beta.md": "# Beta\n"})

	q := func(path, vault string) string { return path + "?vault=" + url.QueryEscape(vault) }

	t.Run("note get", func(t *testing.T) {
		rec := doGET(t, mux, q("/api/v1/note", alpha)+"&path=only-alpha.md")
		require.Equal(t, http.StatusOK, rec.Code)
		var note grimoireapi.Note
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &note))
		require.Equal(t, "# Alpha\n", note.Content)

		// The same note path in the other vault isn't there.
		require.Equal(t, http.StatusNotFound, doGET(t, mux, q("/api/v1/note", beta)+"&path=only-alpha.md").Code)
	})

	t.Run("vault tree", func(t *testing.T) {
		require.Contains(t, doGET(t, mux, q("/api/v1/vault", alpha)).Body.String(), "only-alpha")
		require.NotContains(t, doGET(t, mux, q("/api/v1/vault", beta)).Body.String(), "only-alpha")
	})

	t.Run("write lands in the named vault only", func(t *testing.T) {
		rec := doJSON(t, mux, http.MethodPost, q("/api/v1/note", beta),
			map[string]any{"path": "written.md", "content": "# Written\n"})
		require.Equal(t, http.StatusOK, rec.Code)
		require.FileExists(t, filepath.Join(beta, "written.md"))
		require.NoFileExists(t, filepath.Join(alpha, "written.md"))
	})

	t.Run("search answers per vault", func(t *testing.T) {
		// No gateway in tests, so the index never opens: what matters here is that
		// each request resolves its own vault rather than 503-ing on "no vault".
		for _, vault := range []string{alpha, beta} {
			rec := doGET(t, mux, q("/api/v1/search", vault)+"&q=alpha")
			require.NotEqual(t, http.StatusNotFound, rec.Code)
			require.NotContains(t, rec.Body.String(), "no vault")
		}
	})

	t.Run("reindex answers per vault", func(t *testing.T) {
		for _, vault := range []string{alpha, beta} {
			rec := doJSON(t, mux, http.MethodPost, q("/api/v1/reindex", vault), map[string]any{"force": false})
			require.NotEqual(t, http.StatusNotFound, rec.Code)
			require.NotContains(t, rec.Body.String(), "no vault")
		}
	})
}

// The GUI routes take their vault from the page's gVault signal, which Datastar
// puts in the `datastar` query parameter on a GET. This drives a real mounted
// route to prove the signal survives the SDK round trip — the templ @get URLs
// carry no ?vault= of their own and depend on it.
func TestGUIRouteResolvesTheVaultFromTheSignal(t *testing.T) {
	reg := newTestRegistry(t)
	alpha := seedVault(t, map[string]string{"only-alpha.md": "# Alpha\n"})
	beta := seedVault(t, map[string]string{"only-beta.md": "# Beta\n"})

	mux := http.NewServeMux()
	vaultMux{mux: mux, reg: reg, logger: zerolog.Nop()}.handle("GET /api/files/render", filesRenderHandler)

	render := func(vault string) string {
		signals, err := json.Marshal(map[string]string{"gVault": vault})
		require.NoError(t, err)
		return doGET(t, mux, "/api/files/render?datastar="+url.QueryEscape(string(signals))).Body.String()
	}

	require.Contains(t, render(alpha), "only-alpha")
	require.NotContains(t, render(alpha), "only-beta")
	require.Contains(t, render(beta), "only-beta")

	// No signal and no last-used vault: the route says so instead of guessing.
	rec := doGET(t, mux, "/api/files/render")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// A request that names no vault falls back to the last-used one, so an agent that
// never passes --vault keeps working.
func TestAPIFallsBackToTheLastUsedVault(t *testing.T) {
	reg := newTestRegistry(t)
	mux := http.NewServeMux()
	mountAPI(mux, grimoireapi.New(reg.runtimeOrLast, reg.open), testControl(), zerolog.Nop())

	vault := seedVault(t, map[string]string{"n.md": "# N\n"})
	require.NoError(t, vaultdir.SetLastVault(vault))

	rec := doGET(t, mux, "/api/v1/note?path=n.md")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "# N")
}

// With no vault named and none ever opened, the API says so rather than guessing.
func TestAPIWithoutAnyVaultIs503(t *testing.T) {
	mux := newMultiVaultMux(t)
	rec := doGET(t, mux, "/api/v1/note?path=n.md")
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
