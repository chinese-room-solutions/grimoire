package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/chinese-room-solutions/mass-sdk/webview"
	"github.com/rs/zerolog"
)

// The window's half of the control channel. The GUI process owns no backend: it
// holds one long-lived SSE request open against the daemon, applies the theme
// events it carries, and runs the native operations the daemon asks for
// (webview.PickFolder / Screenshot), POSTing each result back. Holding that
// request open is also what keeps an idle-timeout daemon alive under the window.

const (
	// channelRetryMin/Max bound the reconnect backoff. The daemon can go away
	// under a live window — a version-skew restart, a crash — and the window has
	// to find it again without spinning.
	channelRetryMin = 500 * time.Millisecond
	channelRetryMax = 5 * time.Second
)

// runClientChannel keeps the window attached to the daemon for as long as ctx
// lives, reconnecting with backoff whenever the stream drops. runGUI owns it:
// the goroutine returns when the window closes and cancels ctx.
func runClientChannel(ctx context.Context, wv webview.WindowInterface, port int, logger zerolog.Logger) {
	logger = logger.With().Str("component", "client-channel").Logger()
	backoff := channelRetryMin
	for ctx.Err() == nil {
		attached, err := streamClientChannel(ctx, wv, port, logger)
		if attached {
			backoff = channelRetryMin // a working connection earns a fast retry.
		}
		if err != nil && ctx.Err() == nil {
			logger.Info().Err(err).Msg("client channel dropped; reconnecting")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if !attached {
			backoff = min(backoff*2, channelRetryMax)
		}
	}
}

// streamClientChannel holds one connection to the daemon's channel, dispatching
// events until it ends. attached reports whether the stream was established at
// all, which tells the caller whether to back off further.
func streamClientChannel(
	ctx context.Context, wv webview.WindowInterface, port int, logger zerolog.Logger,
) (attached bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, daemonURL(port, "/api/client/channel"), nil)
	if err != nil {
		return false, fmt.Errorf("building the channel request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	// No client timeout: the stream is meant to live as long as the window does,
	// and ctx is what ends it.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("attaching to the daemon: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("attaching to the daemon: status %d", resp.StatusCode)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(nil, clientResultBodyLimit)
	var name, data string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if name != "" {
				dispatchClientEvent(ctx, wv, port, clientEvent{name: name, data: data}, logger)
			}
			name, data = "", ""
		case strings.HasPrefix(line, "event:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	return true, sc.Err()
}

// dispatchClientEvent applies one channel event. A native op runs on its own
// goroutine — the folder dialog blocks until the user answers, and the stream
// must keep reading meanwhile; the goroutine ends when the op does (at the
// latest when the window closes and ctx cancels its result POST).
func dispatchClientEvent(
	ctx context.Context, wv webview.WindowInterface, port int, ev clientEvent, logger zerolog.Logger,
) {
	switch ev.name {
	case eventTheme:
		// The daemon resolves the theme to its native base (dark|light), so this
		// process needs no theme registry of its own.
		wv.SetTheme(ev.data)
	case eventOp:
		var req bridgeRequest
		if err := json.Unmarshal([]byte(ev.data), &req); err != nil {
			logger.Warn().Err(err).Msg("decoding a native operation request")
			return
		}
		go func() {
			res := runClientOp(wv, req)
			if err := postClientResult(ctx, port, res); err != nil && ctx.Err() == nil {
				logger.Warn().Err(err).Str("op", req.Op).Msg("reporting a native operation's result")
			}
		}()
	default:
		logger.Debug().Str("event", ev.name).Msg("ignoring an unknown channel event")
	}
}

// runClientOp performs one native operation on the window. Both webview calls
// are documented safe off the UI thread, which is where this runs.
func runClientOp(wv webview.WindowInterface, req bridgeRequest) bridgeResult {
	res := bridgeResult{ID: req.ID}
	switch req.Op {
	case opPickFolder:
		path, ok, err := wv.PickFolder(req.Title)
		if err != nil {
			res.Error = err.Error()
			return res
		}
		res.Path, res.OK = path, ok
	case opScreenshot:
		png, err := wv.Screenshot()
		if err != nil {
			res.Error = err.Error()
			return res
		}
		res.PNG, res.OK = base64.StdEncoding.EncodeToString(png), true
	default:
		res.Error = "unknown operation " + req.Op
	}
	return res
}

// postClientResult hands one op's outcome back to the daemon.
func postClientResult(ctx context.Context, port int, res bridgeResult) error {
	body, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("encoding the result: %w", err)
	}
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, daemonURL(port, "/api/client/result"), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building the result request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("posting the result: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("posting the result: status %d", resp.StatusCode)
	}
	return nil
}
