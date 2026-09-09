// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"
)

// tenantResolver is the per-tenant, out-of-band-refreshed, fail-open TTL cache shared
// by every setting an enforcing service resolves on its hot path: the ADR-023 rate
// ceilings (TenantLimitResolver, V = Limits) and the ADR-063 shed priority
// (ShedPriorityResolver, V = int). It is generic in the VALUE only — the mechanics
// (the TTL, the inflight dedupe, the refresh rate bound, fail-open to a default)
// are identical for every setting, and are the subtle, security-relevant part the
// package comment warns must not be copied per consumer. The per-setting part is only
// what to fetch and what the default is, both injected.
//
// Fail-open means "serve the default (or last-known)", and the default is itself a
// real value (a metered ceiling / a bronze-band priority), never "unbounded" or
// "never shed" — so a slow or unreachable authority degrades safely.
//
// 🔴 CONCURRENCY IS NOT RATE, and the difference is the whole reason maxRefreshesPerSecond
// exists alongside maxConcurrentRefreshes. A cap of N in-flight fetches bounds how many
// refreshes run AT ONCE; it says nothing about how many run per unit time, because each one
// frees its slot the moment it completes. Against a backend answering in a millisecond, a
// cap of 8 admits several thousand fetches a second — measured, not estimated. The bound a
// hot path resolving an attacker-influenced tenant string actually needs is on the RATE, so
// this type carries both: the semaphore bounds concurrent load on user-management, and the
// token bucket bounds the request rate.
type tenantResolver[V any] struct {
	fetch func(ctx context.Context, tenant string) (V, error)
	def   V
	ttl   time.Duration
	// negativeTTL is how long a FAILED refresh holds off the next attempt for that
	// tenant (see recordFailureLocked).
	negativeTTL time.Duration
	now         func() time.Time
	// label names this resolver's setting in logs, so a service resolving more than
	// one reports which failed to refresh.
	label string

	mu    sync.Mutex
	cache map[string]cacheEntry[V]
	// negatives counts the cache entries that hold no fetched value, so the
	// attacker-influenced half of the map can be bounded independently of the
	// tenants that really exist (see recordFailureLocked).
	negatives int
	inflight  map[string]struct{}
	// sem bounds concurrent refreshes (see maxConcurrentRefreshes).
	sem chan struct{}
	// refreshes bounds the RATE at which refreshes start (see maxRefreshesPerSecond).
	//
	// It is per resolver rather than per process on purpose. A process-wide bucket would
	// bound the pod more tightly, but it would also let a flood against one setting starve
	// another's refreshes — a flood of unresolvable tenants at the shed-priority resolver
	// would stop the tenant-lifecycle gate learning about a deleted tenant. Per-resolver
	// buckets keep one setting's contention off the others, the same isolation argument
	// core.TenantRateLimiter makes for per-tenant buckets.
	//
	// What it does NOT give is fairness WITHIN a resolver, and that limit is accepted
	// deliberately. A sustained flood of NOVEL tenant names keeps the bucket near empty
	// — the hold-off cannot help, since each name is used once — so a legitimate
	// tenant whose TTL expires during the flood may keep serving its last-known value
	// for as long as the flood lasts. That is tolerable because of what the stale value
	// IS: a real ceiling or a real priority, never unlimited, and never worse than the
	// platform default. Deletion is the one thing that must not wait on this, and it
	// does not — ADR-077's per-area fence, not this gate, is the erasure guarantee.
	refreshes *rate.Limiter
}

// cacheEntry is one tenant's cached state. It holds a value only when a fetch has
// actually succeeded; an entry with have == false is the record of a FAILED attempt,
// kept solely to hold off the next one.
type cacheEntry[V any] struct {
	val  V
	have bool
	// nextRefreshAt is when this tenant may be refreshed again: the fetch time plus
	// the TTL after a success, plus the negative TTL after a failure. Storing the
	// deadline rather than the fetch time is what lets one field carry both, so a
	// failed refresh of a known tenant does not have to choose between forgetting
	// the last-known value and forgetting that the attempt failed.
	nextRefreshAt time.Time
}

// ResolverOptions tunes a resolver's caching and refresh bounds. Every field is
// optional: a zero or non-positive value means "the package default", never
// "unbounded" — the ADR-023 polarity, applied to the resolver's own knobs. Call
// Configure before the resolver is first used.
//
// It is exported because the defaults are otherwise reachable only from inside this
// package, which leaves a service unable to test that its own path honours a changed
// ceiling without waiting a real TTL for the cache to expire.
type ResolverOptions struct {
	// TTL bounds how stale a successfully fetched value may be before a refresh is
	// triggered. Default defaultCacheTTL.
	TTL time.Duration
	// NegativeTTL is how long a failed refresh holds off the next attempt for that
	// tenant. Default defaultNegativeTTL.
	NegativeTTL time.Duration
	// MaxConcurrent bounds refreshes in flight at once. Default maxConcurrentRefreshes.
	MaxConcurrent int
	// RefreshesPerSecond and RefreshBurst bound the RATE at which refreshes start.
	// Defaults maxRefreshesPerSecond / maxRefreshBurst.
	RefreshesPerSecond float64
	RefreshBurst       int
	// Clock supplies the resolver's notion of now, for tests that must not sleep.
	// Default time.Now.
	Clock func() time.Time
}

// newTenantResolver builds a resolver over fetch, defaulting an uncached or
// unresolvable tenant to def. label names the setting for logs.
func newTenantResolver[V any](fetch func(context.Context, string) (V, error), def V, label string) *tenantResolver[V] {
	r := &tenantResolver[V]{
		fetch:    fetch,
		def:      def,
		label:    label,
		cache:    make(map[string]cacheEntry[V]),
		inflight: make(map[string]struct{}),
	}
	r.applyLocked(ResolverOptions{})
	return r
}

// Configure applies opts to the resolver, replacing every knob — a zero or
// non-positive field resolves to the package default rather than to "unbounded", so
// a partially filled ResolverOptions cannot silently remove a bound.
//
// It must be called before the resolver is first used, and that is a convention rather
// than something enforced. Calling it later is safe in the sense that nothing is
// stranded — a refresh releases the semaphore it actually took, not whichever one the
// field holds when it finishes — but replacing the semaphore while refreshes are in
// flight allows a transient overshoot of the concurrency cap, since the slots held
// against the old channel are not counted against the new one.
func (r *tenantResolver[V]) Configure(opts ResolverOptions) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.applyLocked(opts)
}

// applyLocked folds opts onto the package defaults. The caller must hold r.mu, or
// hold the only reference to r (construction).
func (r *tenantResolver[V]) applyLocked(opts ResolverOptions) {
	r.ttl = positiveDuration(opts.TTL, defaultCacheTTL)
	r.negativeTTL = positiveDuration(opts.NegativeTTL, defaultNegativeTTL)
	r.now = opts.Clock
	if r.now == nil {
		r.now = time.Now
	}
	concurrent := opts.MaxConcurrent
	if concurrent <= 0 {
		concurrent = maxConcurrentRefreshes
	}
	r.sem = make(chan struct{}, concurrent)
	perSecond := opts.RefreshesPerSecond
	// The negated comparison also rejects NaN, which no comparison would. +Inf is
	// rejected separately because it passes `> 0` and is the one value that would make
	// this struct's documented promise false: rate.Limit(+Inf) leaves the bucket's
	// token arithmetic undefined at zero elapsed time (Inf × 0), so the resolver would
	// admit refreshes without a bound while claiming a knob left unset never can.
	if !(perSecond > 0) || math.IsInf(perSecond, 0) {
		perSecond = maxRefreshesPerSecond
	}
	burst := opts.RefreshBurst
	if burst <= 0 {
		burst = maxRefreshBurst
	}
	r.refreshes = rate.NewLimiter(rate.Limit(perSecond), burst)
}

// positiveDuration returns d when it is positive, else def.
func positiveDuration(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
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
//
// A tenant whose refresh FAILED reports false and serves the default, exactly like one
// never seen: the negative entry caches the ATTEMPT, never a value, so a caller that
// holds off on unresolved tenants keeps holding off.
func (r *tenantResolver[V]) resolveOK(tenant string) (V, bool) {
	r.mu.Lock()
	e, ok := r.cache[tenant]
	if !ok || !r.now().Before(e.nextRefreshAt) {
		r.triggerRefreshLocked(tenant)
	}
	r.mu.Unlock()

	if ok && e.have {
		return e.val, true
	}
	return r.def, false
}

// triggerRefreshLocked starts at most one background refresh per tenant (deduped by
// the inflight set), only while a global concurrency slot is free, and only while the
// resolver's refresh token bucket admits it — so neither a flood of distinct tenants
// nor a fast-failing authority can drive lookups without bound. When either bound is
// hit the refresh is skipped (the caller serves the default and a later call retries).
// The caller must hold r.mu; neither the non-blocking send nor the token check blocks
// under the lock.
func (r *tenantResolver[V]) triggerRefreshLocked(tenant string) {
	if _, running := r.inflight[tenant]; running {
		return
	}
	select {
	case r.sem <- struct{}{}:
	default:
		return // at the concurrency cap; serve default and retry on a later call
	}
	if !r.refreshes.AllowN(r.now(), 1) {
		// Give the slot back before returning. Taking the slot FIRST and the token
		// second is deliberate: the reverse order would spend a token on a refresh
		// the concurrency cap then refuses to start, letting over-cap traffic drain
		// the budget that legitimate refreshes need.
		<-r.sem
		return // at the refresh rate bound; serve default and retry on a later call
	}
	r.inflight[tenant] = struct{}{}
	// The semaphore is passed rather than re-read in the goroutine: Configure replaces
	// the channel, and a refresh must release the slot it actually took. Reading the
	// field later would have it return a token to a channel it never filled.
	go r.refresh(tenant, r.sem)
}

// refresh fetches a tenant's value and updates the cache. On error it keeps serving the
// last-known value (or the default) rather than caching a sentinel that would drop the
// tenant to an unsafe reading — but it does record that the ATTEMPT happened, which is
// what stops an unresolvable tenant re-triggering a fetch on every message.
func (r *tenantResolver[V]) refresh(tenant string, sem chan struct{}) {
	defer func() {
		r.mu.Lock()
		delete(r.inflight, tenant)
		r.mu.Unlock()
		<-sem
	}()

	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()

	val, err := r.fetch(ctx, tenant)
	if err != nil {
		r.mu.Lock()
		r.recordFailureLocked(tenant, r.now())
		r.mu.Unlock()
		log.Warn().Err(err).Str("tenant", tenant).Str("setting", r.label).
			Msg("Failed to refresh per-tenant setting; keeping last-known (or platform default)")
		return
	}
	r.mu.Lock()
	if e, ok := r.cache[tenant]; ok && !e.have {
		// The tenant is leaving the bounded half of the map, so give its slot back.
		// Without this the count only ever rises: a tenant that fails once and then
		// resolves holds a slot for the life of the process, and after enough such
		// events recordFailureLocked stops recording at all — which silently returns
		// the resolver to a fetch (and a log line) per message for every tenant that
		// cannot be resolved.
		r.negatives--
	}
	now := r.now()
	r.cache[tenant] = cacheEntry[V]{val: val, have: true, nextRefreshAt: now.Add(r.ttl)}
	r.mu.Unlock()
}

// recordFailureLocked holds a failed tenant off for negativeTTL without disturbing any
// value it already has. The caller must hold r.mu.
//
// A tenant that has never resolved gets an entry carrying no value, which resolveOK
// still reads as "unresolved". That map half is keyed by input the platform does not
// control — on the hot paths the tenant string is only grammar-validated — so it is
// bounded at maxNegativeEntries, expired entries first. At the bound nothing is
// recorded and those tenants simply re-trigger refreshes, which the rate bound in
// triggerRefreshLocked still holds down; trading the hold-off for a bounded map is the
// right way round, because the map is the only one of the two an attacker could grow.
func (r *tenantResolver[V]) recordFailureLocked(tenant string, now time.Time) {
	next := now.Add(r.negativeTTL)
	if e, ok := r.cache[tenant]; ok {
		e.nextRefreshAt = next
		r.cache[tenant] = e
		return
	}
	if r.negatives >= maxNegativeEntries {
		for k, e := range r.cache {
			// !e.have is what keeps the sweep to the bounded half. A RESOLVED entry
			// past its refresh time is not junk to be reclaimed — it is a legitimate
			// tenant's last-known value, and its refresh being overdue is the normal
			// state during a flood, since the rate bound is refusing refreshes.
			// Evicting one would drop that tenant to the platform default and make
			// resolveOK report it unresolved, which for the shed resolver means a gold
			// tenant losing its priority at exactly the moment contention is high.
			if !e.have && !now.Before(e.nextRefreshAt) {
				delete(r.cache, k)
				r.negatives--
			}
		}
	}
	if r.negatives >= maxNegativeEntries {
		return
	}
	r.cache[tenant] = cacheEntry[V]{nextRefreshAt: next}
	r.negatives++
}
