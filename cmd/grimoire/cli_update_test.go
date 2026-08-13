package main

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// `grimoire update` reads the daemon's answer and says what to do about it;
// --apply asks for the install and reports what the daemon started. A refusal
// (409) is the "you have to run the installer yourself" case, and exits 4 so a
// script can tell it from a real failure.
func TestCLIUpdate(t *testing.T) {
	tests := []struct {
		name     string
		routes   map[string]http.HandlerFunc
		args     []string
		json     bool
		wantOut  string
		wantErr  string
		wantCode int
	}{
		{
			name: "up to date",
			routes: map[string]http.HandlerFunc{
				"GET /api/v1/ping": func(w http.ResponseWriter, _ *http.Request) {
					stubJSON(t, w, map[string]string{"version": "v0.4.1", "available": ""})
				},
			},
			args:     []string{"update"},
			wantOut:  "grimoire v0.4.1 — up to date\n",
			wantCode: exitOK,
		},
		{
			name: "an update is available",
			routes: map[string]http.HandlerFunc{
				"GET /api/v1/ping": func(w http.ResponseWriter, _ *http.Request) {
					stubJSON(t, w, map[string]string{"version": "v0.4.1", "available": "v0.5.0"})
				},
			},
			args:     []string{"update"},
			wantOut:  "v0.5.0 available (run grimoire update --apply)\n",
			wantCode: exitOK,
		},
		{
			name: "--apply reports the install it started",
			routes: map[string]http.HandlerFunc{
				"POST /api/v1/update/apply": func(w http.ResponseWriter, _ *http.Request) {
					stubJSON(t, w, map[string]string{"status": "updating", "version": "v0.5.0"})
				},
			},
			args:     []string{"update", "--apply"},
			wantOut:  "installing grimoire v0.5.0 — the app restarts itself when it's done\n",
			wantCode: exitOK,
		},
		{
			name: "--apply on an install that needs the installer exits 4",
			routes: map[string]http.HandlerFunc{
				"POST /api/v1/update/apply": func(w http.ResponseWriter, _ *http.Request) {
					stubErr(w, http.StatusConflict, "Grimoire is installed system-wide")
				},
			},
			args:     []string{"update", "--apply"},
			wantErr:  "error: Grimoire is installed system-wide\n",
			wantCode: exitConflict,
		},
		{
			name: "--json emits the raw shape",
			routes: map[string]http.HandlerFunc{
				"GET /api/v1/ping": func(w http.ResponseWriter, _ *http.Request) {
					stubJSON(t, w, map[string]string{"version": "v0.4.1", "available": "v0.5.0"})
				},
			},
			args:     []string{"update"},
			json:     true,
			wantOut:  "{\n  \"version\": \"v0.4.1\",\n  \"available\": \"v0.5.0\"\n}\n",
			wantCode: exitOK,
		},
		{
			name:     "a positional argument is a usage error",
			routes:   map[string]http.HandlerFunc{},
			args:     []string{"update", "now"},
			wantCode: exitUsage,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newCLIBackend(t, tc.routes)
			e, out, errBuf := b.env(t, tc.json)

			require.Equal(t, tc.wantCode, e.dispatch(tc.args))
			if tc.wantOut != "" {
				require.Equal(t, tc.wantOut, out.String())
			}
			if tc.wantErr != "" {
				require.Equal(t, tc.wantErr, errBuf.String())
			}
		})
	}
}
