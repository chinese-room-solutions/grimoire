package app

import "os/exec"

// reap waits out a started opener process so the OS can release it: on Unix an
// unwaited child stays a zombie for the app's lifetime, and on Windows its
// process handle is never closed. The opener is fire-and-forget — nothing acts
// on how it exited — so Wait's error is deliberately discarded here.
func reap(cmd *exec.Cmd) {
	go func() { _ = cmd.Wait() }()
}
