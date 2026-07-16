// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"testing"
)

// accessClaims builds the credential a human carries while acting inside one
// tenant — the tier the whole partition exists to bound.
func tenantAccessClaims(authorities ...string) *Claims {
	return &Claims{TokenType: TokenTypeAccess, Tenant: "acme", Authorities: authorities}
}

func identityTierClaims(authorities ...string) *Claims {
	return &Claims{TokenType: TokenTypeIdentity, Authorities: authorities}
}

func serviceTierClaims(authorities ...string) *Claims {
	return &Claims{TokenType: TokenTypeService, Authorities: authorities}
}

func ctxWith(c *Claims) context.Context { return WithClaims(context.Background(), c) }

// TestTenantAdminStarCannotReachAiAdmin is THE regression test for ADR-065's
// origin story. The seeded `tenant-admin` role is TENANT-scoped and grants "*"
// (iam.SeedSuperuser), and HasAuthority passes "*" for every check — so before the
// tier partition, any member holding tenant-admin passed an ai:admin check, and an
// instance-scoped operator resource was reachable from the tenant console.
//
// The move of the provider screen to the admin plane fixes today's instance of
// this. This test fixes the CLASS: it must stay impossible to satisfy a system-tier
// authority from a tenant access token even if someone later hangs one off a
// data-plane resolver again.
func TestTenantAdminStarCannotReachAiAdmin(t *testing.T) {
	ctx := ctxWith(tenantAccessClaims(string(AuthorityAll)))
	if err := Authorize(ctx, AIAdmin); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a tenant-admin's \"*\" access token satisfied the system-tier ai:admin: %v", err)
	}
	// The same "*" still grants everything it legitimately should within the tenant.
	if err := Authorize(ctx, DeviceWrite); err != nil {
		t.Fatalf("\"*\" must still grant every tenant-tier authority: %v", err)
	}
}

// TestSudoSuperuserCannotReachSystemTierFromATenant covers the other half of the
// original bug: a superuser breaking glass into a tenant carries "*" on a TENANT
// ACCESS token (identity.issueTenantTokens, sudo). They are still a superuser —
// they simply have to use the admin plane, where their identity token carries the
// same authority, rather than reaching instance config from inside a tenant.
func TestSudoSuperuserCannotReachSystemTierFromATenant(t *testing.T) {
	sudo := &Claims{TokenType: TokenTypeAccess, Tenant: "acme", ActingAsSuperuser: true, Authorities: []string{string(AuthorityAll)}}
	for _, a := range []Authority{AIAdmin, TenantWrite, SettingsWrite, ClientWrite, RoleWrite, UserWrite} {
		if err := Authorize(ctxWith(sudo), a); !errors.Is(err, ErrForbidden) {
			t.Fatalf("a sudo access token satisfied system-tier %q: %v", a, err)
		}
	}
	// ...and the same person on the admin plane is unaffected.
	for _, a := range []Authority{AIAdmin, TenantWrite, SettingsWrite} {
		if err := Authorize(ctxWith(identityTierClaims(string(AuthorityAll))), a); err != nil {
			t.Fatalf("an identity token must satisfy system-tier %q: %v", a, err)
		}
	}
}

// TestServiceTokensAreNotBoundByTheTenantRule pins the exemption the platform
// depends on. event-sources, event-processing, outbound-connectors and
// ai-inference each mint a service token holding ONLY tenant:read and call
// user-management's data-plane tenantGovernance with it. tenant:read is
// system-tier, so binding service tokens to the tenant rule would break every
// governance ceiling fetch on the platform — a fail-safe-to-platform-default
// cascade collapse (ADR-023) that no test in those services would catch, since
// they stub the fetch.
func TestServiceTokensAreNotBoundByTheTenantRule(t *testing.T) {
	if err := Authorize(ctxWith(serviceTierClaims(string(TenantRead))), TenantRead); err != nil {
		t.Fatalf("a service token must satisfy system-tier tenant:read (the governance fetch): %v", err)
	}
	// The exemption is not a blank cheque: a service token still needs the authority.
	if err := Authorize(ctxWith(serviceTierClaims(string(TenantRead))), TenantWrite); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a service token must not satisfy an authority it was not minted with: %v", err)
	}
}

// TestAuthorizeRejectsSystemTierOnAccessTokenEvenWhenNamed guards the narrower
// case: an access token that names the system authority outright (rather than via
// "*") is refused just the same. Otherwise the write-time role guard would be the
// only thing standing between a hand-minted token and the surface.
func TestAuthorizeRejectsSystemTierOnAccessTokenEvenWhenNamed(t *testing.T) {
	ctx := ctxWith(tenantAccessClaims(string(AIAdmin), string(DeviceRead)))
	if err := Authorize(ctx, AIAdmin); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an access token naming ai:admin satisfied it: %v", err)
	}
	if err := Authorize(ctx, DeviceRead); err != nil {
		t.Fatalf("the same token must still satisfy its tenant-tier authority: %v", err)
	}
}

// TestAuthorizeFailsClosedOnAnUntieredAuthority pins the fail-closed default. An
// authority missing from the vocabulary has no tier, so it cannot be shown safe on
// an access token and must be refused there — the alternative (treat unknown as
// tenant-tier) would make forgetting a vocabulary entry silently grant a new
// operator capability to every tenant-admin holding "*".
func TestAuthorizeFailsClosedOnAnUntieredAuthority(t *testing.T) {
	const ghost Authority = "ghost:write"
	if _, known := TiersOf(ghost); known {
		t.Fatal("precondition: ghost:write must not be in the vocabulary")
	}
	if err := Authorize(ctxWith(tenantAccessClaims(string(AuthorityAll))), ghost); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an untiered authority must fail closed on an access token: %v", err)
	}
}

// TestAuthorizeAnyAppliesTheTier makes sure the second entry point is not a way
// around the first. AuthorizeAny is used where a resolver accepts either of two
// authorities; a tier check on Authorize alone would leave it open.
func TestAuthorizeAnyAppliesTheTier(t *testing.T) {
	ctx := ctxWith(tenantAccessClaims(string(AuthorityAll)))
	if err := AuthorizeAny(ctx, AIAdmin, SettingsWrite); !errors.Is(err, ErrForbidden) {
		t.Fatalf("AuthorizeAny satisfied a system-tier authority from an access token: %v", err)
	}
	if err := AuthorizeAny(ctx, AIAdmin, DeviceRead); err != nil {
		t.Fatalf("AuthorizeAny must still pass on a tenant-tier member of the set: %v", err)
	}
}

// TestEveryAuthorityDeclaresATier fails loud when a new authority is added without
// a tier. The cost of forgetting is not cosmetic: TierOf reports unknown, Authorize
// fails closed, and the new capability is unreachable from the data plane — a
// confusing outage rather than a leak, but an outage. Naming the vocabulary
// independently here (rather than iterating it) is deliberate: a test that derives
// its expectation from the list it checks passes by construction (the ADR-065 S2
// lesson).
func TestEveryAuthorityDeclaresATier(t *testing.T) {
	expected := []Authority{
		UserRead, UserWrite, RoleRead, RoleWrite, TenantRead, TenantWrite,
		DeviceRead, DeviceWrite, EventRead, StateRead, AlarmRead, AlarmWrite,
		CommandRead, CommandWrite, DashboardRead, DashboardWrite,
		NotificationRead, NotificationWrite, AuditRead, SettingsRead, SettingsWrite,
		ClientRead, ClientWrite, ConnectorRead, ConnectorWrite, BrandingWrite,
		AIAdmin, AIInfer,
	}
	for _, a := range expected {
		tiers, known := TiersOf(a)
		if !known {
			t.Errorf("authority %q declares no tier", a)
			continue
		}
		if len(tiers) == 0 {
			t.Errorf("authority %q declares an empty tier set — it would be unreachable", a)
		}
		for _, tier := range tiers {
			if tier != TierSystem && tier != TierTenant {
				t.Errorf("authority %q declares an unknown tier %q", a, tier)
			}
		}
	}
	if len(expected) != len(vocabulary) {
		t.Errorf("the vocabulary has %d authorities but this test names %d — a new authority must be tiered AND named here",
			len(vocabulary), len(expected))
	}
	// "*" is deliberately untiered: it means "everything at the bearer's tier".
	if _, known := TiersOf(AuthorityAll); known {
		t.Error(`"*" must not declare a tier of its own`)
	}
	if !ValidAuthority(string(AuthorityAll)) {
		t.Error(`"*" must remain a valid authority a role can grant`)
	}
}

// TestTheOperatorSurfacesAreSystemTier names the instance-global capabilities
// independently of the map, so demoting one to tenant-tier — which would re-open
// exactly the hole ADR-065 was written to close — fails here rather than silently
// widening who can reach an operator surface.
func TestTheOperatorSurfacesAreSystemTier(t *testing.T) {
	for _, a := range []Authority{
		AIAdmin, TenantRead, TenantWrite, UserRead, UserWrite,
		RoleRead, RoleWrite, SettingsRead, SettingsWrite, ClientRead, ClientWrite,
	} {
		tiers, _ := TiersOf(a)
		if !tiers.Has(TierSystem) {
			t.Errorf("authority %q must be system-tier, got %v", a, tiers)
		}
		// An operator surface must be system-tier ONLY. Listing tenant as well would
		// make it satisfiable from a tenant access token, quietly undoing the whole
		// partition for that authority.
		if tiers.Has(TierTenant) {
			t.Errorf("authority %q is an instance-global operator surface and must not "+
				"also be tenant-tier — that would let a tenant access token satisfy it", a)
		}
	}
}

// TestAuditReadIsTheOnlyDualTierAuthority pins the model's one deliberate exception,
// and pins that it stays the only one.
//
// audit:read must be BOTH: device-management's data plane gates a tenant reading its
// OWN journal on it, and user-management's ADMIN plane gates an operator reading the
// INSTANCE journal on it. An identity token carries only system-role authorities, so
// a tenant-only audit:read would leave "an operator who may read the audit journal"
// unexpressible — the grant side refuses it on a system role — and any existing
// system role holding it uneditable, since UpdateRole revalidates the whole
// replacement set.
//
// Dual-tier is the weakest thing in this model (it is satisfiable from an access
// token), so this test names the whole set rather than just asserting audit:read is
// in it: a second dual-tier authority must be an argued decision, not a slip.
func TestAuditReadIsTheOnlyDualTierAuthority(t *testing.T) {
	var dual []Authority
	for a, tiers := range vocabulary {
		if tiers.Has(TierSystem) && tiers.Has(TierTenant) {
			dual = append(dual, a)
		}
	}
	if len(dual) != 1 || dual[0] != AuditRead {
		t.Fatalf("audit:read must be the only dual-tier authority; got %v. A new one is a "+
			"real decision: a dual-tier authority is satisfiable from a tenant access token, "+
			"so it must never gate anything instance-global.", dual)
	}
}

// TestAuditReadWorksOnBothPlanes states the consequence directly, at the check.
func TestAuditReadWorksOnBothPlanes(t *testing.T) {
	// A tenant member reads its own journal (device-management's data plane).
	if err := Authorize(ctxWith(tenantAccessClaims(string(AuditRead))), AuditRead); err != nil {
		t.Fatalf("a tenant must be able to read its own audit journal: %v", err)
	}
	// An operator reads the instance journal (user-management's admin plane).
	if err := Authorize(ctxWith(identityTierClaims(string(AuditRead))), AuditRead); err != nil {
		t.Fatalf("an operator must be able to read the instance audit journal: %v", err)
	}
}

// TestAuthoritiesForScopeOffersOnlyGrantableAuthorities backs the role editor: the
// checklist it renders must not offer an authority the write path will reject.
func TestAuthoritiesForScopeOffersOnlyGrantableAuthorities(t *testing.T) {
	tenantList := AuthoritiesForScope(TierTenant)
	if contains(tenantList, string(AIAdmin)) {
		t.Error("the tenant-scope checklist must not offer ai:admin")
	}
	if !contains(tenantList, string(DeviceWrite)) {
		t.Error("the tenant-scope checklist must offer device:write")
	}
	systemList := AuthoritiesForScope(TierSystem)
	if !contains(systemList, string(AIAdmin)) {
		t.Error("the system-scope checklist must offer ai:admin")
	}
	if contains(systemList, string(DeviceWrite)) {
		t.Error("the system-scope checklist must not offer device:write")
	}
	// "*" is valid at either tier — it expands to whatever the bearer's tier allows.
	for _, list := range [][]string{tenantList, systemList} {
		if !contains(list, string(AuthorityAll)) {
			t.Error(`every scope's checklist must offer "*"`)
		}
	}
	// Together the two scopes cover the whole vocabulary: every authority appears in
	// at least one, "*" in both, and a dual-tier authority in both.
	var dual int
	for _, tiers := range vocabulary {
		if len(tiers) > 1 {
			dual++
		}
	}
	if len(tenantList)+len(systemList) != len(vocabulary)+2+dual {
		t.Errorf("the scoped checklists (%d + %d) do not cover the vocabulary (%d, of which %d dual-tier)",
			len(tenantList), len(systemList), len(vocabulary), dual)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
