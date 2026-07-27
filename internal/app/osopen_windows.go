package app

import "os/exec"

// osOpen opens path with the OS-registered default application. On Windows that
// is the shell "open" verb, reached via rundll32's FileProtocolHandler so a
// path with spaces or special characters is passed as a single argument (unlike
// `cmd /c start`, which parses its first quoted token as a window title).
func osOpen(path string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", path).Start()
}
