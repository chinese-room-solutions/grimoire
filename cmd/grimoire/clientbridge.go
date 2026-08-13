package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"

	grimoireapp "github.com/chinese-room-solutions/grimoire/internal/app"
	"github.com/rs/zerolog"
)

// The client control channel is how the daemon reaches the GUI window it has no
// in-process handle on. The window is a plain webview pointed at the daemon's
// page, so anything native — the folder dialog, a window capture, the title-bar
// theme — has to travel over HTTP:
//
//	GET  /api/client/channel  the window attaches; an SSE stream of theme changes
//	                          and native-op requests flows down it.
//	POST /api/client/result   the window reports one op's outcome back.
//
// The open channel request is also the daemon's keep-alive: it counts as an
// in-flight request for the window's whole life, so an idle-timeout daemon can't
// retire under an open window.

// Native operations the daemon asks the attached window to perform.
const (
	opPickFolder = "pick-folder"
	opScreenshot = "screenshot"
	// opUpdateRestarting tells the window an update is being installed and it is
	// about to be replaced: it says so and closes itself. Unlike the others it
	// reports no result — the window is gone by the time one could arrive.
	opUpdateRestarting = "update-restarting"
)

// SSE event names on the channel.
const (
	eventTheme = "theme" // payload: the native chrome base, "dark" or "light".
	eventOp    = "op"    // payload: a JSON bridgeRequest.
)

// clientEventBuffer is how many events may queue for a window that is slow to
// read. Ops are user-driven and rare, so anything beyond this means the window
// is wedged rather than busy.
const clientEventBuffer = 16

var (
	// errNoClient reports a native op asked for with no window attached — a
	// headless `serve`, or a browser pointed at the daemon. It wraps
	// ErrNoScreenshot so the API surface keeps answering 503 for a capture with
	// nothing to capture.
	errNoClient = fmt.Errorf("%w: no grimoire window is attached to this daemon", grimoireapp.ErrNoScreenshot)
	// errClientDetached reports an op whose window went away before it answered.
	errClientDetached = errors.New("the grimoire window detached before the operation finished")
)

// bridgeRequest is an `op` event's payload: which native operation to run, and
// the id the window echoes back with the result.
type bridgeRequest struct {
	ID      string `json:"id"`
	Op      string `json:"op"`
	Title   string `json:"title,omitempty"`   // pick-folder's dialog title.
	Version string `json:"version,omitempty"` // update-restarting's incoming release tag.
}

// bridgeResult is what the window POSTs once an op finishes. OK is the
// pick-folder dialog's "the user chose something"; Error is a failure of the op
// itself (the platform has no picker, the capture failed).
type bridgeResult struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Path  string `json:"path,omitempty"`
	PNG   string `json:"pngBase64,omitempty"`
	Error string `json:"error,omitempty"`
}

// clientEvent is one message pushed down the channel.
type clientEvent struct {
	name string
	data string
}

// clientConn is one attached window: the queue of events waiting to go down its
// stream, and a done channel closed when it detaches (the stream handler
// returned, or a newer window replaced it).
type clientConn struct {
	events chan clientEvent
	done   chan struct{}
	once   sync.Once
}

// close marks the connection detached, releasing every send and every op
// waiting on it. Idempotent.
func (c *clientConn) close() { c.once.Do(func() { close(c.done) }) }

// send queues an event, waiting for room while the connection lives. It reports
// false when the connection detached or ctx ended first.
func (c *clientConn) send(ctx context.Context, ev clientEvent) bool {
	select {
	case c.events <- ev:
		return true
	case <-c.done:
		return false
	case <-ctx.Done():
		return false
	}
}

// trySend queues an event without waiting, for the callers that have no context
// to wait under (a theme change is a notification, not a request). A full queue
// means the window isn't reading, so the event is dropped; the theme is re-sent
// whole on the next attach.
func (c *clientConn) trySend(ev clientEvent) {
	select {
	case c.events <- ev:
	default:
	}
}

// clientBridge relays native-UI ops to the attached GUI client and holds the
// theme the window's chrome should show. One client at a time — multi-window is
// out of scope — so a new attach replaces the previous one and fails whatever
// ops the old window still owed.
type clientBridge struct {
	mu       sync.Mutex
	attached *clientConn
	pending  map[string]chan bridgeResult
	nextID   uint64
	theme    string // the native chrome base ("dark"/"light") sent on attach.
}

// newClientBridge returns an unattached bridge whose first attach will be told
// to use theme (a "dark"/"light" base, already resolved).
func newClientBridge(theme string) *clientBridge {
	return &clientBridge{pending: map[string]chan bridgeResult{}, theme: theme}
}

// attach makes conn the daemon's window and returns it. The previous window (if
// any) is detached and every op it still owed fails: only the attached window
// knows the ids in flight, and the replacement never saw them.
func (b *clientBridge) attach() *clientConn {
	conn := &clientConn{events: make(chan clientEvent, clientEventBuffer), done: make(chan struct{})}

	b.mu.Lock()
	prev := b.attached
	orphaned := b.pending
	b.attached, b.pending = conn, map[string]chan bridgeResult{}
	theme := b.theme
	b.mu.Unlock()

	if prev != nil {
		prev.close()
	}
	failPending(orphaned, errClientDetached)
	// The window opens before it attaches, so its chrome may be a step behind the
	// stored theme; the first event settles it.
	conn.trySend(clientEvent{name: eventTheme, data: theme})
	return conn
}

// detach drops conn as the daemon's window, failing the ops it still owes. A
// connection already replaced by a newer one is left alone — the replacement
// owns the pending map now.
func (b *clientBridge) detach(conn *clientConn) {
	b.mu.Lock()
	if b.attached != conn {
		b.mu.Unlock()
		return
	}
	orphaned := b.pending
	b.attached, b.pending = nil, map[string]chan bridgeResult{}
	b.mu.Unlock()

	conn.close()
	failPending(orphaned, errClientDetached)
}

// failPending releases every waiter in a detached window's pending set. The
// result channels are buffered, so no waiter has to be there yet.
func failPending(pending map[string]chan bridgeResult, err error) {
	for id, ch := range pending {
		ch <- bridgeResult{ID: id, Error: err.Error()}
	}
}

// setTheme records the native chrome base and pushes it to the attached window.
func (b *clientBridge) setTheme(base string) {
	b.mu.Lock()
	b.theme = base
	conn := b.attached
	b.mu.Unlock()
	if conn != nil {
		conn.trySend(clientEvent{name: eventTheme, data: base})
	}
}

// notifyUpdateRestarting tells the attached window that tag is being installed
// and it is about to be closed. Nothing is waited for: the window's job is to
// say so and quit, and the daemon is retiring right behind it. With no window
// attached (a headless serve, a browser client) there is simply nobody to tell.
func (b *clientBridge) notifyUpdateRestarting(tag string) {
	b.mu.Lock()
	conn := b.attached
	b.mu.Unlock()
	if conn == nil {
		return
	}
	data, err := json.Marshal(bridgeRequest{Op: opUpdateRestarting, Version: tag})
	if err != nil {
		return // a two-field struct cannot fail to marshal.
	}
	conn.trySend(clientEvent{name: eventOp, data: string(data)})
}

// PickFolder runs the window's native folder dialog. With no window attached it
// reports errNoClient — a headless daemon and a browser client both land there,
// and the caller has to offer a path field instead. That is a different answer
// from a cancelled dialog (ok=false), which means the user said no.
func (b *clientBridge) PickFolder(ctx context.Context, title string) (string, bool, error) {
	res, err := b.call(ctx, bridgeRequest{Op: opPickFolder, Title: title})
	if err != nil {
		return "", false, err
	}
	return res.Path, res.OK, nil
}

// Screenshot captures the attached window. With no window it reports errNoClient
// (an ErrNoScreenshot, so the API answers 503 headless).
func (b *clientBridge) Screenshot(ctx context.Context) ([]byte, error) {
	res, err := b.call(ctx, bridgeRequest{Op: opScreenshot})
	if err != nil {
		return nil, err
	}
	png, err := base64.StdEncoding.DecodeString(res.PNG)
	if err != nil {
		return nil, fmt.Errorf("decoding the window's screenshot: %w", err)
	}
	return png, nil
}

// call sends one op to the attached window and waits for its result, the
// caller's context, or the window detaching — whichever comes first.
func (b *clientBridge) call(ctx context.Context, req bridgeRequest) (bridgeResult, error) {
	b.mu.Lock()
	conn := b.attached
	if conn == nil {
		b.mu.Unlock()
		return bridgeResult{}, errNoClient
	}
	b.nextID++
	req.ID = strconv.FormatUint(b.nextID, 10)
	ch := make(chan bridgeResult, 1)
	b.pending[req.ID] = ch
	b.mu.Unlock()

	data, err := json.Marshal(req)
	if err != nil {
		b.drop(req.ID)
		return bridgeResult{}, fmt.Errorf("encoding the %s request: %w", req.Op, err)
	}
	if !conn.send(ctx, clientEvent{name: eventOp, data: string(data)}) {
		b.drop(req.ID)
		if ctx.Err() != nil {
			return bridgeResult{}, ctx.Err()
		}
		return bridgeResult{}, errClientDetached
	}

	select {
	case res := <-ch:
		if res.Error != "" {
			return res, fmt.Errorf("the grimoire window could not %s: %s", req.Op, res.Error)
		}
		return res, nil
	case <-ctx.Done():
		b.drop(req.ID)
		return bridgeResult{}, ctx.Err()
	case <-conn.done:
		b.drop(req.ID)
		return bridgeResult{}, errClientDetached
	}
}

// drop forgets an op nobody is waiting for any more, so a late result finds
// nothing to deliver to instead of leaking the entry.
func (b *clientBridge) drop(id string) {
	b.mu.Lock()
	delete(b.pending, id)
	b.mu.Unlock()
}

// deliver routes a window's result to the waiter that asked for it, reporting
// false for an id nobody is waiting on (a late answer to an abandoned op).
func (b *clientBridge) deliver(res bridgeResult) bool {
	b.mu.Lock()
	ch, ok := b.pending[res.ID]
	delete(b.pending, res.ID)
	b.mu.Unlock()
	if !ok {
		return false
	}
	ch <- res
	return true
}

// mountClientChannel registers the window's control channel. It is not part of
// the /api/v1 agent surface: it exists for the process's own GUI client, and the
// loopback guard is what keeps anything else off it.
func mountClientChannel(mux *http.ServeMux, bridge *clientBridge, ctl *daemonControl, logger zerolog.Logger) {
	logger = logger.With().Str("component", "client-channel").Logger()
	mux.HandleFunc("GET /api/client/channel", clientChannelHandler(bridge, ctl, logger))
	mux.HandleFunc("POST /api/client/result", clientResultHandler(bridge, logger))
}

// clientChannelHandler streams theme changes and native-op requests to the
// attached window, for as long as the window holds the request open. It returns
// when the window disconnects, when a newer window replaces it, or when the
// daemon starts shutting down — that last case matters because Shutdown waits
// for in-flight handlers without cancelling their contexts, so a stream that
// only watched r.Context() would hold the graceful window hostage.
func clientChannelHandler(bridge *clientBridge, ctl *daemonControl, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		conn := bridge.attach()
		defer bridge.detach(conn)
		logger.Debug().Msg("grimoire window attached")

		for {
			select {
			case ev := <-conn.events:
				if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.name, ev.data); err != nil {
					logger.Info().Err(err).Msg("writing to the window's channel; detaching")
					return
				}
				flusher.Flush()
			case <-conn.done:
				return // replaced by a newer window.
			case <-r.Context().Done():
				return
			case <-ctl.closing:
				return
			}
		}
	}
}

// clientResultBodyLimit caps a result body. A screenshot rides in it base64-
// encoded, so it is sized for a window capture rather than for a JSON blob.
const clientResultBodyLimit = 64 << 20

// clientResultHandler takes one op's outcome from the window and hands it to
// the waiting caller. An id nobody waits on is 404 rather than an error: the
// op was abandoned (its request was cancelled) while the window worked on it.
func clientResultHandler(bridge *clientBridge, logger zerolog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var res bridgeResult
		if err := json.NewDecoder(io.LimitReader(r.Body, clientResultBodyLimit)).Decode(&res); err != nil {
			writeAPIError(w, http.StatusBadRequest, "malformed result body: "+err.Error(), logger)
			return
		}
		if res.ID == "" {
			writeAPIError(w, http.StatusBadRequest, "missing operation id", logger)
			return
		}
		if !bridge.deliver(res) {
			writeAPIError(w, http.StatusNotFound, "no operation is waiting for id "+res.ID, logger)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
