package kernel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// CREATE_NO_WINDOW keeps a console child from popping its own window — without it
// every spawned shell (and each subprocess it runs) flashes a black console on a
// GUI app. https://learn.microsoft.com/windows/win32/procthread/process-creation-flags
const createNoWindow = 0x08000000

// hideConsole stops the kernel process from flashing a console window on Windows.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

// ensureToolchain makes the spawned shell find its coreutils. Git Bash's bash.exe
// lives in usr\bin alongside mktemp, base64, date, etc., but those are only on PATH
// when bash is started through its launcher — a bare `bash.exe script` spawned by a
// GUI app inherits Explorer's Windows-only PATH, so the runner's `mktemp`/`base64`
// fail with "command not found" and every block dies. Prepending the bash binary's
// own directory to the child's PATH puts the coreutils back in reach.
func ensureToolchain(cmd *exec.Cmd, exe string) {
	binDir := filepath.Dir(exe)
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	for i, kv := range env {
		if name, val, ok := strings.Cut(kv, "="); ok && strings.EqualFold(name, "PATH") {
			env[i] = name + "=" + binDir + string(os.PathListSeparator) + val
			cmd.Env = env
			return
		}
	}
	cmd.Env = append(env, "PATH="+binDir)
}

// lookExe resolves an executable on Windows, dodging two traps that break a
// POSIX-shell kernel. For bash, PATH often surfaces the WSL launcher
// (System32\bash.exe) or the Microsoft Store stub (WindowsApps\bash.exe) first;
// neither can run a Windows-path script, so a kernel started on them dies
// immediately. We skip those and, if PATH yields nothing usable, probe the
// standard Git for Windows locations (Git Bash isn't always on a GUI app's PATH).
// When only a stub is on PATH and no Git Bash exists, it fails with that as the
// reason rather than returning the stub.
func lookExe(name string) (string, error) {
	p, lookErr := exec.LookPath(name)
	stub := lookErr == nil && isUnusableShell(p)
	if lookErr == nil && !stub {
		return p, nil
	}
	if isBash(name) {
		if git, ok := findGitBash(); ok {
			return git, nil
		}
	}
	if stub {
		// Spawning on the stub dies cryptically, so report why nothing usable exists
		// instead of handing it back.
		return "", fmt.Errorf("only the WSL/Store bash stub was found (%s); install Git for Windows", p)
	}
	return "", lookErr
}

// isBash reports whether the configured executable is bash (with or without the
// .exe suffix).
func isBash(name string) bool {
	base := strings.ToLower(filepath.Base(name))
	return base == "bash" || base == "bash.exe"
}

// isUnusableShell flags the WSL launcher and the Store stub, which masquerade as
// bash on PATH but can't run a Windows-path script.
func isUnusableShell(path string) bool {
	low := strings.ToLower(filepath.ToSlash(path))
	return strings.Contains(low, "/system32/") || strings.Contains(low, "/windowsapps/")
}

// findGitBash returns the Git for Windows bash from its standard install
// locations, preferring the MSYS shell under usr/bin.
func findGitBash() (string, bool) {
	var roots []string
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
		if dir := os.Getenv(env); dir != "" {
			roots = append(roots, dir)
		}
	}
	rel := []string{
		`Git\usr\bin\bash.exe`,
		`Git\bin\bash.exe`,
		`Programs\Git\usr\bin\bash.exe`, // user-scope install under LocalAppData.
		`Programs\Git\bin\bash.exe`,
	}
	for _, root := range roots {
		for _, r := range rel {
			cand := filepath.Join(root, r)
			if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
				return cand, true
			}
		}
	}
	return "", false
}
