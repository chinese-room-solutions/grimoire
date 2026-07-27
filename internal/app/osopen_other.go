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
	return exec.Command(opener, path).Start()
}
