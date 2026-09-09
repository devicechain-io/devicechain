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
// and the bounds on refresh rate and concurrency — are subtle, security-relevant,
// and identical for every dimension; the per-dimension part is only which two fields
// of the tenantGovernance query to read. Earlier slices carried a copy per service
// (event-sources, event-processing, outbound-connectors) and the copies drifted:
// two of them floored a non-positive override to the platform default and one did
// not, which would have handed core.TenantRateLimiter a zero ceiling that admits
// nothing. Consolidating removes that whole class of drift.
package governance

import (
	"context"
	"math"
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
	// defaultNegativeTTL is how long a FAILED refresh holds a tenant off from being
	// retried. It bounds the cost of a tenant that does not resolve at all — an
	// unknown token is ErrRecordNotFound at user-management, which is an error, so
	// nothing is ever cached for it and every message carrying it would otherwise
	// trigger a fresh lookup. Deliberately much shorter than defaultCacheTTL: a
	// failure is usually transient, and holding a real tenant at the platform
	// default for a whole TTL after one blip would make an outage last longer than
	// it lasted.
	defaultNegativeTTL = 10 * time.Second
	// maxConcurrentRefreshes bounds how many refreshes run at once, which is a bound
	// on the LOAD one resolver can put on user-management — not on its request rate.
	// The rate bound is maxRefreshesPerSecond; see the tenantResolver comment for why
	// the two are not the same guarantee.
	maxConcurrentRefreshes = 8
	// maxRefreshesPerSecond and maxRefreshBurst bound how fast one resolver may START
	// refreshes, whatever the cardinality of the tenants asking. On some hot paths the
	// tenant is only grammar-validated, not existence-validated, so a flood of distinct
	// (possibly nonexistent) tenants would otherwise amplify sheddable work into one
	// lookup + goroutine per novel value, at whatever rate the authority can answer.
	// Over-rate misses serve the default and retry later, exactly like over-cap ones.
	//
	// The numbers are sized off steady state rather than off the flood: a resolver
	// refreshes each known tenant once per defaultCacheTTL, so 60 * maxRefreshesPerSecond
	// is roughly how many tenants one resolver keeps warm without ever touching the
	// bound, and the burst is what lets a cold cache fill in one go after a restart.
	maxRefreshesPerSecond = 50
	maxRefreshBurst       = 100
	// maxNegativeEntries bounds the cached record of tenants that failed to resolve.
	// Those keys come from the wire, so unlike the resolved half of the cache their
	// count is not bounded by the tenants that exist; past this many the hold-off is
	// simply not recorded (see recordFailureLocked).
	maxNegativeEntries = 1024
)

// defaultLimits is the last-resort ceiling this package falls back to when an
// enforcing service supplies a non-positive platform default — a config key left
// unset arriving as a zero value, say. It is not the platform's answer: that is the
// service's own configured number, and every service has one.
//
// 🔴 The polarity is the point (ADR-023). A zero rate or burst is not "unlimited",
// it is a bucket that admits NOTHING — a total ingest outage for every tenant with
// no override, and for every tenant during the cold-cache window. So a non-positive
// default is floored to a real, metered ceiling here rather than passed through.
// Generous enough not to shed a normally busy tenant, low enough that it is still a
// ceiling; a service wanting a different number configures one.
var defaultLimits = Limits{MessagesPerSecond: 100, Burst: 200}

// floorLimits replaces a non-positive (or non-finite) rate or burst with the
// corresponding defaultLimits field, per field.
//
// The fold ONTO a platform default was consolidated into this package when the
// per-service copies drifted; the floor UNDER that default was not, and
// NewHeldCommandCeilingResolver was left as the only constructor applying one. The
// argument it gives holds identically here: a default of zero reads as an outage for
// every tenant on the instance the moment user-management is unreachable, which is
// the exact inversion of fail-open.
func floorLimits(l Limits) Limits {
	if !(l.MessagesPerSecond > 0) || math.IsInf(l.MessagesPerSecond, 0) {
		// The negated comparison also rejects NaN, which no comparison would.
		l.MessagesPerSecond = defaultLimits.MessagesPerSecond
	}
	if l.Burst <= 0 {
		l.Burst = defaultLimits.Burst
	}
	return l
}

// TenantLimitResolver serves per-tenant limits for one dimension to the rate limiter
// from the shared tenantResolver cache (out-of-band refresh, inflight dedupe,
// rate- and concurrency-bounded, fail-open to the platform default — the default is itself a
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
//
// A non-positive rate or burst in def is floored to a real, metered ceiling rather
// than honoured — see floorLimits for why a zero default is an outage rather than the
// absence of one. NewServiceFetcher floors the same way, so the value served on a cold
// miss and the value a null override resolves to cannot disagree about the platform
// ceiling.
func NewTenantLimitResolver(fetch Fetcher, def Limits, dimension string) *TenantLimitResolver {
	def = floorLimits(def)
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
