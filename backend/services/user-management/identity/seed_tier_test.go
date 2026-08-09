// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"slices"
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

// The viewer baseline grants read authorities only.
//
// This is the invariant a security property in ANOTHER service leans on.
// device-management gates the three queries returning a DeviceCredential on
// device:write rather than device:read, because for an ACCESS_TOKEN the readable
// credentialId IS the bearer the device authenticates with — so whoever may read
// a credential can impersonate that device. That gate is only meaningful while
// device:write stays out of the baseline every enabled tenant member receives.
//
// Nothing else would notice the regression. Adding a write authority here does
// not break any test in this package: the baseline is a plain list, unioned onto
// every access token, and device-management's own tests spell the baseline out
// as a literal rather than importing it across the module boundary. The failure
// would surface as a read-only user quietly acquiring write capability.
//
// Widening the baseline is not forbidden — it has to be a decision. If an entry
// here legitimately does not end in ":read", change this test deliberately and
// re-check what elsewhere assumed the baseline was harmless.
func TestViewerAuthoritiesAreReadOnly(t *testing.T) {
	if len(viewerAuthorities) == 0 {
		t.Fatal("precondition: the viewer baseline is empty")
	}
	for _, a := range viewerAuthorities {
		if a == string(auth.AuthorityAll) {
			t.Errorf("viewer grants the super-authority %q — the read-only baseline would grant everything", a)
			continue
		}
		if !strings.HasSuffix(a, ":read") {
			t.Errorf("viewer grants %q, which is not a read authority; the baseline every enabled "+
				"tenant member receives must stay read-only (device-management gates credential "+
				"reads on device:write on the strength of that)", a)
		}
	}
}

// location:read is deliberately NOT in the viewer baseline, and that absence is the
// whole reason the authority exists.
//
// 🔴 This needs its own test because TestViewerAuthoritiesAreReadOnly above CANNOT
// catch it. That test asserts every entry ends in ":read" — and "location:read"
// does. It constrains what may be in the baseline, never what must be absent from
// it, so adding location:read here would satisfy it perfectly while dissolving the
// separation it was introduced to create.
//
// The separation is the point. Every event read is otherwise gated on a single
// event:read, which would give a device's POSITION the same permission as its
// temperature. Knowing where a vehicle — or a person — is differs in kind, and it
// is the first question a data-protection review asks. Granting it to every enabled
// member by default would mean nobody had ever chosen to grant it.
//
// This is the same shape as the credential defect the test above guards: a
// "read-only" baseline silently conferring a capability no one selected. It was
// found there by audit rather than by test, which is why it is asserted here.
//
// Widening the baseline is not forbidden — it has to be a DECISION. If location
// should become universally readable, delete this test on purpose and say why.
func TestLocationReadIsNotInTheViewerBaseline(t *testing.T) {
	if len(viewerAuthorities) == 0 {
		t.Fatal("precondition: the viewer baseline is empty")
	}
	// The precondition that makes the assertion meaningful: the baseline really is
	// the thing that grants reads, so an absence from it is a real restriction
	// rather than a vacuous one.
	if !slices.Contains(viewerAuthorities, string(auth.EventRead)) {
		t.Fatal("precondition: the baseline no longer grants event:read, so this test is " +
			"no longer comparing location against the telemetry it is meant to differ from")
	}
	if slices.Contains(viewerAuthorities, string(auth.LocationRead)) {
		t.Fatalf("the viewer baseline grants %q. Every enabled tenant member receives this "+
			"list on top of their roles, so granting it here makes the separation from "+
			"event:read ceremonial — a device's position becomes readable by everyone who "+
			"can read its temperature, which is exactly what the authority exists to prevent",
			auth.LocationRead)
	}
}
