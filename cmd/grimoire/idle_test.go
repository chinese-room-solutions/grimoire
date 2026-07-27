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
	tr := newIdleTracker(30*time.Millisecond, func() { fired.Store(true) })
	defer tr.stop()
	require.Eventually(t, fired.Load, time.Second, 5*time.Millisecond,
		"onIdle should run once the window elapses with no activity")
}

func TestIdleTrackerResetOnRequest(t *testing.T) {
	var fired atomic.Bool
	tr := newIdleTracker(60*time.Millisecond, func() { fired.Store(true) })
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

func TestIdleTrackerStopPreventsFiring(t *testing.T) {
	var fired atomic.Bool
	tr := newIdleTracker(20*time.Millisecond, func() { fired.Store(true) })
	tr.stop()
	time.Sleep(60 * time.Millisecond)
	require.False(t, fired.Load(), "a stopped tracker must not fire")
}
