package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
	"github.com/stretchr/testify/require"
)

func TestWaitForPortReturnsOncePublished(t *testing.T) {
	portFile := filepath.Join(t.TempDir(), portFileName)
	// Publish the port shortly after the wait starts, the way a backend does.
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = writePortFile(portFile, 54321)
	}()
	port, err := waitForPort(context.Background(), portFile, 2*time.Second)
	require.NoError(t, err)
	require.Equal(t, 54321, port)
}

func TestWaitForPortTimesOut(t *testing.T) {
	portFile := filepath.Join(t.TempDir(), portFileName) // never written.
	_, err := waitForPort(context.Background(), portFile, 50*time.Millisecond)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// The daemon's port advertisement is app-level, not per-vault: one file, so any
// CLI verb for any vault finds the one running process.
func TestDaemonPortFileIsAppLevel(t *testing.T) {
	isolateVaultDirs(t)
	path, err := daemonPortFile()
	require.NoError(t, err)

	appDir, err := vaultdir.AppDir()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(appDir, "daemon.port"), path)
	require.NotContains(t, path, "vaults", "the advertisement is not scoped to a vault")

	again, err := daemonPortFile()
	require.NoError(t, err)
	require.Equal(t, path, again, "every process resolves the same file")
}

// respawnDaemon must clear a stale advertisement before waiting, or waitForPort
// returns the dead port immediately and the retry hits the same corpse. The
// launch itself is expected to fail here (the test binary is not the daemon);
// what matters is that the stale file is gone by the time it does.
func TestRespawnDaemonClearsTheStalePortFile(t *testing.T) {
	isolateVaultDirs(t)
	path, err := daemonPortFile()
	require.NoError(t, err)
	require.NoError(t, writePortFile(path, 65000))
	require.Equal(t, 65000, readPort(path))

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err = respawnDaemon(ctx, "/vaults/A")
	require.Error(t, err, "no daemon comes up in a test binary")
	require.Zero(t, readPort(path), "the stale advertisement is dropped before the wait")
}

// A cancelled caller (Ctrl-C during a CLI command) stops the wait instead of
// polling out the full launch timeout.
func TestWaitForPortHonoursCancellation(t *testing.T) {
	portFile := filepath.Join(t.TempDir(), portFileName) // never written.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := waitForPort(ctx, portFile, time.Minute)
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 10*time.Second, "the wait ends with the context, not the timeout")
}
