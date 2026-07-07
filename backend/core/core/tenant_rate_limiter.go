// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	// defaultRateLimiterIdleTTL is how long a tenant's bucket is kept after its
	// last request before eviction. Long enough that a tenant pausing between
	// bursts keeps its accumulated tokens across the gap; short enough that churn
	// through many short-lived tenants cannot leak buckets without bound.
	defaultRateLimiterIdleTTL = 10 * time.Minute
	// defaultRateLimiterSweepInterval bounds how often idle-eviction scans the
	// tenant map, so a hot Allow path does not walk every entry on every call.
	defaultRateLimiterSweepInterval = time.Minute
)

// TenantRateLimiter enforces an independent token-bucket rate limit per tenant.
// Each tenant gets its own bucket, created lazily on first use, so one tenant's
// flood consumes only its own allowance and cannot starve another (the
// noisy-neighbor guarantee behind per-tenant ingest governance). Buckets that go
// idle past a TTL are evicted so the map stays bounded by the count of recently
// active tenants rather than every tenant ever seen. It is safe for concurrent
// use by many callers.
type TenantRateLimiter struct {
	limit rate.Limit
	burst int

	idleTTL       time.Duration
	sweepInterval time.Duration
	now           func() time.Time

	mu        sync.Mutex
	buckets   map[string]*tenantBucket
	lastSweep time.Time
}

// tenantBucket is one tenant's limiter plus the last time it was touched, used to
// evict buckets that have gone idle.
type tenantBucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// NewTenantRateLimiter creates a limiter that admits ratePerSecond sustained
// events per tenant with a burst ceiling (the largest instantaneous batch a
// tenant may send before the sustained rate applies). Callers are expected to
// pass positive values — a non-positive rate or burst yields a bucket that
// admits nothing, so the fail-safe defaulting to a platform rate belongs in the
// configuration layer, never here.
//
// The ceiling is fixed at construction and applied uniformly to every tenant. A
// future per-tenant-override slice can extend this with a per-tenant limit
// resolver at bucket creation and rate.Limiter's SetLimit/SetBurst to retune
// already-created buckets, without changing Allow's signature.
func NewTenantRateLimiter(ratePerSecond float64, burst int) *TenantRateLimiter {
	return &TenantRateLimiter{
		limit:         rate.Limit(ratePerSecond),
		burst:         burst,
		idleTTL:       defaultRateLimiterIdleTTL,
		sweepInterval: defaultRateLimiterSweepInterval,
		now:           time.Now,
		buckets:       make(map[string]*tenantBucket),
	}
}

// Allow reports whether an event for the given tenant may proceed now, consuming
// one token from that tenant's bucket when it does. A denied call consumes
// nothing, so a shed event does not deepen the tenant's deficit.
func (l *TenantRateLimiter) Allow(tenant string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweepLocked(now)

	b := l.buckets[tenant]
	if b == nil {
		b = &tenantBucket{limiter: rate.NewLimiter(l.limit, l.burst)}
		l.buckets[tenant] = b
	}
	b.lastSeen = now
	return b.limiter.AllowN(now, 1)
}

// sweepLocked evicts buckets untouched for longer than idleTTL. It runs at most
// once per sweepInterval (tracked by lastSweep) so the linear scan is amortized
// away from the per-call hot path. The caller must hold l.mu.
func (l *TenantRateLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < l.sweepInterval {
		return
	}
	l.lastSweep = now
	for tenant, b := range l.buckets {
		if now.Sub(b.lastSeen) >= l.idleTTL {
			delete(l.buckets, tenant)
		}
	}
}
