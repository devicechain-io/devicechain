// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package governance resolves per-tenant ingest limits (ADR-023) for the rate
// limiter: it fetches a tenant's overrides from user-management and caches them
// in memory, refreshing out of band so the hot admission path never blocks on a
// network call, and fails open to the platform default so a slow or unreachable
// authority degrades to metered-at-default, never to unmetered.
package governance

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Limits is a tenant's effective ingest ceiling.
type Limits struct {
	MessagesPerSecond float64
	Burst             int
}

// Fetcher retrieves a tenant's effective ingest limits from the authority
// (user-management), already resolved against the platform default so a tenant
// with no override yields the default, never a zero/unlimited value.
type Fetcher interface {
	Fetch(ctx context.Context, tenant string) (Limits, error)
}

const (
	// defaultCacheTTL bounds how stale a cached per-tenant limit can be: an
	// override change takes effect within this window. Short enough to be
	// responsive, long enough that steady-state ingest resolves from cache.
	defaultCacheTTL = 60 * time.Second
	// fetchTimeout caps a single background refresh so a stuck authority cannot
	// pin a refresh goroutine indefinitely.
	fetchTimeout = 5 * time.Second
	// maxConcurrentRefreshes bounds how many refreshes run at once. The tenant
	// reaching ingest is only grammar-validated, not existence-validated, so a
	// flood of distinct (possibly nonexistent) tenants would otherwise amplify
	// sheddable ingest into one lookup + goroutine per novel value. Capping
	// concurrency keeps user-management load bounded regardless of that
	// cardinality; over-cap misses just serve the default and retry later.
	maxConcurrentRefreshes = 8
)

// TenantLimitResolver serves per-tenant ingest limits to the rate limiter from an
// in-memory cache, refreshing entries out of band. It is fail-open to the
// platform default: a tenant with no cached entry (or whose refresh is failing)
// is metered at the platform default rather than unmetered — the default is
// itself a limit, so "fail open" never means "unlimited".
type TenantLimitResolver struct {
	fetch Fetcher
	def   Limits
	ttl   time.Duration
	now   func() time.Time

	mu       sync.Mutex
	cache    map[string]entry
	inflight map[string]struct{}
	// sem bounds concurrent refreshes (see maxConcurrentRefreshes).
	sem chan struct{}
}

type entry struct {
	limits    Limits
	fetchedAt time.Time
}

// NewTenantLimitResolver builds a resolver over fetch, defaulting an uncached or
// unresolvable tenant to def (the platform default).
func NewTenantLimitResolver(fetch Fetcher, def Limits) *TenantLimitResolver {
	return &TenantLimitResolver{
		fetch:    fetch,
		def:      def,
		ttl:      defaultCacheTTL,
		now:      time.Now,
		cache:    make(map[string]entry),
		inflight: make(map[string]struct{}),
		sem:      make(chan struct{}, maxConcurrentRefreshes),
	}
}

// Resolve returns the tenant's effective (ratePerSecond, burst) without blocking.
// A fresh cache entry is served directly; a missing or stale entry triggers an
// out-of-band refresh and serves the last-known value (or the platform default if
// none). It is the hot-path function handed to core.TenantRateLimiter.
func (r *TenantLimitResolver) Resolve(tenant string) (float64, int) {
	r.mu.Lock()
	e, ok := r.cache[tenant]
	fresh := ok && r.now().Sub(e.fetchedAt) < r.ttl
	if !fresh {
		r.triggerRefreshLocked(tenant)
	}
	r.mu.Unlock()

	if ok {
		return e.limits.MessagesPerSecond, e.limits.Burst
	}
	return r.def.MessagesPerSecond, r.def.Burst
}

// triggerRefreshLocked starts at most one background refresh per tenant (deduped
// by the inflight set) and only while a global concurrency slot is free — so a
// flood of distinct tenants cannot spawn unbounded lookups. When the cap is full
// the refresh is skipped (the caller serves the default and a later event
// retries). The caller must hold r.mu; the non-blocking send never blocks under
// the lock.
func (r *TenantLimitResolver) triggerRefreshLocked(tenant string) {
	if _, running := r.inflight[tenant]; running {
		return
	}
	select {
	case r.sem <- struct{}{}:
	default:
		return // at the concurrency cap; serve default and retry on a later event
	}
	r.inflight[tenant] = struct{}{}
	go r.refresh(tenant)
}

// refresh fetches a tenant's limits and updates the cache. On error it leaves the
// cache untouched (fail open to last-known or default) rather than caching a
// sentinel that would drop the tenant to unmetered.
func (r *TenantLimitResolver) refresh(tenant string) {
	defer func() {
		r.mu.Lock()
		delete(r.inflight, tenant)
		r.mu.Unlock()
		<-r.sem
	}()

	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()

	limits, err := r.fetch.Fetch(ctx, tenant)
	if err != nil {
		log.Warn().Err(err).Str("tenant", tenant).
			Msg("Failed to refresh per-tenant ingest limits; keeping last-known (or platform default)")
		return
	}
	r.mu.Lock()
	r.cache[tenant] = entry{limits: limits, fetchedAt: r.now()}
	r.mu.Unlock()
}
