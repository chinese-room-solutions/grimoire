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
	"github.com/rs/zerolog"
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

// ensureDaemon returns the loopback port of a live Grimoire daemon running this
// build, starting one when needed. It is the single entry point both clients use
// — the CLI for a one-shot verb, the GUI for the window it attaches:
//
//   - no advertisement: spawn a headless daemon and wait for its port;
//   - advertised but silent (a crash, a retirement mid-read): relaunch;
//   - advertised but a different build: ask it to stop, wait for it to drop the
//     advertisement, and relaunch — an upgraded binary must never drive
//     yesterday's process, whose API and page it no longer matches.
func ensureDaemon(ctx context.Context, logger zerolog.Logger) (int, error) {
	portFile, err := daemonPortFile()
	if err != nil {
		return 0, err
	}
	port := readPort(portFile)
	if port == 0 {
		return relaunchDaemon(ctx, portFile)
	}

	switch running, err := daemonVersion(ctx, port); {
	case err != nil:
		logger.Info().Err(err).Int("port", port).Msg("the advertised daemon does not answer; starting a fresh one")
	case running == version:
		return port, nil
	default:
		logger.Info().Str("daemon", running).Str("client", version).
			Msg("the running daemon is a different build; restarting it")
		if err := requestDaemonShutdown(ctx, port); err != nil {
			logger.Warn().Err(err).Msg("asking the outdated daemon to stop; replacing it anyway")
		} else if err := waitForPortRelease(ctx, portFile, port, daemonStopTimeout); err != nil {
			logger.Warn().Err(err).Msg("waiting for the outdated daemon to retire; replacing it anyway")
		}
	}
	return relaunchDaemon(ctx, portFile)
}

// connectDaemon returns an API client for a live daemon, launching or replacing
// one as ensureDaemon decides. vault is the vault the client's requests act on
// ("" lets the daemon fall back to the last-used one). The CLI has no logger of
// its own — its output belongs to the caller's script — so the launch decisions
// are silent there; the GUI calls ensureDaemon directly and logs them.
func connectDaemon(ctx context.Context, vault string) (*apiclient.Client, error) {
	port, err := ensureDaemon(ctx, zerolog.Nop())
	if err != nil {
		return nil, err
	}
	return apiclient.New(port, vault), nil
}

// respawnDaemon forces a fresh headless daemon, ignoring any advertisement, and
// returns a client for it. The CLI calls it after a transport error against a
// port that turned out dead, so a request that hit a crashed or retired daemon
// retries once against a live one.
func respawnDaemon(ctx context.Context, vault string) (*apiclient.Client, error) {
	portFile, err := daemonPortFile()
	if err != nil {
		return nil, err
	}
	port, err := relaunchDaemon(ctx, portFile)
	if err != nil {
		return nil, err
	}
	return apiclient.New(port, vault), nil
}

// relaunchDaemon drops any existing advertisement and starts a fresh headless
// daemon, returning its port. Clearing the file first is what makes the wait
// meaningful: otherwise waitForPort returns the dead port immediately and the
// retry hits the same corpse. Best-effort removal — removePortFile only clears
// this process's own file, and a dead daemon's is another pid's.
func relaunchDaemon(ctx context.Context, portFile string) (int, error) {
	if err := os.Remove(portFile); err != nil && !os.IsNotExist(err) {
		return 0, fmt.Errorf("clearing the stale daemon port file: %w", err)
	}
	return launchDaemonAndWait(ctx, portFile)
}

const (
	// daemonProbeTimeout bounds a ping or a stop request against the advertised
	// port. The daemon is on loopback and answers both without touching a vault, so
	// a slow answer means a wedged process, not a busy one.
	daemonProbeTimeout = 2 * time.Second
	// daemonStopTimeout bounds the wait for a daemon asked to retire, which
	// finishes its in-flight requests first.
	daemonStopTimeout = 10 * time.Second
)

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

// waitForPortRelease blocks until the advertisement at portFile no longer names
// port — the daemon asked to stop has dropped it — so the relaunch that follows
// waits for the replacement's port rather than reading the corpse's.
func waitForPortRelease(ctx context.Context, portFile string, port int, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tick := time.NewTicker(launchPoll)
	defer tick.Stop()
	for {
		if readPort(portFile) != port {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting up to %s for the daemon on port %d to retire: %w", timeout, port, ctx.Err())
		case <-tick.C:
		}
	}
}
