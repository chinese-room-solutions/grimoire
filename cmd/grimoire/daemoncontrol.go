package main

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// shutdownGrace bounds how long a graceful stop waits for in-flight requests
// before the server gives up on them.
const shutdownGrace = 3 * time.Second

// daemonControl is the part of the daemon no vault owns: the build it is running
// (so a client can spot a version skew), the graceful stop that /api/v1/shutdown
// triggers, and the closing signal long-lived streams select on. Streams need
// that signal because http.Server.Shutdown waits for in-flight handlers without
// cancelling their contexts — an open stream would otherwise sit through the
// whole grace window.
type daemonControl struct {
	version string
	server  *http.Server
	logger  zerolog.Logger

	closeOnce sync.Once
	closing   chan struct{}
}

// newDaemonControl returns the control surface for a server that has not started
// serving yet.
func newDaemonControl(version string, server *http.Server, logger zerolog.Logger) *daemonControl {
	return &daemonControl{version: version, server: server, logger: logger, closing: make(chan struct{})}
}

// beginClosing releases every long-lived stream so the graceful stop that
// follows has nothing open to wait on. Idempotent.
func (d *daemonControl) beginClosing() { d.closeOnce.Do(func() { close(d.closing) }) }

// stopGracefully ends the streams and shuts the HTTP server down, giving
// in-flight requests up to shutdownGrace to finish. It blocks until the server
// has stopped accepting; callers inside a handler run it on their own goroutine
// so their response goes out first (Shutdown waits for them).
func (d *daemonControl) stopGracefully() {
	d.beginClosing()
	ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := d.server.Shutdown(ctx); err != nil {
		d.logger.Warn().Err(err).Msg("shutting the daemon's HTTP server down")
	}
}

// apiPingHandler reports that the daemon is alive and which build it is. A
// client compares the version against its own and restarts the daemon on a
// mismatch, so an upgraded binary never drives yesterday's process.
func apiPingHandler(ctl *daemonControl, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"version": ctl.version}, logger)
	}
}

// apiShutdownHandler asks the daemon to retire. It answers 200 first and stops
// the server from a goroutine — Shutdown waits for this very request, so doing
// it inline would deadlock the response it is supposed to send.
func apiShutdownHandler(ctl *daemonControl, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		logger.Info().Msg("shutdown requested over the API")
		writeJSON(w, map[string]string{"status": "stopping"}, logger)
		go ctl.stopGracefully()
	}
}
