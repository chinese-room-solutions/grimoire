package app

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGateBoundsConcurrency(t *testing.T) {
	const limit = 3
	g := newGate(limit)

	var inflight, peak atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := g.Acquire(context.Background())
			require.NoError(t, err)
			defer g.Release(tok)
			n := inflight.Add(1)
			for { // record the running peak.
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			inflight.Add(-1)
		}()
	}
	wg.Wait()
	require.LessOrEqual(t, peak.Load(), int64(limit), "never exceeds the limit")
	require.Positive(t, peak.Load())
}

func TestGateAcquireRespectsContext(t *testing.T) {
	g := newGate(1)
	tok, err := g.Acquire(context.Background())
	require.NoError(t, err)
	defer g.Release(tok)

	// The single slot is taken; a cancelled context unblocks the second acquire.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = g.Acquire(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

func TestGateResize(t *testing.T) {
	g := newGate(1)
	tok, err := g.Acquire(context.Background())
	require.NoError(t, err)

	// Grow to 2: a second slot is now available even while the first is held.
	g.resize(2)
	tok2, err := g.Acquire(context.Background())
	require.NoError(t, err)

	// Releasing the pre-resize token (against its old channel) is safe.
	g.Release(tok)
	g.Release(tok2)
}

func TestGateClampsToOne(t *testing.T) {
	g := newGate(0) // clamps to 1.
	tok, err := g.Acquire(context.Background())
	require.NoError(t, err)
	g.Release(tok)
}
