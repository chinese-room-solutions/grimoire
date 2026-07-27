package main

import (
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
	port, err := waitForPort(portFile, 2*time.Second)
	require.NoError(t, err)
	require.Equal(t, 54321, port)
}

func TestWaitForPortTimesOut(t *testing.T) {
	portFile := filepath.Join(t.TempDir(), "singleton.port") // never written.
	_, err := waitForPort(portFile, 50*time.Millisecond)
	require.Error(t, err)
}
