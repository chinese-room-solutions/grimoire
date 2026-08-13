package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// shutdownGrace bounds how long a graceful stop waits for in-flight requests
// before the server gives up on them.
const shutdownGrace = 3 * time.Second

// daemonControl is the part of the daemon no vault owns: the build it is running
// (so a client can spot a version skew), the bridge to the attached GUI window,
// the graceful stop that /api/v1/shutdown triggers, and the closing signal
// long-lived streams select on. Streams need that signal because
// http.Server.Shutdown waits for in-flight handlers without cancelling their
// contexts — an open client channel would otherwise sit through the whole grace
// window.
type daemonControl struct {
	version string
	bridge  *clientBridge
	server  *http.Server
	logger  zerolog.Logger

	// update is what the startup check found: the newer release's tag, or "".
	// The apply handler reads it, and applies it through updater against
	// updateURL. They live here because they are the process's own state, like
	// the version above — no vault owns them.
	update    updateState
	updater   updateCheckerInterface
	updateURL string

	closeOnce sync.Once
	closing   chan struct{}
}

// newDaemonControl returns the control surface for a server that has not started
// serving yet. updater/updateURL drive the self-update surface; a nil updater
// leaves it inert (nothing is ever available, and an apply reports as much).
func newDaemonControl(
	version string, bridge *clientBridge, server *http.Server,
	updater updateCheckerInterface, updateURL string, logger zerolog.Logger,
) *daemonControl {
	return &daemonControl{
		version:   version,
		bridge:    bridge,
		server:    server,
		updater:   updater,
		updateURL: updateURL,
		logger:    logger,
		closing:   make(chan struct{}),
	}
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

// apiPingHandler reports that the daemon is alive, which build it is, and
// whether a newer one has been published. A client compares the version against
// its own and restarts the daemon on a mismatch, so an upgraded binary never
// drives yesterday's process; "available" is the self-update check's finding,
// empty when the daemon is current (or hasn't managed to look).
func apiPingHandler(ctl *daemonControl, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"version": ctl.version, "available": ctl.update.get()}, logger)
	}
}

// apiUpdateApplyHandler installs the release the check found. It does the parts
// that can fail — resolving the install, downloading and verifying the installer
// — inside the request, so a caller that gets a 200 knows the installer is
// running. Only then does it tell the window and retire the daemon, from a
// goroutine: Shutdown waits for this very handler.
func apiUpdateApplyHandler(ctl *daemonControl, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tag := ctl.update.get()
		if tag == "" || ctl.updater == nil {
			writeAPIError(w, http.StatusConflict, "no Grimoire update is available", logger)
			return
		}
		logger.Info().Str("tag", tag).Msg("applying a Grimoire update")
		if err := applyUpdate(r.Context(), ctl.updater, ctl.updateURL, tag); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, errNotInstalled) || errors.Is(err, errNeedsElevation) {
				status = http.StatusConflict
			} else {
				logger.Warn().Err(err).Msg("applying the Grimoire update")
			}
			writeAPIError(w, status, err.Error(), logger)
			return
		}
		writeJSON(w, map[string]string{"status": "updating", "version": tag}, logger)

		// The installer is waiting for this process's files to be replaceable, so
		// the window has to go too: it tells the user what is happening and closes
		// itself, which drops the last thing holding the daemon open.
		go func() {
			ctl.bridge.notifyUpdateRestarting(tag)
			ctl.stopGracefully()
		}()
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
