package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// newTestHolder builds a holder with a nil gateway client (enough to exercise
// bind/unbind/swap — no embedding happens) and isolates vaultdir's cache root in
// a temp dir so binding doesn't touch the real data directories.
func newTestHolder(t *testing.T, port int) *serviceHolder {
	t.Helper()
	cache := t.TempDir()
	t.Setenv("LocalAppData", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("HOME", cache)
	t.Setenv("AppData", cache)
	t.Setenv("XDG_CONFIG_HOME", cache)
	return &serviceHolder{logger: zerolog.Nop(), port: port}
}

// tempVault makes an empty vault folder and returns its path.
func tempVault(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "vault")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return dir
}

// portFileFor returns the port advertisement path for a vault.
func portFileFor(t *testing.T, vault string) string {
	t.Helper()
	dir, err := vaultdir.For(vault)
	require.NoError(t, err)
	return filepath.Join(dir, portFileName)
}

func TestHolderBindThenCurrent(t *testing.T) {
	h := newTestHolder(t, 4242)
	require.Nil(t, h.current(), "unbound holder has no service")
	require.Empty(t, h.currentVault())

	vault := tempVault(t)
	require.NoError(t, h.bind(context.Background(), vault))
	t.Cleanup(h.close)

	require.NotNil(t, h.current(), "a service is bound after bind")
	require.Equal(t, vault, h.current().Vault())
	require.Equal(t, vault, h.currentVault())
	require.Equal(t, 4242, readPort(portFileFor(t, vault)), "the port is advertised for the bound vault")
}

func TestHolderUnbindClearsAndRemovesPortFile(t *testing.T) {
	h := newTestHolder(t, 7)
	vault := tempVault(t)
	require.NoError(t, h.bind(context.Background(), vault))
	pf := portFileFor(t, vault)
	require.NotZero(t, readPort(pf))

	h.unbind()
	require.Nil(t, h.current(), "unbind clears the service")
	require.Empty(t, h.currentVault())
	require.Zero(t, readPort(pf), "unbind removes the port advertisement")
}

func TestHolderBindOverSwitchesVaultAndPortFile(t *testing.T) {
	h := newTestHolder(t, 99)
	t.Cleanup(h.close)
	first := tempVault(t)
	second := tempVault(t)

	require.NoError(t, h.bind(context.Background(), first))
	require.Equal(t, first, h.currentVault())
	require.Equal(t, 99, readPort(portFileFor(t, first)))

	require.NoError(t, h.bind(context.Background(), second))
	require.Equal(t, second, h.currentVault(), "binding a second vault switches to it")
	require.Equal(t, 99, readPort(portFileFor(t, second)), "the new vault is advertised")
	require.Zero(t, readPort(portFileFor(t, first)), "the old vault's advertisement is removed")
}

// TestHolderConcurrentReadsDuringSwap exercises the swap path under -race:
// readers calling current() must never see a torn state while bind/unbind run.
func TestHolderConcurrentReadsDuringSwap(t *testing.T) {
	h := newTestHolder(t, 1)
	t.Cleanup(h.close)
	first := tempVault(t)
	second := tempVault(t)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = h.current()
				_ = h.currentVault()
			}
		}
	}()

	for i := 0; i < 20; i++ {
		require.NoError(t, h.bind(context.Background(), first))
		require.NoError(t, h.bind(context.Background(), second))
		h.unbind()
	}
	close(stop)
	wg.Wait()
}
