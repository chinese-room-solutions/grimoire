---
tags: [golang, errors]
---

# Error handling in Go

Errors are values. That is the whole design, and most of the good advice follows
from taking it literally.

## Wrapping

`fmt.Errorf("open config: %w", err)` adds context and keeps the chain
inspectable. `%v` formats it and severs the chain — a deliberate choice when you
do not want callers coupling to an implementation detail, and a bug the rest of
the time.

Context should say what *this* layer was attempting, not repeat what the callee
already said. "failed to" adds nothing; the whole string is already a failure.
The good chain reads as a path:

```
load vault: read index: open /var/db/index.sqlite: permission denied
```

Multiple errors join with `errors.Join`, which produces a tree that both
`errors.Is` and `errors.As` traverse.

## Sentinels and types

```go
var ErrNotFound = errors.New("not found")

type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Msg }
```

`errors.Is` for identity, `errors.As` for structure. Never compare error strings
— that is a test that passes until someone improves a message.

A sentinel is part of your API. Exporting one is a promise you keep across
versions, so export the few that callers can actually act on: not found, already
exists, conflict, permission denied. Everything else is opaque and should stay
that way.

## Handle once

The single most common defect: logging an error *and* returning it. Now every
layer logs, and one failure produces six lines that look like six failures.
Either handle it (log, metric, fallback, retry) or return it enriched. Not both.

The exception is a boundary where the error stops: an HTTP handler, a worker
loop, `main`. There you log the full chain once, with the request id, and map it
to a status code. Mapping belongs in exactly one place:

```go
switch {
case errors.Is(err, ErrNotFound):
	http.Error(w, "not found", http.StatusNotFound)
case errors.As(err, &verr):
	writeJSON(w, http.StatusBadRequest, verr)
default:
	log.Error("request failed", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
```

Never leak the raw chain to the client — it names your files, hosts, and
libraries. See [[http-services#Handlers]].

## Panics

Reserve them for invariants that indicate a broken program: an impossible switch
default, a nil dependency at construction time, a `MustCompile` at package init.
Do not panic across an API boundary. Recover only at the top of a goroutine you
own, and only to log and shut down cleanly — a recovered panic that continues
serving is a process with unknown state.

Note that a panic in *any* goroutine kills the process, including one inside a
library's callback. The recover has to be in the goroutine that panics; a defer
in the parent does nothing.

## Things that stay wrong

- **Ignoring errors.** `_ = f()` hides the one case you did not think about.
  `defer f.Close()` on a *write* handle discards the error that means "your data
  is not on disk"; capture it in a named return.
- **Retrying without discrimination.** Retry timeouts and 503s; do not retry a
  400. Distinguish them with typed errors, add jittered backoff, and cap it.
- **`errors.Is` on a wrapped nil.** A non-nil interface holding a nil concrete
  pointer is not nil. Return a bare `nil`, never a typed nil error variable.

## Logging

Structured, with the error as a field: `log.Error("reconcile failed", "err",
err, "resource", key)`. One event per failure, at the layer that decides what to
do about it. In the operator loops this matters double, because a returned error
becomes a requeue with backoff and a logged-and-swallowed one becomes silence;
see [[operators#The reconcile contract]].
