---
tags: [golang, concurrency]
---

# Go concurrency

## Goroutines have owners

Every goroutine needs a defined exit path and someone responsible for it. The
rule I hold to: the function that starts a goroutine must know how it stops,
before it starts it. A leaked goroutine blocked on a channel send is invisible
until the heap profile shows thousands of them.

```go
g, ctx := errgroup.WithContext(ctx)
for _, u := range urls {
	g.Go(func() error { return fetch(ctx, u) })
}
if err := g.Wait(); err != nil {
	return err
}
```

`errgroup` is the default shape for fan-out: it propagates the first error,
cancels the derived context so the siblings unwind, and `Wait` is the join
point. `g.SetLimit(8)` bounds concurrency without a separate semaphore channel.
Note that it returns only the first error — when you need all of them, collect
into a slice under a mutex and join with `errors.Join`, see
[[error-handling]].

## Channels

- Unbuffered is a rendezvous: the send completes when a receiver takes it. Use
  it for handoff and for signalling.
- Buffered decouples producer and consumer up to the buffer. Picking a size is
  picking how much queueing you will tolerate; there is no "just in case" size.
- Close means "no more values", and only the sender closes. Closing a channel
  with multiple senders is a design error; add a done channel or a WaitGroup
  instead.
- A nil channel blocks forever, which is genuinely useful — set a case's channel
  to nil in a `select` loop to disable that branch.
- Receiving from a closed channel returns the zero value immediately, so a
  closed `chan struct{}` is the standard broadcast.

`select` with a `default` is non-blocking. `select` on `ctx.Done()` is how
anything blocking stays cancellable — including a send, which is the case people
forget:

```go
select {
case out <- v:
case <-ctx.Done():
	return ctx.Err()
}
```

## sync primitives

`sync.Mutex` for shared state, held for as short a span as possible, never
across I/O or a network call. `sync.RWMutex` when reads genuinely dominate;
under write pressure it is slower than a plain mutex.

`sync.Once` gives exactly-once initialization and is the correct way to do lazy
setup that several goroutines might race into:

```go
var (
	once   sync.Once
	client *http.Client
)

func Client() *http.Client {
	once.Do(func() { client = newClient() })
	return client
}
```

`Do` blocks concurrent callers until the first one returns, so nobody sees a
half-built value. It also considers the work done even if the function panics,
which means a failed initialization is permanent — for anything that can fail,
`sync.OnceValues` (1.21+) returning a value and an error is better, or an
explicit retry with a mutex.

`sync.WaitGroup` counts; `Add` before the goroutine starts, never inside it.
`atomic.Int64` and friends beat a mutex for a counter. `sync.Map` is for the two
patterns it was written for (write-once-read-many, disjoint key sets per
goroutine) and is slower than a guarded map otherwise.

## Context

Cancellation and deadlines travel down, values almost never should. Contexts
are for request scope: a timeout, a cancel signal, a trace id. Anything the
function needs to do its job belongs in the arguments.

Always `defer cancel()`, even when the context has a deadline — the vet check
that flags a missing cancel is catching a real leak in the parent's timer.

## The race detector

`go test -race ./...` in CI, always. It only reports races it actually observes,
so it needs the tests to exercise the concurrency; it never reports a false
positive. A data race is undefined behaviour in the memory model, not merely a
stale read — "it is only a counter" is not a defence.

Common findings: the loop variable captured by a closure (fixed by the language
in 1.22, still present in older modules), a map written from a handler, and a
`time.AfterFunc` callback touching state the caller is mutating.

## Patterns worth remembering

- Worker pool: N goroutines ranging over one jobs channel. Closing the channel
  is the shutdown signal.
- Pipeline stages connected by channels, each stage closing its output when its
  input is drained and returning early on `ctx.Done()`.
- `singleflight.Group` to collapse duplicate concurrent work behind one call —
  cache stampede protection in four lines. Used in the reconcile loops in
  [[operators]] and in front of the embedding calls in [[rag]].
