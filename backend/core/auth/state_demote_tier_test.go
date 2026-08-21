// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"errors"
	"testing"
)

// TestStateDemoteIsReachableFromATenantStarToken is the vocabulary entry's OWN gate,
// and it lives here rather than in device-state because the thing that can break it
// lives here.
//
// 🔴 THE VOCABULARY ENTRY IS WHAT MAKES "*" WORK, WHICH IS NOT THE INTUITION. The
// constant compiles, the resolver calls Authorize with it, the seeded tenant-admin
// role grants "*", HasAuthority passes "*" for every check — and the mutation is
// still refused to everyone, because satisfies() ends at `known && tiers.Has(...)`
// and an authority absent from the vocabulary reports known == false. So the failure
// mode of forgetting the map entry is not "the tier came out wrong": it is a mutation
// no credential in the instance can reach, on a service whose other doors all work,
// with nothing in the constant's own declaration to suggest why.
//
// Deleting the `StateDemote: tenant` line from the vocabulary is the mutation this
// kills, and it has been run: with the entry removed TiersOf reports known == false
// and BOTH admission cases below fail — including the "*" one, which is the
// counter-intuitive half and the reason this test names it separately.
func TestStateDemoteIsReachableFromATenantStarToken(t *testing.T) {
	tiers, known := TiersOf(StateDemote)
	if !known {
		t.Fatal("state:demote is absent from the vocabulary — satisfies() fails closed on it, so " +
			"demoteAssertedPresence is refused to every caller including a tenant-admin holding \"*\"")
	}
	if !tiers.Has(TierTenant) {
		t.Fatalf("state:demote must be tenant-tier (it writes one tenant's own rows), got %v", tiers)
	}
	if tiers.Has(TierSystem) {
		t.Fatalf("state:demote must not also be system-tier: nothing about a demotion spans tenants, "+
			"and the GRANT side would then refuse it on the tenant role that needs it, got %v", tiers)
	}

	// The two credentials that must reach it: one naming it outright, and one holding
	// only the super-authority. The second is what the vocabulary entry buys.
	for _, c := range []struct {
		name   string
		claims *Claims
	}{
		{"a token naming state:demote", tenantAccessClaims(string(StateDemote))},
		{`a tenant-admin holding only "*"`, tenantAccessClaims(string(AuthorityAll))},
	} {
		if err := Authorize(ctxWith(c.claims), StateDemote); err != nil {
			t.Errorf("%s was refused state:demote: %v", c.name, err)
		}
	}

	// The counterweight, without which the admissions above are satisfied by folding
	// the demotion into the read authority beside it. state:read is in the read-only
	// baseline every enabled tenant member receives, so that fold would put a
	// fleet-wide presence write inside it.
	if err := Authorize(ctxWith(tenantAccessClaims(string(StateRead))), StateDemote); !errors.Is(err, ErrForbidden) {
		t.Fatalf("state:read satisfied state:demote — a fleet-wide presence write would sit inside the read-only baseline: %v", err)
	}
}
