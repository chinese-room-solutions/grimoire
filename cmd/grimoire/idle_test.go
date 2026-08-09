package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIdleTrackerFiresAfterTimeout(t *testing.T) {
	var fired atomic.Bool
	tr := newIdleTracker(30*time.Millisecond, nil, func() { fired.Store(true) })
	defer tr.stop()
	require.Eventually(t, fired.Load, time.Second, 5*time.Millisecond,
		"onIdle should run once the window elapses with no activity")
}

func TestIdleTrackerResetOnRequest(t *testing.T) {
	var fired atomic.Bool
	tr := newIdleTracker(60*time.Millisecond, nil, func() { fired.Store(true) })
	defer tr.stop()
	handler := tr.wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))

	// Keep it busy across more than one window; it must not fire while requests arrive.
	for i := 0; i < 5; i++ {
		time.Sleep(20 * time.Millisecond)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/vaults", nil))
	}
	require.False(t, fired.Load(), "activity within the window keeps it alive")

	// Once requests stop, it eventually fires.
	require.Eventually(t, fired.Load, time.Second, 5*time.Millisecond)
}

// TestIdleTrackerHeldByInFlightRequest guards the long-call case (a forced
// reindex runs minutes against the on-demand backend's 2-minute window): the
// countdown must not fire while a request is in flight, and restarts once the
// last one ends.
func TestIdleTrackerHeldByInFlightRequest(t *testing.T) {
	var fired atomic.Bool
	tr := newIdleTracker(30*time.Millisecond, nil, func() { fired.Store(true) })
	defer tr.stop()
	release := make(chan struct{})
	handler := tr.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/reindex", nil))
	}()

	// Hold the request across several idle windows.
	time.Sleep(120 * time.Millisecond)
	require.False(t, fired.Load(), "an in-flight request must hold the backend alive")

	close(release)
	<-done
	require.Eventually(t, fired.Load, time.Second, 5*time.Millisecond,
		"the countdown restarts once the last request ends")
}

func TestIdleTrackerStopPreventsFiring(t *testing.T) {
	var fired atomic.Bool
	tr := newIdleTracker(20*time.Millisecond, nil, func() { fired.Store(true) })
	tr.stop()
	time.Sleep(60 * time.Millisecond)
	require.False(t, fired.Load(), "a stopped tracker must not fire")
}

// TestIdleTrackerHeldByBusyWork guards the case an in-flight request can't cover:
// a kernel session still running after the SSE stream that started it returned.
// Nothing calls end() to restart the countdown, so the tracker must reschedule
// itself while busy reports true — and retire once it stops.
func TestIdleTrackerHeldByBusyWork(t *testing.T) {
	var busy, fired atomic.Bool
	busy.Store(true)
	tr := newIdleTracker(20*time.Millisecond, busy.Load, func() { fired.Store(true) })
	defer tr.stop()

	// Several windows pass with no requests at all: busy alone keeps it alive.
	time.Sleep(100 * time.Millisecond)
	require.False(t, fired.Load(), "busy work must hold the backend alive")

	busy.Store(false)
	require.Eventually(t, fired.Load, time.Second, 5*time.Millisecond,
		"the tracker retires once the work finishes")
}
