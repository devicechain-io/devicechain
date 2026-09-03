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

// The viewer baseline is EXACTLY this list, and every change to it is a decision
// somebody made on purpose.
//
// 🔴 THIS IS THE ONLY TEST THAT CAN CATCH AN ADDITION, which is why it is worth the
// maintenance it costs. The other three constrain the list without bounding it:
// TestViewerAuthoritiesAreReadOnly accepts anything ending in ":read";
// TestViewerAuthoritiesAreAllTenantTier accepts any tenant-tier authority; and
// TestLocationReadIsNotInTheViewerBaseline names one specific absence. So an entry
// like secret:read would satisfy all three and silently join the set every enabled
// tenant member receives on top of their roles — the same shape as the credential and
// location defects those tests were written for, arriving by a route none of them
// watches.
//
// The expectation is spelled out as a LITERAL rather than derived from
// viewerAuthorities, because a fixture computed from the thing under test cannot catch
// that thing moving: it would agree with any list at all.
//
// Widening is not forbidden, it has to be deliberate. If you are here because this
// test failed, work out what the new authority lets every member of every tenant do,
// check whether anything elsewhere is relying on the baseline NOT containing it (the
// device-credential gate in device-management is the standing example), then edit the
// literal below and say why in the commit.
func TestViewerAuthoritiesAreExactlyTheseAuthorities(t *testing.T) {
	want := []string{
		"device:read",
		"event:read",
		"state:read",
		"command:read",
		"alarm:read",
		"dashboard:read",
	}

	// Written as strings rather than auth constants on purpose: this asserts the WIRE
	// values every access token carries, so renaming a constant while keeping its value
	// is invisible here, and changing a value is not.
	got := append([]string(nil), viewerAuthorities...)
	slices.Sort(got)
	sorted := append([]string(nil), want...)
	slices.Sort(sorted)

	if !slices.Equal(got, sorted) {
		t.Errorf("the viewer baseline is %v, want exactly %v.\n"+
			"Every enabled tenant member receives this list on top of their roles, and the "+
			"OAuth read-only ceiling is kept equal to it, so a change here changes what every "+
			"member and every read-only token can do. If the change is intended, update the "+
			"literal in this test and say why.", got, sorted)
	}
}

// A member with NO assigned role can open a dashboard. This is the outcome the
// baseline change exists for, asserted as an outcome rather than as a list.
//
// The two assertions above constrain the baseline's CONTENTS; this one asserts what
// the contents buy, on the path that actually issues authorities. Both matter,
// because a list can be correct while nothing reads it — the defect this fixes was
// exactly that shape from the other direction: dashboard-management gated three
// queries on an authority the grant path never handed out, so every affordance
// existed and every call was refused.
//
// 🔴 What this does NOT prove: it exercises effectiveAuthorities, which is the OAuth
// path. issueTenantTokens performs the same union for a console login, written out
// separately — so this covers one of the two callers, and the shared list is what
// keeps them equal.
func TestAMemberWithNoRoleCanOpenADashboard(t *testing.T) {
	member := effectiveAuthorities(nil, false)

	if !slices.Contains(member, string(auth.DashboardRead)) {
		t.Errorf("a tenant member with no assigned role holds %v, which does not include %q — "+
			"dashboard-management gates every dashboard read on it, so the console and the "+
			"/dash viewer would refuse an ordinary member with nothing to explain why",
			member, auth.DashboardRead)
	}

	// The counterweight: the baseline must not have become a way to hand out writes.
	// Without this, "add whatever makes the dashboard open" passes.
	if slices.Contains(member, string(auth.DashboardWrite)) {
		t.Errorf("the baseline grants %q; creating, publishing and deleting dashboards must "+
			"stay role-gated", auth.DashboardWrite)
	}
}

// The CONSOLE login path unions the baseline onto a member's token.
//
// 🔴 THIS COVERS THE CALLER THE OTHER TESTS DO NOT, AND A REVIEW SHOWED WHY THAT MATTERS:
// replacing the union in issueTenantTokens with a no-op left the entire user-management
// module green. Every console login and every /dash sign-in goes through that function,
// so a member would have carried NO baseline at all and nothing would have said so. The
// other tests in this file, and TestEffectiveAuthorities, exercise effectiveAuthorities —
// the OAuth path — which performs the same union in its own copy of the line.
//
// A shared list keeps the two callers' CONTENTS equal. It does not make either of them
// perform the union, and a comment claiming otherwise (this test replaced one) asserts an
// invariant nothing enforces. So this asserts the minted token itself, decoded, rather
// than an intermediate value.
func TestTheConsoleLoginPathUnionsTheViewerBaseline(t *testing.T) {
	e := newMintTestEnv(t)

	// A member with no roles and no authorities of their own: the population this whole
	// baseline exists for.
	pair, err := e.m.issueTenantTokens("acme", "someone@acme.example", nil, nil, false)
	if err != nil {
		t.Fatalf("issueTenantTokens: %v", err)
	}
	claims, err := e.validator.Validate(pair.AccessToken)
	if err != nil {
		t.Fatalf("the minted access token does not validate: %v", err)
	}

	for _, want := range viewerAuthorities {
		if !slices.Contains(claims.Authorities, want) {
			t.Errorf("a console login for a roleless member minted a token carrying %v, "+
				"which omits the baseline authority %q — the console path is not unioning "+
				"the viewer baseline", claims.Authorities, want)
		}
	}

	// And the counterweight, so "grant everything" would not satisfy this: the baseline
	// is a floor for a roleless member, not a licence.
	if slices.Contains(claims.Authorities, string(auth.AuthorityAll)) {
		t.Errorf("a roleless member's token carries the super-authority %q", auth.AuthorityAll)
	}
	for _, a := range claims.Authorities {
		if !strings.HasSuffix(a, ":read") {
			t.Errorf("a roleless member's token carries %q, which is not a read authority", a)
		}
	}
}
