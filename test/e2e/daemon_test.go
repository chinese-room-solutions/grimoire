//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// The daemon's lifecycle, exercised through the real detached spawn (not the
// in-process seam the unit tests use): a CLI verb with nothing running brings a
// daemon up, the next verb reuses that same process, a dead advertisement is
// replaced rather than believed, and an idle-timeout daemon retires itself once
// no client is left holding it.

// hello is the one-vault scratch layout the lifecycle tests use — they care
// about the process, not about what is in the vault.
var hello = map[string]map[string]string{"vault": {"hello.md": "# Hello\n\nworld\n"}}

func TestCLISpawnsAndReusesTheDaemon(t *testing.T) {
	bin := grimoireBin(t)
	s := newScratch(t, hello)

	// Registered before the first verb runs: a daemon spawned by a verb that then
	// fails must still be killed, or it outlives the test with the scratch vault
	// open.
	t.Cleanup(func() { killDaemon(t, s.portFile) })

	if _, err := os.Stat(s.portFile); !os.IsNotExist(err) {
		t.Fatalf("the scratch app dir already advertises a daemon: %v", err)
	}

	// First verb: nothing is running, so the CLI spawns a detached daemon.
	runCLI(t, bin, s.env, "vault", "list")
	var first int
	poll(t, "the CLI-spawned daemon to advertise its port", func() (bool, string) {
		port, pid := readPortFile(t, s.portFile)
		first = pid
		return port != 0 && pid != 0, "no usable port file yet"
	})

	// Second verb: the advertisement is live, so it is reused rather than
	// replaced — same process, no second daemon.
	runCLI(t, bin, s.env, "vault", "list")
	if _, pid := readPortFile(t, s.portFile); pid != first {
		t.Fatalf("the second verb replaced the daemon: pid %d then %d", first, pid)
	}
}

// A verb that finds an advertisement nothing answers on must replace it, not
// fail against the corpse: a daemon that crashed (or was killed) leaves the file
// behind, and the pid in it is long gone.
func TestCLIReplacesAStaleDaemonAdvertisement(t *testing.T) {
	bin := grimoireBin(t)
	s := newScratch(t, hello)
	t.Cleanup(func() { killDaemon(t, s.portFile) })

	stalePort, stalePid := freePort(t), deadPID(t, bin)
	writeStalePortFile(t, s.portFile, stalePort, stalePid)

	runCLI(t, bin, s.env, "vault", "list")

	port, pid := readPortFile(t, s.portFile)
	if port == 0 || port == stalePort {
		t.Fatalf("the verb kept the stale port %d (file now says %d)", stalePort, port)
	}
	if pid == 0 || pid == stalePid {
		t.Fatalf("the verb kept the stale pid %d (file now says %d)", stalePid, pid)
	}
	pollErr(t, "the replacement daemon to answer", func() error { return pingDaemon(port) })
}

// idleWindow is the retirement window the idle tests run the daemon with: short
// enough to keep them quick, long enough that attaching a client right after the
// advertisement appears isn't a race with the very first countdown.
const idleWindow = 3 * time.Second

func TestDaemonIdleTimeout(t *testing.T) {
	_ = grimoireBin(t) // skip before spawning anything when the binary is missing.

	t.Run("RetiresWithNoClients", func(t *testing.T) {
		d := newScratch(t, hello).spawn(t, "--idle-timeout", idleWindow.String())
		d.waitPort(t)
		waitExit(t, d, "the idle daemon to retire on its own")
	})

	t.Run("AClientHoldingTheChannelKeepsItUp", func(t *testing.T) {
		d := newScratch(t, hello).spawn(t, "--idle-timeout", idleWindow.String())
		d.waitPort(t)

		// The GUI window holds this stream open for as long as it lives; that open
		// request is what keeps an on-demand daemon from retiring under it.
		drop := attachClientChannel(t, d)
		select {
		case <-d.exited:
			t.Fatal("the daemon retired while a client held its control channel open")
		case <-time.After(2 * idleWindow):
		}
		if err := pingDaemon(d.port); err != nil {
			t.Fatalf("the held-open daemon stopped answering: %v", err)
		}

		drop()
		waitExit(t, d, "the daemon to retire once its last client left")
	})
}

// waitExit blocks until the daemon's process has been reaped, failing the test
// with desc when it outlives the deadline.
func waitExit(t *testing.T, d *daemon, desc string) {
	t.Helper()
	poll(t, desc, func() (bool, string) {
		select {
		case <-d.exited:
			return true, ""
		default:
			return false, "still running"
		}
	})
}

// attachClientChannel opens the GUI's control channel against the daemon and
// holds it, the way a live window does. The returned func drops it (and runs on
// cleanup, so a failing test leaves nothing attached).
func attachClientChannel(t *testing.T, d *daemon) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.baseURL+"api/client/channel", nil)
	if err != nil {
		cancel()
		t.Fatalf("building the channel request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("attaching to the daemon's control channel: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		cancel()
		_ = resp.Body.Close()
		t.Fatalf("attaching to the daemon's control channel: status %d", resp.StatusCode)
	}
	drop := sync.OnceFunc(func() {
		cancel()
		_ = resp.Body.Close()
	})
	t.Cleanup(drop)
	return drop
}

// pingDaemon reports whether a daemon answers on port.
func pingDaemon(port int) error {
	hc := &http.Client{Timeout: 2 * time.Second}
	resp, err := hc.Get("http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) + "/api/v1/ping")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ping: status %d", resp.StatusCode)
	}
	return nil
}

// writeStalePortFile forges a daemon advertisement, so a verb meets the state a
// crashed daemon leaves behind.
func writeStalePortFile(t *testing.T, path string, port, pid int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("creating the app dir: %v", err)
	}
	if err := os.WriteFile(path, fmt.Appendf(nil, "%d %d", port, pid), 0o600); err != nil {
		t.Fatalf("writing the stale port file: %v", err)
	}
}

// deadPID is the pid of a process that has already exited — a plausible but
// unreachable owner for a stale advertisement.
func deadPID(t *testing.T, bin string) int {
	t.Helper()
	cmd := exec.Command(bin, "--version")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running %s --version: %v", bin, err)
	}
	return cmd.ProcessState.Pid()
}
