package main

import (
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
	reg := newTestRegistry(t)
	mux := http.NewServeMux()
	mountAPI(mux, grimoireapi.New(reg.runtimeOrLast, reg.open), zerolog.Nop())
	return mux
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
