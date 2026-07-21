// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// tenantResolver is the per-tenant, out-of-band-refreshed, fail-open TTL cache shared
// by every setting an enforcing service resolves on its hot path: the ADR-023 rate
// ceilings (TenantLimitResolver, V = Limits) and the ADR-063 shed priority
// (ShedPriorityResolver, V = int). It is generic in the VALUE only — the mechanics
// (the TTL, the inflight dedupe, the refresh concurrency cap, fail-open to a default)
// are identical for every setting, and are the subtle, security-relevant part the
// package comment warns must not be copied per consumer. The per-setting part is only
// what to fetch and what the default is, both injected.
//
// Fail-open means "serve the default (or last-known)", and the default is itself a
// real value (a metered ceiling / a bronze-band priority), never "unbounded" or
// "never shed" — so a slow or unreachable authority degrades safely.
type tenantResolver[V any] struct {
	fetch func(ctx context.Context, tenant string) (V, error)
	def   V
	ttl   time.Duration
	now   func() time.Time
	// label names this resolver's setting in logs, so a service resolving more than
	// one reports which failed to refresh.
	label string

	mu       sync.Mutex
	cache    map[string]cacheEntry[V]
	inflight map[string]struct{}
	// sem bounds concurrent refreshes (see maxConcurrentRefreshes).
	sem chan struct{}
}

type cacheEntry[V any] struct {
	val       V
	fetchedAt time.Time
}

// newTenantResolver builds a resolver over fetch, defaulting an uncached or
// unresolvable tenant to def. label names the setting for logs.
func newTenantResolver[V any](fetch func(context.Context, string) (V, error), def V, label string) *tenantResolver[V] {
	return &tenantResolver[V]{
		fetch:    fetch,
		def:      def,
		ttl:      defaultCacheTTL,
		now:      time.Now,
		label:    label,
		cache:    make(map[string]cacheEntry[V]),
		inflight: make(map[string]struct{}),
		sem:      make(chan struct{}, maxConcurrentRefreshes),
	}
}

// resolve returns the tenant's cached value without blocking. A fresh entry is served
// directly; a missing or stale one triggers an out-of-band refresh and serves the
// last-known value (or the default if none). It is the hot-path function.
func (r *tenantResolver[V]) resolve(tenant string) V {
	v, _ := r.resolveOK(tenant)
	return v
}

// resolveOK is resolve plus whether a REAL fetched value backed it — a cache hit (even
// a stale one being refreshed) — versus the default served on a miss. A caller for
// which the default is itself a live answer (a rate ceiling: the platform default is a
// real limit) ignores the bool via resolve. A caller that must not ACT on a tenant it
// has not yet learned about — shedding: you do not preferentially shed a tenant whose
// priority is still the fail-safe default only because it has never been fetched, since
// that fail-safe is a bronze band and could shed a gold tenant during a cold-cache
// window — uses the bool to hold off until the value is known.
func (r *tenantResolver[V]) resolveOK(tenant string) (V, bool) {
	r.mu.Lock()
	e, ok := r.cache[tenant]
	fresh := ok && r.now().Sub(e.fetchedAt) < r.ttl
	if !fresh {
		r.triggerRefreshLocked(tenant)
	}
	r.mu.Unlock()

	if ok {
		return e.val, true
	}
	return r.def, false
}

// triggerRefreshLocked starts at most one background refresh per tenant (deduped by
// the inflight set) and only while a global concurrency slot is free — so a flood of
// distinct tenants cannot spawn unbounded lookups. When the cap is full the refresh
// is skipped (the caller serves the default and a later call retries). The caller
// must hold r.mu; the non-blocking send never blocks under the lock.
func (r *tenantResolver[V]) triggerRefreshLocked(tenant string) {
	if _, running := r.inflight[tenant]; running {
		return
	}
	select {
	case r.sem <- struct{}{}:
	default:
		return // at the concurrency cap; serve default and retry on a later call
	}
	r.inflight[tenant] = struct{}{}
	go r.refresh(tenant)
}

// refresh fetches a tenant's value and updates the cache. On error it leaves the cache
// untouched (fail open to last-known or default) rather than caching a sentinel that
// would drop the tenant to an unsafe reading.
func (r *tenantResolver[V]) refresh(tenant string) {
	defer func() {
		r.mu.Lock()
		delete(r.inflight, tenant)
		r.mu.Unlock()
		<-r.sem
	}()

	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()

	val, err := r.fetch(ctx, tenant)
	if err != nil {
		log.Warn().Err(err).Str("tenant", tenant).Str("setting", r.label).
			Msg("Failed to refresh per-tenant setting; keeping last-known (or platform default)")
		return
	}
	r.mu.Lock()
	r.cache[tenant] = cacheEntry[V]{val: val, fetchedAt: r.now()}
	r.mu.Unlock()
}
