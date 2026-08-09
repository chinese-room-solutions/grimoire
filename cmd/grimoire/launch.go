package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// headlessIdleTimeout is how long a CLI-spawned headless daemon stays up after
// its last request before retiring itself, so an on-demand instance an agent
// touched once doesn't linger. A working agent (calls every few seconds) keeps
// resetting it; the next call after expiry just respawns it.
const headlessIdleTimeout = 2 * time.Minute

// spawnLogName is the file a detached daemon's stdout/stderr append to, under the
// app dir. Without it the child would inherit the launching CLI's streams and
// write log noise into a script's output long after the verb returned.
const spawnLogName = "daemon-spawn.log"

// launchDaemonHeadless starts a new detached Grimoire daemon with no window (the
// `serve` subcommand) — used by the CLI to bring the backend up on demand for an
// agent without disturbing the user with a window. It serves every vault (each
// request names its own), and self-retires after headlessIdleTimeout of
// inactivity.
func launchDaemonHeadless() error {
	return spawnDetached("serve", "--idle-timeout", headlessIdleTimeout.String())
}

// spawnDetached starts this executable with args as a detached child that owns
// its own lifecycle (we don't wait on or reap it). Its stdout/stderr go to the
// spawn log rather than to ours, so the child can't write into a script's output
// after this process exits.
func spawnDetached(args ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating executable: %w", err)
	}
	cmd := exec.Command(exe, args...)
	if logFile, err := openSpawnLog(); err != nil {
		return err
	} else if logFile != nil {
		defer func() { _ = logFile.Close() }() // the child holds its own descriptor.
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launching grimoire process: %w", err)
	}
	return cmd.Process.Release()
}

// openSpawnLog opens the detached daemon's stdio log for appending. A nil file
// with a nil error means there is nowhere to write it (no app dir), in which case
// the child gets the OS default and the launch still proceeds.
func openSpawnLog() (*os.File, error) {
	dir, err := vaultdir.AppDir()
	if err != nil {
		return nil, nil //nolint:nilnil // no app dir: launch anyway, without redirection.
	}
	f, err := os.OpenFile(filepath.Join(dir, spawnLogName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening the daemon spawn log: %w", err)
	}
	return f, nil
}

// launchDaemonAndWait starts a detached headless daemon and waits for it to
// publish its port, returning it.
func launchDaemonAndWait(ctx context.Context, portFile string) (int, error) {
	if err := launchDaemonHeadless(); err != nil {
		return 0, fmt.Errorf("launching the grimoire daemon: %w", err)
	}
	port, err := waitForPort(ctx, portFile, launchTimeout)
	if err != nil {
		return 0, fmt.Errorf("grimoire daemon: %w", err)
	}
	return port, nil
}

// waitForPort polls portFile until it holds a non-zero port, returning it. It
// gives up when ctx is cancelled, or after timeout so a startup that never
// completes doesn't hang the caller.
func waitForPort(ctx context.Context, portFile string, timeout time.Duration) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tick := time.NewTicker(launchPoll)
	defer tick.Stop()
	for {
		if port := readPort(portFile); port != 0 {
			return port, nil
		}
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("waiting up to %s for the backend's port: %w", timeout, ctx.Err())
		case <-tick.C:
		}
	}
}

const (
	// launchTimeout bounds how long a caller waits for a freshly launched backend
	// to publish its port; index-opening can take a few seconds on a cold gateway,
	// so this is generous.
	launchTimeout = 30 * time.Second
	launchPoll    = 100 * time.Millisecond
)

// daemonProbeTimeout bounds a ping or a stop request against the advertised
// port. The daemon is on loopback and answers both without touching a vault, so
// a slow answer means a wedged process, not a busy one.
const daemonProbeTimeout = 2 * time.Second

// daemonVersion asks the daemon on port which build it is running.
func daemonVersion(ctx context.Context, port int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, daemonProbeTimeout)
	defer cancel()
	resp, err := daemonRequest(ctx, http.MethodGet, port, "/api/v1/ping")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("pinging the daemon: status %d", resp.StatusCode)
	}
	var out struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return "", fmt.Errorf("decoding the daemon's ping: %w", err)
	}
	return out.Version, nil
}

// requestDaemonShutdown asks the daemon on port to retire gracefully. It returns
// once the daemon has accepted; the stop itself runs after the response.
func requestDaemonShutdown(ctx context.Context, port int) error {
	ctx, cancel := context.WithTimeout(ctx, daemonProbeTimeout)
	defer cancel()
	resp, err := daemonRequest(ctx, http.MethodPost, port, "/api/v1/shutdown")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("asking the daemon to stop: status %d", resp.StatusCode)
	}
	return nil
}

// daemonRequest issues one bodiless request against a daemon's loopback API.
func daemonRequest(ctx context.Context, method string, port int, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, daemonURL(port, path), nil)
	if err != nil {
		return nil, fmt.Errorf("building the daemon request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	return resp, nil
}

// daemonURL is an absolute URL for one of the daemon's routes on port.
func daemonURL(port int, path string) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
}

// connectDaemon returns an API client for the running Grimoire daemon, launching
// a headless one on demand when none is up. It reads the advertised port from the
// app-level daemon.port and — when absent (0) — spawns a daemon and waits for it
// to publish. vault is the vault the client's requests act on ("" lets the daemon
// fall back to the last-used one). The CLI uses it as its single entry point:
// one daemon serves every vault.
func connectDaemon(ctx context.Context, vault string) (*apiclient.Client, error) {
	portFile, err := daemonPortFile()
	if err != nil {
		return nil, err
	}
	port := readPort(portFile)
	if port == 0 {
		if port, err = launchDaemonAndWait(ctx, portFile); err != nil {
			return nil, err
		}
	}
	return apiclient.New(port, vault), nil
}

// respawnDaemon forces a fresh headless daemon, ignoring any stale port
// advertisement, and returns a client for it. The CLI calls it after a transport
// error against a port that turned out dead, so a request that hit a crashed or
// retired daemon retries once against a live one.
func respawnDaemon(ctx context.Context, vault string) (*apiclient.Client, error) {
	portFile, err := daemonPortFile()
	if err != nil {
		return nil, err
	}
	// Drop the stale advertisement so waitForPort blocks on the new daemon's port
	// rather than returning the dead one immediately. Best-effort: a foreign or
	// already-gone file is left alone (removePortFile only clears our own; a dead
	// daemon's file is another pid's, so force it here).
	if err := os.Remove(portFile); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("clearing the stale daemon port file: %w", err)
	}
	port, err := launchDaemonAndWait(ctx, portFile)
	if err != nil {
		return nil, err
	}
	return apiclient.New(port, vault), nil
}
