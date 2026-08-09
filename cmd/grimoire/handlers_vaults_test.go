package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// vaultsEnv is the sidebar's Vaults tab wired the way the daemon wires it: the
// registry-backed API behind the render/add/forget routes.
type vaultsEnv struct {
	mux *http.ServeMux
	reg *vaultRegistry
}

func newVaultsEnv(t *testing.T) vaultsEnv {
	t.Helper()
	reg := newTestRegistry(t)
	api := grimoireapi.New(reg.runtimeOrLast, reg.open).WithVaultRegistry(reg.live, reg.close)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/vaults/add", openVaultHandler(reg, zerolog.Nop()))
	mux.HandleFunc("GET /api/vaults/render", vaultsRenderHandler(api, zerolog.Nop()))
	mux.HandleFunc("POST /api/vaults/forget", forgetVaultHandler(api, zerolog.Nop()))
	return vaultsEnv{mux: mux, reg: reg}
}

// render returns the SSE body of the vault list fragment.
func (e vaultsEnv) render(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/vaults/render", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	return rec.Body.String()
}

// postForm drives one of the tab's form-encoded actions.
func (e vaultsEnv) postForm(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

// TestVaultsRenderHandler checks the fragment the tab paints on load: a row per
// known vault, the current one marked, and an unavailable vault still listed —
// it can only be forgotten if it shows up.
func TestVaultsRenderHandler(t *testing.T) {
	env := newVaultsEnv(t)
	ctx := context.Background()
	first, second := tempVault(t), tempVault(t)
	require.NoError(t, env.reg.open(ctx, first))
	require.NoError(t, env.reg.open(ctx, second))

	body := env.render(t)
	require.Contains(t, body, `data-vault-path="`+first)
	require.Contains(t, body, `data-vault-path="`+second)
	require.Contains(t, body, "g-vault-row-current", "the page's vault is highlighted")
	require.Contains(t, body, `data-vault-available="true"`)
	require.NotContains(t, body, `data-vault-available="false"`)
}

// TestVaultsAddHandler covers the tab's "Add vault…": a posted path registers the
// vault and reloads onto it, and a client with no native folder dialog is told to
// ask for a path instead of being left with a silent no-op.
func TestVaultsAddHandler(t *testing.T) {
	env := newVaultsEnv(t)
	vault := tempVault(t)

	rec := env.postForm(t, "/api/vaults/add", "path="+url.QueryEscape(vault))
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"ok":true,"reload":true,"vault":"`+vault+`"}`, rec.Body.String(),
		"the answer names the vault, so the page navigates there instead of reloading its own ?vault=")

	last, err := vaultdir.LastVault()
	require.NoError(t, err)
	require.Equal(t, vault, last, "the added vault becomes the default")
	require.Contains(t, env.render(t), `data-vault-path="`+vault, "and joins the list")

	rec = env.postForm(t, "/api/vaults/add", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"ok":false,"needsPath":true}`, rec.Body.String(),
		"no window is attached in a test, as in a browser client")
}

// TestVaultsForgetHandler checks that forgetting takes the row off the list and
// stops the runtime while leaving the folder untouched — forget is not delete.
func TestVaultsForgetHandler(t *testing.T) {
	env := newVaultsEnv(t)
	ctx := context.Background()
	keep, drop := tempVault(t), tempVault(t)
	require.NoError(t, env.reg.open(ctx, keep))
	require.NoError(t, env.reg.open(ctx, drop))
	require.Len(t, env.reg.live(), 2)

	rec := env.postForm(t, "/api/vaults/forget", "path="+url.QueryEscape(drop))
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"ok":true}`, rec.Body.String())

	body := env.render(t)
	require.Contains(t, body, `data-vault-path="`+keep)
	require.NotContains(t, body, `data-vault-path="`+drop)
	require.DirExists(t, drop, "the folder and its notes stay on disk")
	require.NotContains(t, env.reg.live(), mustCanonical(t, drop), "its runtime is retired")

	rec = env.postForm(t, "/api/vaults/forget", "")
	require.Equal(t, http.StatusBadRequest, rec.Code, "a forget needs to name a vault")
}
