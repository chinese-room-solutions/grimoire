//go:build !windows

package kernel

import "os/exec"

// hideConsole is a no-op off Windows — there's no stray console window to hide.
func hideConsole(*exec.Cmd) {}

// ensureToolchain is a no-op off Windows — a POSIX shell already has its coreutils
// on PATH.
func ensureToolchain(*exec.Cmd, string) {}

// lookExe resolves an executable on PATH. Off Windows there are no WSL/Store stubs
// to dodge, so it's a plain PATH lookup.
func lookExe(name string) (string, error) {
	return exec.LookPath(name)
}
