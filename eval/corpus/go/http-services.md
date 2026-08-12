---
tags: [golang, http, services]
---

# Writing HTTP services in Go

## Server defaults are wrong

`http.ListenAndServe` has no timeouts. A single slow client holds a connection
and a goroutine indefinitely, which is a denial of service you built yourself.

```go
srv := &http.Server{
	Addr:              ":8080",
	Handler:           mux,
	ReadHeaderTimeout: 5 * time.Second,
	ReadTimeout:       30 * time.Second,
	WriteTimeout:      30 * time.Second,
	IdleTimeout:       120 * time.Second,
	MaxHeaderBytes:    1 << 20,
}
```

`WriteTimeout` covers the whole exchange, so it is wrong for streaming
responses and for server-sent events — set it to zero there and enforce a
deadline per handler with `http.TimeoutHandler` or a context timeout instead.

## Routing

The 1.22 `http.ServeMux` finally does methods and wildcards:
`mux.HandleFunc("GET /notes/{id}", h)` with `r.PathValue("id")`. For most
services that removes the router dependency entirely. Reach for chi when you
want sub-routers with per-group middleware.

## Handlers

Constructor functions returning a handler keep dependencies explicit and
testable:

```go
func handleGetNote(store StoreInterface) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		note, err := store.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, note)
	}
}
```

`r.Context()` is cancelled when the client disconnects, so passing it into every
downstream call means an abandoned request stops costing you database time. One
error-mapping helper for the whole service, per [[error-handling]].

Decode with `json.NewDecoder(r.Body)` and `DisallowUnknownFields` when the
schema is yours; cap the body with `http.MaxBytesReader` before decoding, or an
upload decides your memory limit for you.

## Middleware

An `func(http.Handler) http.Handler` chain: recovery outermost, then request id,
logging, metrics, auth, then the handler. Two rules that keep biting people:

- A wrapper around `http.ResponseWriter` must preserve the optional interfaces —
  `http.Flusher`, `http.Hijacker`, `io.ReaderFrom`. Losing `Flusher` silently
  breaks streaming and SSE, and nothing errors; the bytes just never arrive.
  `http.ResponseController` (1.20+) is the sanctioned way to reach through.
- Middleware that writes a status must record it once. Two `WriteHeader` calls
  log a "superfluous" warning and the second is discarded.

## Clients

Never `http.DefaultClient` for outbound calls: no timeout. Build one with a
`Timeout`, and tune the transport — `MaxIdleConnsPerHost` defaults to 2, which
quietly serializes a service that talks to one backend a lot. Reuse the client;
constructing one per request leaks connections and defeats keep-alive.

Always drain and close the body (`io.Copy(io.Discard, resp.Body)` then `Close`),
or the connection is not returned to the pool.

## Graceful shutdown

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()
<-ctx.Done()
shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
defer cancel()
_ = srv.Shutdown(shutdownCtx)
```

`Shutdown` stops accepting, then waits for in-flight requests. It does **not**
wait for hijacked connections or your background workers — those need their own
context and a `WaitGroup`, per [[concurrency]].

In the cluster this pairs with a `preStop` sleep, because the process gets
SIGTERM before it has been removed from the Service endpoints; without the
sleep, graceful shutdown races endpoint propagation and clients see connection
resets. Details in [[services-and-networking]].

## Observability

`log/slog` with a handler chosen at startup, a request id in the context, and
one log line per request at the boundary. Histogram of latency by route and
status, not by full path — cardinality is a monitoring bill, see
[[monitoring]].
