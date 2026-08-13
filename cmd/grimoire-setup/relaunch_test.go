package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chinese-room-solutions/mass-sdk/install"
	"github.com/stretchr/testify/require"
)

// --relaunch has to survive an elevated re-run: the child does the actual
// install, so if the flag didn't ride along, a system-wide update would stage
// the new build and never start it.
func TestInstallArgsCarryRelaunch(t *testing.T) {
	tests := []struct {
		name     string
		relaunch bool
		want     []string
	}{
		{
			name: "an operator's install doesn't relaunch",
			want: []string{"--install", "--scope", "user", "--install-dir", "/apps/grimoire"},
		},
		{
			name:     "a self-update's install does",
			relaunch: true,
			want:     []string{"--install", "--scope", "user", "--install-dir", "/apps/grimoire", "--relaunch"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := collected{scope: install.ScopeUser, installDir: "/apps/grimoire", relaunch: tc.relaunch}
			require.Equal(t, tc.want, installArgs(c))
		})
	}
}

// The wait before staging returns as soon as the old build's files are free,
// and gives up at the cap rather than blocking the install forever — a stage
// that then fails reports a better error than a silent hang here.
func TestWaitReplaceable(t *testing.T) {
	tests := []struct {
		name    string
		exe     func(t *testing.T) string
		timeout time.Duration
		wantMax time.Duration
	}{
		{
			name: "a free path returns at once",
			exe: func(t *testing.T) string {
				t.Helper()
				path := filepath.Join(t.TempDir(), "grimoire")
				require.NoError(t, os.WriteFile(path, []byte("binary"), 0o600))
				return path
			},
			timeout: 2 * time.Second,
			wantMax: time.Second,
		},
		{
			name: "a path that never frees gives up at the cap",
			exe: func(t *testing.T) string {
				t.Helper()
				// A path inside a directory that doesn't exist can never be
				// renamed, so the probe keeps failing until the cap.
				return filepath.Join(t.TempDir(), "missing", "grimoire")
			},
			timeout: 400 * time.Millisecond,
			wantMax: 3 * time.Second,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			waitReplaceable(tc.exe(t), tc.timeout)
			require.Less(t, time.Since(start), tc.wantMax)
		})
	}
}
