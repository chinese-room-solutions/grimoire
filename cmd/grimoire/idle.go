package main

import (
	"net/http"
	"sync"
	"time"
)

// idleTracker shuts a backend down after a period with no HTTP requests, so a
// headless instance the bridge spawned on demand doesn't linger once the agent
// stops using it. It is only installed when a positive idle timeout is set (the
// `serve --idle-timeout` path); a GUI instance or a plain `serve` never has one.
type idleTracker struct {
	timeout time.Duration
	onIdle  func() // called once, when the idle window elapses with no requests.

	mu    sync.Mutex
	timer *time.Timer
	fired bool
}

// newIdleTracker starts the idle countdown immediately (a backend nobody calls
// should still expire) and returns the tracker. onIdle runs at most once.
func newIdleTracker(timeout time.Duration, onIdle func()) *idleTracker {
	t := &idleTracker{timeout: timeout, onIdle: onIdle}
	t.timer = time.AfterFunc(timeout, t.fire)
	return t
}

// wrap returns next with each request resetting the idle countdown.
func (t *idleTracker) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.reset()
		next.ServeHTTP(w, r)
	})
}

// reset restarts the countdown. It's a no-op once the timer has already fired,
// so a request racing the shutdown can't revive a backend that's stopping.
func (t *idleTracker) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fired {
		return
	}
	t.timer.Reset(t.timeout)
}

// fire runs onIdle once, the first time the window elapses.
func (t *idleTracker) fire() {
	t.mu.Lock()
	if t.fired {
		t.mu.Unlock()
		return
	}
	t.fired = true
	t.mu.Unlock()
	t.onIdle()
}

// stop halts the timer (on normal shutdown) so it can't fire afterward.
func (t *idleTracker) stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.fired = true
	t.timer.Stop()
}
