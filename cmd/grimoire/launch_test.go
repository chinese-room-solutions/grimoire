package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
	"github.com/rs/zerolog"
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

// A daemon already up and running this build is reused as it stands — no
// version dance, no second process.
func TestEnsureDaemonReusesALiveDaemon(t *testing.T) {
	isolateVaultDirs(t)
	portFile, err := daemonPortFile()
	require.NoError(t, err)

	port, _ := controlServer(t, version)
	require.NoError(t, writePortFile(portFile, port))

	got, err := ensureDaemon(t.Context(), zerolog.Nop())
	require.NoError(t, err)
	require.Equal(t, port, got)
	require.Equal(t, port, readPort(portFile), "a healthy daemon keeps its advertisement")
}

// An advertisement pointing at a port nobody answers on (a crashed or retired
// daemon) is dropped and replaced. The relaunch itself can't succeed in a test
// binary — what matters is that the corpse's advertisement is gone, so nothing
// retries against it.
func TestEnsureDaemonReplacesADeadAdvertisement(t *testing.T) {
	isolateVaultDirs(t)
	portFile, err := daemonPortFile()
	require.NoError(t, err)
	require.NoError(t, writePortFile(portFile, deadPort(t)))

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	_, err = ensureDaemon(ctx, zerolog.Nop())
	require.Error(t, err, "no daemon comes up in a test binary")
	require.Zero(t, readPort(portFile), "the dead advertisement is dropped")
}

// A daemon running a different build is asked to retire before a fresh one
// takes its place: an upgraded binary must never drive yesterday's process.
func TestEnsureDaemonRestartsAnOutdatedDaemon(t *testing.T) {
	isolateVaultDirs(t)
	portFile, err := daemonPortFile()
	require.NoError(t, err)

	var asked atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"version":"an-older-build"}`)
	})
	mux.HandleFunc("POST /api/v1/shutdown", func(w http.ResponseWriter, _ *http.Request) {
		asked.Store(true)
		// A retiring daemon drops its advertisement; the caller waits for that
		// before launching the replacement.
		require.NoError(t, os.Remove(portFile))
		_, _ = io.WriteString(w, `{"status":"stopping"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	require.NoError(t, writePortFile(portFile, serverPort(t, srv)))

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	_, err = ensureDaemon(ctx, zerolog.Nop())
	require.Error(t, err, "no daemon comes up in a test binary")
	require.True(t, asked.Load(), "the outdated daemon is asked to stop")
	require.Zero(t, readPort(portFile), "its advertisement is gone before the replacement is awaited")
}

// deadPort returns a loopback port nothing listens on.
func deadPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

// serverPort is the loopback port an httptest server listens on.
func serverPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	return port
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
