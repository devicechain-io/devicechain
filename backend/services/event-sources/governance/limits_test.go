// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"
	"errors"
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
	r := NewTenantLimitResolver(f, platformDefault)

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
	r := NewTenantLimitResolver(f, platformDefault)

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
	r := NewTenantLimitResolver(f, platformDefault)

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
	r := NewTenantLimitResolver(f, platformDefault)
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
	r := NewTenantLimitResolver(f, platformDefault)

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
