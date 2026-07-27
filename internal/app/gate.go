package app

import (
	"context"
	"sync"
)

// gate is a resizable counting semaphore: it caps how many operations run at once
// and its limit can change live (when the operator edits the concurrency setting)
// without disturbing in-flight work. acquire returns the token channel it used;
// release must be given that same channel, so a resize between acquire and release
// can't block or panic — a holder always drains back into its own generation's
// channel, which is then discarded once empty.
type gate struct {
	mu     sync.Mutex
	tokens chan struct{}
}

// newGate builds a gate allowing n concurrent holders (clamped to >= 1).
func newGate(n int) *gate {
	return &gate{tokens: make(chan struct{}, clampGate(n))}
}

// Acquire blocks until a slot is free or ctx is done. On success it returns the
// token (the channel used) to pass back to Release; on cancellation it returns
// ctx.Err(). Satisfies embed.LimiterInterface.
func (g *gate) Acquire(ctx context.Context) (any, error) {
	g.mu.Lock()
	ch := g.tokens
	g.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return ch, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Release frees the slot for the token Acquire returned. Non-blocking: a stale
// channel (after a resize) simply has its buffered token discarded.
func (g *gate) Release(token any) {
	ch, ok := token.(chan struct{})
	if !ok {
		return
	}
	select {
	case <-ch:
	default:
	}
}

// resize sets a new concurrency limit for subsequent acquirers. In-flight holders
// keep their old channel and drain into it harmlessly.
func (g *gate) resize(n int) {
	g.mu.Lock()
	g.tokens = make(chan struct{}, clampGate(n))
	g.mu.Unlock()
}

func clampGate(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
