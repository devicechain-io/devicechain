// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"
	"fmt"
	"sync"
)

// refreshGroup owns the context and lifetime of background refreshes. Previously,
// untracked refreshes used context.Background, calling user-management during
// teardown and potentially writing the cache after stop returned. Cancellation
// stops cooperative fetches; joining covers cache writes and resolver cleanup.
type refreshGroup struct {
	mu     sync.Mutex
	closed bool
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newRefreshGroup() *refreshGroup {
	ctx, cancel := context.WithCancel(context.Background())
	return &refreshGroup{ctx: ctx, cancel: cancel}
}

// begin admits a refresh unless shutdown has begun. Add runs under mu so it
// cannot race close's Wait, even when the group has no refreshes yet.
func (g *refreshGroup) begin() (context.Context, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil, false
	}
	g.wg.Add(1)
	return g.ctx, true
}

// end marks an admitted refresh finished, including all of its cache effects.
func (g *refreshGroup) end() {
	g.wg.Done()
}

// close refuses new refreshes, cancels those in flight and joins them up to ctx.
// Repeated calls wait again, allowing a caller to finish a previously bounded wait.
func (g *refreshGroup) close(ctx context.Context) error {
	g.mu.Lock()
	g.closed = true
	g.cancel()
	g.mu.Unlock()

	done := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("governance refreshes were still running: %w", ctx.Err())
	}
}

// processRefreshes is the group every resolver built by newTenantResolver joins.
var processRefreshes = newRefreshGroup()

// StopRefreshes refuses new refreshes across the process, cancels those in flight
// and waits for them to return, bounded by ctx. Services use this LifecycleCallback
// as Stopper.Postprocess, after their own stop and the shared ExecuteStop, so no
// callers are still resolving. This is terminal: resolvers continue serving cached
// or default values but never fetch again, including resolvers hidden inside gates.
func StopRefreshes(ctx context.Context) error { return processRefreshes.close(ctx) }
