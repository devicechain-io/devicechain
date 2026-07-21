// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package governance resolves per-tenant rate ceilings (ADR-023) for
// core.TenantRateLimiter. It fetches a tenant's overrides from user-management and
// caches them in memory, refreshing out of band so a hot enforcement path never
// blocks on a network call, and fails open to the platform default so a slow or
// unreachable authority degrades to metered-at-default, never to unmetered.
//
// One resolver serves ONE governance dimension (ingest, outbound, ai-inference —
// see Dimension). The dimensions are independent: a tenant may ingest heavily yet
// fan out few outbound calls, or the reverse, so each is overridden and enforced
// separately. A service that governs two dimensions builds two resolvers.
//
// This lives in core because the mechanics — the TTL cache, the inflight dedupe,
// and the refresh concurrency cap — are subtle, security-relevant, and identical
// for every dimension; the per-dimension part is only which two fields of the
// tenantGovernance query to read. Earlier slices carried a copy per service
// (event-sources, event-processing, outbound-connectors) and the copies drifted:
// two of them floored a non-positive override to the platform default and one did
// not, which would have handed core.TenantRateLimiter a zero ceiling that admits
// nothing. Consolidating removes that whole class of drift.
package governance

import (
	"context"
	"time"
)

// Limits is a tenant's effective ceiling for one governance dimension.
type Limits struct {
	MessagesPerSecond float64
	Burst             int
}

// Fetcher retrieves a tenant's effective limits from the authority
// (user-management), already resolved against the platform default so a tenant
// with no override yields the default, never a zero/unlimited value.
type Fetcher interface {
	Fetch(ctx context.Context, tenant string) (Limits, error)
}

const (
	// defaultCacheTTL bounds how stale a cached per-tenant limit can be: an
	// override change takes effect within this window. Short enough to be
	// responsive, long enough that a steady-state hot path resolves from cache.
	defaultCacheTTL = 60 * time.Second
	// fetchTimeout caps a single background refresh so a stuck authority cannot
	// pin a refresh goroutine indefinitely.
	fetchTimeout = 5 * time.Second
	// maxConcurrentRefreshes bounds how many refreshes run at once. On some hot
	// paths the tenant is only grammar-validated, not existence-validated, so a
	// flood of distinct (possibly nonexistent) tenants would otherwise amplify
	// sheddable work into one lookup + goroutine per novel value. Capping
	// concurrency keeps user-management load bounded regardless of that
	// cardinality; over-cap misses just serve the default and retry later.
	maxConcurrentRefreshes = 8
)

// TenantLimitResolver serves per-tenant limits for one dimension to the rate limiter
// from the shared tenantResolver cache (out-of-band refresh, inflight dedupe,
// concurrency-capped, fail-open to the platform default — the default is itself a
// limit, so "fail open" never means "unlimited"). This type is the Limits-shaped face
// of that cache: it adapts the dimension Fetcher to the generic fetch signature and
// unpacks the cached Limits into the (rate, burst) pair the limiter wants.
type TenantLimitResolver struct {
	*tenantResolver[Limits]
}

// NewTenantLimitResolver builds a resolver over fetch, defaulting an uncached or
// unresolvable tenant to def (the platform default). dimension names the governed
// dimension for logs. Most callers want NewServiceLimitResolver, which wires the
// user-management fetcher too; this constructor exists for tests and any
// non-GraphQL authority.
func NewTenantLimitResolver(fetch Fetcher, def Limits, dimension string) *TenantLimitResolver {
	return &TenantLimitResolver{
		tenantResolver: newTenantResolver(
			func(ctx context.Context, tenant string) (Limits, error) { return fetch.Fetch(ctx, tenant) },
			def, dimension),
	}
}

// Resolve returns the tenant's effective (ratePerSecond, burst) without blocking —
// the hot-path function handed to core.TenantRateLimiter.
func (r *TenantLimitResolver) Resolve(tenant string) (float64, int) {
	l := r.resolve(tenant)
	return l.MessagesPerSecond, l.Burst
}
