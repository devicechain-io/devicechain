// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock is the resolver's notion of now under test control. Every assertion about
// a TIME bound below is made by advancing it, never by sleeping: a wall-clock test of
// a rate bound is a test whose result depends on how loaded the runner is.
type fakeClock struct{ nanos atomic.Int64 }

func newFakeClock() *fakeClock {
	c := &fakeClock{}
	c.nanos.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	return c
}

func (c *fakeClock) now() time.Time          { return time.Unix(0, c.nanos.Load()).UTC() }
func (c *fakeClock) advance(d time.Duration) { c.nanos.Add(int64(d)) }

// idle reports whether no refresh is in flight, so a test can wait for the background
// work its resolve calls started before counting fetches.
func (r *tenantResolver[V]) idle() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.inflight) == 0
}

func (r *tenantResolver[V]) cacheLen() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.cache)
}

// countingFetch returns a fetch function that counts its calls and returns err.
func countingFetch(calls *atomic.Int64, val int, err error) func(context.Context, string) (int, error) {
	return func(ctx context.Context, tenant string) (int, error) {
		calls.Add(1)
		return val, err
	}
}

// drain waits for every refresh the test's resolve calls started to finish. It is a
// wait for BACKGROUND WORK, not a measurement: the assertions that follow are exact
// counts, so a slow runner delays this test rather than changing its verdict.
func drain[V any](t *testing.T, r *tenantResolver[V]) {
	t.Helper()
	require.Eventually(t, r.idle, 10*time.Second, time.Millisecond, "refreshes should finish")
}

// 🔴 THE PROPERTY: one resolver starts at most burst + rate*elapsed refreshes, whatever
// the number of distinct tenants asking and however fast the authority answers.
//
// That is a bound on the RATE, which the concurrency cap alone never gave. A cap of N
// in-flight fetches bounds how many run at once; each frees its slot on completion, so
// against a fast-answering authority the same cap admits thousands of fetches a second.
// This test drives many thousands of distinct, never-resolvable tenants — the shape of
// an attacker-influenced hot path — and pins the exact number of fetches that escape,
// which is the token budget and nothing more.
func TestRefreshRate_BoundedRegardlessOfTenantCardinality(t *testing.T) {
	var calls atomic.Int64
	clk := newFakeClock()
	r := newTenantResolver(countingFetch(&calls, 42, errors.New("no such tenant")), 7, "test")
	r.Configure(ResolverOptions{RefreshesPerSecond: 5, RefreshBurst: 5, Clock: clk.now})

	// flood resolves a batch of tenants nothing has ever seen before, and returns
	// once every refresh they triggered has completed.
	flood := func(round int) {
		for i := 0; i < 2000; i++ {
			v, ok := r.resolveOK(fmt.Sprintf("round%d-tenant%d", round, i))
			assert.Equal(t, 7, v, "an unresolvable tenant serves the default")
			assert.False(t, ok, "an unresolvable tenant is never reported as resolved")
		}
		drain(t, r)
	}

	// Time frozen: only the burst may escape, however many tenants ask.
	flood(1)
	assert.Equal(t, int64(5), calls.Load(), "with no time elapsed, only the burst may fetch")

	// One second later: exactly the per-second rate has been replenished.
	clk.advance(time.Second)
	flood(2)
	assert.Equal(t, int64(10), calls.Load(), "one second buys exactly RefreshesPerSecond more fetches")

	// A long idle refills to the burst and no further — the bucket does not bank
	// arbitrarily many refreshes for one flood to spend at once.
	clk.advance(time.Hour)
	flood(3)
	assert.Equal(t, int64(15), calls.Load(), "an idle hour refills to the burst, not beyond it")
}

// A failed refresh is recorded, so a tenant that does not resolve is retried once per
// negative TTL rather than on every message carrying its name.
func TestFailedRefresh_HoldsOffUntilNegativeTTL(t *testing.T) {
	var calls atomic.Int64
	clk := newFakeClock()
	r := newTenantResolver(countingFetch(&calls, 42, errors.New("record not found")), 7, "test")
	// A burst far larger than the number of resolves below, so the rate bound is not
	// what this test is measuring.
	r.Configure(ResolverOptions{NegativeTTL: 30 * time.Second, RefreshBurst: 1000, Clock: clk.now})

	// Several rounds, each waiting for the refresh the previous one started: without
	// the recorded attempt, every round's first resolve finds an empty cache again and
	// starts another fetch, so a single round would not distinguish the hold-off from
	// the inflight dedupe.
	for round := 0; round < 5; round++ {
		for i := 0; i < 40; i++ {
			v, ok := r.resolveOK("ghost")
			assert.Equal(t, 7, v)
			assert.False(t, ok)
		}
		drain(t, r)
	}
	assert.Equal(t, int64(1), calls.Load(), "200 resolves of an unresolvable tenant fetch once")

	clk.advance(30*time.Second - time.Nanosecond)
	for i := 0; i < 200; i++ {
		r.resolve("ghost")
	}
	drain(t, r)
	assert.Equal(t, int64(1), calls.Load(), "still held off one nanosecond short of the negative TTL")

	clk.advance(time.Nanosecond)
	r.resolve("ghost")
	drain(t, r)
	assert.Equal(t, int64(2), calls.Load(), "retried once the negative TTL has passed")
}

// A refresh that fails for a tenant already resolved keeps serving the last-known
// value — the failure caches the ATTEMPT, never a value — and holds off for the
// negative TTL rather than the full TTL.
func TestFailedRefresh_KeepsLastKnownValue(t *testing.T) {
	var calls atomic.Int64
	var failing atomic.Bool
	clk := newFakeClock()
	fetch := func(ctx context.Context, tenant string) (int, error) {
		calls.Add(1)
		if failing.Load() {
			return 0, errors.New("user-management unreachable")
		}
		return 42, nil
	}
	r := newTenantResolver(fetch, 7, "test")
	r.Configure(ResolverOptions{TTL: time.Minute, NegativeTTL: 10 * time.Second, Clock: clk.now})

	r.resolve("acme")
	drain(t, r)
	v, ok := r.resolveOK("acme")
	require.Equal(t, 42, v)
	require.True(t, ok)

	failing.Store(true)
	clk.advance(time.Minute)
	r.resolve("acme")
	drain(t, r)
	v, ok = r.resolveOK("acme")
	assert.Equal(t, 42, v, "a failed refresh keeps the last-known value")
	assert.True(t, ok, "and it stays resolved")
	assert.Equal(t, int64(2), calls.Load())

	// Held off for the negative TTL, not for another full TTL.
	clk.advance(10*time.Second - time.Nanosecond)
	r.resolve("acme")
	drain(t, r)
	assert.Equal(t, int64(2), calls.Load(), "not retried before the negative TTL")

	clk.advance(time.Nanosecond)
	r.resolve("acme")
	drain(t, r)
	assert.Equal(t, int64(3), calls.Load(), "retried after it, well inside the full TTL")
}

// The record of tenants that failed to resolve is keyed by input the platform does not
// control, so it is bounded. Resolved tenants are never evicted to make room.
func TestNegativeEntries_AreBounded(t *testing.T) {
	clk := newFakeClock()
	r := newTenantResolver(countingFetch(new(atomic.Int64), 42, nil), 7, "test")
	r.Configure(ResolverOptions{NegativeTTL: 10 * time.Second, Clock: clk.now})

	r.mu.Lock()
	r.cache["real"] = cacheEntry[int]{val: 42, have: true, nextRefreshAt: clk.now().Add(time.Hour)}
	for i := 0; i < maxNegativeEntries+500; i++ {
		r.recordFailureLocked(fmt.Sprintf("ghost%d", i), clk.now())
	}
	negatives := r.negatives
	cached := len(r.cache)
	_, realStillThere := r.cache["real"]
	r.mu.Unlock()

	assert.Equal(t, maxNegativeEntries, negatives, "unresolvable tenants are capped")
	assert.Equal(t, maxNegativeEntries+1, cached, "and cannot grow the map past that cap")
	assert.True(t, realStillThere, "a resolved tenant is not evicted to make room for one that failed")

	// Once the recorded hold-offs expire they make room for new ones rather than
	// wedging the cap permanently.
	clk.advance(11 * time.Second)
	r.mu.Lock()
	r.recordFailureLocked("ghost-after-expiry", clk.now())
	_, recorded := r.cache["ghost-after-expiry"]
	negatives = r.negatives
	r.mu.Unlock()
	assert.True(t, recorded, "an expired hold-off is swept so a new one can be recorded")
	assert.Equal(t, 1, negatives)
}

// A tenant that fails and then RESOLVES gives its slot back. Without that, the bounded
// half of the cache only ever fills: one user-management blip across enough tenants
// wedges it at the cap for the life of the process, after which no hold-off is ever
// recorded again and every unresolvable tenant is back to a fetch per message.
//
// It drives the real refresh path rather than calling recordFailureLocked, because the
// give-back lives on the success path and a test that only records failures cannot see
// it at all.
func TestFailedThenResolved_ReleasesItsNegativeSlot(t *testing.T) {
	var failing atomic.Bool
	clk := newFakeClock()
	fetch := func(ctx context.Context, tenant string) (int, error) {
		if failing.Load() {
			return 0, errors.New("user-management unreachable")
		}
		return 42, nil
	}
	r := newTenantResolver(fetch, 7, "test")
	r.Configure(ResolverOptions{TTL: time.Minute, NegativeTTL: 10 * time.Second, RefreshBurst: 1000, Clock: clk.now})

	const tenants = 50
	for i := 0; i < tenants; i++ {
		name := fmt.Sprintf("blipped%d", i)

		failing.Store(true)
		r.resolve(name)
		drain(t, r)
		r.mu.Lock()
		negatives, entry := r.negatives, r.cache[name]
		r.mu.Unlock()
		require.Equal(t, 1, negatives, "a failed tenant occupies exactly one slot")
		require.False(t, entry.have, "and holds no value")

		// The authority comes back and the tenant resolves.
		failing.Store(false)
		clk.advance(10 * time.Second)
		r.resolve(name)
		drain(t, r)

		r.mu.Lock()
		negatives, entry = r.negatives, r.cache[name]
		r.mu.Unlock()
		assert.Equal(t, 0, negatives, "a resolved tenant releases the slot its failure took")
		assert.True(t, entry.have, "and is now a resolved entry")
	}

	v, ok := r.resolveOK("blipped0")
	assert.Equal(t, 42, v)
	assert.True(t, ok)
}

// The eviction sweep reclaims only the bounded half. A resolved tenant whose refresh is
// OVERDUE is not junk: that is exactly what a legitimate tenant looks like while the
// rate bound is refusing refreshes during a flood, and evicting it would drop that
// tenant to the platform default and report it unresolved — for the shed resolver, a
// gold tenant losing its priority mid-flood.
func TestEvictionSweep_KeepsStaleResolvedEntries(t *testing.T) {
	clk := newFakeClock()
	r := newTenantResolver(countingFetch(new(atomic.Int64), 42, nil), 7, "test")
	r.Configure(ResolverOptions{NegativeTTL: 10 * time.Second, Clock: clk.now})

	r.mu.Lock()
	// A resolved tenant whose refresh fell due an hour ago — overdue, not expendable.
	r.cache["stale-but-known"] = cacheEntry[int]{
		val: 42, have: true, nextRefreshAt: clk.now().Add(-time.Hour),
	}
	// Fill the bounded half so the next record has to sweep.
	for i := 0; i < maxNegativeEntries; i++ {
		r.recordFailureLocked(fmt.Sprintf("ghost%d", i), clk.now())
	}
	r.mu.Unlock()

	// Let every recorded hold-off expire, then force a sweep.
	clk.advance(11 * time.Second)
	r.mu.Lock()
	r.recordFailureLocked("one-more-ghost", clk.now())
	entry, present := r.cache["stale-but-known"]
	swept := len(r.cache)
	r.mu.Unlock()

	require.True(t, present, "a resolved tenant survives the sweep even when its refresh is overdue")
	assert.True(t, entry.have)
	assert.Equal(t, 42, entry.val, "with its last-known value intact")
	assert.Less(t, swept, maxNegativeEntries, "and the sweep did reclaim the expired hold-offs")

	v, ok := r.resolveOK("stale-but-known")
	assert.Equal(t, 42, v, "so it still serves its last-known value")
	assert.True(t, ok, "and still reads as resolved")
}

// Every ResolverOptions field left zero resolves to the package default, never to
// "unbounded" — the ADR-023 polarity applied to the resolver's own knobs, so a
// partially filled struct cannot silently remove a bound.
func TestConfigure_ZeroFieldsKeepPackageDefaults(t *testing.T) {
	r := newTenantResolver(countingFetch(new(atomic.Int64), 42, nil), 7, "test")
	r.Configure(ResolverOptions{})

	assert.Equal(t, defaultCacheTTL, r.ttl)
	assert.Equal(t, defaultNegativeTTL, r.negativeTTL)
	assert.Equal(t, maxConcurrentRefreshes, cap(r.sem))
	assert.Equal(t, float64(maxRefreshesPerSecond), float64(r.refreshes.Limit()))
	assert.Equal(t, maxRefreshBurst, r.refreshes.Burst())
	assert.NotNil(t, r.now)
}

// A non-finite refresh rate resolves to the package default too. +Inf is the one value
// that passes a "> 0" test and still removes the bound: rate.Limit(+Inf) leaves the
// bucket's arithmetic undefined at zero elapsed time, so it must not reach the limiter.
func TestConfigure_RejectsNonFiniteRefreshRate(t *testing.T) {
	for _, tc := range []struct {
		name string
		rate float64
	}{
		{"positive infinity", math.Inf(1)},
		{"negative infinity", math.Inf(-1)},
		{"NaN", math.NaN()},
		{"negative", -5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTenantResolver(countingFetch(new(atomic.Int64), 42, nil), 7, "test")
			r.Configure(ResolverOptions{RefreshesPerSecond: tc.rate})
			assert.Equal(t, float64(maxRefreshesPerSecond), float64(r.refreshes.Limit()),
				"an unusable rate falls back to the package bound, never to unbounded")
		})
	}
}
