// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package adapter

import "sync"

// tokenCache memoizes the (tenant, external id) → device token resolution so that only
// the FIRST sight of a device calls device-management; every subsequent message for it
// resolves in memory. It caches only POSITIVE resolutions: an unknown device
// (auto-register off) or a failed lookup is not stored, so an operator registering the
// device later, or device-management recovering, is picked up on the next message
// rather than being permanently shadowed.
//
// It is unbounded for GA — bounded by the tenant's device count, the same profile as a
// source's session map — and per-DEVICE eviction is a post-GA concern (a deleted device
// leaves a stale entry whose now-unknown token the resolver handles gracefully). Per
// TENANT eviction is not deferred; see invalidateTenant.
//
// The map is nested by tenant rather than flat under a composite key, and that is the
// reason invalidateTenant can exist at all: dropping one tenant is a single delete
// instead of a scan of every entry in the process, so the refusal path can invalidate
// unconditionally without turning a purging tenant's message rate into O(cache) work.
type tokenCache struct {
	mu sync.RWMutex
	m  map[string]map[string]string
}

func newTokenCache() *tokenCache {
	return &tokenCache{m: map[string]map[string]string{}}
}

// get resolves a cached token. External ids are unique only within a tenant, so the
// tenant MUST scope the lookup or one tenant's device could serve another's telemetry.
func (c *tokenCache) get(tenant, externalId string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.m[tenant][externalId]
	return v, ok
}

func (c *tokenCache) put(tenant, externalId, token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	byId, ok := c.m[tenant]
	if !ok {
		byId = map[string]string{}
		c.m[tenant] = byId
	}
	byId[externalId] = token
}

// invalidateTenant drops every entry for one tenant (ADR-077).
//
// It exists because this cache is the one thing that keeps a purged tenant's telemetry
// flowing after everything else has been cut: it never expires, so an in-flight hot
// device resolves from memory forever and never asks device-management again. Deleting
// the device rows does not stop it — the cache does not consult them.
//
// And once ADR-077 releases a purged token, a stale entry stops being merely useless and
// becomes a cross-tenant hazard: a SUCCESSOR tenant reusing the token would resolve its
// own external ids to the PREDECESSOR's device tokens, writing one customer's telemetry
// under another's device. It is called on every refusal rather than once on the
// transition because it is cheap by construction (see the type comment) and so no path
// has to remember to do it.
//
// 🔴 That does NOT fully close the hazard, and the residue is worth stating rather than
// implying. Invalidation happens on a REFUSAL, so it requires a message to arrive while
// the tenant reads purging. A tenant that is idle for its entire purge in a process that
// stays up is never refused, never invalidated, and its entries are still there when a
// successor at the reused token sends its first message. Closing that needs invalidation
// keyed on something a successor cannot share with its predecessor — the purge epoch, or
// the tenant row's identity — which arrives with the token release in slice 2. Until
// then the honest statement is: this stops the hot flows, and a process restart or any
// traffic during the purge closes the rest.
func (c *tokenCache) invalidateTenant(tenant string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, tenant)
}
