package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// isolateVaultDirs points every vaultdir root at a temp dir, so a test's vault
// data, cache and last-vault pointer never touch the real ones.
func isolateVaultDirs(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CACHE_HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("AppData", root)
	t.Setenv("LocalAppData", root)
}

// newTestRegistry builds a registry over isolated vaultdir roots and shared
// state with no gateway client (no embedding happens in these tests).
func newTestRegistry(t *testing.T) *vaultRegistry {
	t.Helper()
	isolateVaultDirs(t)
	reg := newVaultRegistry(testShared(t), zerolog.Nop())
	t.Cleanup(reg.closeAll)
	return reg
}

// testShared builds process-wide app state for a test: no gateway client, a
// temp app dir, no registries.
func testShared(t *testing.T) *app.Shared {
	t.Helper()
	return testSharedWith(t, "", "")
}

// testSharedWith is testShared with the shared kernels dir and theme registry a
// test cares about.
func testSharedWith(t *testing.T, kernelsDir, themeRegistryURL string) *app.Shared {
	t.Helper()
	sh, err := app.NewShared(nil, t.TempDir(), kernelsDir, "", themeRegistryURL, zerolog.Nop())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sh.Close()) })
	return sh
}

// tempVault makes an empty vault folder and returns its path.
func tempVault(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "vault")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return dir
}

func TestVaultRegistryRuntime(t *testing.T) {
	t.Run("creates one runtime lazily and reuses it", func(t *testing.T) {
		reg := newTestRegistry(t)
		require.Empty(t, reg.live(), "nothing is resident before the first request")

		vault := tempVault(t)
		first, err := reg.runtime(context.Background(), vault)
		require.NoError(t, err)
		require.Equal(t, vault, first.Vault())
		require.Len(t, reg.live(), 1)

		second, err := reg.runtime(context.Background(), vault)
		require.NoError(t, err)
		require.Same(t, first, second, "a second request reuses the resident runtime")
	})

	t.Run("equivalent spellings share one runtime", func(t *testing.T) {
		reg := newTestRegistry(t)
		vault := tempVault(t)
		canonical, err := reg.runtime(context.Background(), vault)
		require.NoError(t, err)

		for _, spelling := range []string{
			filepath.Join(vault, "."),
			filepath.Join(vault, "sub", ".."),
			vault + string(filepath.Separator),
		} {
			svc, err := reg.runtime(context.Background(), spelling)
			require.NoError(t, err, spelling)
			require.Same(t, canonical, svc, "spelling %q maps to the same runtime", spelling)
		}
		require.Len(t, reg.live(), 1)
	})

	t.Run("distinct vaults get distinct runtimes", func(t *testing.T) {
		reg := newTestRegistry(t)
		a, err := reg.runtime(context.Background(), tempVault(t))
		require.NoError(t, err)
		b, err := reg.runtime(context.Background(), tempVault(t))
		require.NoError(t, err)
		require.NotSame(t, a, b)
		require.Len(t, reg.live(), 2)
	})

	t.Run("errors", func(t *testing.T) {
		reg := newTestRegistry(t)
		tests := []struct {
			name, vault string
			wantErr     error
		}{
			{"no vault", "", app.ErrNoVault},
			{"blank vault", "   ", app.ErrNoVault},
			{"missing folder", filepath.Join(t.TempDir(), "gone"), errVaultUnavailable},
			{"not a folder", writeTempFile(t), errVaultUnavailable},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				_, err := reg.runtime(context.Background(), tc.vault)
				require.ErrorIs(t, err, tc.wantErr)
			})
		}
		require.Empty(t, reg.live(), "a failed resolve leaves nothing resident")
	})

	t.Run("concurrent requests for one vault yield one runtime", func(t *testing.T) {
		reg := newTestRegistry(t)
		vault := tempVault(t)
		const goroutines = 16
		got := make([]*app.Service, goroutines)
		var wg sync.WaitGroup
		for i := range goroutines {
			wg.Add(1)
			go func() {
				defer wg.Done()
				svc, err := reg.runtime(context.Background(), vault)
				require.NoError(t, err)
				got[i] = svc
			}()
		}
		wg.Wait()
		for _, svc := range got {
			require.Same(t, got[0], svc)
		}
		require.Len(t, reg.live(), 1)
	})
}

func TestVaultRegistryCloseAll(t *testing.T) {
	reg := newVaultRegistry(testShared(t), zerolog.Nop())
	isolateVaultDirs(t)
	_, err := reg.runtime(context.Background(), tempVault(t))
	require.NoError(t, err)
	_, err = reg.runtime(context.Background(), tempVault(t))
	require.NoError(t, err)
	require.Len(t, reg.live(), 2)

	reg.closeAll()
	require.Empty(t, reg.live(), "closeAll drops every runtime")

	_, err = reg.runtime(context.Background(), tempVault(t))
	require.ErrorIs(t, err, errVaultUnavailable, "a closed registry starts nothing new")
	reg.closeAll() // idempotent.
}

func TestVaultRegistryBusyKernels(t *testing.T) {
	reg := newTestRegistry(t)
	require.False(t, reg.busyKernels(), "an empty registry is never busy")

	_, err := reg.runtime(context.Background(), tempVault(t))
	require.NoError(t, err)
	require.False(t, reg.busyKernels(), "a resident vault running nothing is not busy")
}

func TestVaultRegistryWarmup(t *testing.T) {
	reg := newTestRegistry(t)
	known := tempVault(t)
	gone := filepath.Join(t.TempDir(), "gone")
	require.NoError(t, vaultdir.SetLastVault(known))
	require.NoError(t, vaultdir.SetLastVault(gone))

	reg.warmup(context.Background(), 0)

	live := reg.live()
	require.Len(t, live, 1, "the vault whose folder is gone is skipped")
	key, err := vaultdir.Canonical(known)
	require.NoError(t, err)
	require.Contains(t, live, key)
}

func TestRequestVault(t *testing.T) {
	isolateVaultDirs(t)
	last := tempVault(t)
	require.NoError(t, vaultdir.SetLastVault(last))

	signals := func(vault string) string {
		encoded, err := json.Marshal(vault)
		require.NoError(t, err)
		return `{"gVault":` + string(encoded) + `,"gQuery":"x"}`
	}

	tests := []struct {
		name string
		req  func() *http.Request
		want string
	}{
		{
			name: "explicit query parameter wins",
			req: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/api/v1/search?vault="+url.QueryEscape("/vaults/A"), nil)
			},
			want: "/vaults/A",
		},
		{
			name: "the query parameter beats the signal",
			req: func() *http.Request {
				return httptest.NewRequest(http.MethodGet,
					"/api/files/render?vault=/vaults/A&datastar="+url.QueryEscape(signals("/vaults/B")), nil)
			},
			want: "/vaults/A",
		},
		{
			name: "a GET carries the signal in the datastar query parameter",
			req: func() *http.Request {
				return httptest.NewRequest(http.MethodGet,
					"/api/files/render?datastar="+url.QueryEscape(signals("/vaults/B")), nil)
			},
			want: "/vaults/B",
		},
		{
			name: "a POST carries the signal in the JSON body",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, "/action/search", strings.NewReader(signals("/vaults/C")))
				r.Header.Set("Content-Type", "application/json")
				return r
			},
			want: "/vaults/C",
		},
		{
			name: "a form POST without a vault falls back to the last-used one",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, "/api/open-vault", strings.NewReader("path=/x"))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				return r
			},
			want: last,
		},
		{
			name: "a bare request falls back to the last-used vault",
			req:  func() *http.Request { return httptest.NewRequest(http.MethodGet, "/api/v1/search?q=x", nil) },
			want: last,
		},
		{
			name: "an empty signal falls back to the last-used vault",
			req: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/api/files/render?datastar="+url.QueryEscape(signals("")), nil)
			},
			want: last,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, requestVault(tc.req()))
		})
	}
}

// A handler downstream of requestVault must still be able to read the signals
// off a POST body: the peek puts them back.
func TestRequestVaultLeavesTheBodyReadable(t *testing.T) {
	isolateVaultDirs(t)
	body := `{"gVault":"/vaults/A","gQuery":"hello"}`
	r := httptest.NewRequest(http.MethodPost, "/action/search", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")

	require.Equal(t, "/vaults/A", requestVault(r))

	rest, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	require.Equal(t, body, string(rest), "the handler sees the whole signals payload")
}

// writeTempFile makes a regular file and returns its path, for the "a vault path
// that isn't a folder" case.
func writeTempFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "not-a-vault")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
	return path
}
