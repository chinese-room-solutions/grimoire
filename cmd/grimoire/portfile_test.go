package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPortFileWriteReadRemove(t *testing.T) {
	pf := filepath.Join(t.TempDir(), portFileName)
	require.Zero(t, readPort(pf), "an absent port file reads as 0")

	require.NoError(t, writePortFile(pf, 51234))
	require.Equal(t, 51234, readPort(pf))

	// This process wrote the file, so it owns it and the removal takes.
	removePortFile(pf)
	require.Zero(t, readPort(pf), "a removed port file reads as 0")
	removePortFile(pf) // removing again is a no-op, not an error.
}

func TestPortFileLastWriterWins(t *testing.T) {
	// No lock: two writers for the same vault just overwrite; the last one wins and
	// neither errors.
	pf := filepath.Join(t.TempDir(), portFileName)
	require.NoError(t, writePortFile(pf, 1111))
	require.NoError(t, writePortFile(pf, 2222))
	require.Equal(t, 2222, readPort(pf))
}

func TestRemovePortFileLeavesForeignFile(t *testing.T) {
	// The file belongs to another process (a later writer for the same vault); a
	// departing instance must not tear down the survivor's advertisement.
	pf := filepath.Join(t.TempDir(), portFileName)
	foreign := fmt.Sprintf("%d %d", 2222, os.Getpid()+1)
	require.NoError(t, os.WriteFile(pf, []byte(foreign), 0o600))

	removePortFile(pf)
	require.Equal(t, 2222, readPort(pf), "a foreign port file is left in place")
}

func TestReadPortIgnoresGarbage(t *testing.T) {
	tests := []struct {
		name     string
		contents string
	}{
		{name: "not numbers", contents: "not-a-port"},
		{name: "pre-pid format", contents: "51234"}, // an old writer is gone; relaunch overwrites.
		{name: "empty", contents: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pf := filepath.Join(t.TempDir(), portFileName)
			require.NoError(t, os.WriteFile(pf, []byte(tt.contents), 0o600))
			require.Zero(t, readPort(pf), "unparseable contents read as 0 (no reachable instance)")
			removePortFile(pf) // unowned/unparseable: left alone, no panic.
			_, err := os.Stat(pf)
			require.NoError(t, err, "an unparseable file is not deleted")
		})
	}
}
