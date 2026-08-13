package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chinese-room-solutions/grimoire/internal/appspec"
	"github.com/chinese-room-solutions/mass-sdk/install"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// fakeUpdater stands in for mass-sdk/selfupdate: the tests here exercise what
// Grimoire does with a release, not how the release is discovered (that is the
// SDK's own httptest coverage).
type fakeUpdater struct {
	latest    string
	latestErr error
	newer     bool
	// setup is copied to the fetch destination as the "installer"; fetchErr
	// fails the download instead.
	setup    []byte
	fetchErr error

	// what the daemon asked for, for the assertions.
	fetchedTag   string
	fetchedAsset string
}

func (f *fakeUpdater) Latest(context.Context, string) (string, error) {
	return f.latest, f.latestErr
}

func (f *fakeUpdater) IsNewer(string, string) bool { return f.newer }

func (f *fakeUpdater) FetchSetup(_ context.Context, _, tag, asset, destDir string) (string, error) {
	f.fetchedTag, f.fetchedAsset = tag, asset
	if f.fetchErr != nil {
		return "", f.fetchErr
	}
	path := filepath.Join(destDir, asset)
	if err := os.WriteFile(path, f.setup, 0o700); err != nil { //nolint:gosec // a test's fake installer.
		return "", err
	}
	return path, nil
}

// The startup check records a newer release and leaves the state alone
// otherwise — including for a dev build, where IsNewer is what says no (the
// daemon must not carry a guard of its own that could disagree with the SDK's).
func TestCheckForUpdate(t *testing.T) {
	tests := []struct {
		name string
		up   *fakeUpdater
		want string
	}{
		{
			name: "a newer release is recorded",
			up:   &fakeUpdater{latest: "v0.5.0", newer: true},
			want: "v0.5.0",
		},
		{
			name: "the running build is current",
			up:   &fakeUpdater{latest: "v0.4.1", newer: false},
			want: "",
		},
		{
			name: "a dev build sees nothing (IsNewer refuses it)",
			up:   &fakeUpdater{latest: "v0.5.0", newer: false},
			want: "",
		},
		{
			name: "an unreachable repository leaves the state empty",
			up:   &fakeUpdater{latestErr: errors.New("dial tcp: no route to host")},
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var state updateState
			checkForUpdate(t.Context(), tc.up, &state, "https://example.invalid/repo", "v0.4.1", zerolog.Nop())
			require.Equal(t, tc.want, state.get())
		})
	}
}

// A cancelled context ends the check without recording anything, so a daemon
// stopping mid-check leaves no half-answer behind.
func TestCheckForUpdateHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var state updateState
	checkForUpdate(ctx, &fakeUpdater{latestErr: context.Canceled}, &state, "u", "v0.4.1", zerolog.Nop())
	require.Empty(t, state.get())
}

// Ping carries the check's finding, so the window and the CLI read the same
// answer off the route they already call.
func TestAPIPingReportsAnAvailableUpdate(t *testing.T) {
	port, ctl, _ := controlServerWith(t, "v0.4.1", &fakeUpdater{}, "u")
	ctl.update.set("v0.5.0")

	body := getJSONBody(t, port, "/api/v1/ping")
	require.Equal(t, map[string]string{"version": "v0.4.1", "available": "v0.5.0"}, body)
}

// The apply refuses what it cannot do, and does what it can: it downloads the
// platform's installer into the app dir and starts it over the recorded
// install. Each case is the whole request/response, since the status is the
// contract the UI branches on.
func TestAPIUpdateApply(t *testing.T) {
	tests := []struct {
		name string
		// available is the tag the check found ("" = none).
		available string
		// record writes an install record at the returned dir; nil writes none.
		record func(t *testing.T, home string) string
		// unwritable makes the recorded install dir refuse new files.
		unwritable bool
		fetchErr   error
		wantStatus int
		wantErr    string
	}{
		{
			name:       "nothing to install",
			available:  "",
			wantStatus: http.StatusConflict,
			wantErr:    "no Grimoire update is available",
		},
		{
			name:       "no install record: this build was placed by hand",
			available:  "v0.5.0",
			wantStatus: http.StatusConflict,
			wantErr:    "wasn't installed by the Grimoire installer",
		},
		{
			name:      "a recorded dir this user can't write needs the installer",
			available: "v0.5.0",
			record: func(t *testing.T, home string) string {
				t.Helper()
				return filepath.Join(home, "system-install")
			},
			unwritable: true,
			wantStatus: http.StatusConflict,
			wantErr:    "needs administrator rights",
		},
		{
			name:      "a failed download is a server error, not a refusal",
			available: "v0.5.0",
			record: func(t *testing.T, home string) string {
				t.Helper()
				return filepath.Join(home, "grimoire")
			},
			fetchErr:   errors.New("checksum mismatch"),
			wantStatus: http.StatusInternalServerError,
			wantErr:    "checksum mismatch",
		},
		{
			name:      "the happy path answers 200 and starts the installer",
			available: "v0.5.0",
			record: func(t *testing.T, home string) string {
				t.Helper()
				return filepath.Join(home, "grimoire")
			},
			wantStatus: http.StatusOK,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := isolateAppEnv(t)
			marker := filepath.Join(home, "installer-ran")

			var installDir string
			if tc.record != nil {
				installDir = tc.record(t, home)
				require.NoError(t, os.MkdirAll(installDir, 0o755))
				require.NoError(t, appspec.Spec.SaveRecord(install.Record{InstallDir: installDir}))
				if tc.unwritable {
					require.NoError(t, os.Chmod(installDir, 0o500))
					t.Cleanup(func() { _ = os.Chmod(installDir, 0o755) })
				}
			}

			up := &fakeUpdater{setup: fakeSetupScript(marker), fetchErr: tc.fetchErr}
			port, ctl, stopped := controlServerWith(t, "v0.4.1", up, "https://example.invalid/repo")
			ctl.update.set(tc.available)

			resp, err := daemonRequest(t.Context(), http.MethodPost, port, "/api/v1/update/apply")
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, tc.wantStatus, resp.StatusCode)

			raw, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			if tc.wantErr != "" {
				require.Contains(t, string(raw), tc.wantErr)
				return
			}

			// 200 means the installer is running and the daemon is retiring.
			require.Contains(t, string(raw), "v0.5.0")
			require.Equal(t, "v0.5.0", up.fetchedTag)
			require.Equal(t, setupAssetName(), up.fetchedAsset)
			select {
			case <-stopped:
			case <-time.After(5 * time.Second):
				t.Fatal("the daemon kept serving after starting the update")
			}
			// The installer really ran, with the arguments setupArgs built (it
			// echoes them into the marker file). Windows won't execute a shell
			// script under the .exe name the asset carries, so the spawn itself
			// is asserted on POSIX; TestSetupArgs covers the arguments on both.
			if runtime.GOOS == "windows" {
				return
			}
			require.Eventually(t, func() bool {
				_, err := os.Stat(marker)
				return err == nil
			}, 10*time.Second, 50*time.Millisecond, "the installer was never started")
			args, err := os.ReadFile(marker) //nolint:gosec // a path this test built.
			require.NoError(t, err)
			require.Equal(t, strings.Join(setupArgs(installDir), " "), strings.TrimSpace(string(args)))
		})
	}
}

// A second update download starts from an empty directory: the installer a
// previous run left behind has already done its job, and a half-finished one
// must never be mistaken for a good download.
func TestStageDirClearsThePreviousDownload(t *testing.T) {
	isolateAppEnv(t)

	dir, err := stageDir()
	require.NoError(t, err)
	stale := filepath.Join(dir, "grimoire-setup-old")
	require.NoError(t, os.WriteFile(stale, []byte("stale"), 0o600))

	again, err := stageDir()
	require.NoError(t, err)
	require.Equal(t, dir, again)
	entries, err := os.ReadDir(again)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// The installer is told exactly where to install and in which scope, so a
// custom directory survives the update instead of snapping to a scope default.
func TestSetupArgs(t *testing.T) {
	sep := string(filepath.Separator)
	tests := []struct {
		name string
		// installDir is built from the isolated home, since the user/system
		// split is decided against the running user's own directories.
		installDir func(home string) string
		wantScope  install.Scope
	}{
		{
			name:       "a user-scoped dir installs without elevation",
			installDir: func(home string) string { return filepath.Join(home, "apps", "grimoire") },
			wantScope:  install.ScopeUser,
		},
		{
			name:       "a machine-wide dir keeps the system scope",
			installDir: func(string) string { return filepath.Join(sep, "opt", "grimoire") },
			wantScope:  install.ScopeSystem,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.installDir(isolateAppEnv(t))
			args := setupArgs(dir)
			require.Equal(t, []string{
				"--install", "--install-dir", dir, "--scope", string(tc.wantScope), "--relaunch",
			}, args)
		})
	}
}

// fakeSetupScript is a stand-in for the downloaded grimoire-setup: it echoes the
// arguments it was spawned with into marker, so the apply test can assert on
// them without building a second binary. POSIX-only (see the caller).
func fakeSetupScript(marker string) []byte {
	return []byte("#!/bin/sh\nprintf '%s' \"$*\" > '" + marker + "'\n")
}

// The asset name is the platform's, since the release carries one per OS/arch
// and downloading the wrong one would fail the checksum rather than say why.
func TestSetupAssetName(t *testing.T) {
	name := setupAssetName()
	require.True(t, strings.HasPrefix(name, "grimoire-setup_"+runtime.GOOS+"_"+runtime.GOARCH), name)
	if runtime.GOOS == "windows" {
		require.True(t, strings.HasSuffix(name, ".exe"), name)
	}
}

// isolateAppEnv points the home, config, and cache roots at a temp dir, so the
// install record and app dir a test touches are its own.
func isolateAppEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("LocalAppData", filepath.Join(home, "AppData", "Local"))
	return home
}

// getJSONBody reads one of the daemon's JSON routes.
func getJSONBody(t *testing.T, port int, path string) map[string]string {
	t.Helper()
	resp, err := daemonRequest(t.Context(), http.MethodGet, port, path)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return body
}
