package grimoireapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/appconfig"
	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// vaultStub is a stand-in for the daemon's vault registry: services are created
// on first resolve and keyed canonically, exactly as the registry keys its
// runtimes, and closed records which ones the API retired.
type vaultStub struct {
	api      *API
	services map[string]*app.Service
	closed   []string
}

// newVaultAPI builds an API over a stub daemon. See newVaultStub.
func newVaultAPI(t *testing.T) *API {
	t.Helper()
	return newVaultStub(t).api
}

// newVaultStub builds the API plus the stub behind it: resolve hands back a
// service per vault (creating one on demand, as the registry does), open records
// the vault as the last-used one, and the vault-registry seams report and retire
// the resident ones. The real vaultdir roots are redirected to a temp dir first,
// so the last-vault pointer and the known-vaults registry under test are the
// test's own.
func newVaultStub(t *testing.T) *vaultStub {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CACHE_HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("AppData", root)
	t.Setenv("LocalAppData", root)

	shared := testShared(t)
	stub := &vaultStub{services: map[string]*app.Service{}}
	resolve := func(_ context.Context, vault string) (*app.Service, error) {
		if vault == "" {
			return nil, app.ErrNoVault
		}
		key, err := vaultdir.Canonical(vault)
		if err != nil {
			return nil, err
		}
		if svc, ok := stub.services[key]; ok {
			return svc, nil
		}
		if info, err := os.Stat(vault); err != nil || !info.IsDir() {
			return nil, errStubUnavailable
		}
		// The real per-vault dirs, so the service reads the same config the
		// listing does for a vault it hasn't opened.
		dir, err := vaultdir.For(vault)
		require.NoError(t, err)
		cacheDir, err := vaultdir.CacheFor(vault)
		require.NoError(t, err)
		svc := app.New(shared, dir, cacheDir, vault, zerolog.Nop())
		t.Cleanup(func() { _ = svc.Close() })
		stub.services[key] = svc
		return svc, nil
	}
	open := func(ctx context.Context, vault string) error {
		if _, err := resolve(ctx, vault); err != nil {
			return err
		}
		return vaultdir.SetLastVault(vault)
	}
	closeVault := func(vault string) {
		key, err := vaultdir.Canonical(vault)
		require.NoError(t, err)
		stub.closed = append(stub.closed, key)
		delete(stub.services, key)
	}
	live := func() map[string]*app.Service { return stub.services }
	stub.api = New(resolve, open).WithVaultRegistry(live, closeVault)
	return stub
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

// TestListVaultsStatus covers what the Vaults tab and `vault list` render: every
// recorded vault is listed — including one whose folder is gone, since that is
// what forgetting exists to clear — with its availability, its embedding model
// (from the live runtime when there is one, else from the vault's own config on
// disk) and the index's last-written time.
func TestListVaultsStatus(t *testing.T) {
	stub := newVaultStub(t)
	ctx := context.Background()

	// Resident: opened, so the listing reads its live config.
	resident := makeVault(t, "resident")
	writeVaultModel(t, resident, "embed-live")
	_, err := stub.api.OpenVault(ctx, resident)
	require.NoError(t, err)

	// Recorded but never opened: only what's on disk is known about it.
	shut := makeVault(t, "shut")
	require.NoError(t, vaultdir.SetLastVault(shut))
	writeVaultModel(t, shut, "embed-shut")
	syncedAt := writeVaultIndex(t, shut, "embed-shut")

	// Recorded, then its folder went away.
	gone := makeVault(t, "gone")
	require.NoError(t, vaultdir.SetLastVault(gone))
	require.NoError(t, os.RemoveAll(gone))

	// The default is back on the resident vault, so exactly one row is current.
	require.NoError(t, vaultdir.SetLastVault(resident))

	vaults, err := stub.api.ListVaults(ctx)
	require.NoError(t, err)
	byPath := map[string]Vault{}
	for _, v := range vaults {
		byPath[v.Path] = v
	}
	require.Len(t, byPath, 3, "every recorded vault is listed, reachable or not")

	tests := []struct {
		name string
		want Vault
	}{
		{
			name: "the resident vault is current and reports its live model",
			want: Vault{Name: "resident", Path: resident, Current: true, Available: true, EmbedModel: "embed-live"},
		},
		{
			name: "a vault the daemon never opened still reports its config and last sync",
			want: Vault{Name: "shut", Path: shut, Available: true, EmbedModel: "embed-shut", LastSync: syncedAt},
		},
		{
			name: "a vault whose folder is gone is listed as unavailable",
			want: Vault{Name: "gone", Path: gone},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := byPath[tt.want.Path]
			require.Equal(t, tt.want, got)
			require.Zero(t, got.Chunks, "no store is open in these tests, so no count is claimed")
		})
	}
}

// TestForgetVault covers the three outcomes the vault-management surface needs:
// a known vault leaves the list and its runtime is retired, an unknown path is a
// no-op, and forgetting the current vault repoints the default rather than
// leaving it dangling.
func TestForgetVault(t *testing.T) {
	stub := newVaultStub(t)
	ctx := context.Background()
	first := makeVault(t, "first")
	second := makeVault(t, "second")
	_, err := stub.api.OpenVault(ctx, first)
	require.NoError(t, err)
	_, err = stub.api.OpenVault(ctx, second)
	require.NoError(t, err)

	require.NoError(t, stub.api.ForgetVault(ctx, filepath.Join(t.TempDir(), "never-known")),
		"forgetting what isn't listed is not an error")
	vaults, err := stub.api.ListVaults(ctx)
	require.NoError(t, err)
	require.Len(t, vaults, 2, "an unknown path changes nothing")

	require.NoError(t, stub.api.ForgetVault(ctx, second))

	vaults, err = stub.api.ListVaults(ctx)
	require.NoError(t, err)
	require.Len(t, vaults, 1)
	require.Equal(t, first, vaults[0].Path)
	require.True(t, vaults[0].Current, "the default repoints at the surviving vault")

	key, err := vaultdir.Canonical(second)
	require.NoError(t, err)
	// Every forget retires the runtime for that path — a vault can be resident
	// without being listed — so the assertion is on the forgotten one being gone,
	// not on the call count.
	require.Contains(t, stub.closed, key, "the forgotten vault's runtime is retired")
	require.NotContains(t, stub.services, key)

	require.Error(t, stub.api.ForgetVault(ctx, " "), "a blank path is a caller mistake")
}

// writeVaultModel records an embedding model in the vault's own config, the way
// the app does, so a listing can read it without the vault being open.
func writeVaultModel(t *testing.T, vault, model string) {
	t.Helper()
	dir, err := vaultdir.For(vault)
	require.NoError(t, err)
	require.NoError(t, appconfig.Save(dir, appconfig.Config{EmbedModel: model}))
}

// writeVaultIndex plants an index file for the model and returns the RFC3339
// mtime the listing should report for it.
func writeVaultIndex(t *testing.T, vault, model string) string {
	t.Helper()
	cacheDir, err := vaultdir.CacheFor(vault)
	require.NoError(t, err)
	path := app.IndexPath(cacheDir, model)
	require.NoError(t, os.WriteFile(path, []byte("index"), 0o600))
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info.ModTime().UTC().Format(time.RFC3339)
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
