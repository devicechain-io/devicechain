// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"fmt"
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

// tenantBucket is one tenant's limiter, the last wall-clock time it was touched
// (used to evict buckets that have gone idle), and its mark: no admission on it is
// ever charged at a time behind the mark (admitTimeLocked).
type tenantBucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time // wall; drives the idle sweep only
	mark     time.Time // the latest admission time this bucket was charged or retuned at
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

// AllowN reports whether a batch of n events for the tenant may proceed now,
// consuming n tokens from that tenant's bucket when it does. It is the batch-honest
// form of Allow for a caller that admits a whole batch atomically — the LwM2M sample
// budget charges one Notify's decoded samples in a single call (ADR-023 / ADR-075 L2c)
// rather than looping Allow, which could partially admit a batch.
//
// A non-positive n admits and consumes nothing (an empty batch is not a rate event).
// Because the underlying token bucket can never satisfy a request for more than its
// burst, an n greater than the tenant's burst is ALWAYS denied (and consumes nothing) —
// so a caller that charges a variable batch size must size the tenant's burst at or
// above the largest batch it will ever pass, or a legitimately large batch is shed
// every time. The LwM2M sample limiter floors its burst at the per-Notify sample cap
// for exactly this reason.
func (l *TenantRateLimiter) AllowN(tenant string, n int) bool {
	return l.AllowNAt(tenant, time.Time{}, n)
}

// AllowNAt reports whether a batch of n events the tenant SENT at time `when` may
// proceed, consuming n tokens when it does. A zero `when` means now (making it
// identical to AllowN); a non-positive n admits and consumes nothing. It is AllowAt
// generalized to a batch — see AllowAt for why admission is metered at send time and
// how the bucket keeps one clock.
func (l *TenantRateLimiter) AllowNAt(tenant string, when time.Time, n int) bool {
	if n <= 0 {
		return true
	}
	rps, burst := l.resolve(tenant)

	l.mu.Lock()
	defer l.mu.Unlock()

	b, at := l.tuneBucketLocked(tenant, rate.Limit(rps), burst, when, l.now())
	ok := b.limiter.AllowN(at, n)
	b.mark = at
	return ok
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
// # A bucket's clock never goes backwards — by construction
//
// The underlying token bucket accrues from the last timestamp it saw and rewinds
// that timestamp when handed an older one, so every later jump forward re-accrues
// from the stale point and refills to `burst`, which the following rewind then
// spends. Fed unordered times directly it mints roughly `burst` admissions per
// rewind (measured: interleaving live traffic with a draining backlog admitted 900
// of a 1000-message flood the ceiling permitted 109 of).
//
// So each bucket carries a mark — the latest time it was charged or retuned at —
// and every admission is charged at `at = min(max(when, mark), now)` (see
// admitTimeLocked). The times any one limiter is fed are therefore non-decreasing,
// and over admission times at_1 ≤ … ≤ at_k a bucket admits at most
// `burst + rate·(at_k − at_1)`. Mixing clocks on one bucket, or feeding it an
// older time than it has already seen, now costs OVER-SHEDDING (the older time is
// charged at the mark, where the tokens it would have used are already spent) and
// never mints. event-sources still gives live traffic and a draining backlog a
// limiter each (processor.NewRateGate), but for correct admission — a live post
// must not pace against a drain's timeline — not to avoid minting.
//
// Times fed here are not monotonic on their own and must not be assumed to be. A
// broker's append time is stamped by the stream leader's wall clock, so it steps
// backwards on a leader change between servers whose clocks disagree, or on an NTP
// step; a redelivery carries an older time than messages already admitted.
//
// The one thing the mark does not bound is forgery. A caller that lets a tenant
// choose `when` lets it claim any spread of times up to now and so mint
// `burst + rate·span`; `when` must be a time the PLATFORM stamped (a broker append
// time, a processing time), never one the tenant supplies.
//
// A `when` in the future is clamped to now, so a broker with a skewed clock
// cannot mint tokens by claiming its messages were sent later than they were.
func (l *TenantRateLimiter) AllowAt(tenant string, when time.Time) bool {
	return l.AllowNAt(tenant, when, 1)
}

// ErrWaitBudget is WaitAt's (and Wait's) answer when the token an event needs would
// not be free until after ctx's deadline: the event is shed, and no token is taken.
// It wraps context.DeadlineExceeded, so a caller that tests for that keeps working.
var ErrWaitBudget = fmt.Errorf("tenant rate limit: token not available within the wait budget: %w",
	context.DeadlineExceeded)

// Wait blocks until a token is available for the tenant, then consumes it, or
// returns an error (consuming nothing) if one does not free before ctx is done.
// It generalizes Allow for a caller that can afford to WAIT a bounded time for
// admission rather than shed immediately: the outbound-connectors egress limiter
// waits up to a small budget so a brief burst just over a tenant's rate is smoothed
// into pacing rather than shed, while a tenant sustained over its rate exceeds the
// budget and is shed (ErrWaitBudget). ctx SHOULD therefore carry a deadline
// bounding the wait — without one, Wait can block until a token frees. It is WaitAt
// at now; see WaitAt.
func (l *TenantRateLimiter) Wait(ctx context.Context, tenant string) error {
	return l.WaitAt(ctx, tenant, time.Time{})
}

// WaitAt admits one event the tenant produced at `when`, waiting up to ctx's deadline for a
// token. The decision is the one a LIVE caller would have made at `when`: the event is shed iff
// the token's delay on the `when` timeline exceeds the wall-clock budget left before ctx's
// deadline; an admitted event then sleeps only until at+delay, which for a backlog (at in the
// past) is usually already over. A zero when is now, making it Wait.
//
// A ctx that is already done is refused before any token is taken. A shed (ErrWaitBudget)
// or an interrupted wait (ctx's error) consumes no token: the reservation is returned on the
// bucket's own timeline, never at the wall clock, which would put a send-timed bucket back
// on the arrival clock. The tenant's ceiling is (re)resolved and the bucket retuned exactly as
// AllowAt does.
//
// The admission decision is taken under the limiter's lock and the actual sleep happens AFTER
// releasing it, so one tenant's wait never blocks another tenant's admission. If a concurrent
// sweep evicts the bucket mid-wait the wait still completes against the detached bucket.
func (l *TenantRateLimiter) WaitAt(ctx context.Context, tenant string, when time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rps, burst := l.resolve(tenant)

	l.mu.Lock()
	now := l.now()
	b, at := l.tuneBucketLocked(tenant, rate.Limit(rps), burst, when, now)
	r := b.limiter.ReserveN(at, 1)
	if !r.OK() {
		l.mu.Unlock()
		return fmt.Errorf("tenant rate limit: a burst of %d admits nothing", burst)
	}
	delay := r.DelayFrom(at)
	if deadline, ok := ctx.Deadline(); ok && delay > deadline.Sub(now) {
		r.CancelAt(at)
		l.mu.Unlock()
		return ErrWaitBudget
	}
	b.mark = at
	l.mu.Unlock()

	wait := at.Add(delay).Sub(now)
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		// Return the token at the bucket's current mark, NEVER r.Cancel(): that is
		// CancelAt(time.Now()), which moves the limiter's clock to the wall clock and so
		// puts a send-timed bucket back on the arrival timeline. The mark is at or after
		// `at` (later admissions only move it forward), so cancelling there never rewinds.
		l.mu.Lock()
		r.CancelAt(b.mark)
		l.mu.Unlock()
		return ctx.Err()
	}
}

// admitTimeLocked returns the time an admission is charged at:
// at = min(max(when, b.mark), now), with a zero when read as now. It is the only place
// that policy lives. It does NOT move the mark; the caller sets b.mark = at after
// charging. The caller must hold l.mu.
//
// max(…, mark) keeps the bucket's clock from going backwards (see AllowAt); min(…, now)
// clamps a future time so a skewed clock cannot claim tokens that have not accrued yet.
// The clamp to now is applied last, so a wall clock that has stepped back behind the mark
// charges at now: the bucket re-accrues at most (mark − now)·rate tokens, capped at one
// burst, once per step. A live caller (zero when) is therefore charged at now exactly as
// before the mark existed.
func admitTimeLocked(b *tenantBucket, when, now time.Time) time.Time {
	at := when
	if at.IsZero() {
		at = now
	}
	if at.Before(b.mark) {
		at = b.mark
	}
	if at.After(now) {
		at = now
	}
	return at
}

// tuneBucketLocked returns the tenant's bucket and the time this admission is charged at
// (admitTimeLocked), creating the bucket (at limit/burst) or retuning an existing one in
// place when its resolved ceiling has changed, and stamps lastSeen = now. It also runs the
// amortized idle sweep. The caller must hold l.mu, charge at the returned time, and then
// set b.mark to it.
//
// 🔴 A retune happens AT THE ADMISSION TIME, never at now. SetLimitAt/SetBurstAt advance the
// limiter to the time they are given; retuned at now while the bucket drains a backlog on
// send times, every retune jumps its clock forward and refills it to burst, which the next
// (older) admission then spends — measured at 12000 admissions against a budget of 6199 for
// a ceiling that flips once a second. A fresh limiter needs no call: it accrues from a zero
// last-event time and so starts full at whatever time it is first charged. The bucket keeps
// its accumulated tokens across a retune.
func (l *TenantRateLimiter) tuneBucketLocked(tenant string, limit rate.Limit, burst int, when, now time.Time) (*tenantBucket, time.Time) {
	l.sweepLocked(now)

	b := l.buckets[tenant]
	if b == nil {
		b = &tenantBucket{limiter: rate.NewLimiter(limit, burst)}
		l.buckets[tenant] = b
	}
	at := admitTimeLocked(b, when, now)
	if b.limiter.Limit() != limit || b.limiter.Burst() != burst {
		b.limiter.SetLimitAt(at, limit)
		b.limiter.SetBurstAt(at, burst)
		b.mark = at
	}
	b.lastSeen = now
	return b, at
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
