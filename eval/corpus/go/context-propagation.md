---
tags: [golang, context]
---

# Context propagation

`context.Context` is two things the language did not otherwise have: a
cancellation signal and a deadline, flowing down a call chain. The rules are
short; the discipline is keeping them.

## The shape

First parameter, named `ctx`:

```go
func (s *Store) Get(ctx context.Context, id string) (*Note, error)
```

Not stored in structs (a context belongs to a request, a struct does not),
not a field on an options bag, not nil — `context.TODO()` while refactoring,
`context.Background()` at the true roots: `main`, tests, a goroutine you own
outright, a request handler's reconstructed context.

Downward only. A callee cancels nothing; it either observes its parent's
cancellation or creates a child scope for work it owns:

```go
ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
defer cancel()
```

The `defer cancel()` is not optional ceremony: it releases the parent's timer
and reference even on the success path, and the lostcancel vet check is
catching a real leak, see [[concurrency#Context]].

## What travels in it

Cancellation and deadlines travel down. Values are the exception people
overuse: request-scoped identity only — a trace id, an authenticated
principal, a tenant key — never arguments in disguise. If the function needs
a config value to do its job, the config value is a parameter, and the
context copy of it exists only so middleware can fish it out once at the
boundary.

The test I use: would this value make sense in a `net/http` request scope?
Yes for the request id that `http-services#Middleware` injects; no for the
database handle, the pool size, or the feature flags.

## HTTP in and out

Inbound, the work is already done: `r.Context()` is cancelled when the client
disconnects, and passing it into every downstream call means an abandoned
request stops costing database time and upstream quota. Wrapping it for extra
values or a shorter deadline is the standard handler prologue.

Outbound, the same context goes into the request: `http.NewRequestWithContext(ctx,
...)`. The classic bug is a background context on the outbound call — the
client gives up, the handler returns, and the goroutine behind the outbound
call keeps running with nobody waiting for it, holding a connection from the
pool described in [[http-services#Clients]].

## Deadline budgeting

A deadline is a budget, and intermediate hops should take a share rather than
the whole thing. The handler takes 2s for the request; the call to the
enrichment service takes `WithTimeout(ctx, 700*time.Millisecond)`; its call
to the database takes 300ms of what is left. `ctx.Err()` returning
`context.DeadlineExceeded` at the innermost layer, with the remaining time
visible via `ctx.Deadline()`, is how the layers stay honest about who
consumed the budget.

Retries must recompute against the parent budget, not start fresh timers —
a retry loop that mints new deadlines can outlive the request that started
it, which is the leak version of the concurrency rule that every goroutine
has an owner.

## Detached work: WithoutCancel

Some work must survive the request: audit events, cache writes, a commit
record. `context.WithoutCancel(ctx)` (1.21+) keeps the values while dropping
the cancellation — the correct tool, replacing the old hack of reading values
into a fresh `context.Background()`. Pair it with its own timeout, because
detached plus unbounded is how background queues silently grow:

```go
bg := context.WithoutCancel(r.Context())
bg, cancel := context.WithTimeout(bg, 5*time.Second)
defer cancel()
go audit.Emit(bg, event)
```

The goroutine owns `bg` and exits when it is done or cancelled — defined
owner, defined exit path, per [[concurrency#Goroutines have owners]].

## Cancellation is not cleanup

`ctx.Done()` firing mid-query returns `context.Canceled` from the database
call, and the transaction rolls back — that part is correct. What a context
cannot do is interrupt a computation that never checks it: a tight loop over
a billion rows, a cgo call, a file read. Cancellation propagates only through
code that cooperates, so the interfaces worth auditing are the ones that take
a context and might ignore it. In the reconcile loops this is enforced by the
manager cancelling on shutdown and every client call taking the ctx,
[[operators#The reconcile contract]]; errors surfacing from that path are
wrapped, never swallowed, per [[error-handling#Handle once]].
