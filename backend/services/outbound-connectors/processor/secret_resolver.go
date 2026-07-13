// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/secrets"
)

const (
	// defaultSecretCacheTTL bounds how stale a cached credential can be. The outbound-connectors
	// service resolves an httpCall/connector SecretRef per dispatch; a short cache spares the store
	// a decrypt on every event in a burst without holding a credential long after a Rotate. There is
	// no cross-service Rotate signal (the secret is rotated in this service's own store, or a future
	// authoring surface), so a short TTL — not invalidation — bounds staleness. Kept off any replay
	// path: dispatch is at-least-once execution, not a deterministic replay, so a cache hit changing
	// nothing observable is fine.
	defaultSecretCacheTTL = 60 * time.Second
	// defaultSecretCacheMax bounds the number of cached credentials so a tenant referencing many
	// distinct handles cannot grow the cache unboundedly. On overflow the cache is purged of expired
	// entries and, if still full, cleared — a cache is an optimization, never a correctness
	// dependency, so dropping it only costs a re-resolve.
	defaultSecretCacheMax = 2048
)

// SecretResolver resolves an ADR-059 secret handle to its cleartext credential for an outbound send,
// with a short bounded in-process cache. It is the ONLY place cleartext is materialized in this
// service, and it is SERVER-INTERNAL: the value is returned to the executor to present as a request
// header and is never logged, never surfaced on the API, and never written to the wire beyond the
// authenticated outbound call itself (the ADR-059 red line). A resolve failure is returned to the
// caller, which fails the action CLOSED (never sends unauthenticated when a handle was specified).
type SecretResolver struct {
	store secrets.SecretStore
	ttl   time.Duration
	max   int
	now   func() time.Time

	mu    sync.Mutex
	cache map[cacheKey]cachedSecret
}

// cacheKey identifies a cached credential by its (tenant, handle). A struct key is injective by
// construction — unlike a delimiter-joined string, no combination of tenant and handle values can
// collide onto another pair — so a subject-derived tenant (not grammar-checked on the consume side)
// can never alias a different tenant's cache entry.
type cacheKey struct {
	tenant string
	handle string
}

type cachedSecret struct {
	value   string
	expires time.Time
}

// NewSecretResolver builds a resolver over the service's secret store.
func NewSecretResolver(store secrets.SecretStore) *SecretResolver {
	return &SecretResolver{
		store: store,
		ttl:   defaultSecretCacheTTL,
		max:   defaultSecretCacheMax,
		now:   time.Now,
		cache: make(map[cacheKey]cachedSecret),
	}
}

// Resolve returns the cleartext credential for a tenant-scoped secret handle. It serves a fresh
// cache entry directly; otherwise it resolves through the store (envelope-decrypt), caches the
// result under the TTL, and returns it. The handle is a tenant-scoped ADR-059 ref Name; an
// instance-scoped credential is not addressable here (an httpCall/connector secret is a tenant's
// own). A store error (including secrets.ErrSecretNotFound) is returned unwrapped so the caller can
// distinguish "no such secret" from a transient failure and fail closed either way.
func (r *SecretResolver) Resolve(ctx context.Context, tenant, handle string) (string, error) {
	ref := secrets.SecretRef{Scope: secrets.ScopeTenant, Tenant: tenant, Name: handle}
	if err := ref.Valid(); err != nil {
		return "", err
	}
	key := cacheKey{tenant: tenant, handle: handle}

	now := r.now()
	r.mu.Lock()
	if c, ok := r.cache[key]; ok && now.Before(c.expires) {
		r.mu.Unlock()
		return c.value, nil
	}
	r.mu.Unlock()

	// Resolve outside the lock — the store does I/O + decryption; holding the mutex would serialize
	// all resolves behind one slow decrypt.
	value, err := r.store.Resolve(ctx, ref)
	if err != nil {
		return "", err
	}

	r.mu.Lock()
	r.evictLocked(now)
	r.cache[key] = cachedSecret{value: string(value), expires: now.Add(r.ttl)}
	r.mu.Unlock()
	return string(value), nil
}

// evictLocked keeps the cache bounded: it is called before an insert. When the cache is at capacity
// it first drops expired entries, and if still at capacity clears the map entirely (a cache is an
// optimization; a full clear only costs re-resolves). The caller holds r.mu.
func (r *SecretResolver) evictLocked(now time.Time) {
	if len(r.cache) < r.max {
		return
	}
	for k, c := range r.cache {
		if !now.Before(c.expires) {
			delete(r.cache, k)
		}
	}
	if len(r.cache) >= r.max {
		r.cache = make(map[cacheKey]cachedSecret, r.max)
	}
}
