package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// newVaultMux mounts the JSON API over the real registry, so the vault-navigation
// endpoints run against the daemon's own resolution.
func newVaultMux(t *testing.T) *http.ServeMux {
	t.Helper()
	mux, _ := newVaultMuxReg(t)
	return mux
}

// newVaultMuxReg is newVaultMux plus the registry behind it, for the endpoints
// whose effect shows up in the resident runtimes.
func newVaultMuxReg(t *testing.T) (*http.ServeMux, *vaultRegistry) {
	t.Helper()
	reg := newTestRegistry(t)
	api := grimoireapi.New(reg.runtimeOrLast, reg.open).WithVaultRegistry(reg.live, reg.close)
	mux := http.NewServeMux()
	mountAPI(mux, api, testControl(), zerolog.Nop())
	return mux, reg
}

// TestAPIVaultsListsStatus checks the wrapped listing an agent reads: one entry
// per known vault, with the status fields the management surfaces render.
func TestAPIVaultsListsStatus(t *testing.T) {
	mux, _ := newVaultMuxReg(t)
	vault := tempVault(t)
	require.Equal(t, http.StatusOK,
		doJSON(t, mux, http.MethodPost, "/api/v1/vault/open", map[string]string{"path": vault}).Code)

	rec := doGET(t, mux, "/api/v1/vaults")
	require.Equal(t, http.StatusOK, rec.Code)
	var got struct {
		Vaults []grimoireapi.Vault `json:"vaults"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Vaults, 1)
	require.Equal(t, vault, got.Vaults[0].Path)
	require.True(t, got.Vaults[0].Current)
	require.True(t, got.Vaults[0].Available)
}

// TestAPIForgetVault covers the JSON forget: the vault leaves the list and its
// runtime is retired, the folder stays, and a path nobody knows is a 200 no-op
// (the end state is what the caller asked for).
func TestAPIForgetVault(t *testing.T) {
	mux, reg := newVaultMuxReg(t)
	vault := tempVault(t)
	require.Equal(t, http.StatusOK,
		doJSON(t, mux, http.MethodPost, "/api/v1/vault/open", map[string]string{"path": vault}).Code)
	require.Len(t, reg.live(), 1)

	rec := doJSON(t, mux, http.MethodPost, "/api/v1/vault/forget", map[string]string{"path": vault})
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), vault)
	require.Empty(t, reg.live(), "the forgotten vault stops being served")
	require.DirExists(t, vault, "forgetting deletes nothing")

	rec = doGET(t, mux, "/api/v1/vaults")
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), vault)

	require.Equal(t, http.StatusOK,
		doJSON(t, mux, http.MethodPost, "/api/v1/vault/forget", map[string]string{"path": vault}).Code,
		"forgetting again is a no-op, not an error")
	require.Equal(t, http.StatusBadRequest,
		doJSON(t, mux, http.MethodPost, "/api/v1/vault/forget", map[string]string{}).Code)
}

func TestAPIOpenAndSwitchVault(t *testing.T) {
	mux := newVaultMux(t)
	first, second := tempVault(t), tempVault(t)

	// current → nothing is the default yet.
	rec := doGET(t, mux, "/api/v1/vault/current")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"open":false`)

	// open → the vault becomes the default.
	rec = doJSON(t, mux, http.MethodPost, "/api/v1/vault/open", map[string]string{"path": first})
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), first)
	last, err := vaultdir.LastVault()
	require.NoError(t, err)
	require.Equal(t, first, last)

	// switch → the default moves; the first vault is still served.
	rec = doJSON(t, mux, http.MethodPost, "/api/v1/vault/switch", map[string]string{"path": second})
	require.Equal(t, http.StatusOK, rec.Code)
	last, err = vaultdir.LastVault()
	require.NoError(t, err)
	require.Equal(t, second, last)
	require.Equal(t, http.StatusOK, doGET(t, mux, "/api/v1/vault?vault="+first).Code)
}

func TestAPIOpenVaultRequiresPath(t *testing.T) {
	rec := doJSON(t, newVaultMux(t), http.MethodPost, "/api/v1/vault/open", map[string]string{})
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// A vault whose folder isn't there is 503: the daemon can't serve it, but it may
// come back (an unmounted drive), so it isn't the caller's mistake.
func TestAPIUnavailableVaultIs503(t *testing.T) {
	mux := newVaultMux(t)
	rec := doJSON(t, mux, http.MethodPost, "/api/v1/vault/open", map[string]string{"path": "/definitely/not/here"})
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Equal(t, http.StatusServiceUnavailable, doGET(t, mux, "/api/v1/vault?vault=/definitely/not/here").Code)
}

func TestAPIVaultOpsUnsupportedWhenStatic(t *testing.T) {
	// The standard newAPIMux uses a static API (no open hook); open reports 501.
	mux := newAPIMux(t, nil)
	rec := doJSON(t, mux, http.MethodPost, "/api/v1/vault/open", map[string]string{"path": "/x"})
	require.Equal(t, http.StatusNotImplemented, rec.Code)
}
