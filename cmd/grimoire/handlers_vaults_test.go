package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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
	api := grimoireapi.New(reg.runtimeOrLast, reg.open).
		WithVaultRegistry(reg.live, reg.close).
		WithVaultRename(reg.renameVault)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/vaults/add", openVaultHandler(reg, zerolog.Nop()))
	mux.HandleFunc("GET /api/vaults/render", vaultsRenderHandler(api, zerolog.Nop()))
	mux.HandleFunc("POST /api/vaults/forget", forgetVaultHandler(api, zerolog.Nop()))
	mux.HandleFunc("POST /api/vaults/rename", renameVaultHandler(api, zerolog.Nop()))
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

// TestVaultsRenameHandler covers the tab's rename: the folder moves on disk,
// the list follows, and the last-vault pointer moves only when it named the
// renamed vault. A resident vault is retired for the move and re-warmed at its
// new path.
func TestVaultsRenameHandler(t *testing.T) {
	env := newVaultsEnv(t)
	ctx := context.Background()
	keep, target := tempVault(t), tempVault(t)
	require.NoError(t, env.reg.open(ctx, keep))
	require.NoError(t, env.reg.open(ctx, target)) // target is the current one.
	require.Len(t, env.reg.live(), 2)

	// The daemon answers with the canonical spelling of the new path (the old
	// path's casing folds on Windows), so derive the expectation from the
	// registry's own key.
	newPath := filepath.Join(filepath.Dir(mustCanonical(t, target)), "renamed")
	rec := env.postForm(t, "/api/vaults/rename",
		"path="+url.QueryEscape(target)+"&name=renamed")
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"ok":true,"path":`+strconv.Quote(newPath)+`}`, rec.Body.String())

	require.DirExists(t, newPath, "the folder itself is renamed")
	require.NoDirExists(t, target)
	body := env.render(t)
	require.Contains(t, body, `data-vault-path="`+newPath, "the list names the new path")
	require.NotContains(t, body, `data-vault-path="`+target, "and drops the old one")
	last, err := vaultdir.LastVault()
	require.NoError(t, err)
	require.Equal(t, newPath, last, "renaming the current vault repoints the default at the new path")
	require.Contains(t, env.reg.live(), mustCanonical(t, newPath), "a resident vault is re-warmed at its new path")

	// Renaming a vault that isn't the default must not move the default.
	rec = env.postForm(t, "/api/vaults/rename",
		"path="+url.QueryEscape(keep)+"&name=keep-renamed")
	require.Equal(t, http.StatusOK, rec.Code)
	last, err = vaultdir.LastVault()
	require.NoError(t, err)
	require.Equal(t, newPath, last, "renaming another vault leaves the default alone")

	// A folder already occupying the new name is a conflict, not a merge.
	require.NoError(t, os.MkdirAll(filepath.Join(filepath.Dir(target), "occupied"), 0o755))
	rec = env.postForm(t, "/api/vaults/rename",
		"path="+url.QueryEscape(newPath)+"&name=occupied")
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.DirExists(t, newPath, "the conflicting rename moved nothing")
}

// TestVaultsRenameHandlerValidation: a name that isn't a bare folder name is
// rejected before anything touches the disk, and a missing field or a vault
// whose folder is gone is reported rather than ignored — unlike forget, a
// failed rename leaves the end state unchanged.
func TestVaultsRenameHandlerValidation(t *testing.T) {
	env := newVaultsEnv(t)
	ctx := context.Background()
	vault := tempVault(t)
	require.NoError(t, env.reg.open(ctx, vault))

	for _, tc := range []struct {
		name, body string
		wantCode   int
	}{
		{"a separator in the name", "path=" + url.QueryEscape(vault) + "&name=sub%2Frenamed", http.StatusInternalServerError},
		{"a parent hop", "path=" + url.QueryEscape(vault) + "&name=..", http.StatusInternalServerError},
		{"an empty name", "path=" + url.QueryEscape(vault) + "&name=", http.StatusBadRequest},
		{"an empty path", "path=&name=renamed", http.StatusBadRequest},
		{"a missing folder", "path=" + url.QueryEscape(filepath.Join(t.TempDir(), "gone")) + "&name=renamed", http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.postForm(t, "/api/vaults/rename", tc.body)
			require.Equal(t, tc.wantCode, rec.Code)
			require.DirExists(t, vault, "nothing moved")
			require.Len(t, env.reg.live(), 1, "the vault's own runtime is untouched")
		})
	}
}
