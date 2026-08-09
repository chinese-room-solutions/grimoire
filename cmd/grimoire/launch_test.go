package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWaitForPortReturnsOncePublished(t *testing.T) {
	portFile := filepath.Join(t.TempDir(), portFileName)
	// Publish the port shortly after the wait starts, the way a backend does.
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = writePortFile(portFile, 54321)
	}()
	port, err := waitForPort(context.Background(), portFile, 2*time.Second)
	require.NoError(t, err)
	require.Equal(t, 54321, port)
}

func TestWaitForPortTimesOut(t *testing.T) {
	portFile := filepath.Join(t.TempDir(), "singleton.port") // never written.
	_, err := waitForPort(context.Background(), portFile, 50*time.Millisecond)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// A cancelled caller (Ctrl-C during a CLI command) stops the wait instead of
// polling out the full launch timeout.
func TestWaitForPortHonoursCancellation(t *testing.T) {
	portFile := filepath.Join(t.TempDir(), portFileName) // never written.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := waitForPort(ctx, portFile, time.Minute)
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 10*time.Second, "the wait ends with the context, not the timeout")
}
