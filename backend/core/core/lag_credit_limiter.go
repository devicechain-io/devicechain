// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"fmt"
	"hash/fnv"
	"math"
	"sync"
	"time"
)

// LagCreditLimiter is the ingest ceiling bucket that tolerates a PLATFORM stall.
//
// It is a pure library: nothing in a service constructs it yet. event-sources adopts it behind the
// ingest-metering switch (default: the existing gate, unchanged); until that wiring lands this type
// is exercised only by its own tests.
//
// # The problem it solves
//
// A token bucket charged at pod time measures how fast WE read, not how fast the tenant SENT. After
// a reader stall the queued traffic of a tenant exactly at its ceiling is read in a burst into a
// bucket that holds `burst`, and the overflow is shed after the broker already acknowledged it. The
// existing gate answers by metering old traffic on the send timeline, which sheds compliant tenants
// for stalls it classes as "young" and drags the broker's clock into the live path.
//
// This bucket keeps the live path on POD time (monotonic, nothing a stamp can move) and instead
// raises its CAPACITY: at each admission the capacity is
//
//	cap = scale · (burst + rate · credit)
//
// where credit is a platform-measured bound on how far behind the readers are for this tenant.
//
// # Why that is sound
//
// Let s be any read time at which the bucket sits at its capacity, and let credit(s) bound the age of
// every message of this tenant that is read at or after s (see Credit). A tenant whose true send
// sequence conforms to (rate, burst) sent at most burst + rate·(t − s + credit(s)) messages that are
// read in [s, t], because they were all sent at or after s − credit(s). The bucket then holds
// cap(s) = burst + rate·credit(s) at s and accrues rate·(t − s), so none of those charges fails: a
// compliant tenant loses nothing to a stall of up to the credit cap. The argument is over the UNION
// of the tenant's sources, so capture, store and external MQTT interleave freely, and it never
// relies on stamp order inside the bucket.
//
// Conversely the bucket cannot be used to exceed the ceiling: capacity rises, but accrual stays
// rate·Δnow, so over any window it admits at most burst + rate·(Δnow + creditCap).
//
// # What carries the credit
//
// Per source k, the caller supplies (LagCreditSource) the corrected head-of-line lag of that reader.
// The bucket remembers, per source, the broker stamp of the last first-delivery message it was
// charged for. By stream order every FUTURE message of this tenant on source k was appended at or
// after max(lastStamp, now − lag_k), so
//
//	credit = min(creditCap, max over k of min(age(lastStamp_k), lag_k))
//
// A source whose reader is current contributes 0 even when the tenant last used it long ago. A source
// the bucket has NO stamp for contributes its full lag, not 0 (an unknown last stamp is an infinitely
// old one): a pod restarted in the middle of a stall, an idle bucket that was swept, and a tenant that
// switches source all have no stamp to measure from, and crediting them 0 would shed exactly the
// compliant traffic this exists to protect. That errs lenient only, bounded by the lag itself.
//
// # Exit-grant suppression
//
// When a source leaves its backlog phase, the bucket must not hand the tenant the whole phase's
// accrual as a fresh burst on top of the burst it already spent on the backlog route. The first
// admission after an exit therefore accrues only from exitAt − creditCap.
//
// # What it is not
//
// It does not decide routes, estimate lag or skew, or shed by contention; LagCreditMeter composes
// those around it. Redeliveries never reach it (a redelivery already paid on delivery 1 and must not
// feed lastStamp).
type LagCreditLimiter struct {
	resolve TenantCeilingResolver
	sources int

	now           func() time.Time
	idleTTL       time.Duration
	sweepInterval time.Duration

	shards [lagCreditShards]lagCreditShard
}

const (
	// lagCreditShards spreads tenants over independent locks. A power of two so the hash maps with a mask.
	lagCreditShards = 32
	// LagCreditMaxCover is the ceiling on the credit a bucket will ever grant, whatever a tenant's burst
	// and rate say. It equals the existing gate's BacklogThreshold: a lag longer than this is handled
	// exactly by the backlog route instead.
	LagCreditMaxCover = 5 * time.Second
)

type lagCreditShard struct {
	mu        sync.Mutex
	buckets   map[string]*lagCreditBucket
	lastSweep time.Time
}

// lagCreditBucket is one tenant's state: the token level on the pod clock, the last stamp read per
// source, and the exit epoch last accounted for per source.
type lagCreditBucket struct {
	tokens    float64
	last      time.Time // pod time tokens was last brought up to
	lastStamp []time.Time
	seenEpoch []uint64
	lastSeen  time.Time
}

// LagCreditOption configures a LagCreditLimiter at construction.
type LagCreditOption func(*LagCreditLimiter)

// WithLagCreditClock replaces the limiter's notion of now (time.Now by default). A nil clock is ignored.
func WithLagCreditClock(now func() time.Time) LagCreditOption {
	return func(l *LagCreditLimiter) {
		if now != nil {
			l.now = now
		}
	}
}

// WithLagCreditIdle sets how long an untouched bucket is kept and how often the sweep may scan. Values
// that are not positive keep the defaults (ten minutes, one minute).
func WithLagCreditIdle(ttl, sweepEvery time.Duration) LagCreditOption {
	return func(l *LagCreditLimiter) {
		if ttl > 0 {
			l.idleTTL = ttl
		}
		if sweepEvery > 0 {
			l.sweepInterval = sweepEvery
		}
	}
}

// LagCreditSource is the platform's view of one reader source at an admission.
type LagCreditSource struct {
	// Lag is the source's current head-of-line lag, corrected for the broker's clock skew and never
	// negative: how far behind this reader is, an upper bound on the age of any message it has yet to
	// read. A reader that is caught up reports 0.
	Lag time.Duration
	// Skew is the estimated offset of the broker's stamps from pod time (see the corrected age of a
	// stamp: now − stamp − Skew).
	Skew time.Duration
	// ExitEpoch counts how many times this source has left its backlog phase, and ExitAt is the pod
	// time of the latest exit. A source that never entered backlog has epoch 0.
	ExitEpoch uint64
	ExitAt    time.Time
}

// LagCreditRequest is one admission.
type LagCreditRequest struct {
	Tenant string
	// N is the charge. A non-positive N admits and charges nothing.
	N int
	// Scale multiplies both the rate and the capacity: 1 for the ceiling bucket, the contention
	// factor in (0,1) for the overload bucket. A scale that is not positive is a programming error:
	// a factor of 0 is a REFUSAL, decided by the caller as a boolean, never priced here.
	Scale float64
	// Source is the index of the reader source the message came from, with Stamp the broker's append
	// time of this first delivery. Source < 0 or a zero Stamp means a message with no source or no
	// stamp (external MQTT, or metadata unavailable): it is charged at pod time like any other and
	// feeds no last-stamp, but its capacity still carries the credit the OTHER sources need.
	Source int
	Stamp  time.Time
	// Sources describes every reader source, indexed as Source is; its length must equal the limiter's.
	Sources []LagCreditSource
	// Delay is extra credit added on top of the measured one (the reading stage adds the time the
	// message sat between the two stages). Never negative.
	Delay time.Duration
	// CreditCap bounds the credit. Zero means the tenant's own cover (LagCreditMax).
	CreditCap time.Duration
}

// LagCreditDecision is the outcome of one admission.
type LagCreditDecision struct {
	Admitted bool
	// Credit is the lag credit that sized the capacity this admission was judged against.
	Credit time.Duration
}

// LagCreditMax is the largest credit a tenant's bucket may carry: the time its BASE burst covers,
// min(LagCreditMaxCover, burst/rate). It is computed from the base ceiling and never the
// contention-scaled one, so a shed factor cannot stretch it. A ceiling with no usable rate or burst
// admits nothing wherever it is metered and keeps the fixed cover.
func LagCreditMax(c TenantCeiling) time.Duration {
	if c.RatePerSecond <= 0 || c.Burst <= 0 {
		return LagCreditMaxCover
	}
	if cover := time.Duration(float64(c.Burst) / c.RatePerSecond * float64(time.Second)); cover < LagCreditMaxCover {
		return cover
	}
	return LagCreditMaxCover
}

// NewLagCreditLimiter creates a limiter over `sources` reader sources whose per-tenant ceiling comes
// from resolve (called on every admission, outside any lock, so a changed ceiling is read each time).
// A nil resolver or a non-positive source count is a construction error and panics.
func NewLagCreditLimiter(resolve TenantCeilingResolver, sources int, opts ...LagCreditOption) *LagCreditLimiter {
	if resolve == nil || sources <= 0 {
		panic("core.NewLagCreditLimiter: a ceiling resolver and at least one source are required")
	}
	l := &LagCreditLimiter{
		resolve:       resolve,
		sources:       sources,
		now:           time.Now,
		idleTTL:       defaultRateLimiterIdleTTL,
		sweepInterval: defaultRateLimiterSweepInterval,
	}
	for i := range l.shards {
		l.shards[i].buckets = make(map[string]*lagCreditBucket)
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Ceiling is the base ceiling the limiter meters the tenant at, resolved as an admission resolves it.
func (l *LagCreditLimiter) Ceiling(tenant string) TenantCeiling { return l.resolve(tenant) }

// Len is the number of tenant buckets currently held.
func (l *LagCreditLimiter) Len() int {
	n := 0
	for i := range l.shards {
		sh := &l.shards[i]
		sh.mu.Lock()
		n += len(sh.buckets)
		sh.mu.Unlock()
	}
	return n
}

func (l *LagCreditLimiter) shardFor(tenant string) *lagCreditShard {
	h := fnv.New32a()
	_, _ = h.Write([]byte(tenant))
	return &l.shards[h.Sum32()&(lagCreditShards-1)]
}

// Admit judges one request: it folds the message's stamp into the tenant's per-source last stamp,
// computes the credit, sizes the capacity with it BEFORE accruing, accrues on the pod clock, and
// charges N when the bucket holds it. A refusal charges nothing.
func (l *LagCreditLimiter) Admit(req LagCreditRequest) LagCreditDecision {
	if len(req.Sources) != l.sources {
		panic(fmt.Sprintf("core.LagCreditLimiter.Admit: %d sources described, limiter has %d", len(req.Sources), l.sources))
	}
	if !(req.Scale > 0) || req.Scale > 1 {
		panic(fmt.Sprintf("core.LagCreditLimiter.Admit: scale %v is outside (0,1]; a zero factor is a refusal the caller decides", req.Scale))
	}
	if req.N <= 0 {
		return LagCreditDecision{Admitted: true}
	}
	if req.Source >= l.sources {
		panic(fmt.Sprintf("core.LagCreditLimiter.Admit: source %d out of range", req.Source))
	}
	c := l.resolve(req.Tenant)
	now := l.now()
	cover := LagCreditMax(c)
	creditCap := req.CreditCap
	if creditCap <= 0 {
		creditCap = cover
	}
	delay := max(req.Delay, 0)

	sh := l.shardFor(req.Tenant)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.sweepLocked(now, l.idleTTL, l.sweepInterval)

	b := sh.buckets[req.Tenant]
	fresh := b == nil
	if fresh {
		b = &lagCreditBucket{
			last:      now,
			lastStamp: make([]time.Time, l.sources),
			seenEpoch: make([]uint64, l.sources),
		}
		for k, s := range req.Sources {
			b.seenEpoch[k] = s.ExitEpoch
		}
		sh.buckets[req.Tenant] = b
	}
	b.lastSeen = now

	// Fold this message's stamp BEFORE the credit is computed: the message being charged is itself a
	// future message of its source as of the previous read, and its own age must be covered.
	if req.Source >= 0 && !req.Stamp.IsZero() {
		b.lastStamp[req.Source] = req.Stamp
	}

	credit := min(l.creditLocked(b, now, req.Sources)+delay, creditCap)
	if credit < 0 {
		credit = 0
	}

	rate := req.Scale * max(c.RatePerSecond, 0)
	capacity := req.Scale * (float64(max(c.Burst, 0)) + max(c.RatePerSecond, 0)*credit.Seconds())
	if fresh {
		b.tokens = capacity
	}

	// Exit-grant suppression: the first admission after a source left its backlog phase accrues only
	// from exitAt − creditCap, not across the whole phase.
	for k, s := range req.Sources {
		if b.seenEpoch[k] < s.ExitEpoch {
			if from := s.ExitAt.Add(-creditCap); from.After(b.last) {
				b.last = from
			}
			b.seenEpoch[k] = s.ExitEpoch
		}
	}

	// Capacity first, then accrual: the clamp uses THIS admission's capacity, so a stall's worth of
	// accrual is not cut back to the capacity the bucket had before the lag was known.
	if now.After(b.last) {
		b.tokens = math.Min(capacity, b.tokens+rate*now.Sub(b.last).Seconds())
		b.last = now
	} else {
		b.tokens = math.Min(capacity, b.tokens)
	}

	if b.tokens < float64(req.N) {
		return LagCreditDecision{Admitted: false, Credit: credit}
	}
	b.tokens -= float64(req.N)
	return LagCreditDecision{Admitted: true, Credit: credit}
}

// creditLocked is the credit bound for a bucket now, before any caller delay: the largest, over all
// sources, of the lesser of the age of the last stamp read from that source and that source's lag.
func (l *LagCreditLimiter) creditLocked(b *lagCreditBucket, now time.Time, sources []LagCreditSource) time.Duration {
	var credit time.Duration
	for k, s := range sources {
		lag := max(s.Lag, 0)
		term := lag
		if stamp := b.lastStamp[k]; !stamp.IsZero() {
			age := max(now.Sub(stamp)-s.Skew, 0)
			term = min(age, lag)
		}
		credit = max(credit, term)
	}
	return credit
}

// sweepLocked evicts buckets untouched for idleTTL, at most once per sweepEvery.
func (sh *lagCreditShard) sweepLocked(now time.Time, idleTTL, sweepEvery time.Duration) {
	if now.Sub(sh.lastSweep) < sweepEvery {
		return
	}
	sh.lastSweep = now
	for tenant, b := range sh.buckets {
		if now.Sub(b.lastSeen) >= idleTTL {
			delete(sh.buckets, tenant)
		}
	}
}
