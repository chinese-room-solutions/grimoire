package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	grimoireapp "github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// nextOp reads one op request off an attached connection, failing the test if
// none arrives.
func nextOp(t *testing.T, conn *clientConn) bridgeRequest {
	t.Helper()
	select {
	case ev := <-conn.events:
		require.Equal(t, eventOp, ev.name)
		var req bridgeRequest
		require.NoError(t, json.Unmarshal([]byte(ev.data), &req))
		return req
	case <-time.After(2 * time.Second):
		t.Fatal("no op reached the attached window")
		return bridgeRequest{}
	}
}

// The bridge must keep two in-flight ops apart: each answer goes to the caller
// that asked for it, whatever order the window answers in.
func TestClientBridgeCorrelatesConcurrentOps(t *testing.T) {
	bridge := newClientBridge("dark")
	conn := bridge.attach()
	<-conn.events // the theme sent on attach.

	type outcome struct {
		path string
		err  error
	}
	first := make(chan outcome, 1)
	second := make(chan outcome, 1)
	go func() {
		p, _, err := bridge.PickFolder(t.Context(), "first")
		first <- outcome{p, err}
	}()
	reqA := nextOp(t, conn)
	go func() {
		p, _, err := bridge.PickFolder(t.Context(), "second")
		second <- outcome{p, err}
	}()
	reqB := nextOp(t, conn)

	require.Equal(t, "first", reqA.Title)
	require.Equal(t, "second", reqB.Title)
	require.NotEqual(t, reqA.ID, reqB.ID, "each op carries its own id")

	// Answer out of order: correlation is by id, not arrival.
	require.True(t, bridge.deliver(bridgeResult{ID: reqB.ID, OK: true, Path: "/vaults/B"}))
	require.True(t, bridge.deliver(bridgeResult{ID: reqA.ID, OK: true, Path: "/vaults/A"}))

	gotA := <-first
	gotB := <-second
	require.NoError(t, gotA.err)
	require.NoError(t, gotB.err)
	require.Equal(t, "/vaults/A", gotA.path)
	require.Equal(t, "/vaults/B", gotB.path)
}

// A window that goes away mid-op must fail the caller instead of leaving it
// waiting for an answer nobody will send.
func TestClientBridgeDetachFailsPendingOps(t *testing.T) {
	bridge := newClientBridge("dark")
	conn := bridge.attach()
	<-conn.events

	done := make(chan error, 1)
	go func() {
		_, err := bridge.Screenshot(t.Context())
		done <- err
	}()
	nextOp(t, conn)

	bridge.detach(conn)
	select {
	case err := <-done:
		require.ErrorContains(t, err, errClientDetached.Error())
	case <-time.After(2 * time.Second):
		t.Fatal("the pending op was not failed by the detach")
	}
}

// A second window replaces the first: the old stream ends, the ops it still
// owed fail, and new ops go to the newcomer.
func TestClientBridgeSecondAttachReplacesFirst(t *testing.T) {
	bridge := newClientBridge("dark")
	old := bridge.attach()
	<-old.events

	done := make(chan error, 1)
	go func() {
		_, err := bridge.Screenshot(t.Context())
		done <- err
	}()
	nextOp(t, old)

	fresh := bridge.attach()
	select {
	case <-old.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the replaced window was not detached")
	}
	select {
	case err := <-done:
		require.ErrorContains(t, err, errClientDetached.Error())
	case <-time.After(2 * time.Second):
		t.Fatal("the replaced window's pending op was not failed")
	}

	ev := <-fresh.events
	require.Equal(t, eventTheme, ev.name, "a fresh window is told the current theme")
	go func() {
		_, _, _ = bridge.PickFolder(t.Context(), "after replace")
	}()
	require.Equal(t, "after replace", nextOp(t, fresh).Title)
}

// With no window attached, the two native ops degrade the way their callers
// already handle: the picker reports "nothing picked" (a headless daemon and a
// browser client both land here), and a capture reports ErrNoScreenshot, which
// the API surface answers 503 with.
func TestClientBridgeWithoutAClient(t *testing.T) {
	bridge := newClientBridge("dark")

	path, ok, err := bridge.PickFolder(t.Context(), "pick")
	require.NoError(t, err)
	require.False(t, ok)
	require.Empty(t, path)

	_, err = bridge.Screenshot(t.Context())
	require.ErrorIs(t, err, errNoClient)
	require.ErrorIs(t, err, grimoireapp.ErrNoScreenshot)
}

// A theme change reaches the attached window, and the next window to attach is
// told the current theme before anything else.
func TestClientBridgeThemeReachesTheWindow(t *testing.T) {
	bridge := newClientBridge("dark")
	conn := bridge.attach()
	require.Equal(t, clientEvent{name: eventTheme, data: "dark"}, <-conn.events)

	bridge.setTheme("light")
	require.Equal(t, clientEvent{name: eventTheme, data: "light"}, <-conn.events)

	next := bridge.attach()
	require.Equal(t, clientEvent{name: eventTheme, data: "light"}, <-next.events)
}

// The HTTP surface end to end: a window attaches over SSE, an op reaches it as
// an event, and the result it POSTs back completes the waiting call.
func TestClientChannelRoundTrip(t *testing.T) {
	ctl := testControl()
	mux := http.NewServeMux()
	mountClientChannel(mux, ctl.bridge, ctl, zerolog.Nop())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/client/channel", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	events := make(chan clientEvent, 4)
	go func() {
		defer close(events)
		sc := bufio.NewScanner(resp.Body)
		var name, data string
		for sc.Scan() {
			switch line := sc.Text(); {
			case line == "":
				if name != "" {
					events <- clientEvent{name: name, data: data}
				}
				name, data = "", ""
			case strings.HasPrefix(line, "event:"):
				name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
	}()
	require.Equal(t, clientEvent{name: eventTheme, data: "dark"}, <-events)

	shot := make(chan []byte, 1)
	go func() {
		png, err := ctl.bridge.Screenshot(ctx)
		if err != nil {
			shot <- nil
			return
		}
		shot <- png
	}()

	ev := <-events
	require.Equal(t, eventOp, ev.name)
	var opReq bridgeRequest
	require.NoError(t, json.Unmarshal([]byte(ev.data), &opReq))
	require.Equal(t, opScreenshot, opReq.Op)

	body, err := json.Marshal(bridgeResult{
		ID: opReq.ID, OK: true, PNG: base64.StdEncoding.EncodeToString([]byte("PNG")),
	})
	require.NoError(t, err)
	post, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/api/client/result", strings.NewReader(string(body)))
	require.NoError(t, err)
	post.Header.Set("Content-Type", "application/json")
	postResp, err := http.DefaultClient.Do(post)
	require.NoError(t, err)
	require.NoError(t, postResp.Body.Close())
	require.Equal(t, http.StatusNoContent, postResp.StatusCode)

	require.Equal(t, []byte("PNG"), <-shot)
}

// A result for an op nobody waits on (its caller gave up) is a 404, not a
// server error.
func TestClientResultForAnUnknownOp(t *testing.T) {
	ctl := testControl()
	mux := http.NewServeMux()
	mountClientChannel(mux, ctl.bridge, ctl, zerolog.Nop())

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/client/result", strings.NewReader(`{"id":"99","ok":true}`)))
	require.Equal(t, http.StatusNotFound, rec.Code)

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/client/result", strings.NewReader(`{"ok":true}`)))
	require.Equal(t, http.StatusBadRequest, rec.Code, "a result without an id says who it is for")
}

// The open channel is the window's keep-alive: an idle-timeout daemon must not
// retire while a window holds it, and must retire once the window lets go.
func TestClientChannelHoldsTheIdleTracker(t *testing.T) {
	ctl := testControl()
	mux := http.NewServeMux()
	mountClientChannel(mux, ctl.bridge, ctl, zerolog.Nop())

	var fired atomic.Bool
	tr := newIdleTracker(30*time.Millisecond, nil, func() { fired.Store(true) })
	defer tr.stop()

	ctx, cancel := context.WithCancel(t.Context())
	served := make(chan struct{})
	go func() {
		defer close(served)
		req := httptest.NewRequest(http.MethodGet, "/api/client/channel", nil).WithContext(ctx)
		tr.wrap(mux).ServeHTTP(httptest.NewRecorder(), req)
	}()

	time.Sleep(150 * time.Millisecond) // several idle windows with the channel open.
	require.False(t, fired.Load(), "an attached window holds the daemon alive")

	cancel()
	<-served
	require.Eventually(t, fired.Load, time.Second, 5*time.Millisecond,
		"the countdown restarts once the window detaches")
}

// A daemon shutting down must not wait out its grace window on an idle stream:
// the channel handler watches the closing signal, not just the request context
// (Shutdown never cancels those).
func TestClientChannelEndsOnDaemonClosing(t *testing.T) {
	ctl := testControl()
	mux := http.NewServeMux()
	mountClientChannel(mux, ctl.bridge, ctl, zerolog.Nop())

	served := make(chan struct{})
	go func() {
		defer close(served)
		mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/client/channel", nil))
	}()
	require.Eventually(t, func() bool {
		ctl.bridge.mu.Lock()
		defer ctl.bridge.mu.Unlock()
		return ctl.bridge.attached != nil
	}, time.Second, 5*time.Millisecond, "the window attaches")

	ctl.beginClosing()
	select {
	case <-served:
	case <-time.After(2 * time.Second):
		t.Fatal("the channel handler ignored the daemon's closing signal")
	}
}
