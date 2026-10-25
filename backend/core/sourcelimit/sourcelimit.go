// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package sourcelimit is a keyed token-bucket limiter for unauthenticated endpoints: it
// rations how often one SOURCE (a client address) may reach a handler before that handler
// does any expensive work.
//
// It is deliberately small, and two things are deliberately absent:
//
//   - there is no trusted-source exemption. A source that succeeded once is no evidence
//     about its next request, and an exemption is a policy of one endpoint (user-management's
//     token endpoint keeps its own, on top of this), not of the primitive;
//   - there is no global bucket. A bucket shared by every tenant that refuses requests is a
//     lever a stranger can pull, from enough addresses, to stop everyone. A caller that
//     wants one builds it from a second Limiter used with a single constant key.
//
// Buckets are in process and per replica, not shared: the bound on what a sender can do is
// the sum over replicas, which is the same order of magnitude and needs no store of its own.
package sourcelimit

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// Limits are one limiter's schedule and its key bound.
type Limits struct {
	// Burst is how many requests a key may make at once, and the most it can bank.
	Burst int
	// Every is how often one more request is earned.
	Every time.Duration
	// MaxKeys bounds the key map; past it, idle keys are dropped, so a spray of distinct
	// keys cannot grow memory.
	MaxKeys int
}

// Validate refuses limits that would divide by zero or hold nothing.
func (l Limits) Validate() error {
	switch {
	case l.Burst < 1:
		return fmt.Errorf("sourcelimit: burst must be at least 1, got %d", l.Burst)
	case l.Every <= 0:
		return fmt.Errorf("sourcelimit: every must be positive, got %s", l.Every)
	case l.MaxKeys < 1:
		return fmt.Errorf("sourcelimit: maxKeys must be at least 1, got %d", l.MaxKeys)
	}
	return nil
}

type bucket struct {
	tokens float64
	last   time.Time
}

func (b *bucket) take(now time.Time, burst int, every time.Duration) bool {
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

// Option configures a Limiter at construction.
type Option func(*Limiter)

// WithClock replaces the clock. A test moves it to refill a bucket without waiting.
func WithClock(now func() time.Time) Option {
	return func(l *Limiter) { l.now = now }
}

// Limiter is a set of token buckets, one per key. The zero value refuses every request:
// a limiter nobody configured must not read as one that allows everything.
type Limiter struct {
	mu      sync.Mutex
	limits  Limits
	buckets map[string]*bucket
	now     func() time.Time
}

// New returns a Limiter, or an error for limits that cannot be honoured.
func New(limits Limits, opts ...Option) (*Limiter, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	l := &Limiter{limits: limits, buckets: map[string]*bucket{}, now: time.Now}
	for _, o := range opts {
		o(l)
	}
	if l.now == nil {
		return nil, errors.New("sourcelimit: clock must not be nil")
	}
	return l, nil
}

// Must is New for limits fixed in code: it panics on invalid ones, at construction and
// never at request time.
func Must(limits Limits, opts ...Option) *Limiter {
	l, err := New(limits, opts...)
	if err != nil {
		panic(err)
	}
	return l
}

// Every is the refill interval, which is what a refusal tells the caller to wait.
func (l *Limiter) Every() time.Duration { return l.limits.Every }

// Allow charges key one request and reports whether it may proceed.
func (l *Limiter) Allow(key string) bool {
	if l == nil || l.limits.Burst < 1 {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.buckets[key]
	if b == nil {
		if len(l.buckets) >= l.limits.MaxKeys {
			l.evictLocked(now)
		}
		b = &bucket{}
		l.buckets[key] = b
	}
	return b.take(now, l.limits.Burst, l.limits.Every)
}

// evictLocked drops every key idle long enough to have refilled, and if that frees nothing
// (a sustained spray) all of them: forgetting a sender's history only ever gives it a fresh
// allowance.
func (l *Limiter) evictLocked(now time.Time) {
	idle := l.limits.Every * time.Duration(l.limits.Burst)
	for k, b := range l.buckets {
		if now.Sub(b.last) >= idle {
			delete(l.buckets, k)
		}
	}
	if len(l.buckets) >= l.limits.MaxKeys {
		l.buckets = map[string]*bucket{}
	}
}

// SourceKey is the limiter key for a source address: an IPv4 address as it is, an IPv6
// address by its /64 (the block one subscriber is routinely given, so walking addresses
// inside it buys nothing), anything unparseable verbatim.
func SourceKey(source string) string {
	ip := net.ParseIP(source)
	if ip == nil {
		return source
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}
