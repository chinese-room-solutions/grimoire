package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/grimoireapi"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// newVaultMux mounts the JSON API over an API whose binding is driven by an
// in-memory stub, so the open/switch/close endpoints can be exercised without a
// real backend. The bound vault path is tracked in *string.
func newVaultMux(t *testing.T) (*http.ServeMux, *string) {
	t.Helper()
	var bound string
	var svc *app.Service
	api := grimoireapi.New(
		func() (*app.Service, error) {
			if svc == nil {
				return nil, app.ErrNoVault
			}
			return svc, nil
		},
		func(_ context.Context, vault string) error {
			s := app.New(testShared(t), t.TempDir(), t.TempDir(), vault, zerolog.Nop())
			t.Cleanup(func() { _ = s.Close() })
			bound = vault
			svc = s
			return nil
		},
		func() error { bound = ""; svc = nil; return nil },
	)
	mux := http.NewServeMux()
	mountAPI(mux, api, zerolog.Nop())
	return mux, &bound
}

func TestAPIOpenSwitchCloseVault(t *testing.T) {
	mux, bound := newVaultMux(t)

	// current → not open.
	rec := doGET(t, mux, "/api/v1/vault/current")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"open":false`)

	// open → binds and reports it.
	rec = doJSON(t, mux, http.MethodPost, "/api/v1/vault/open", map[string]string{"path": "/vaults/a"})
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "/vaults/a", *bound)

	// switch → rebinds.
	rec = doJSON(t, mux, http.MethodPost, "/api/v1/vault/switch", map[string]string{"path": "/vaults/b"})
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "/vaults/b", *bound)

	// close → unbinds.
	rec = doJSON(t, mux, http.MethodPost, "/api/v1/vault/close", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, *bound)
}

func TestAPIOpenVaultRequiresPath(t *testing.T) {
	mux, _ := newVaultMux(t)
	rec := doJSON(t, mux, http.MethodPost, "/api/v1/vault/open", map[string]string{})
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAPIVaultOpsUnsupportedWhenStatic(t *testing.T) {
	// The standard newAPIMux uses a static API (no bind/unbind); open reports 501.
	mux := newAPIMux(t, nil)
	rec := doJSON(t, mux, http.MethodPost, "/api/v1/vault/open", map[string]string{"path": "/x"})
	require.Equal(t, http.StatusNotImplemented, rec.Code)
}
