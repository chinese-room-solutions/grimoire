//go:build windows

// Command grimoire-cli is the Windows console stub. The app binary is built
// -H windowsgui, so a terminal gives it no stdio and no exit code; this
// console-subsystem grimoire.exe relays argv and stdio to the real GUI binary
// (grimoire-app.exe) beside it and mirrors its exit code. Linux and macOS have
// no console/GUI split and ship no stub.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

func appExePath(selfExe string) string {
	return filepath.Join(filepath.Dir(selfExe), "grimoire-app.exe")
}

func tracef(format string, a ...any) {
	path := os.Getenv("GRIMOIRE_CLI_TRACE")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "[%7.3f] %s\n", time.Since(start).Seconds(), fmt.Sprintf(format, a...))
}

var start = time.Now()

func run(selfExe string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	tracef("run: enter")
	appExe := appExePath(selfExe)
	if _, err := os.Stat(appExe); err != nil {
		_, _ = fmt.Fprintf(stderr, "grimoire: real binary not found at %s; reinstall Grimoire\n", appExe)
		return 1
	}

	// A GUI-subsystem child ignores console handles passed through
	// StartupInfo — under mintty the stub's stdio IS a console (the pty's
	// hidden console), so relaying those handles verbatim loses the app's
	// output. Re-medium everything through pipes: real kernel handles a GUI
	// child can read and write whatever the stub's own stdio is.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "grimoire: cannot create pipe: %v\n", err)
		return 1
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "grimoire: cannot create pipe: %v\n", err)
		return 1
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "grimoire: cannot create pipe: %v\n", err)
		return 1
	}

	cmd := exec.Command(appExe, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinR, stdoutW, stderrW
	if err := cmd.Start(); err != nil {
		_, _ = fmt.Fprintf(stderr, "grimoire: cannot run %s: %v\n", appExe, err)
		return 1
	}
	// The child holds its pipe ends now; closing ours makes EOF propagate.
	_ = stdinR.Close()
	_ = stdoutW.Close()
	_ = stderrW.Close()
	tracef("child started")

	// Drain stdout/stderr while the child runs — a full pipe would block it.
	// Both copies end on their own: the child's exit closes its pipe ends.
	// The stdin relay must NOT be waited on: against a tty, os.Stdin never
	// EOFs, and once the child has exited nobody reads its stdin pipe — the
	// goroutine dies with the process.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { _, _ = io.Copy(stdinW, stdin); _ = stdinW.Close() }()
	go func() {
		defer wg.Done()
		n, err := io.Copy(stdout, stdoutR)
		tracef("stdout drain end (n=%d err=%v)", n, err)
	}()
	go func() {
		defer wg.Done()
		n, err := io.Copy(stderr, stderrR)
		tracef("stderr drain end (n=%d err=%v)", n, err)
	}()
	wg.Wait()
	tracef("drains done; waiting child")

	if err := cmd.Wait(); err != nil && cmd.ProcessState == nil {
		_, _ = fmt.Fprintf(stderr, "grimoire: cannot run %s: %v\n", appExe, err)
		return 1
	}
	tracef("child waited; exit=%d", cmd.ProcessState.ExitCode())
	if code := cmd.ProcessState.ExitCode(); code >= 0 {
		return code
	}
	return 1
}

func main() {
	selfExe, err := os.Executable()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "grimoire: cannot locate myself: %v\n", err)
		os.Exit(1)
	}
	os.Exit(run(selfExe, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
