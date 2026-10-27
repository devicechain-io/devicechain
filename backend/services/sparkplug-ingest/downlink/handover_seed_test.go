// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 🔴 A COMMAND SETTLED WHILE THE BUCKET COULD NOT BE READ IS NOT BROUGHT BACK BY A LISTING THAT
// PREDATES ITS SETTLEMENT. The term starts with the bucket unreadable (it serves, but reboots are
// refused and entries are not yet re-armed); a write is published and confirmed meanwhile; then the
// background load succeeds with a listing taken BEFORE that (it still holds the entry). The entry
// must not be re-armed and answered a second time. Exactly one response per command. Run under -race.
func TestACommandSettledWhileTheBucketWasUnreadableIsNotAnsweredTwice(t *testing.T) {
	r := newRig(t)
	clk := r.useClock()
	oldRetry, oldBg := seedRetryDelay, seedBackground
	seedRetryDelay, seedBackground = time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { seedRetryDelay, seedBackground = oldRetry, oldBg })

	r.store.listErr = errBoom
	ctx, _ := r.term(t) // seedAttempts failed; the background load keeps trying
	r.liveIn(ctx, "c", CommandWrite, writeCmd("Setpoint", 5), nil)
	// A listing taken now, before the command is settled.
	r.store.mu.Lock()
	stale := Stored{Entries: map[string]Awaiting{}}
	for k, a := range r.store.entries {
		stale.Entries[k] = a
	}
	r.store.mu.Unlock()
	require.Len(t, stale.Entries, 1)

	r.report(metric(t, "Setpoint", i8(t, 5), clk.now()))
	assert.True(t, r.responseTo(t, "c").env.Success)
	require.Eventually(t, func() bool { return r.store.len() == 0 }, confirmWait, 5*time.Millisecond)

	// The bucket becomes readable, answering with the stale listing.
	r.store.mu.Lock()
	r.store.staleList = &stale
	r.store.listErr = nil
	r.store.mu.Unlock()
	require.Eventually(t, func() bool { return !r.d.unseeded.Load() }, confirmWait, 5*time.Millisecond)

	r.d.mu.Lock()
	assert.Empty(t, r.d.entries, "a settled command was re-armed from a stale listing")
	r.d.mu.Unlock()
	clk.advance(10 * time.Minute)
	r.d.sweep()
	time.Sleep(100 * time.Millisecond)
	assert.Len(t, r.responses.of("c"), 1, "the command was answered more than once")
	assert.Zero(t, r.result(ResultRearmed))
}

// The same load, concurrently with live confirmations: every command is answered exactly once.
func TestASeedRacingLiveSettlementsAnswersEachCommandOnce(t *testing.T) {
	r := newRig(t)
	clk := r.useClock()
	ctxA, stopA := r.term(t)
	const n = 20
	for i := 0; i < n; i++ {
		r.liveIn(ctxA, fmt.Sprintf("c%02d", i), CommandRebirth, ``, nil)
	}
	stopA()
	require.Equal(t, n, r.store.len())

	r.build(nil)
	r.store.listErr = errBoom
	oldRetry, oldBg := seedRetryDelay, seedBackground
	seedRetryDelay, seedBackground = time.Millisecond, time.Millisecond
	t.Cleanup(func() { seedRetryDelay, seedBackground = oldRetry, oldBg })
	r.term(t)
	// Births keep arriving while the load is retried and then succeeds.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
				r.report(nodeBirth(clk.now(), 5))
			}
		}
	}()
	time.Sleep(20 * time.Millisecond)
	r.store.mu.Lock()
	r.store.listErr = nil
	r.store.mu.Unlock()
	require.Eventually(t, func() bool { return !r.d.unseeded.Load() }, confirmWait, 5*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	close(stop)
	<-done
	clk.advance(time.Hour)
	r.d.sweep()
	require.Eventually(t, func() bool { return len(r.responses.all()) >= n }, confirmWait, 5*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	seen := map[string]int{}
	for _, res := range r.responses.all() {
		seen[res.env.CommandToken]++
	}
	for tok, c := range seen {
		assert.Equal(t, 1, c, "%s was answered %d times", tok, c)
	}
	assert.Len(t, seen, n)
}

// 🔴 A DECIDED OUTCOME SURVIVES A CHANGE OF LEADER. A confirmation whose response could not be
// published is stored with its record; the next leader settles the command with THAT outcome. Past
// its deadline it would otherwise come back as a timeout: a confirmed command must stay confirmed.
func TestADecidedOutcomeSurvivesAChangeOfLeader(t *testing.T) {
	r := newRig(t)
	clk := r.useClock()
	ctxA, stopA := r.term(t)
	r.liveIn(ctxA, "c", CommandWrite, writeCmd("Setpoint", 5), nil)
	r.responses.mu.Lock()
	r.responses.err = errBoom
	r.responses.mu.Unlock()
	r.report(metric(t, "Setpoint", i8(t, 5), clk.now()))
	require.Eventually(t, func() bool {
		a, ok := r.store.get(AwaitingKey("acme", "c"))
		return ok && a.Decided != nil
	}, confirmWait, 5*time.Millisecond, "the decided outcome was not stored with the record")
	stopA()
	a, _ := r.store.get(AwaitingKey("acme", "c"))
	assert.True(t, a.Decided.Success)
	assert.Empty(t, r.responses.of("c"))

	// The next leader comes up long after the window, with the bucket back.
	r.responses.mu.Lock()
	r.responses.err = nil
	r.responses.mu.Unlock()
	clk.advance(time.Hour)
	r.build(nil)
	r.term(t)
	res := r.responseTo(t, "c")
	assert.True(t, res.env.Success, "a confirmed command was re-decided into a timeout")
	assert.Equal(t, "claim-nonce", res.env.DispatchNonce)
	require.NotNil(t, res.env.Payload)
	assert.Equal(t, 1, r.result(ResultConfirmed))
	assert.Zero(t, r.result(ResultUnconfirmed))
	require.Eventually(t, func() bool { return r.store.len() == 0 }, confirmWait, 5*time.Millisecond)
}
