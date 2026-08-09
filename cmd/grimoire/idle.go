package main

import (
	"net/http"
	"sync"
	"time"
)

// idleTracker shuts a backend down after a period with no HTTP requests, so a
// headless instance the CLI spawned on demand doesn't linger once the agent stops
// using it. It is only installed when a positive idle timeout is set (the
// `serve --idle-timeout` path); a GUI instance or a plain `serve` never has one.
//
// A request in flight counts as activity for its whole duration, not just its
// arrival: the countdown is suspended while any request runs and restarts when
// the last one ends. Otherwise a call that outlives the window — a forced
// reindex of a large vault runs for minutes against the on-demand backend's
// 2-minute timeout — would have the backend shut down mid-request.
//
// Work that outlives the request that started it (a kernel session left running
// by a finished SSE stream) has no in-flight request to hold the countdown, so
// the tracker asks busy instead.
type idleTracker struct {
	timeout time.Duration
	busy    func() bool // work in progress outside any request; nil = never busy.
	onIdle  func()      // called once, when the idle window elapses with no requests.

	mu       sync.Mutex
	timer    *time.Timer
	fired    bool
	inFlight int // requests currently being served; >0 suppresses firing.
}

// newIdleTracker starts the idle countdown immediately (a backend nobody calls
// should still expire) and returns the tracker. busy reports work the request
// count can't see (pass nil when there is none); onIdle runs at most once.
func newIdleTracker(timeout time.Duration, busy func() bool, onIdle func()) *idleTracker {
	t := &idleTracker{timeout: timeout, busy: busy, onIdle: onIdle}
	// Under the lock: a zero timeout can fire before AfterFunc returns, and fire
	// reads t.timer.
	t.mu.Lock()
	defer t.mu.Unlock()
	t.timer = time.AfterFunc(timeout, t.fire)
	return t
}

// wrap returns next with each request suspending the idle countdown for as
// long as it runs.
func (t *idleTracker) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.begin()
		defer t.end()
		next.ServeHTTP(w, r)
	})
}

// begin marks a request in flight, holding the countdown. It's a no-op once
// the timer has already fired, so a request racing the shutdown can't revive a
// backend that's stopping.
func (t *idleTracker) begin() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fired {
		return
	}
	t.inFlight++
	t.timer.Stop()
}

// end retires an in-flight request, restarting the countdown when it was the
// last one. Skipped once fired, mirroring begin.
func (t *idleTracker) end() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fired {
		return
	}
	if t.inFlight--; t.inFlight == 0 {
		t.timer.Reset(t.timeout)
	}
}

// fire runs onIdle once, the first time the window elapses with nothing in
// flight and nothing busy. With a request in flight it declines without
// rescheduling — end restarts the countdown when the last request finishes.
// Busy work has no such completion hook, so that case reschedules itself and
// checks again a window later.
func (t *idleTracker) fire() {
	t.mu.Lock()
	if t.fired || t.inFlight > 0 {
		t.mu.Unlock()
		return
	}
	if t.busy != nil && t.busy() {
		t.timer.Reset(t.timeout)
		t.mu.Unlock()
		return
	}
	t.fired = true
	// onIdle shuts the server down, which waits for in-flight handlers — and those
	// call end(), which takes this lock. Release it first.
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
