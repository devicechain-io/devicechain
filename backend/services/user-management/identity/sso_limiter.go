// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"sync"
	"time"
)

// beginSso is public, so anyone can call it, and each call that gets as far as a
// connection writes a ten-minute record to the state bucket. Two in-process token buckets
// ration it:
//
//   - one per SOURCE address, which stops a single sender from taking the whole allowance;
//   - one GLOBAL, which is the part that holds against a sender who varies the source
//     (the address is whatever the nearest proxy reports, and a fairness key only).
//
// They are per replica, not shared: the bound on what a sender can write is the sum over
// replicas, which is the same order of magnitude and needs no store of its own. The state
// bucket's byte ceiling is the hard stop behind them, and a full bucket makes beginSso fail
// closed (and raises an alert) rather than degrade.
const (
	// ssoBeginPerSourceBurst and ssoBeginPerSourceEvery: a person retries a few times;
	// a script cannot sustain more than one begin every two seconds from one address.
	ssoBeginPerSourceBurst = 5
	ssoBeginPerSourceEvery = 2 * time.Second
	// ssoBeginGlobalBurst and ssoBeginGlobalEvery: a few hundred sign-ins a minute for the
	// whole replica, far above any real login rate and far below what fills the bucket.
	ssoBeginGlobalBurst = 200
	ssoBeginGlobalEvery = 250 * time.Millisecond
	// ssoLimiterMaxSources bounds the per-source map; past it the oldest idle entries are
	// dropped, so a spray of distinct addresses cannot grow memory.
	ssoLimiterMaxSources = 10000
)

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// limiterLimits are one limiter's two schedules and its key bound.
type limiterLimits struct {
	perKeyBurst int
	perKeyEvery time.Duration
	globalBurst int
	globalEvery time.Duration
	maxKeys     int
}

// ssoBeginLimits are beginSso's, above.
var ssoBeginLimits = limiterLimits{
	perKeyBurst: ssoBeginPerSourceBurst, perKeyEvery: ssoBeginPerSourceEvery,
	globalBurst: ssoBeginGlobalBurst, globalEvery: ssoBeginGlobalEvery, maxKeys: ssoLimiterMaxSources,
}

// sourceLimiter is the pair of buckets above. The zero value is ready to use, with
// beginSso's limits; another endpoint sets its own.
type sourceLimiter struct {
	mu      sync.Mutex
	global  tokenBucket
	sources map[string]*tokenBucket
	// limits, when set, replaces ssoBeginLimits.
	limits *limiterLimits
	// now is the clock; a test may replace it.
	now func() time.Time
}

func (l *sourceLimiter) lim() limiterLimits {
	if l.limits != nil {
		return *l.limits
	}
	return ssoBeginLimits
}

func (l *sourceLimiter) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

func (b *tokenBucket) take(now time.Time, burst int, every time.Duration) bool {
	if b.last.IsZero() {
		b.tokens, b.last = float64(burst), now
	}
	b.tokens += float64(now.Sub(b.last)) / float64(every)
	if b.tokens > float64(burst) {
		b.tokens = float64(burst)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// allow reports whether a begin from source may proceed. A refusal from either bucket
// refuses; the per-source bucket is charged first so a sender already over its own
// allowance does not also drain the global one.
func (l *sourceLimiter) allow(source string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()
	lim := l.lim()
	if l.sources == nil {
		l.sources = map[string]*tokenBucket{}
	}
	sb := l.sources[source]
	if sb == nil {
		if len(l.sources) >= lim.maxKeys {
			l.evictLocked(now)
		}
		sb = &tokenBucket{}
		l.sources[source] = sb
	}
	if !sb.take(now, lim.perKeyBurst, lim.perKeyEvery) {
		return false
	}
	if lim.globalBurst == 0 {
		return true
	}
	return l.global.take(now, lim.globalBurst, lim.globalEvery)
}

// evictLocked drops every source that has been idle long enough to have refilled, and if
// that frees nothing (a sustained spray) all of them: forgetting a sender's history only
// ever gives it a fresh per-source allowance, which the global bucket still bounds.
func (l *sourceLimiter) evictLocked(now time.Time) {
	lim := l.lim()
	idle := lim.perKeyEvery * time.Duration(lim.perKeyBurst)
	for k, b := range l.sources {
		if now.Sub(b.last) >= idle {
			delete(l.sources, k)
		}
	}
	if len(l.sources) >= lim.maxKeys {
		l.sources = map[string]*tokenBucket{}
	}
}
