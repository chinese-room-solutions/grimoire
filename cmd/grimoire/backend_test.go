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

// TestStartBackendBindsBeforeServing guards the ordering that a restored note tab
// depends on: when a vault is requested, startBackend must bind it before the HTTP
// server starts serving, so the first page load can't race the bind and observe an
// empty state (which made a restored tab fail to read its note). By the time
// startBackend returns, the vault is bound and its notes are readable.
func TestStartBackendBindsBeforeServing(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("LocalAppData", cache)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("HOME", cache)
	t.Setenv("AppData", cache)
	t.Setenv("XDG_CONFIG_HOME", cache)

	vault := filepath.Join(t.TempDir(), "vault")
	require.NoError(t, os.MkdirAll(vault, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(vault, "note.md"), []byte("# hi\n\nbody"), 0o644))

	b, err := startBackend(zerolog.Nop(), vault, 0)
	require.NoError(t, err)
	t.Cleanup(b.stop)

	svc := b.holder.current()
	require.NotNil(t, svc, "the vault is bound before startBackend returns")
	require.Equal(t, vault, svc.Vault())
	// The note a restored tab would request is readable immediately — no race.
	content, err := svc.ReadNote("note.md")
	require.NoError(t, err)
	require.Equal(t, "# hi\n\nbody", content)
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
