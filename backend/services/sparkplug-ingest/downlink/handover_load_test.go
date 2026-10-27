// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 🔴 AN OVERDUE ENTRY IS CLAIMED BEFORE IT IS REACHABLE. The start-of-term load adds entries that
// are already past their deadline and resolves them at once. If an entry were added first and
// claimed afterwards, the sweep (or a report) running at the same moment could resolve it too, and
// the command would be answered twice. Here the load runs while a sweep and a stream of reports
// hammer the mirror; every command must be answered exactly once. Run under -race as well, where an
// unlocked claim after the add is flagged as a data race on its own.
func TestAnOverdueEntryIsResolvedOnceWhileTheTermLoadsIt(t *testing.T) {
	r := newRig(t)
	clk := r.useClock()
	ctxA, stopA := r.term(t)
	const n = 80
	for i := 0; i < n; i++ {
		r.liveIn(ctxA, fmt.Sprintf("c%03d", i), CommandRebirth, ``, nil)
	}
	stopA()
	require.Equal(t, n, r.store.len())
	clk.advance(time.Hour) // every window ended while nobody was leading

	r.build(nil)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					r.d.sweep()
					r.report(nodeBirth(clk.now(), 5))
				}
			}
		}()
	}
	require.NoError(t, r.d.seedTerm(context.Background()))
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
	r.d.sweep()
	require.Eventually(t, func() bool { return len(r.responses.all()) >= n }, confirmWait, 5*time.Millisecond)
	time.Sleep(100 * time.Millisecond)

	seen := map[string]int{}
	for _, res := range r.responses.all() {
		seen[res.env.CommandToken]++
	}
	assert.Len(t, seen, n)
	for tok, c := range seen {
		assert.Equal(t, 1, c, "%s was answered %d times", tok, c)
	}
}

// 🔴 A COMMAND PUBLISHED WHILE THE BUCKET WAS UNREADABLE KEEPS ITS LIVE ENTRY when the load finally
// lists it: the listing holds it too (it is not settled), and replacing the live entry with a
// re-armed copy would lose what the live one knows (its source, and any contrary report it saw).
func TestTheLoadDoesNotReplaceAnEntryThisTermPublished(t *testing.T) {
	r := newRig(t)
	clk := r.useClock()
	oldRetry, oldBg := seedRetryDelay, seedBackground
	seedRetryDelay, seedBackground = time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { seedRetryDelay, seedBackground = oldRetry, oldBg })

	r.store.listErr = errBoom
	ctx, _ := r.term(t)
	r.liveIn(ctx, "c", CommandWrite, writeCmd("Setpoint", 5), nil)
	key := AwaitingKey("acme", "c")
	r.d.mu.Lock()
	live := r.d.entries[key]
	r.d.mu.Unlock()
	require.NotNil(t, live)

	r.store.mu.Lock()
	r.store.listErr = nil
	r.store.mu.Unlock()
	require.Eventually(t, func() bool { return !r.d.unseeded.Load() }, confirmWait, 5*time.Millisecond)

	r.d.mu.Lock()
	cur := r.d.entries[key]
	r.d.mu.Unlock()
	assert.Same(t, live, cur, "the load replaced the entry this term published")
	assert.False(t, cur.rearmed)
	assert.Zero(t, r.result(ResultRearmed))
	r.report(metric(t, "Setpoint", i8(t, 5), clk.now()))
	assert.True(t, r.responseTo(t, "c").env.Success)
}
