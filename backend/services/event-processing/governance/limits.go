// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package governance resolves per-tenant OUTBOUND limits (ADR-060 SD-3) for REACT's
// SOURCE-side egress cost-gate: before REACT publishes a connector-dispatch it charges
// the tenant's outbound budget, so a runaway rule sheds at the source rather than
// flooding the connector-dispatch stream and the downstream outbound-connectors
// service. It fetches a tenant's outbound overrides from user-management and caches
// them in memory, refreshing out of band so the hot dispatch path never blocks on a
// network call, and fails open to the platform default so a slow or unreachable
// authority degrades to metered-at-default, never to unmetered.
//
// This reads the SAME governance dimension (outboundMessagesPerSecond / outboundBurst)
// the outbound-connectors service's egress limiter reads — SD-3 charges both ends: the
// source (here, an immediate Allow-drop, so an over-quota rule never publishes) and the
// sink (a bounded egress Wait in outbound-connectors, defense-in-depth). It deliberately
// mirrors that resolver rather than sharing it: the two live in separate service modules
// and event-sources holds a THIRD copy for the independent ingest dimension. Promoting
// the resolver mechanics to core is a possible future consolidation (now three copies),
// deferred here to keep this slice bounded and avoid refactoring the live ingest and
// egress hot paths for a defense-in-depth source gate.
package governance

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Limits is a tenant's effective outbound ceiling.
type Limits struct {
	MessagesPerSecond float64
	Burst             int
}

// Fetcher retrieves a tenant's effective outbound limits from the authority
// (user-management), already resolved against the platform default so a tenant
// with no override yields the default, never a zero/unlimited value.
type Fetcher interface {
	Fetch(ctx context.Context, tenant string) (Limits, error)
}

const (
	// defaultCacheTTL bounds how stale a cached per-tenant limit can be: an override
	// change takes effect within this window. Short enough to be responsive, long
	// enough that steady-state dispatch resolves from cache.
	defaultCacheTTL = 60 * time.Second
	// fetchTimeout caps a single background refresh so a stuck authority cannot pin a
	// refresh goroutine indefinitely.
	fetchTimeout = 5 * time.Second
	// maxConcurrentRefreshes bounds how many refreshes run at once. In this service the
	// charged tenant is strongly validated before the gate is reached — REACT's consumer
	// requires subject-tenant == payload-tenant == the rule-id tenant prefix, and the gate
	// fires only after the rule resolves from the durable projection — so the tenant set is
	// bounded by real tenants with published rules, not arbitrary subject values. The cap is
	// retained anyway as cheap defense-in-depth (and to keep this resolver a faithful mirror
	// of the ingest/egress copies, whose upstreams are only grammar-validated): it keeps
	// user-management load bounded regardless of cardinality; over-cap misses just serve the
	// default and retry later.
	maxConcurrentRefreshes = 8
)

// TenantLimitResolver serves per-tenant outbound limits to the rate limiter from an
// in-memory cache, refreshing entries out of band. It is fail-open to the platform
// default: a tenant with no cached entry (or whose refresh is failing) is metered at
// the platform default rather than unmetered — the default is itself a limit, so
// "fail open" never means "unlimited".
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

// Resolve returns the tenant's effective (ratePerSecond, burst) without blocking. A
// fresh cache entry is served directly; a missing or stale entry triggers an
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

// triggerRefreshLocked starts at most one background refresh per tenant (deduped by
// the inflight set) and only while a global concurrency slot is free — so a flood of
// distinct tenants cannot spawn unbounded lookups. When the cap is full the refresh
// is skipped (the caller serves the default and a later dispatch retries). The
// caller must hold r.mu; the non-blocking send never blocks under the lock.
func (r *TenantLimitResolver) triggerRefreshLocked(tenant string) {
	if _, running := r.inflight[tenant]; running {
		return
	}
	select {
	case r.sem <- struct{}{}:
	default:
		return // at the concurrency cap; serve default and retry on a later dispatch
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
			Msg("Failed to refresh per-tenant outbound limits; keeping last-known (or platform default)")
		return
	}
	r.mu.Lock()
	r.cache[tenant] = entry{limits: limits, fetchedAt: r.now()}
	r.mu.Unlock()
}
