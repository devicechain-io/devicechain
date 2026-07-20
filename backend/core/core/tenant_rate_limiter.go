// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
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
//
// A tenant's ceiling is supplied by a resolver rather than baked in, so a
// per-tenant override (or its removal) is picked up without recreating the
// limiter: an existing bucket is retuned in place when its resolved ceiling
// changes. The resolver is called on the hot path, so it must be fast and
// non-blocking — resolve from an in-memory cache and refresh out of band.
type TenantRateLimiter struct {
	resolve func(tenant string) (ratePerSecond float64, burst int)

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

// NewTenantRateLimiter creates a limiter whose per-tenant ceiling is supplied by
// resolve, called with the tenant on each admission to obtain its sustained rate
// (events/sec) and burst (the largest instantaneous batch before the sustained
// rate applies). resolve is expected to return positive values — a non-positive
// rate or burst yields a bucket that admits nothing, so the fail-safe defaulting
// to a platform rate belongs in resolve (or the layer behind it), never here.
// resolve runs on the hot path under no lock of its own here, so it must be fast
// and non-blocking (serve from cache; refresh out of band).
func NewTenantRateLimiter(resolve func(tenant string) (ratePerSecond float64, burst int)) *TenantRateLimiter {
	return &TenantRateLimiter{
		resolve:       resolve,
		idleTTL:       defaultRateLimiterIdleTTL,
		sweepInterval: defaultRateLimiterSweepInterval,
		now:           time.Now,
		buckets:       make(map[string]*tenantBucket),
	}
}

// Allow reports whether an event for the given tenant may proceed now, consuming
// one token from that tenant's bucket when it does. A denied call consumes
// nothing, so a shed event does not deepen the tenant's deficit. The tenant's
// ceiling is (re)resolved each call: a bucket whose resolved ceiling has changed
// since creation is retuned in place, preserving its current token level, so an
// override applied or cleared upstream takes effect on the next event.
//
// It is AllowAt at the current time — correct for any caller admitting a message
// as it arrives. A caller draining a durable backlog must use AllowAt instead.
func (l *TenantRateLimiter) Allow(tenant string) bool {
	return l.AllowAt(tenant, time.Time{})
}

// AllowAt reports whether a message the tenant SENT at time `when` may proceed,
// consuming one token from that tenant's bucket when it does. A zero `when` means
// now, making it identical to Allow.
//
// # Why admission is metered at send time rather than arrival time
//
// A token bucket is a function of time, so which clock it is fed decides what it
// actually measures. Feeding it arrival time measures the rate WE PROCESS at;
// feeding it send time measures the rate the TENANT SENT at. Those agree exactly
// as long as processing keeps up, which is why the distinction never surfaced
// while every ingest path admitted messages as they arrived.
//
// They stop agreeing the moment a consumer falls behind a durable backlog. After
// an outage the ingest capture stream (ADR-030) holds everything the broker
// PUBACKed while the consumer was down, and that backlog drains at fetch speed —
// far above any tenant's ceiling. Metered on arrival, a tenant who sent a
// perfectly compliant trickle for ten minutes has nearly the entire backlog shed
// on the way back up, because it all "arrived" in the same second. The platform
// would be discarding messages it had already told the device were safe, which
// makes the durability guarantee the capture stream exists to provide false.
//
// Metered at send time the same backlog is admitted in full, because it is
// replayed against the timeline it was actually produced on. Crucially this
// gives up no flood protection: a tenant who genuinely sent above their ceiling
// carries that burst in their timestamps too, and is shed by the same amount they
// would have been had the outage never happened. Measured over a 1000-message
// backlog at a 100/s ceiling: at-ceiling traffic admits 1000/1000, while 10x
// traffic admits 109 — the same as live.
//
// # A bucket must be fed ONE clock — this is a caller obligation
//
// Do not route both wall-clock arrivals and old send times to the same tenant's
// bucket. A token bucket accrues from the last timestamp it saw, and the
// underlying limiter rewinds that mark when handed an older time. So every jump
// forward to now re-accrues from a stale mark and refills to `burst`, which the
// following rewind then spends — minting roughly `burst` admissions per
// interleave.
//
// That is not a bounded rounding error, and it was measured rather than reasoned
// about: interleaving live traffic with a draining backlog on one bucket admitted
// 900 of a 1000-message flood that the ceiling permitted 109 of, and the minting
// scaled with lag until, at one second of consumer lag, a 100/s ceiling admitted
// ~2000. Any caller mixing timelines must give each its own limiter instance —
// see processor.NewRateGate in event-sources, which routes on exactly this basis.
//
// Within one stream, send times are monotonic per tenant, so a caller that meters
// only a drain is safe by construction.
//
// A `when` in the future is clamped to now, so a broker with a skewed clock
// cannot mint tokens by claiming its messages were sent later than they were.
func (l *TenantRateLimiter) AllowAt(tenant string, when time.Time) bool {
	rps, burst := l.resolve(tenant)

	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	// The bucket is tuned at `now` regardless of `when`, so retuning an override
	// never rewinds the bucket's clock — only the admission itself is measured on
	// the send timeline.
	lim := l.tuneBucketLocked(tenant, rate.Limit(rps), burst, now)
	if when.IsZero() || when.After(now) {
		when = now
	}
	return lim.AllowN(when, 1)
}

// Wait blocks until a token is available for the tenant, then consumes it, or
// returns ctx's error (consuming nothing) if one does not free before ctx is done.
// It generalizes Allow for a caller that can afford to WAIT a bounded time for
// admission rather than shed immediately: the outbound-connectors egress limiter
// waits up to a small budget so a brief burst just over a tenant's rate is smoothed
// into pacing rather than shed, while a tenant sustained over its rate exceeds the
// budget and is shed (ctx deadline exceeded). ctx SHOULD therefore carry a deadline
// bounding the wait — without one, Wait can block until a token frees. The tenant's
// ceiling is (re)resolved and the bucket retuned in place exactly like Allow. A
// denied (deadline-exceeded) call consumes no token, so a shed send does not deepen
// the tenant's deficit.
//
// The bucket's limiter is looked up under the lock and the actual wait happens
// AFTER releasing it, so one tenant's wait never blocks another tenant's admission
// on the shared mutex. If a concurrent sweep evicts the bucket mid-wait the wait
// still completes correctly against the extracted limiter (a detached limiter is
// still valid); a stale retune is benign.
func (l *TenantRateLimiter) Wait(ctx context.Context, tenant string) error {
	rps, burst := l.resolve(tenant)

	l.mu.Lock()
	lim := l.tuneBucketLocked(tenant, rate.Limit(rps), burst, l.now())
	l.mu.Unlock()

	return lim.Wait(ctx)
}

// tuneBucketLocked returns the tenant's bucket limiter, creating it (at limit/burst)
// or retuning an existing one in place when its resolved ceiling has changed, and
// stamps lastSeen. It also runs the amortized idle sweep. The caller must hold l.mu.
// Retuning with the injected clock keeps a frozen-clock test and the admission that
// follows in agreement on "now"; the bucket keeps its accumulated tokens across a
// retune.
func (l *TenantRateLimiter) tuneBucketLocked(tenant string, limit rate.Limit, burst int, now time.Time) *rate.Limiter {
	l.sweepLocked(now)

	b := l.buckets[tenant]
	if b == nil {
		b = &tenantBucket{limiter: rate.NewLimiter(limit, burst)}
		l.buckets[tenant] = b
	} else if b.limiter.Limit() != limit || b.limiter.Burst() != burst {
		b.limiter.SetLimitAt(now, limit)
		b.limiter.SetBurstAt(now, burst)
	}
	b.lastSeen = now
	return b.limiter
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
