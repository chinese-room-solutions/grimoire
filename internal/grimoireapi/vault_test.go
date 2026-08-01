package grimoireapi

import (
	"context"
	"errors"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// stubAPI builds an API whose service binding is driven by a tiny in-memory
// holder, so the vault ops can be tested without a real backend. bound tracks the
// currently bound vault path; bind/unbind mutate it and a matching service.
type stubBinding struct {
	bound string
	svc   *app.Service
}

func newStubAPI(t *testing.T) (*API, *stubBinding) {
	t.Helper()
	b := &stubBinding{}
	api := New(
		func() (*app.Service, error) {
			if b.svc == nil {
				return nil, app.ErrNoVault
			}
			return b.svc, nil
		},
		func(_ context.Context, vault string) error {
			svc := app.New(nil, t.TempDir(), t.TempDir(), vault, t.TempDir(), "", zerolog.Nop())
			t.Cleanup(func() { _ = svc.Close() })
			b.bound = vault
			b.svc = svc
			return nil
		},
		func() error {
			b.bound = ""
			b.svc = nil
			return nil
		},
	)
	return api, b
}

func TestOpenAndCurrentVault(t *testing.T) {
	api, _ := newStubAPI(t)
	ctx := context.Background()

	_, ok := api.CurrentVault(ctx)
	require.False(t, ok, "no vault open initially")

	v, err := api.OpenVault(ctx, "/vaults/notes")
	require.NoError(t, err)
	require.Equal(t, "/vaults/notes", v.Path)
	require.True(t, v.Current)

	got, ok := api.CurrentVault(ctx)
	require.True(t, ok)
	require.Equal(t, "/vaults/notes", got.Path)
}

func TestSwitchVaultReplaces(t *testing.T) {
	api, b := newStubAPI(t)
	ctx := context.Background()
	_, err := api.OpenVault(ctx, "/vaults/a")
	require.NoError(t, err)
	v, err := api.SwitchVault(ctx, "/vaults/b")
	require.NoError(t, err)
	require.Equal(t, "/vaults/b", v.Path)
	require.Equal(t, "/vaults/b", b.bound, "switching replaces the bound vault")
}

func TestCloseVault(t *testing.T) {
	api, _ := newStubAPI(t)
	ctx := context.Background()
	_, err := api.OpenVault(ctx, "/vaults/a")
	require.NoError(t, err)
	require.NoError(t, api.CloseVault(ctx))
	_, ok := api.CurrentVault(ctx)
	require.False(t, ok, "vault is closed")
}

func TestOpenVaultRequiresPath(t *testing.T) {
	api, _ := newStubAPI(t)
	_, err := api.OpenVault(context.Background(), "")
	require.Error(t, err)
}

func TestVaultOpsUnsupportedWhenStatic(t *testing.T) {
	// A static API has no bind/unbind hooks: the runtime vault ops report it.
	api := NewStatic(app.New(nil, t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir(), "", zerolog.Nop()))
	_, err := api.OpenVault(context.Background(), "/vaults/a")
	require.True(t, errors.Is(err, ErrSwitchUnsupported))
	require.True(t, errors.Is(api.CloseVault(context.Background()), ErrSwitchUnsupported))
}
