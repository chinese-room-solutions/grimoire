//go:build !windows

package app

import (
	"os/exec"
	"runtime"
)

// osOpen opens path with the OS-registered default application: `open` on macOS,
// `xdg-open` on other Unix-likes.
func osOpen(path string) error {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	cmd := exec.Command(opener, path)
	if err := cmd.Start(); err != nil {
		return err
	}
	reap(cmd)
	return nil
}
