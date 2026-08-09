package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chinese-room-solutions/grimoire/internal/vaultdir"
)

// The port file advertises the loopback port the Grimoire daemon is serving on,
// so the CLI can discover and reuse a running instance instead of spawning a new
// one. One daemon serves every vault, so there is one file: <app dir>/daemon.port,
// holding "port pid".
//
// There is no lock: a GUI and a headless `serve` may both be up, and the last
// writer wins the advertised port — a stale port simply fails to connect, and the
// reader falls back to launching a fresh daemon. The pid makes removal owned: an
// instance only deletes the file it wrote itself, so a departing earlier writer
// can't tear down the surviving instance's advertisement.

const portFileName = "daemon.port"

// daemonPortFile is where the running daemon advertises its loopback port.
func daemonPortFile() (string, error) {
	dir, err := vaultdir.AppDir()
	if err != nil {
		return "", fmt.Errorf("resolving app data dir: %w", err)
	}
	return filepath.Join(dir, portFileName), nil
}

// writePortFile records port as the daemon's, stamped with this process's pid so
// only the writer later removes it.
func writePortFile(path string, port int) error {
	if err := os.WriteFile(path, fmt.Appendf(nil, "%d %d", port, os.Getpid()), 0o600); err != nil {
		return fmt.Errorf("writing port file %s: %w", path, err)
	}
	return nil
}

// removePortFile clears the daemon's port advertisement, but only when this
// process owns it (the pid in the file is ours). With several instances up the
// last writer holds the file, and a departing earlier one must not delete the
// survivor's advertisement. Best-effort: a missing, unparseable, or foreign file
// is left alone.
func removePortFile(path string) {
	if _, pid, ok := readPortFile(path); !ok || pid != os.Getpid() {
		return
	}
	_ = os.Remove(path)
}

// readPort returns the port advertised at path, or 0 if the file is absent or
// unparseable (treated the same: no reachable instance).
func readPort(path string) int {
	port, _, ok := readPortFile(path)
	if !ok {
		return 0
	}
	return port
}

// readPortFile parses a port file's "port pid" contents. ok is false when the
// file is absent or doesn't parse (including the pre-pid "port" format — its
// writer is gone, so treating it as no instance just relaunches and overwrites).
func readPortFile(path string) (port, pid int, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d %d", &port, &pid); err != nil {
		return 0, 0, false
	}
	return port, pid, true
}
