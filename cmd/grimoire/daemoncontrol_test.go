package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// testControl is a daemonControl over a server nothing serves, for the handler
// tests that only need the routes mounted.
func testControl() *daemonControl {
	return newDaemonControl(version, newClientBridge("dark"),
		&http.Server{ReadHeaderTimeout: time.Second}, zerolog.Nop())
}

// controlServer runs the daemon's control routes on a real loopback listener,
// returning its port and a channel closed when the server stops. A real server
// is what makes the shutdown assertion meaningful: the handler has to answer
// before the process it is stopping goes away.
func controlServer(t *testing.T, buildVersion string) (port int, stopped <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	mux := http.NewServeMux()
	server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	ctl := newDaemonControl(buildVersion, newClientBridge("dark"), server, zerolog.Nop())
	mountAPI(mux, nil, ctl, zerolog.Nop())

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serving: %v", err)
		}
	}()
	t.Cleanup(func() {
		_ = server.Close()
		<-done
	})
	return ln.Addr().(*net.TCPAddr).Port, done
}

// Ping reports the build the daemon is running — the fact a client compares
// against its own to decide whether to restart it.
func TestAPIPingReportsTheVersion(t *testing.T) {
	port, _ := controlServer(t, "1.2.3-test")

	got, err := daemonVersion(t.Context(), port)
	require.NoError(t, err)
	require.Equal(t, "1.2.3-test", got)
}

// Shutdown answers first and stops the daemon after: a client that got a 200
// knows the request landed, and the server is gone a moment later.
func TestAPIShutdownAnswersThenStops(t *testing.T) {
	port, stopped := controlServer(t, "1.2.3-test")

	require.NoError(t, requestDaemonShutdown(t.Context(), port))
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon kept serving after the shutdown request")
	}

	// And it really stopped listening.
	_, err := daemonVersion(t.Context(), port)
	require.Error(t, err)
}

// The ping body is the documented shape, so a client can read it without
// guessing.
func TestAPIPingBodyShape(t *testing.T) {
	port, _ := controlServer(t, "9.9.9")

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	resp, err := daemonRequest(ctx, http.MethodGet, port, "/api/v1/ping")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, map[string]string{"version": "9.9.9"}, body)
}
