// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

var platformDefault = Limits{MessagesPerSecond: 1000, Burst: 2000}

// fakeFetcher records call counts and returns a fixed result (or error).
type fakeFetcher struct {
	mu     sync.Mutex
	calls  int
	result Limits
	err    error
}

func (f *fakeFetcher) Fetch(ctx context.Context, tenant string) (Limits, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.result, f.err
}

func (f *fakeFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// The first Resolve serves the platform default (nothing cached) and triggers an
// out-of-band refresh; once it completes, Resolve serves the fetched override.
func TestResolve_DefaultThenOverride(t *testing.T) {
	f := &fakeFetcher{result: Limits{MessagesPerSecond: 5, Burst: 10}}
	r := NewTenantLimitResolver(f, platformDefault, "test")

	rps, burst := r.Resolve("acme")
	assert.Equal(t, float64(1000), rps, "uncached tenant serves the platform default")
	assert.Equal(t, 2000, burst)

	assert.Eventually(t, func() bool {
		rps, burst := r.Resolve("acme")
		return rps == 5 && burst == 10
	}, time.Second, 5*time.Millisecond, "override should populate the cache")
}

// A fetch error leaves the tenant at the platform default (fail-open, never
// unmetered).
func TestResolve_FailOpenToDefault(t *testing.T) {
	f := &fakeFetcher{err: errors.New("user-management unreachable")}
	r := NewTenantLimitResolver(f, platformDefault, "test")

	rps, burst := r.Resolve("acme")
	assert.Equal(t, float64(1000), rps)
	assert.Equal(t, 2000, burst)

	// Give the background refresh a chance to run and fail; the value must not
	// change (and must never be zero/unmetered).
	assert.Eventually(t, func() bool { return f.callCount() >= 1 }, time.Second, 5*time.Millisecond)
	rps, burst = r.Resolve("acme")
	assert.Equal(t, float64(1000), rps, "still the platform default after a failed refresh")
	assert.Equal(t, 2000, burst)
}

// Rapid repeated Resolve calls for the same uncached tenant collapse to a single
// in-flight refresh (deduped), not one fetch per call.
func TestResolve_DedupesInflight(t *testing.T) {
	// A fetcher that blocks until released, so several Resolve calls land while the
	// first refresh is still running.
	release := make(chan struct{})
	f := &blockingFetcher{gate: release, result: Limits{MessagesPerSecond: 5, Burst: 10}}
	r := NewTenantLimitResolver(f, platformDefault, "test")

	for i := 0; i < 20; i++ {
		r.Resolve("acme")
	}
	close(release)

	assert.Eventually(t, func() bool {
		rps, _ := r.Resolve("acme")
		return rps == 5
	}, time.Second, 5*time.Millisecond)
	assert.Equal(t, 1, f.callCount(), "20 rapid resolves must trigger exactly one fetch")
}

// A cached entry older than the TTL is refreshed; a fresh one is served without a
// fetch.
func TestResolve_StaleRefresh(t *testing.T) {
	f := &fakeFetcher{result: Limits{MessagesPerSecond: 5, Burst: 10}}
	r := NewTenantLimitResolver(f, platformDefault, "test")
	now := time.Unix(0, 0)
	r.now = func() time.Time { return now }

	// Populate the cache.
	r.Resolve("acme")
	assert.Eventually(t, func() bool {
		rps, _ := r.Resolve("acme")
		return rps == 5
	}, time.Second, 5*time.Millisecond)
	callsAfterFirst := f.callCount()

	// A fresh entry serves without another fetch.
	r.Resolve("acme")
	assert.Equal(t, callsAfterFirst, f.callCount(), "fresh cache entry must not refetch")

	// Advance past the TTL and change the upstream value; the stale entry refreshes.
	now = now.Add(r.ttl + time.Second)
	f.mu.Lock()
	f.result = Limits{MessagesPerSecond: 7, Burst: 14}
	f.mu.Unlock()
	r.Resolve("acme") // serves stale (5) and triggers refresh
	assert.Eventually(t, func() bool {
		rps, _ := r.Resolve("acme")
		return rps == 7
	}, time.Second, 5*time.Millisecond, "stale entry should refresh to the new value")
}

// A flood of distinct uncached tenants cannot spawn more than the concurrency cap
// of simultaneous fetches, so a fake-tenant flood can't amplify into unbounded
// lookups against user-management.
func TestResolve_CapsConcurrentRefreshes(t *testing.T) {
	release := make(chan struct{})
	f := &blockingFetcher{gate: release, result: Limits{MessagesPerSecond: 5, Burst: 10}}
	r := NewTenantLimitResolver(f, platformDefault, "test")

	// Resolve many distinct tenants while every fetch is blocked; each is uncached
	// so each wants a refresh, but the cap bounds how many actually launch.
	for i := 0; i < 100; i++ {
		r.Resolve(tenantName(i))
	}
	// Concurrent (blocked) fetches must not exceed the cap.
	assert.Eventually(t, func() bool { return f.callCount() == maxConcurrentRefreshes }, time.Second, 5*time.Millisecond)
	// Give any stragglers a moment; it must still be exactly the cap, no more.
	assert.Equal(t, maxConcurrentRefreshes, f.callCount(), "concurrent fetches must be capped")
	close(release)
}

// Each dimension names a distinct pair of tenantGovernance fields; mixing them up
// would silently govern the wrong resource.
func TestDimensions_AreDistinct(t *testing.T) {
	assert.Equal(t, "ingestMessagesPerSecond", Ingest.RateField)
	assert.Equal(t, "ingestBurst", Ingest.BurstField)
	assert.Equal(t, "outboundMessagesPerSecond", Outbound.RateField)
	assert.Equal(t, "outboundBurst", Outbound.BurstField)
	assert.Equal(t, "aiInferenceRequestsPerMinute", AIInference.RateField)
	assert.Equal(t, "aiInferenceBurst", AIInference.BurstField)

	seen := map[string]bool{}
	for _, d := range []Dimension{Ingest, Outbound, AIInference} {
		assert.False(t, seen[d.Name], "dimension names must be distinct: %s", d.Name)
		assert.False(t, seen[d.RateField], "rate fields must be distinct: %s", d.RateField)
		assert.False(t, seen[d.BurstField], "burst fields must be distinct: %s", d.BurstField)
		seen[d.Name], seen[d.RateField], seen[d.BurstField] = true, true, true
	}
}

// Every declared dimension must carry a positive scale. A zero value would mean the
// declared unit is unstated; PerSecond falls back to identity rather than zeroing a
// ceiling, but a real dimension should never rely on that fallback.
func TestDimensions_DeclareAPositiveScale(t *testing.T) {
	for _, d := range []Dimension{Ingest, Outbound, AIInference} {
		assert.Positive(t, d.PerSecondScale, "%s must declare its rate unit", d.Name)
	}
}

// The per-second dimensions pass rates through untouched; the AI dimension is
// declared per minute, so its rate divides by 60 on the way to the bucket.
func TestDimension_PerSecond(t *testing.T) {
	assert.Equal(t, 1000.0, Ingest.PerSecond(1000))
	assert.Equal(t, 100.0, Outbound.PerSecond(100))
	assert.InDelta(t, 0.5, AIInference.PerSecond(30), 1e-9, "30/minute is 0.5/second")
	assert.InDelta(t, 1.0, AIInference.PerSecond(60), 1e-9)

	// A zero-value Dimension meters at the declared number rather than at nothing.
	assert.Equal(t, 42.0, Dimension{Name: "unscaled"}.PerSecond(42))
}

// A non-positive platform default is floored to defaultLimits rather than served. A
// config key left unset arrives here as a zero value, and a zero ceiling is NOT
// "unlimited" — it is a bucket that admits nothing, i.e. a total outage for every
// tenant with no override and for every tenant during the cold-cache window.
func TestNewTenantLimitResolver_FloorsNonPositiveDefault(t *testing.T) {
	cases := []struct {
		name string
		def  Limits
		want Limits
	}{
		{"both unset", Limits{}, defaultLimits},
		{"rate unset", Limits{Burst: 2000}, Limits{MessagesPerSecond: defaultLimits.MessagesPerSecond, Burst: 2000}},
		{"burst unset", Limits{MessagesPerSecond: 1000}, Limits{MessagesPerSecond: 1000, Burst: defaultLimits.Burst}},
		{"negative", Limits{MessagesPerSecond: -1, Burst: -1}, defaultLimits},
		{"NaN rate", Limits{MessagesPerSecond: math.NaN(), Burst: 2000}, Limits{MessagesPerSecond: defaultLimits.MessagesPerSecond, Burst: 2000}},
		{"infinite rate", Limits{MessagesPerSecond: math.Inf(1), Burst: 2000}, Limits{MessagesPerSecond: defaultLimits.MessagesPerSecond, Burst: 2000}},
		{"usable default is untouched", platformDefault, platformDefault},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A fetcher that never answers, so what is asserted is the value served
			// for an unresolved tenant — the cold-cache and fail-open reading.
			f := &blockingFetcher{gate: make(chan struct{})}
			r := NewTenantLimitResolver(f, tc.def, "test")
			rps, burst := r.Resolve("acme")
			assert.Equal(t, tc.want.MessagesPerSecond, rps)
			assert.Equal(t, tc.want.Burst, burst)
			assert.Positive(t, rps, "a served ceiling must admit something")
			assert.Positive(t, burst, "a served ceiling must admit something")
		})
	}
}

// The fetcher folds overrides onto the same floored default, so the value served on a
// cold miss and the value a null override resolves to cannot disagree.
func TestNewServiceFetcher_FloorsNonPositiveDefault(t *testing.T) {
	f := NewServiceFetcher(nil, "", Limits{}, Ingest).(*serviceFetcher)
	assert.Equal(t, defaultLimits, f.def)

	limits, floored := resolveLimits(map[string]json.RawMessage{}, f.def, Ingest)
	assert.Empty(t, floored)
	assert.Equal(t, defaultLimits, limits, "a tenant with no override inherits the floored default")
}

func tenantName(i int) string {
	return "tenant-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
}

// blockingFetcher blocks in Fetch until its gate is closed, to exercise inflight
// dedup and the concurrency cap.
type blockingFetcher struct {
	mu     sync.Mutex
	calls  int
	gate   chan struct{}
	result Limits
}

func (f *blockingFetcher) Fetch(ctx context.Context, tenant string) (Limits, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	<-f.gate
	return f.result, nil
}

func (f *blockingFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}
