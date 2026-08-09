package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/chinese-room-solutions/grimoire/internal/apiclient"
	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
)

// signalContext returns a context cancelled on Ctrl-C / SIGTERM, for the
// long-running headless serve subcommand — SIGTERM is how a container or a
// service manager stops it. Call the returned stop to release the signal
// handler. Windows never delivers SIGTERM, so registering it is simply inert
// there.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// headlessIdleTimeout is how long a CLI-spawned headless backend stays up after
// its last request before retiring itself, so an on-demand instance for a vault
// an agent touched once doesn't linger. A working agent (calls every few
// seconds) keeps resetting it; the next call after expiry just respawns it.
const headlessIdleTimeout = 2 * time.Minute

// launchVaultHeadless starts a new detached Grimoire backend for vault with no
// window (the `serve` subcommand) — used by the CLI to bring a vault up on demand
// for an agent without disturbing the user with a window. It self-retires after
// headlessIdleTimeout of inactivity.
func launchVaultHeadless(vault string) error {
	return spawnDetached("serve", "--vault", vault, "--idle-timeout", headlessIdleTimeout.String())
}

// spawnDetached starts this executable with args as a detached child that owns
// its own lifecycle (we don't wait on or reap it).
func spawnDetached(args ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating executable: %w", err)
	}
	cmd := exec.Command(exe, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launching grimoire process: %w", err)
	}
	return cmd.Process.Release()
}

// launchHeadlessAndWait starts a detached headless backend for the vault and
// waits for it to publish its port, returning it.
func launchHeadlessAndWait(vault, portFile string) (int, error) {
	if err := launchVaultHeadless(vault); err != nil {
		return 0, fmt.Errorf("launching headless backend for vault %q: %w", vault, err)
	}
	port, err := waitForPort(portFile, launchTimeout)
	if err != nil {
		return 0, fmt.Errorf("backend for vault %q: %w", vault, err)
	}
	return port, nil
}

// waitForPort polls portFile until it holds a non-zero port, returning it, or
// fails after timeout so a startup that never completes doesn't hang the caller.
func waitForPort(portFile string, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for {
		if port := readPort(portFile); port != 0 {
			return port, nil
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("port not published within %s", timeout)
		}
		time.Sleep(launchPoll)
	}
}

const (
	// launchTimeout bounds how long a caller waits for a freshly launched backend
	// to publish its port; index-opening can take a few seconds on a cold gateway,
	// so this is generous.
	launchTimeout = 30 * time.Second
	launchPoll    = 100 * time.Millisecond
)

// connectVault returns an API client for vault's backend, launching a headless
// one on demand when none is running. It resolves the vault's data dir, reads
// the advertised port from singleton.port, and — when absent (0) — spawns a
// backend and waits for it to publish. The CLI uses it as the single entry point
// to reach any vault, running or not.
func connectVault(vault string) (*apiclient.Client, error) {
	dir, err := vaultdir.For(vault)
	if err != nil {
		return nil, fmt.Errorf("resolving data dir for %q: %w", vault, err)
	}
	portFile := filepath.Join(dir, portFileName)
	port := readPort(portFile)
	if port == 0 {
		if port, err = launchHeadlessAndWait(vault, portFile); err != nil {
			return nil, err
		}
	}
	return apiclient.New(port), nil
}

// respawnVault forces a fresh headless backend for vault, ignoring any stale
// port advertisement, and returns a client for it. The CLI calls it after a
// transport error against a port that turned out dead, so a request that hit a
// crashed or retired backend retries once against a live one.
func respawnVault(vault string) (*apiclient.Client, error) {
	dir, err := vaultdir.For(vault)
	if err != nil {
		return nil, fmt.Errorf("resolving data dir for %q: %w", vault, err)
	}
	portFile := filepath.Join(dir, portFileName)
	// Drop the stale advertisement so waitForPort blocks on the new backend's
	// port rather than returning the dead one immediately. Best-effort: a foreign
	// or already-gone file is left alone (removePortFile only clears our own; a
	// dead backend's file is another pid's, so force it here).
	_ = os.Remove(portFile)
	port, err := launchHeadlessAndWait(vault, portFile)
	if err != nil {
		return nil, err
	}
	return apiclient.New(port), nil
}
