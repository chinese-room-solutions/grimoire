//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The daemon's on-demand lifecycle, exercised through the real detached spawn
// (not the in-process seam the unit tests use): a CLI verb with nothing running
// brings a daemon up, and the next verb reuses that same process.
func TestCLISpawnsAndReusesTheDaemon(t *testing.T) {
	bin := grimoireBin(t)

	scratch := t.TempDir()
	vault := filepath.Join(scratch, "vault")
	for _, sub := range []string{"vault", "home", "config", "cache", "state", "data"} {
		if err := os.MkdirAll(filepath.Join(scratch, sub), 0o700); err != nil {
			t.Fatalf("creating scratch dir %s: %v", sub, err)
		}
	}
	writeNote(t, vault, "hello.md", "# Hello\n\nworld\n")
	env, cfgRoot := scratchEnv(scratch)
	writeLastVault(t, cfgRoot, vault)
	portFile := filepath.Join(cfgRoot, "grimoire", "app", "daemon.port")

	// Registered before the first verb runs: a daemon spawned by a verb that then
	// fails must still be killed, or it outlives the test with the scratch vault
	// open.
	t.Cleanup(func() { killDaemon(t, portFile) })

	if _, err := os.Stat(portFile); !os.IsNotExist(err) {
		t.Fatalf("the scratch app dir already advertises a daemon: %v", err)
	}

	// First verb: nothing is running, so the CLI spawns a detached daemon.
	runCLI(t, bin, env, "vault", "list")
	var first int
	poll(t, "the CLI-spawned daemon to advertise its port", func() (bool, string) {
		port, pid := readPortFile(t, portFile)
		first = pid
		return port != 0 && pid != 0, "no usable port file yet"
	})

	// Second verb: the advertisement is live, so it is reused rather than
	// replaced — same process, no second daemon.
	runCLI(t, bin, env, "vault", "list")
	if _, pid := readPortFile(t, portFile); pid != first {
		t.Fatalf("the second verb replaced the daemon: pid %d then %d", first, pid)
	}
}

// runCLI runs one grimoire verb against the scratch environment, failing the
// test with its output when it exits non-zero.
func runCLI(t *testing.T, bin string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("grimoire %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// readPortFile parses the daemon advertisement, reporting zeroes when it is
// absent or unparseable.
func readPortFile(t *testing.T, path string) (port, pid int) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0
	}
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d %d", &port, &pid); err != nil {
		return 0, 0
	}
	return port, pid
}

// killDaemon ends the daemon a CLI verb spawned. It is a detached grandchild —
// nothing holds its handle — so the pid in the advertisement is the only way to
// reach it. Kill (not an interrupt) because Windows can't be signalled, and this
// is a teardown either way.
func killDaemon(t *testing.T, portFile string) {
	t.Helper()
	_, pid := readPortFile(t, portFile)
	if pid == 0 {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		t.Logf("finding the spawned daemon (pid %d): %v", pid, err)
		return
	}
	if err := proc.Kill(); err != nil {
		t.Logf("killing the spawned daemon (pid %d): %v", pid, err)
		return
	}
	_, _ = proc.Wait() // it is not our child on Unix; this just reaps where it can.
}
