// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package admin

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/iam"
)

// TestTenantScopedRoleCannotGrantAnOperatorAuthority is the write-time half of the
// ADR-065 correction. A role's scope and its authorities used to be unrelated, so
// an operator could grant ai:admin — an instance-global capability — to a
// TENANT-scoped role, which is the shape that put the AI provider screen in the
// tenant console.
func TestTenantScopedRoleCannotGrantAnOperatorAuthority(t *testing.T) {
	for _, a := range []auth.Authority{
		auth.AIAdmin, auth.TenantWrite, auth.TenantRead, auth.SettingsWrite,
		auth.ClientWrite, auth.UserWrite, auth.RoleWrite,
	} {
		err := validateAuthorities(iam.ScopeTenant, []string{string(a)})
		if err == nil {
			t.Errorf("a tenant-scoped role was allowed to grant system-tier %q", a)
			continue
		}
		// The error must name the offender and the way out, not just say "no": an
		// operator hitting this needs to know which authority is wrong and what they
		// could have picked instead.
		if !strings.Contains(err.Error(), string(a)) {
			t.Errorf("the refusal for %q does not name it: %v", a, err)
		}
		if !strings.Contains(err.Error(), string(auth.DeviceRead)) {
			t.Errorf("the refusal for %q does not name the valid tenant-scope alternatives: %v", a, err)
		}
	}
}

// TestSystemScopedRoleCannotGrantATenantAuthority is the mirror. It matters less
// (a system role holding device:read grants nothing dangerous) but an operator who
// builds one has misunderstood the model, and the role would silently do nothing
// on the admin plane it is assignable from.
func TestSystemScopedRoleCannotGrantATenantAuthority(t *testing.T) {
	// audit:read is deliberately absent: it is dual-tier (see TestAuditReadIsGrantableAtEitherScope).
	for _, a := range []auth.Authority{auth.DeviceWrite, auth.DashboardRead, auth.BrandingWrite, auth.AIInfer} {
		if err := validateAuthorities(iam.ScopeSystem, []string{string(a)}); err == nil {
			t.Errorf("a system-scoped role was allowed to grant tenant-tier %q", a)
		}
	}
}

// TestEachScopeGrantsItsOwnTier confirms the guard is a partition and not a blanket
// refusal — the failure mode where "reject everything" passes both tests above.
func TestEachScopeGrantsItsOwnTier(t *testing.T) {
	if err := validateAuthorities(iam.ScopeTenant, []string{
		string(auth.DeviceWrite), string(auth.AlarmWrite), string(auth.ConnectorRead),
	}); err != nil {
		t.Errorf("a tenant-scoped role must grant tenant-tier authorities: %v", err)
	}
	if err := validateAuthorities(iam.ScopeSystem, []string{
		string(auth.AIAdmin), string(auth.TenantRead), string(auth.SettingsWrite),
	}); err != nil {
		t.Errorf("a system-scoped role must grant system-tier authorities: %v", err)
	}
}

// TestStarIsGrantableAtEitherScope pins the deliberate exemption. Both seeded roles
// depend on it: the system `superuser` and the tenant `tenant-admin` are each
// granted "*". Refusing it at tenant scope would break the seed on a fresh
// instance; the check-time tier rule is what makes it safe, by bounding "*" to
// whatever the bearer's own tier allows.
func TestStarIsGrantableAtEitherScope(t *testing.T) {
	for _, scope := range []iam.RoleScope{iam.ScopeSystem, iam.ScopeTenant} {
		if err := validateAuthorities(scope, []string{string(auth.AuthorityAll)}); err != nil {
			t.Errorf(`"*" must be grantable at %s scope (both seeded roles use it): %v`, scope, err)
		}
	}
}

// TestUnknownAuthorityStillRejected confirms the tier check did not displace the
// typo guard that was already there.
func TestUnknownAuthorityStillRejected(t *testing.T) {
	err := validateAuthorities(iam.ScopeTenant, []string{"device:reed"})
	if err == nil || !strings.Contains(err.Error(), "unknown authority") {
		t.Errorf("a typo must still be rejected as an unknown authority: %v", err)
	}
}

// TestOneBadAuthorityRejectsTheWholeRole guards against a partial write: the role's
// authorities are replaced wholesale, so validation must refuse the request rather
// than quietly dropping the offender and saving the rest.
func TestOneBadAuthorityRejectsTheWholeRole(t *testing.T) {
	err := validateAuthorities(iam.ScopeTenant, []string{
		string(auth.DeviceRead), string(auth.AIAdmin), string(auth.EventRead),
	})
	if err == nil {
		t.Fatal("a role mixing valid tenant authorities with ai:admin was accepted")
	}
}

// TestAuditReadIsGrantableAtEitherScope pins the model's one dual-tier authority at
// the GRANT side — the side the check-side reasoning missed.
//
// audit:read gates a tenant reading its OWN journal (device-management's data plane)
// AND an operator reading the INSTANCE journal (user-management's admin plane, at
// admin_audit.go). An identity token carries only system-role authorities, so
// refusing audit:read on a system role would make "an operator who may read the
// audit journal" unexpressible — leaving only the superuser's "*" — and would make
// any EXISTING system role holding it uneditable, since UpdateRole revalidates the
// whole replacement authority set (so even renaming the role would hard-fail).
func TestAuditReadIsGrantableAtEitherScope(t *testing.T) {
	for _, scope := range []iam.RoleScope{iam.ScopeSystem, iam.ScopeTenant} {
		if err := validateAuthorities(scope, []string{string(auth.AuditRead)}); err != nil {
			t.Errorf("audit:read must be grantable at %s scope — both planes gate on it: %v", scope, err)
		}
	}
	// And it is offered in both checklists, so the console cannot hide it from the
	// scope that needs it.
	for _, list := range [][]string{
		auth.AuthoritiesForScope(auth.TierSystem),
		auth.AuthoritiesForScope(auth.TierTenant),
	} {
		found := false
		for _, a := range list {
			if a == string(auth.AuditRead) {
				found = true
			}
		}
		if !found {
			t.Error("audit:read must appear in both scopes' authority checklists")
		}
	}
}
