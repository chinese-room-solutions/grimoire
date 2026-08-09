package grimoireapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// newVaultAPI builds an API over a stub daemon: resolve hands back a service per
// vault (creating one on demand, as the registry does), and open records the
// vault as the last-used one. The real vaultdir roots are redirected to a temp
// dir first, so the last-vault pointer under test is the test's own.
func newVaultAPI(t *testing.T) *API {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CACHE_HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("AppData", root)
	t.Setenv("LocalAppData", root)

	shared := testShared(t)
	services := map[string]*app.Service{}
	resolve := func(_ context.Context, vault string) (*app.Service, error) {
		if vault == "" {
			return nil, app.ErrNoVault
		}
		if svc, ok := services[vault]; ok {
			return svc, nil
		}
		if info, err := os.Stat(vault); err != nil || !info.IsDir() {
			return nil, errStubUnavailable
		}
		svc := app.New(shared, t.TempDir(), t.TempDir(), vault, zerolog.Nop())
		t.Cleanup(func() { _ = svc.Close() })
		services[vault] = svc
		return svc, nil
	}
	return New(resolve, func(ctx context.Context, vault string) error {
		if _, err := resolve(ctx, vault); err != nil {
			return err
		}
		return vaultdir.SetLastVault(vault)
	})
}

// errStubUnavailable stands in for the daemon's "that vault isn't there" error.
var errStubUnavailable = errors.New("vault folder is unavailable")

// makeVault creates a vault folder under a temp dir and returns its path.
func makeVault(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return dir
}

func TestOpenAndCurrentVault(t *testing.T) {
	api := newVaultAPI(t)
	ctx := context.Background()

	_, ok := api.CurrentVault(ctx)
	require.False(t, ok, "nothing is the default before a vault is opened")

	vault := makeVault(t, "notes")
	v, err := api.OpenVault(ctx, vault)
	require.NoError(t, err)
	require.Equal(t, vault, v.Path)
	require.True(t, v.Current)

	got, ok := api.CurrentVault(ctx)
	require.True(t, ok)
	require.Equal(t, vault, got.Path)
}

// Opening a second vault moves the default onto it — what /vault/switch relies
// on — while the first stays reachable by name.
func TestOpenVaultMovesTheDefault(t *testing.T) {
	api := newVaultAPI(t)
	ctx := context.Background()
	first := makeVault(t, "a")
	second := makeVault(t, "b")

	_, err := api.OpenVault(ctx, first)
	require.NoError(t, err)
	v, err := api.OpenVault(ctx, second)
	require.NoError(t, err)
	require.Equal(t, second, v.Path)

	got, ok := api.CurrentVault(ctx)
	require.True(t, ok)
	require.Equal(t, second, got.Path)

	// The first vault is still served — it just isn't the default any more.
	_, err = api.ListVault(ctx, first)
	require.NoError(t, err)
}

func TestOpenVaultRequiresPath(t *testing.T) {
	api := newVaultAPI(t)
	_, err := api.OpenVault(context.Background(), "")
	require.Error(t, err)
}

func TestOpenVaultUnsupportedWhenStatic(t *testing.T) {
	// A static API has no open hook: there is no vault to switch to.
	api := NewStatic(app.New(testShared(t), t.TempDir(), t.TempDir(), t.TempDir(), zerolog.Nop()))
	_, err := api.OpenVault(context.Background(), "/vaults/a")
	require.ErrorIs(t, err, ErrSwitchUnsupported)
}

// An operation naming a vault the daemon can't serve reports the resolver's
// error verbatim, so the transport can map it (503).
func TestOperationsReportTheResolverError(t *testing.T) {
	api := newVaultAPI(t)
	_, err := api.Search(context.Background(), filepath.Join(t.TempDir(), "gone"), "q", 0)
	require.ErrorIs(t, err, errStubUnavailable)

	_, err = api.GetNote(context.Background(), "", "n.md")
	require.ErrorIs(t, err, app.ErrNoVault)
}
