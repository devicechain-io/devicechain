// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"sync"
	"time"
)

// tokenCacheTTL is how long a cached (tenant, external id) → device token resolution is
// served from memory before device-management is asked again.
//
// 🔴 IT IS WHAT MAKES A RELEASED TENANT TOKEN SAFE TO REUSE HERE, and the bound it has to
// respect is the tenant purge's token hold. A purged token is released only once the hold
// has elapsed since the purge swept the tenant's device rows, and an entry can only have been
// cached while those rows existed (the lookup finds nothing afterwards, and the erasure fence
// refuses re-creating them). So every entry for a purged tenant has expired before its token
// can belong to a successor, and the successor's first message resolves against the tenant
// that exists then — whether or not this process ever saw the purge. The refusal-path
// eviction (invalidateTenant) cannot promise that on its own: it needs a message to arrive
// while the tenant reads as deleted, which an idle tenant never sends.
//
// The default hold is the broker credential lifetime (12h); an hour sits well inside it and
// costs one device-management lookup per device per hour, against a cache whose job is to
// spare one per MESSAGE. An operator who lowers the hold below this TTL reopens the window
// this closes.
const tokenCacheTTL = time.Hour

// tokenCache memoizes the (tenant, external id) → device token resolution so that only
// the FIRST sight of a device calls device-management; every subsequent message for it
// resolves in memory until the entry expires (tokenCacheTTL). It caches only POSITIVE
// resolutions: an unknown device (auto-register off) or a failed lookup is not stored, so an
// operator registering the device later, or device-management recovering, is picked up on
// the next message rather than being permanently shadowed.
//
// It is unbounded for GA — bounded by the tenant's device count, the same profile as a
// source's session map. An expired entry is replaced on the next resolution of the same
// device and otherwise stays until its tenant is invalidated; that is memory, not an answer,
// because get never serves one.
//
// The map is nested by tenant rather than flat under a composite key, and that is the
// reason invalidateTenant can exist at all: dropping one tenant is a single delete
// instead of a scan of every entry in the process, so the refusal path can invalidate
// unconditionally without turning a purging tenant's message rate into O(cache) work.
type tokenCache struct {
	mu sync.RWMutex
	m  map[string]map[string]cachedToken
}

// cachedToken is one resolution and when it was cached.
type cachedToken struct {
	token string
	at    time.Time
}

func newTokenCache() *tokenCache {
	return &tokenCache{m: map[string]map[string]cachedToken{}}
}

// get resolves a cached token that is still fresh at now. External ids are unique only
// within a tenant, so the tenant MUST scope the lookup or one tenant's device could serve
// another's telemetry.
func (c *tokenCache) get(tenant, externalId string, now time.Time) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.m[tenant][externalId]
	if !ok || !now.Before(v.at.Add(tokenCacheTTL)) {
		return "", false
	}
	return v.token, true
}

func (c *tokenCache) put(tenant, externalId, token string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	byId, ok := c.m[tenant]
	if !ok {
		byId = map[string]cachedToken{}
		c.m[tenant] = byId
	}
	byId[externalId] = cachedToken{token: token, at: now}
}

// invalidateTenant drops every entry for one tenant (ADR-077).
//
// It stops a purged tenant's hot flows the moment the lifecycle gate reads it as deleted,
// rather than up to tokenCacheTTL later: without it an in-flight hot device would keep
// resolving from memory until its entry expired. It is called on every refusal rather than
// once on the transition because it is cheap by construction (see the type comment) and so
// no path has to remember to do it.
//
// It is NOT what protects a successor at a reused token, because it runs only when a message
// arrives while the tenant reads as deleted, and a tenant idle for its whole purge sends
// none. That is tokenCacheTTL's job.
func (c *tokenCache) invalidateTenant(tenant string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, tenant)
}
