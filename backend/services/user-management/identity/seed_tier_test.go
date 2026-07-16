// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
)

// TestViewerAuthoritiesAreAllTenantTier guards a path the admin-plane validation
// cannot reach. The seeded `viewer` role is written by EnsureRole straight through
// the store, so admin.validateAuthorities never sees it — nothing else would notice
// a system-tier authority being added to the baseline every enabled tenant member
// silently receives (issueTenantTokens unions it onto every access token).
//
// The consequence of getting this wrong is not a leak — Authorize refuses a
// system-tier authority on an access token regardless (ADR-065) — but a baseline
// that grants an authority the token can never satisfy is a lie in the role
// catalog, and it is the exact confusion this partition exists to end.
func TestViewerAuthoritiesAreAllTenantTier(t *testing.T) {
	if len(viewerAuthorities) == 0 {
		t.Fatal("precondition: the viewer baseline is empty")
	}
	for _, a := range viewerAuthorities {
		tiers, known := auth.TiersOf(auth.Authority(a))
		if !known {
			t.Errorf("viewer grants %q, which declares no tier", a)
			continue
		}
		if !tiers.Has(auth.TierTenant) {
			t.Errorf("viewer grants %q, which is %v — the read-only baseline every "+
				"tenant member receives must be satisfiable from a tenant access token", a, tiers)
		}
	}
}
