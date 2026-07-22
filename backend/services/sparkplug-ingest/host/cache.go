// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import "sync"

// tokenCache memoizes the (tenant, external id) → device token resolution so that
// only the FIRST sight of a device calls device-management; every subsequent DATA
// message for it resolves in memory. It caches only POSITIVE resolutions: an
// unknown device (auto-register off) or a failed lookup is not stored, so an
// operator registering the device later, or device-management recovering, is picked
// up on the next message rather than being permanently shadowed.
//
// It is unbounded for GA — bounded by the tenant's device count, the same profile
// as SP2's session map — and eviction is a post-GA concern (a deleted device leaves
// a stale entry whose now-unknown token the resolver handles gracefully, PR #497).
type tokenCache struct {
	mu sync.RWMutex
	m  map[string]string
}

func newTokenCache() *tokenCache {
	return &tokenCache{m: map[string]string{}}
}

// key composes the tenant-scoped cache key. External ids are unique only within a
// tenant, so the tenant MUST be part of the key or one tenant's device could serve
// another's telemetry. NUL separates the two so no tenant/id pair can alias another.
func (c *tokenCache) key(tenant, externalId string) string {
	return tenant + "\x00" + externalId
}

func (c *tokenCache) get(tenant, externalId string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.m[c.key(tenant, externalId)]
	return v, ok
}

func (c *tokenCache) put(tenant, externalId, token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[c.key(tenant, externalId)] = token
}
