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
)

func appExePath(selfExe string) string {
	return filepath.Join(filepath.Dir(selfExe), "grimoire-app.exe")
}

func run(selfExe string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	appExe := appExePath(selfExe)
	if _, err := os.Stat(appExe); err != nil {
		_, _ = fmt.Fprintf(stderr, "grimoire: real binary not found at %s; reinstall Grimoire\n", appExe)
		return 1
	}
	cmd := exec.Command(appExe, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Run(); err != nil && cmd.ProcessState == nil {
		_, _ = fmt.Fprintf(stderr, "grimoire: cannot run %s: %v\n", appExe, err)
		return 1
	}
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
