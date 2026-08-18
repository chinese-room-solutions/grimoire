package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chinese-room-solutions/grimoire/internal/appspec"
	"github.com/chinese-room-solutions/mass-sdk/install"
	"github.com/chinese-room-solutions/mass-sdk/selfupdate"
	"github.com/stretchr/testify/require"
)

// releaseServer stands in for the release repository: /releases/latest answers
// the redirect the SDK reads the newest tag out of. An empty tag answers 404, so
// a test can also drive the "nothing published" path.
func releaseServer(t *testing.T, tag string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tag == "" {
			http.Error(w, "no releases", http.StatusNotFound)
			return
		}
		http.Redirect(w, r, "/releases/tag/"+tag, http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// fakeFetch stands in for the verified download: the tests here exercise what
// Grimoire does with a release, not how the release is fetched (that is the
// SDK's own coverage).
type fakeFetch struct {
	// setup is written to the fetch destination as the "installer"; err fails
	// the download instead.
	setup []byte
	err   error

	// what the daemon asked for, for the assertions.
	tag   string
	asset string
}

func (f *fakeFetch) fetch(_ context.Context, _, tag, asset, destDir string) (string, error) {
	f.tag, f.asset = tag, asset
	if f.err != nil {
		return "", f.err
	}
	path := filepath.Join(destDir, asset)
	if err := os.WriteFile(path, f.setup, 0o700); err != nil { //nolint:gosec // a test's fake installer.
		return "", err
	}
	return path, nil
}

// Ping carries the check's finding, so the window and the CLI read the same
// answer off the route they already call.
func TestAPIPingReportsAnAvailableUpdate(t *testing.T) {
	port, ctl, _ := controlServerWith(t, "v0.4.1", releaseServer(t, "v0.5.0"))
	_, err := ctl.update.Check(t.Context())
	require.NoError(t, err)

	body := getJSONBody(t, port, "/api/v1/ping")
	require.Equal(t, "v0.4.1", body["version"])
	require.Equal(t, "v0.5.0", body["available"])
	require.NotEmpty(t, body["checked_at"])
	require.Empty(t, body["error"])
}

// The check route asks the repository there and then, so a window that rendered
// before the startup check landed — or an app left running past a release — can
// get a current answer on demand. A repository it can't reach still answers 200:
// the sentence is the point, and a status code would read as "up to date".
func TestAPIUpdateCheck(t *testing.T) {
	tests := []struct {
		name          string
		baseURL       func(t *testing.T) string
		wantAvailable string
		wantErr       bool
	}{
		{
			name:          "a newer release is reported",
			baseURL:       func(t *testing.T) string { t.Helper(); return releaseServer(t, "v0.5.0") },
			wantAvailable: "v0.5.0",
		},
		{
			name:    "the running build is current",
			baseURL: func(t *testing.T) string { t.Helper(); return releaseServer(t, "v0.4.1") },
		},
		{
			name:    "an unreachable repository answers 200 with the reason",
			baseURL: func(*testing.T) string { return "http://127.0.0.1:1/grimoire" },
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			port, _, _ := controlServerWith(t, "v0.4.1", tc.baseURL(t))

			resp, err := daemonRequest(t.Context(), http.MethodPost, port, "/api/v1/update/check")
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, http.StatusOK, resp.StatusCode)

			var body map[string]string
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
			require.Equal(t, "v0.4.1", body["version"])
			require.Equal(t, tc.wantAvailable, body["available"])
			require.NotEmpty(t, body["checked_at"])
			if tc.wantErr {
				require.NotEmpty(t, body["error"])
			} else {
				require.Empty(t, body["error"])
			}
		})
	}
}

// The apply refuses what it cannot do, and does what it can: it downloads the
// platform's installer into the app dir and starts it over the recorded
// install. Each case is the whole request/response, since the status is the
// contract the UI branches on.
func TestAPIUpdateApply(t *testing.T) {
	tests := []struct {
		name string
		// release is the tag the repository publishes ("" = none, so the check
		// finds nothing to install).
		release string
		// record writes an install record at the returned dir; nil writes none.
		record func(t *testing.T, home string) string
		// unwritable makes the recorded install dir refuse new files.
		unwritable bool
		// skipWindows, when set, is why this case can't run on Windows.
		skipWindows string
		fetchErr    error
		wantStatus  int
		wantErr     string
	}{
		{
			name:       "nothing to install",
			wantStatus: http.StatusConflict,
			wantErr:    "no Grimoire update is available",
		},
		{
			name:       "no install record: this build was placed by hand",
			release:    "v0.5.0",
			wantStatus: http.StatusConflict,
			wantErr:    "wasn't installed by the Grimoire installer",
		},
		{
			name:    "a recorded dir this user can't write needs the installer",
			release: "v0.5.0",
			record: func(t *testing.T, home string) string {
				t.Helper()
				return filepath.Join(home, "system-install")
			},
			unwritable:  true,
			skipWindows: "a directory's permission bits don't stop writes on Windows",
			wantStatus:  http.StatusConflict,
			wantErr:     "needs administrator rights",
		},
		{
			name:    "a failed download is a server error, not a refusal",
			release: "v0.5.0",
			record: func(t *testing.T, home string) string {
				t.Helper()
				return filepath.Join(home, "grimoire")
			},
			fetchErr:   errors.New("checksum mismatch"),
			wantStatus: http.StatusInternalServerError,
			wantErr:    "checksum mismatch",
		},
		{
			name:    "the happy path answers 200 and starts the installer",
			release: "v0.5.0",
			record: func(t *testing.T, home string) string {
				t.Helper()
				return filepath.Join(home, "grimoire")
			},
			skipWindows: "Windows won't execute the fake setup script under the .exe name the asset carries",
			wantStatus:  http.StatusOK,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.skipWindows != "" && runtime.GOOS == "windows" {
				t.Skip(tc.skipWindows)
			}
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

			up := &fakeFetch{setup: fakeSetupScript(marker), err: tc.fetchErr}
			port, ctl, stopped := controlServerWith(t, "v0.4.1", releaseServer(t, tc.release))
			ctl.applier.FetchSetup = up.fetch
			if tc.release != "" {
				_, err := ctl.update.Check(t.Context())
				require.NoError(t, err)
			}

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
			require.Equal(t, "v0.5.0", up.tag)
			require.Equal(t, selfupdate.SetupAsset(appspec.Spec), up.asset)
			select {
			case <-stopped:
			case <-time.After(5 * time.Second):
				t.Fatal("the daemon kept serving after starting the update")
			}
			// The installer really ran, with the arguments SetupArgs built — it
			// echoes them into the marker file.
			require.Eventually(t, func() bool {
				_, err := os.Stat(marker)
				return err == nil
			}, 10*time.Second, 50*time.Millisecond, "the installer was never started")
			args, err := os.ReadFile(marker) //nolint:gosec // a path this test built.
			require.NoError(t, err)
			require.Equal(t, strings.Join(selfupdate.SetupArgs(installDir), " "), strings.TrimSpace(string(args)))
		})
	}
}

// fakeSetupScript is a stand-in for the downloaded grimoire-setup: it echoes the
// arguments it was spawned with into marker, so the apply test can assert on
// them without building a second binary. POSIX-only (see the caller).
func fakeSetupScript(marker string) []byte {
	return []byte("#!/bin/sh\nprintf '%s' \"$*\" > '" + marker + "'\n")
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
