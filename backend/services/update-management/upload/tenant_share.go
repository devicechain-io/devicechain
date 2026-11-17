// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package upload

import "sync"

// tenantShare caps how many of a replica's upload slots one tenant holds at once: half of them,
// rounded up, and never less than one, so a lone tenant on a one-slot replica still uploads. It
// is the shape outbound-connectors uses for its send pool. Without it one member holding
// artifact:write declares as many artifacts as there are slots, trickles each inside its window,
// and every other tenant's upload on the replica is refused for as long as that lasts.
type tenantShare struct {
	mu     sync.Mutex
	limit  int
	active map[string]int // tenant -> uploads admitted; absent at zero, so it holds only live tenants
}

func newTenantShare(slots int) *tenantShare {
	return &tenantShare{limit: (slots + 1) / 2, active: make(map[string]int)}
}

// acquire admits one upload for tenant, or reports false when the tenant already holds its
// share. Every true is paired with exactly one release.
func (s *tenantShare) acquire(tenant string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[tenant] >= s.limit {
		return false
	}
	s.active[tenant]++
	return true
}

func (s *tenantShare) release(tenant string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[tenant] <= 1 {
		delete(s.active, tenant)
		return
	}
	s.active[tenant]--
}
