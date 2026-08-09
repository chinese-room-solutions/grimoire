package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// TestStartBackendServesAnyVault covers the daemon contract: one process, one
// port advertisement, and a vault resolved per request rather than bound at
// startup. A page load naming a vault gets that vault's workspace with its notes
// readable straight away, and stopping the daemon drops the advertisement.
func TestStartBackendServesAnyVault(t *testing.T) {
	isolateVaultDirs(t)
	t.Setenv("GRIMOIRE_GATEWAY_URL", "http://127.0.0.1:9") // nothing listens: no gateway calls succeed.

	vault := filepath.Join(t.TempDir(), "vault")
	require.NoError(t, os.MkdirAll(vault, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(vault, "note.md"), []byte("# hi\n\nbody"), 0o644))

	b, err := startBackend(zerolog.Nop(), 0)
	require.NoError(t, err)
	t.Cleanup(b.stop)

	portFile, err := daemonPortFile()
	require.NoError(t, err)
	require.NotZero(t, readPort(portFile), "the daemon advertises its port app-wide")

	// A request naming the vault opens it on demand.
	svc, err := b.reg.runtime(t.Context(), vault)
	require.NoError(t, err)
	require.Equal(t, vault, svc.Vault())
	content, err := svc.ReadNote("note.md")
	require.NoError(t, err)
	require.Equal(t, "# hi\n\nbody", content)

	b.stop()
	require.Zero(t, readPort(portFile), "shutdown drops the advertisement")
}

// TestLoopbackGuard covers the DNS-rebinding and cross-site defenses on the
// unauthenticated loopback server: only requests addressed to our own loopback
// host:port get through, and state-changing requests carrying a cross-site
// Origin or Sec-Fetch-Site header are rejected.
func TestLoopbackGuard(t *testing.T) {
	const port = 41234
	handler := loopbackGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), port)

	tests := []struct {
		name   string
		method string
		host   string
		header map[string]string
		want   int
	}{
		{name: "GET to 127.0.0.1", method: http.MethodGet, host: "127.0.0.1:41234", want: http.StatusNoContent},
		{name: "GET to localhost", method: http.MethodGet, host: "localhost:41234", want: http.StatusNoContent},
		{name: "GET to ::1", method: http.MethodGet, host: "[::1]:41234", want: http.StatusNoContent},
		{name: "case-insensitive host", method: http.MethodGet, host: "LocalHost:41234", want: http.StatusNoContent},
		{name: "rebound hostname rejected", method: http.MethodGet, host: "evil.example:41234", want: http.StatusForbidden},
		{name: "wrong port rejected", method: http.MethodGet, host: "127.0.0.1:9999", want: http.StatusForbidden},
		{name: "bare loopback without port rejected", method: http.MethodGet, host: "127.0.0.1", want: http.StatusForbidden},
		{name: "POST without provenance headers (curl, CLI)", method: http.MethodPost, host: "127.0.0.1:41234", want: http.StatusNoContent},
		{name: "same-origin POST (webview UI)", method: http.MethodPost, host: "127.0.0.1:41234",
			header: map[string]string{"Origin": "http://127.0.0.1:41234", "Sec-Fetch-Site": "same-origin"}, want: http.StatusNoContent},
		{name: "user-initiated POST", method: http.MethodPost, host: "127.0.0.1:41234",
			header: map[string]string{"Sec-Fetch-Site": "none"}, want: http.StatusNoContent},
		{name: "cross-site POST by Origin", method: http.MethodPost, host: "127.0.0.1:41234",
			header: map[string]string{"Origin": "http://evil.example"}, want: http.StatusForbidden},
		{name: "null Origin POST", method: http.MethodPost, host: "127.0.0.1:41234",
			header: map[string]string{"Origin": "null"}, want: http.StatusForbidden},
		{name: "cross-site POST by Sec-Fetch-Site", method: http.MethodPost, host: "127.0.0.1:41234",
			header: map[string]string{"Sec-Fetch-Site": "cross-site"}, want: http.StatusForbidden},
		{name: "same-site POST rejected", method: http.MethodPost, host: "127.0.0.1:41234",
			header: map[string]string{"Sec-Fetch-Site": "same-site"}, want: http.StatusForbidden},
		{name: "cross-site GET is read-only, allowed", method: http.MethodGet, host: "127.0.0.1:41234",
			header: map[string]string{"Sec-Fetch-Site": "cross-site"}, want: http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/v1/notes", nil)
			req.Host = tt.host
			for k, v := range tt.header {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			require.Equal(t, tt.want, rec.Code)
		})
	}
}
