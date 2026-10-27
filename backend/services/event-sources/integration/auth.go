// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// The refusals Check can return. The route maps them to 401, 429 and 503.
var (
	// ErrUnauthorized: no such integration, or the bearer token did not match. One answer to
	// both, as device-management gives them.
	ErrUnauthorized = errors.New("integration authentication failed")
	// ErrLimited: the integration's bad-credential budget is spent, so the credential was not
	// even checked.
	ErrLimited = errors.New("too many failed authentications for this integration")
	// ErrUnavailable: device-management could not answer and nothing last-known-good is held.
	ErrUnavailable = errors.New("integration authentication is unavailable")
)

// AuthConfig tunes the pre-check. Every zero field takes its default, which is the number the
// design fixes; the fields exist so a test can move the clock and shrink the bounds.
type AuthConfig struct {
	// Fresh is how long a positive answer is served without asking device-management again.
	Fresh time.Duration
	// StaleMax is the longest a positive answer is served when device-management cannot be
	// asked (or the bad-credential budget is spent). Device-management still authenticates every
	// event, so serving an old answer cannot authorize what it refuses.
	StaleMax time.Duration
	// BadBurst and BadRate are each integration's bad-credential bucket.
	BadBurst int
	BadRate  float64
	// Buckets bounds the per-integration buckets held; past it, names share one overflow
	// bucket. An integration name is a string a sender chose before any credential was checked.
	Buckets int
	// Entries bounds the positive cache.
	Entries int
	Now     func() time.Time
}

func (c AuthConfig) withDefaults() AuthConfig {
	if c.Fresh <= 0 {
		c.Fresh = 60 * time.Second
	}
	if c.StaleMax <= 0 {
		c.StaleMax = 15 * time.Minute
	}
	if c.BadBurst <= 0 {
		c.BadBurst = 30
	}
	if c.BadRate <= 0 {
		c.BadRate = 1
	}
	if c.Buckets <= 0 {
		c.Buckets = 1024
	}
	if c.Entries <= 0 {
		c.Entries = 4096
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Authenticator is the integration pre-check: it proves a request's bearer token belongs to the
// integration its URL names, before a byte of the body is read or the tenant's ingest allowance
// is spent.
//
// 🔴 A BAD TOKEN IS CHARGED BEFORE DEVICE-MANAGEMENT IS ASKED. A wrong token cannot be cached by
// value (there is no end to the values), so every distinct wrong token is a call to
// device-management; the per-integration bucket is what bounds how many such calls one sender
// who knows the URL can cause, and it is taken first so the bound holds while device-management
// is slow. A call that turns out to carry the right token, or that fails for a reason that is
// not the sender's, gives its token back, so good traffic that happens to miss the cache is not
// what drains it.
//
// A token that WAS right is served from memory for Fresh, and for up to StaleMax when the
// question cannot be asked: the last-known-good answer. That is also what keeps the integration's
// good traffic flowing through a spray of bad tokens that has emptied its bucket.
type Authenticator struct {
	cfg     AuthConfig
	backend Backend

	sf singleflight.Group

	mu       sync.Mutex
	entries  map[string]authEntry
	buckets  map[string]*bucket
	overflow *bucket
	failed   map[string]time.Time
}

type authEntry struct {
	desc *Descriptor
	at   time.Time
}

// NewAuthenticator builds the pre-check over a backend.
func NewAuthenticator(backend Backend, cfg AuthConfig) *Authenticator {
	cfg = cfg.withDefaults()
	a := &Authenticator{
		cfg: cfg, backend: backend,
		entries:  make(map[string]authEntry),
		buckets:  make(map[string]*bucket),
		overflow: newBucket(cfg.BadBurst, cfg.Now()),
		failed:   make(map[string]time.Time),
	}
	return a
}

// Checked is a successful pre-check.
type Checked struct {
	// Descriptor is what the integration is. Enabled may be false: a switched-off integration's
	// token is valid, and the caller refuses its traffic without calling it a bad credential.
	Descriptor *Descriptor
	// Stale says the answer is older than Fresh and was served because device-management could
	// not be asked (or the budget was spent).
	Stale bool
}

func cacheKey(tenant, integration, bearer string) string {
	sum := sha256.Sum256([]byte(bearer))
	return tenant + "\x00" + integration + "\x00" + hex.EncodeToString(sum[:])
}

// Check authenticates a bearer token for an integration.
func (a *Authenticator) Check(ctx context.Context, tenant, integration, bearer string) (Checked, error) {
	key := cacheKey(tenant, integration, bearer)
	now := a.cfg.Now()
	if e, ok := a.lookup(key, now); ok && now.Sub(e.at) < a.cfg.Fresh {
		return Checked{Descriptor: e.desc}, nil
	}
	v, err, _ := a.sf.Do(key, func() (any, error) {
		// The leader's request may be cancelled while others wait on its answer.
		return a.refresh(context.WithoutCancel(ctx), key, tenant, integration, bearer)
	})
	if err != nil {
		return Checked{}, err
	}
	return v.(Checked), nil
}

func (a *Authenticator) refresh(ctx context.Context, key, tenant, integration, bearer string) (Checked, error) {
	now := a.cfg.Now()
	stale, haveStale := a.lookup(key, now)
	b := a.bucketFor(tenant, integration, now)

	if !b.take(now, a.cfg) {
		if haveStale {
			return Checked{Descriptor: stale.desc, Stale: true}, nil
		}
		return Checked{}, ErrLimited
	}
	desc, err := a.backend.Auth(ctx, tenant, integration, bearer)
	switch {
	case err != nil:
		b.give(a.cfg)
		if haveStale {
			return Checked{Descriptor: stale.desc, Stale: true}, nil
		}
		return Checked{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	case desc == nil:
		a.forget(key)
		a.noteFailure(tenant, integration, now)
		return Checked{}, ErrUnauthorized
	}
	b.give(a.cfg)
	a.store(key, authEntry{desc: desc, at: a.cfg.Now()})
	return Checked{Descriptor: desc}, nil
}

// Forget drops a token's positive answer, for a caller that has learned from device-management
// that it no longer holds (a rotation, a disable).
func (a *Authenticator) Forget(tenant, integration, bearer string) {
	a.forget(cacheKey(tenant, integration, bearer))
}

func (a *Authenticator) lookup(key string, now time.Time) (authEntry, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.entries[key]
	if !ok {
		return authEntry{}, false
	}
	if now.Sub(e.at) > a.cfg.StaleMax {
		delete(a.entries, key)
		return authEntry{}, false
	}
	return e, true
}

func (a *Authenticator) store(key string, e authEntry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.entries[key]; !ok && len(a.entries) >= a.cfg.Entries {
		for k, old := range a.entries {
			if e.at.Sub(old.at) > a.cfg.StaleMax {
				delete(a.entries, k)
			}
		}
		for k := range a.entries {
			if len(a.entries) < a.cfg.Entries {
				break
			}
			delete(a.entries, k)
		}
	}
	a.entries[key] = e
}

func (a *Authenticator) forget(key string) {
	a.mu.Lock()
	delete(a.entries, key)
	a.mu.Unlock()
}

// bucketFor returns the bad-credential bucket for an integration name. A name past the bound shares the overflow bucket.
func (a *Authenticator) bucketFor(tenant, integration string, now time.Time) *bucket {
	name := tenant + "\x00" + integration
	a.mu.Lock()
	defer a.mu.Unlock()
	if b, ok := a.buckets[name]; ok {
		return b
	}
	if len(a.buckets) >= a.cfg.Buckets {
		// Idle full buckets carry no state worth keeping: a fresh one is the same bucket.
		for k, b := range a.buckets {
			if b.idle(now, a.cfg) {
				delete(a.buckets, k)
			}
		}
	}
	if len(a.buckets) >= a.cfg.Buckets {
		return a.overflow
	}
	b := newBucket(a.cfg.BadBurst, now)
	a.buckets[name] = b
	return b
}

// noteFailure asks device-management to stamp the integration's last-failure marker, at most
// once a minute per integration and never from more than a bounded set of names.
func (a *Authenticator) noteFailure(tenant, integration string, now time.Time) {
	name := tenant + "\x00" + integration
	a.mu.Lock()
	last, seen := a.failed[name]
	if seen && now.Sub(last) < time.Minute {
		a.mu.Unlock()
		return
	}
	if !seen && len(a.failed) >= a.cfg.Buckets {
		for k, t := range a.failed {
			if now.Sub(t) >= time.Minute {
				delete(a.failed, k)
			}
		}
		if len(a.failed) >= a.cfg.Buckets {
			a.mu.Unlock()
			return
		}
	}
	a.failed[name] = now
	a.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Best effort: the marker is for an operator to read, and a failure to write it must not
		// change an answer already given.
		_ = a.backend.RecordAuthFailure(ctx, tenant, integration)
	}()
}

// bucket is a token bucket whose tokens can be given back, which a rate.Limiter reservation
// cannot do once the time it was taken at has passed.
type bucket struct {
	mu     sync.Mutex
	tokens float64
	at     time.Time
}

func newBucket(burst int, now time.Time) *bucket { return &bucket{tokens: float64(burst), at: now} }

func (b *bucket) refill(now time.Time, cfg AuthConfig) {
	if now.After(b.at) {
		b.tokens += now.Sub(b.at).Seconds() * cfg.BadRate
		if max := float64(cfg.BadBurst); b.tokens > max {
			b.tokens = max
		}
		b.at = now
	}
}

func (b *bucket) take(now time.Time, cfg AuthConfig) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill(now, cfg)
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (b *bucket) give(cfg AuthConfig) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tokens++; b.tokens > float64(cfg.BadBurst) {
		b.tokens = float64(cfg.BadBurst)
	}
}

func (b *bucket) idle(now time.Time, cfg AuthConfig) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill(now, cfg)
	return b.tokens >= float64(cfg.BadBurst)
}
