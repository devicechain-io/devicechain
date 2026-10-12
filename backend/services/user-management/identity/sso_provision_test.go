// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Just-in-time provisioning, group-to-role mapping and the recompute of managed
// memberships, driven through the whole sign-in: the stub provider, the real callback
// handler and completeSso. Nothing here calls the recompute directly.

// tenantRole ensures a tenant role exists.
func (e *ssoEnv) tenantRole(t *testing.T, token string, authorities ...string) {
	t.Helper()
	require.NoError(t, e.store.EnsureRole(context.Background(), iam.ScopeTenant, token, token, authorities))
}

// idpGroups makes the provider put these values in the groups claim of every token.
func (e *ssoEnv) idpGroups(groups ...string) {
	vals := make([]any, 0, len(groups))
	for _, g := range groups {
		vals = append(vals, g)
	}
	e.op.mutate = func(c jwt.MapClaims) { c["groups"] = vals }
}

func (e *ssoEnv) mapping(t *testing.T, conn, tenant string, pairs ...string) {
	t.Helper()
	var in []RoleMappingInput
	for i := 0; i+1 < len(pairs); i += 2 {
		in = append(in, RoleMappingInput{Group: pairs[i], Role: pairs[i+1]})
	}
	_, err := e.m.SetIdpRoleMappings(context.Background(), conn, tenant, in, false)
	require.NoError(t, err)
}

func (e *ssoEnv) jit(t *testing.T, conn string, enabled bool, defaults ...string) {
	t.Helper()
	_, err := e.m.SetIdpJit(context.Background(), conn, enabled, defaults, false)
	require.NoError(t, err)
}

// membership returns the identity's membership in tenant, or nil.
func (e *ssoEnv) membership(t *testing.T, email, tenant string) *iam.Membership {
	t.Helper()
	id, err := e.store.IdentityByEmail(context.Background(), email)
	require.NoError(t, err)
	mem, err := e.store.MembershipByIdentityTenant(context.Background(), id.ID, tenant)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	require.NoError(t, err)
	return mem
}

func roleTokensOf(mem *iam.Membership) []string {
	out := []string{}
	for _, r := range mem.TenantRoles {
		out = append(out, r.Token)
	}
	sort.Strings(out)
	return out
}

const newcomer = "nia@" + ssoTestDomain

func TestJitCreatesIdentityWithEmptyPassword(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.tenantRole(t, "viewer", "device:read")
	e.tenantRole(t, "operator", "device:read", "device:write")
	e.jit(t, "acme-idp", true, "viewer")
	e.mapping(t, "acme-idp", "acme", "ops", "operator")
	e.idpGroups("ops", "unrelated")

	got, _ := e.signInOK(t, "acme-idp", "sub-nia", newcomer)

	id, err := e.store.IdentityByEmail(ctx, newcomer)
	require.NoError(t, err)
	require.Empty(t, id.PasswordHash, "an SSO-only identity has no password")
	require.True(t, id.Enabled)
	require.Empty(t, id.SystemRoles, "provisioning grants no system role")
	linked, err := e.store.IdentityLinkedTo(ctx, id.ID, e.reloadConn(t, "acme-idp").ID)
	require.NoError(t, err)
	require.True(t, linked)

	mem := e.membership(t, newcomer, "acme")
	require.NotNil(t, mem)
	require.True(t, mem.Enabled)
	require.NotNil(t, mem.ManagedBy)
	require.Equal(t, e.reloadConn(t, "acme-idp").ID, *mem.ManagedBy)
	require.Equal(t, []string{"operator", "viewer"}, roleTokensOf(mem), "defaults plus the mapped group's role")
	require.Len(t, got.Memberships, 1)
	require.Equal(t, "acme", got.Memberships[0].Tenant)
	require.EqualValues(t, 1, e.auditCount(t, rdb.AuditOpIdentityProvisioned))

	// The second sign-in provisions nothing new.
	e.signInOK(t, "acme-idp", "sub-nia", newcomer)
	require.EqualValues(t, 1, e.auditCount(t, rdb.AuditOpIdentityProvisioned))
}

func TestPasswordLoginForJitIdentityFailsUniformly(t *testing.T) {
	e := newSsoEnv(t)
	e.jit(t, "acme-idp", true)
	e.signInOK(t, "acme-idp", "sub-nia", newcomer)

	ctx := context.Background()
	_, errEmpty := e.m.Login(ctx, newcomer, "")
	_, errGuess := e.m.Login(ctx, newcomer, "password")
	_, errGhost := e.m.Login(ctx, "nobody@"+ssoTestDomain, "password")
	require.Error(t, errEmpty)
	require.Equal(t, errGhost.Error(), errEmpty.Error(), "an empty password is refused like any wrong one")
	require.Equal(t, errGhost.Error(), errGuess.Error(), "the provisioned account is indistinguishable from none")
}

func TestJitNeverForInstanceConnection(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.addConnection(t, "ops-idp", iam.IdpKindInstance, "ops-corp.com")

	// The administrator's call refuses to turn it on...
	_, err := e.m.SetIdpJit(ctx, "ops-idp", true, nil, false)
	require.ErrorIs(t, err, ErrIdpInvalid)
	_, err = e.m.SetIdpJit(ctx, "ops-idp", false, []string{"viewer"}, false)
	require.ErrorIs(t, err, ErrIdpInvalid)

	// ...and a row forced on (as only a direct write could) still provisions nobody.
	conn := e.reloadConn(t, "ops-idp")
	conn.JitEnabled = true
	require.NoError(t, e.store.UpdateIdpConnection(ctx, conn))
	l, _, _, _ := e.signIn(t, "ops-idp", "sub-eve", "eve@ops-corp.com")
	e.refused(t, l, "ops-idp", SsoOutcomeRefusedLinkRequired)
	_, err = e.store.IdentityByEmail(ctx, "eve@ops-corp.com")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "an instance connection never creates an identity")
}

func TestJitRefusedWhenEmailExists(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.tenantRole(t, "viewer", "device:read")
	e.jit(t, "acme-idp", true, "viewer")
	// An identity with the address exists, with a manual membership, and no link.
	existing := e.ssoIdentity(t, newcomer, "", "", "acme")

	l, _, _, _ := e.signIn(t, "acme-idp", "sub-other", newcomer)
	e.refused(t, l, "acme-idp", SsoOutcomeRefusedLinkRequired)

	linked, err := e.store.LinksForIdentity(ctx, existing.ID)
	require.NoError(t, err)
	require.Empty(t, linked, "a collision is a request, never a link")
	reqs, err := e.store.ListSsoLinkRequests(ctx)
	require.NoError(t, err)
	require.Len(t, reqs, 1)
	require.Equal(t, "sub-other", reqs[0].Subject)
	mem := e.membership(t, newcomer, "acme")
	require.Nil(t, mem.ManagedBy, "the manual membership is untouched")
	require.Empty(t, roleTokensOf(mem))
}

func TestJitOffLeavesALinkRequest(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	l, _, _, _ := e.signIn(t, "acme-idp", "sub-nia", newcomer)
	e.refused(t, l, "acme-idp", SsoOutcomeRefusedLinkRequired)
	_, err := e.store.IdentityByEmail(ctx, newcomer)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "without JIT nothing is created")
}

func TestMappingReplacesOnlyManagedMemberships(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.addTenant(t, "beta")
	e.addTenant(t, "gamma")
	_, err := e.m.SetIdpConnectionTenants(ctx, "acme-idp", []string{"acme", "beta", "gamma"})
	require.NoError(t, err)
	e.tenantRole(t, "viewer", "device:read")
	e.tenantRole(t, "operator", "device:write")
	e.tenantRole(t, "keeper", "device:delete")
	e.jit(t, "acme-idp", true)
	e.mapping(t, "acme-idp", "acme", "ops", "operator")
	e.mapping(t, "acme-idp", "beta", "ops", "operator")
	e.mapping(t, "acme-idp", "gamma", "ops", "operator")

	// First sign-in provisions managed memberships everywhere.
	e.idpGroups("ops")
	e.signInOK(t, "acme-idp", "sub-nia", newcomer)
	connID := e.reloadConn(t, "acme-idp").ID
	for _, tn := range []string{"acme", "beta", "gamma"} {
		mem := e.membership(t, newcomer, tn)
		require.NotNil(t, mem.ManagedBy, tn)
		require.Equal(t, []string{"operator"}, roleTokensOf(mem), tn)
	}

	// An administrator turns beta into a manual grant with her own role, and another
	// connection claims gamma.
	betaMem := e.membership(t, newcomer, "beta")
	require.NoError(t, e.sysDB().Model(betaMem).Update("managed_by", nil).Error)
	_, err = e.admin.SetMembershipRoles(ctx, newcomer, "beta", []string{"keeper"})
	require.NoError(t, err)
	other := e.addConnection(t, "other-idp", iam.IdpKindTenant, "other-corp.com", "gamma")
	gammaMem := e.membership(t, newcomer, "gamma")
	require.NoError(t, e.sysDB().Model(gammaMem).Update("managed_by", other.ID).Error)

	// The groups change; the next sign-in recomputes ONLY what this connection manages.
	e.mapping(t, "acme-idp", "acme", "ops", "operator", "eng", "viewer")
	e.idpGroups("eng")
	e.signInOK(t, "acme-idp", "sub-nia", newcomer)

	acme := e.membership(t, newcomer, "acme")
	require.Equal(t, connID, *acme.ManagedBy)
	require.Equal(t, []string{"viewer"}, roleTokensOf(acme), "the managed membership follows the groups")
	require.True(t, acme.Enabled)

	beta := e.membership(t, newcomer, "beta")
	require.Nil(t, beta.ManagedBy, "a manual membership stays manual")
	require.Equal(t, []string{"keeper"}, roleTokensOf(beta), "and keeps the administrator's roles")
	require.True(t, beta.Enabled)

	gamma := e.membership(t, newcomer, "gamma")
	require.Equal(t, other.ID, *gamma.ManagedBy, "another connection's membership is its own")
	require.Equal(t, []string{"operator"}, roleTokensOf(gamma))
}

func TestEmptyMappingDisablesNotDeletes(t *testing.T) {
	e := newSsoEnv(t)
	e.tenantRole(t, "operator", "device:write")
	e.jit(t, "acme-idp", true)
	e.mapping(t, "acme-idp", "acme", "ops", "operator")

	e.idpGroups("ops")
	e.signInOK(t, "acme-idp", "sub-nia", newcomer)
	require.True(t, e.membership(t, newcomer, "acme").Enabled)

	// The person leaves the group at the provider.
	e.idpGroups("something-else")
	got, _ := e.signInOK(t, "acme-idp", "sub-nia", newcomer)
	mem := e.membership(t, newcomer, "acme")
	require.NotNil(t, mem, "the membership is not deleted")
	require.False(t, mem.Enabled, "it is disabled")
	require.Empty(t, roleTokensOf(mem), "and holds nothing")
	require.NotNil(t, mem.ManagedBy)
	_, err := e.m.SelectTenant(context.Background(), got.IdentityToken, "acme")
	require.Error(t, err, "a disabled membership cannot be entered")

	// A token with no groups claim at all is the same as no groups.
	e.op.mutate = nil
	e.signInOK(t, "acme-idp", "sub-nia", newcomer)
	require.False(t, e.membership(t, newcomer, "acme").Enabled)

	// Back in the group: enabled again with the role.
	e.idpGroups("ops")
	e.signInOK(t, "acme-idp", "sub-nia", newcomer)
	mem = e.membership(t, newcomer, "acme")
	require.True(t, mem.Enabled)
	require.Equal(t, []string{"operator"}, roleTokensOf(mem))
}

func TestGroupsOverageFailsLoudly(t *testing.T) {
	for name, mutate := range map[string]func(c jwt.MapClaims){
		"hasgroups": func(c jwt.MapClaims) { c["hasgroups"] = true },
		"claim_names": func(c jwt.MapClaims) {
			c["_claim_names"] = map[string]any{"groups": "src1"}
			c["_claim_sources"] = map[string]any{"src1": map[string]any{"endpoint": "https://graph.example/x"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newSsoEnv(t)
			e.tenantRole(t, "operator", "device:write")
			e.jit(t, "acme-idp", true)
			e.mapping(t, "acme-idp", "acme", "ops", "operator")

			// A member whose groups were fine at the last sign-in...
			e.idpGroups("ops")
			e.signInOK(t, "acme-idp", "sub-nia", newcomer)

			// ...must not have them read as "none" when the token cannot list them.
			e.op.mutate = mutate
			l, _, _, _ := e.signIn(t, "acme-idp", "sub-nia", newcomer)
			e.refused(t, l, "acme-idp", SsoOutcomeRefusedGroupsOverage)
			mem := e.membership(t, newcomer, "acme")
			require.True(t, mem.Enabled, "the refused sign-in changed nothing")
			require.Equal(t, []string{"operator"}, roleTokensOf(mem))
			require.Equal(t, "groups_overage", l.Error)

			// And a NEW person with an overaged token is refused before anything is created.
			l, _, _, _ = e.signIn(t, "acme-idp", "sub-new", "new@"+ssoTestDomain)
			e.refused(t, l, "acme-idp", SsoOutcomeRefusedGroupsOverage)
			_, err := e.store.IdentityByEmail(context.Background(), "new@"+ssoTestDomain)
			require.ErrorIs(t, err, gorm.ErrRecordNotFound)
		})
	}
}

func TestGroupsOverageDoesNotMatterWhereGroupsAreNotUsed(t *testing.T) {
	e := newSsoEnv(t)
	e.jit(t, "acme-idp", true)
	// No mapping names a group, so the sign-in does not depend on the list.
	e.op.mutate = func(c jwt.MapClaims) { c["hasgroups"] = true }
	e.signInOK(t, "acme-idp", "sub-nia", newcomer)
}

func TestMalformedGroupsClaimIsRefused(t *testing.T) {
	e := newSsoEnv(t)
	e.tenantRole(t, "operator", "device:write")
	e.jit(t, "acme-idp", true)
	e.mapping(t, "acme-idp", "acme", "ops", "operator")
	e.op.mutate = func(c jwt.MapClaims) { c["groups"] = map[string]any{"ops": true} }
	l, _, _, _ := e.signIn(t, "acme-idp", "sub-nia", newcomer)
	e.refused(t, l, "acme-idp", SsoOutcomeRefusedGroupsClaim)
	e.op.mutate = func(c jwt.MapClaims) { c["groups"] = []any{"ops", 7} }
	l, _, _, _ = e.signIn(t, "acme-idp", "sub-nia", newcomer)
	e.refused(t, l, "acme-idp", SsoOutcomeRefusedGroupsClaim)
}

func TestFullAuthorityMappingRequiresAck(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.tenantRole(t, "tenant-owner", "*")

	_, err := e.m.SetIdpRoleMappings(ctx, "acme-idp", "acme", []RoleMappingInput{{Group: "owners", Role: "tenant-owner"}}, false)
	require.ErrorIs(t, err, ErrFullAuthorityNotAcknowledged)
	require.Empty(t, e.mappingRows(t), "nothing was written")

	d, err := e.m.SetIdpRoleMappings(ctx, "acme-idp", "acme", []RoleMappingInput{{Group: "owners", Role: "tenant-owner"}}, true)
	require.NoError(t, err)
	require.Len(t, d.RoleMappings, 1)
	require.True(t, d.RoleMappings[0].FullAuthority)
	require.EqualValues(t, 1, e.auditCount(t, rdb.AuditOpRoleMappingChanged), "the acknowledged change is audited")

	// The same gate guards the default roles, which would otherwise be the way round it.
	_, err = e.m.SetIdpJit(ctx, "acme-idp", true, []string{"tenant-owner"}, false)
	require.ErrorIs(t, err, ErrFullAuthorityNotAcknowledged)
	_, err = e.m.SetIdpJit(ctx, "acme-idp", true, []string{"tenant-owner"}, true)
	require.NoError(t, err)
}

func (e *ssoEnv) sysDB() *gorm.DB {
	return e.db().WithContext(core.WithSystemContext(context.Background()))
}

func (e *ssoEnv) mappingRows(t *testing.T) []iam.IdpRoleMapping {
	t.Helper()
	var rows []iam.IdpRoleMapping
	require.NoError(t, e.sysDB().Find(&rows).Error)
	return rows
}

func TestSystemRoleNeverMapped(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	require.NoError(t, e.store.EnsureRole(ctx, iam.ScopeSystem, iam.SuperuserRoleToken, "Superuser", []string{"*"}))
	require.NoError(t, e.store.EnsureRole(ctx, iam.ScopeSystem, "ops-admin", "Ops", []string{"tenant:write"}))

	// By token: a system role is not in the tenant catalog, so the mapping names nothing.
	_, err := e.m.SetIdpRoleMappings(ctx, "acme-idp", "acme", []RoleMappingInput{{Group: "g", Role: iam.SuperuserRoleToken}}, true)
	require.ErrorIs(t, err, ErrIdpInvalid)
	_, err = e.m.SetIdpRoleMappings(ctx, "acme-idp", "acme", []RoleMappingInput{{Group: "g", Role: "ops-admin"}}, true)
	require.ErrorIs(t, err, ErrIdpInvalid)
	_, err = e.m.SetIdpJit(ctx, "acme-idp", true, []string{"ops-admin"}, true)
	require.ErrorIs(t, err, ErrIdpInvalid)
	require.Empty(t, e.mappingRows(t))

	// Second lock: even a mapping row written around the call (a stale restore, a direct
	// write) cannot grant a system role from a claim.
	sys, err := e.store.RoleByScopeToken(ctx, iam.ScopeSystem, "ops-admin")
	require.NoError(t, err)
	conn := e.reloadConn(t, "acme-idp")
	require.NoError(t, e.sysDB().Create(&iam.IdpRoleMapping{
		ConnectionID: conn.ID, TenantId: "acme", GroupValue: "g", RoleID: sys.ID,
	}).Error)
	e.jit(t, "acme-idp", true)
	e.idpGroups("g")
	l, _, _, _ := e.signIn(t, "acme-idp", "sub-nia", newcomer)
	e.refused(t, l, "acme-idp", SsoOutcomeServerError)
	require.Nil(t, e.membership(t, newcomer, "acme"), "no membership came of it")
}

func TestMappingForAnUnservedTenantIsRefused(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.addTenant(t, "beta")
	e.tenantRole(t, "operator", "device:write")

	_, err := e.m.SetIdpRoleMappings(ctx, "acme-idp", "beta", []RoleMappingInput{{Group: "g", Role: "operator"}}, false)
	require.ErrorIs(t, err, ErrIdpInvalid, "the connection does not serve beta")
	_, err = e.m.SetIdpRoleMappings(ctx, "acme-idp", "ghost", nil, false)
	require.ErrorIs(t, err, ErrIdpInvalid)

	// An instance connection maps nothing at all.
	e.addConnection(t, "ops-idp", iam.IdpKindInstance, "ops-corp.com")
	_, err = e.m.SetIdpRoleMappings(ctx, "ops-idp", "acme", []RoleMappingInput{{Group: "g", Role: "operator"}}, false)
	require.ErrorIs(t, err, ErrIdpInvalid)
	require.Empty(t, e.mappingRows(t))

	// Dropping a tenant from the connection takes its mappings with it.
	e.mapping(t, "acme-idp", "acme", "g", "operator")
	_, err = e.m.SetIdpConnectionTenants(ctx, "acme-idp", nil)
	require.NoError(t, err)
	require.Empty(t, e.mappingRows(t))
}

func TestOrphanedManagedMembershipCannotBeReenabled(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.tenantRole(t, "operator", "device:write")
	e.jit(t, "acme-idp", true, "operator")
	e.signInOK(t, "acme-idp", "sub-nia", newcomer)

	// While the connection exists, an administrator may toggle it (the next sign-in
	// recomputes it anyway).
	_, err := e.admin.SetMembershipEnabled(ctx, newcomer, "acme", false)
	require.NoError(t, err)
	_, err = e.admin.SetMembershipEnabled(ctx, newcomer, "acme", true)
	require.NoError(t, err)

	deleted, err := e.m.DeleteIdpConnection(ctx, "acme-idp")
	require.NoError(t, err)
	require.True(t, deleted)
	mem := e.membership(t, newcomer, "acme")
	require.False(t, mem.Enabled, "deleting the connection disabled what it managed")
	require.NotNil(t, mem.ManagedBy, "and kept the marker")

	_, err = e.admin.SetMembershipEnabled(ctx, newcomer, "acme", true)
	require.ErrorIs(t, err, admin.ErrMembershipOrphaned)
	require.False(t, e.membership(t, newcomer, "acme").Enabled)

	// Disabling is still allowed, and the way forward is to remove and add on purpose.
	_, err = e.admin.SetMembershipEnabled(ctx, newcomer, "acme", false)
	require.NoError(t, err)
	_, err = e.admin.RemoveMembership(ctx, newcomer, "acme")
	require.NoError(t, err)
	_, err = e.admin.AddMembership(ctx, newcomer, "acme", []string{"operator"})
	require.NoError(t, err)
	fresh := e.membership(t, newcomer, "acme")
	require.True(t, fresh.Enabled)
	require.Nil(t, fresh.ManagedBy, "a deliberate grant is manual")
}

func TestRoleInUseAsADefaultCannotBeDeleted(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.tenantRole(t, "viewer", "device:read")
	e.jit(t, "acme-idp", true, "viewer")
	_, err := e.admin.DeleteRole(ctx, "tenant", "viewer")
	require.ErrorIs(t, err, admin.ErrRoleInUseByMapping)
	e.jit(t, "acme-idp", true)
	ok, err := e.admin.DeleteRole(ctx, "tenant", "viewer")
	require.NoError(t, err)
	require.True(t, ok)
}

func TestSystemRoleIdentityIsNeverRecomputed(t *testing.T) {
	e := newSsoEnv(t)
	e.tenantRole(t, "operator", "device:write")
	e.jit(t, "acme-idp", true, "operator")
	id := e.ssoIdentity(t, newcomer, "acme-idp", "sub-ops", "acme")
	e.grantSuperuser(t, newcomer)
	_ = id

	l, _, _, _ := e.signIn(t, "acme-idp", "sub-ops", newcomer)
	e.refused(t, l, "acme-idp", SsoOutcomeRefusedSystemRole)
	mem := e.membership(t, newcomer, "acme")
	require.Nil(t, mem.ManagedBy, "a refused sign-in recomputes nothing")
	require.Empty(t, roleTokensOf(mem))
}

func TestLinkedIdentityIsProvisionedMembershipsAtItsNextSignIn(t *testing.T) {
	// An identity a superuser linked by hand, with no membership at all, gains the managed
	// one at its first sign-in: the recompute is not only for JIT identities.
	e := newSsoEnv(t)
	e.tenantRole(t, "operator", "device:write")
	e.ssoIdentity(t, "lin@"+ssoTestDomain, "acme-idp", "sub-lin")
	e.mapping(t, "acme-idp", "acme", "ops", "operator")
	e.idpGroups("ops")
	e.signInOK(t, "acme-idp", "sub-lin", "lin@"+ssoTestDomain)
	mem := e.membership(t, "lin@"+ssoTestDomain, "acme")
	require.NotNil(t, mem)
	require.Equal(t, []string{"operator"}, roleTokensOf(mem))
}

func TestAMappingGrantsOnlyInItsOwnTenant(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.addTenant(t, "beta")
	_, err := e.m.SetIdpConnectionTenants(ctx, "acme-idp", []string{"acme", "beta"})
	require.NoError(t, err)
	e.tenantRole(t, "operator", "device:write")
	e.tenantRole(t, "viewer", "device:read")
	e.jit(t, "acme-idp", true)
	e.mapping(t, "acme-idp", "acme", "ops", "operator")
	e.mapping(t, "acme-idp", "beta", "eng", "viewer")

	// "ops" is mapped in acme only: it must grant nothing in beta.
	e.idpGroups("ops")
	e.signInOK(t, "acme-idp", "sub-nia", newcomer)
	require.Equal(t, []string{"operator"}, roleTokensOf(e.membership(t, newcomer, "acme")))
	require.Nil(t, e.membership(t, newcomer, "beta"), "acme's mapping reached beta")
}

func TestDroppingATenantDisablesWhatTheConnectionManagedThere(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.addTenant(t, "beta")
	_, err := e.m.SetIdpConnectionTenants(ctx, "acme-idp", []string{"acme", "beta"})
	require.NoError(t, err)
	e.tenantRole(t, "operator", "device:write")
	e.jit(t, "acme-idp", true, "operator")
	e.signInOK(t, "acme-idp", "sub-nia", newcomer)
	require.True(t, e.membership(t, newcomer, "beta").Enabled)

	// beta leaves the connection. The recompute never visits it again, so what the provider
	// granted there must not stay live.
	_, err = e.m.SetIdpConnectionTenants(ctx, "acme-idp", []string{"acme"})
	require.NoError(t, err)
	beta := e.membership(t, newcomer, "beta")
	require.False(t, beta.Enabled, "a dropped tenant's managed membership stayed enabled")
	require.NotNil(t, beta.ManagedBy, "and keeps its marker")
	require.True(t, e.membership(t, newcomer, "acme").Enabled, "a tenant still served is untouched")

	// Nor can an administrator re-enable it into a permanent grant.
	_, err = e.admin.SetMembershipEnabled(ctx, newcomer, "beta", true)
	require.ErrorIs(t, err, admin.ErrMembershipOrphaned)

	// Dropping every tenant disables the rest.
	_, err = e.m.SetIdpConnectionTenants(ctx, "acme-idp", nil)
	require.NoError(t, err)
	require.False(t, e.membership(t, newcomer, "acme").Enabled)
}

func TestSignInWritesNoMembershipInAPurgingTenant(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.addTenant(t, "beta")
	_, err := e.m.SetIdpConnectionTenants(ctx, "acme-idp", []string{"acme", "beta"})
	require.NoError(t, err)
	e.tenantRole(t, "operator", "device:write")
	e.jit(t, "acme-idp", true, "operator")
	require.NoError(t, e.sysDB().Model(&iam.Tenant{}).Where("token = ?", "beta").
		Update("purge_state", iam.PurgePurging).Error)

	e.signInOK(t, "acme-idp", "sub-nia", newcomer)
	require.NotNil(t, e.membership(t, newcomer, "acme"))
	require.Nil(t, e.membership(t, newcomer, "beta"), "a sign-in wrote a row into a tenant being erased")
}

func TestOverageIsReadForACustomGroupsClaim(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	conn := e.reloadConn(t, "acme-idp")
	conn.GroupsClaim = "roles"
	require.NoError(t, e.store.UpdateIdpConnection(ctx, conn))
	e.tenantRole(t, "operator", "device:write")
	e.jit(t, "acme-idp", true)
	e.mapping(t, "acme-idp", "acme", "ops", "operator")
	e.op.mutate = func(c jwt.MapClaims) {
		c["_claim_names"] = map[string]any{"roles": "src1"}
		c["_claim_sources"] = map[string]any{"src1": map[string]any{"endpoint": "https://graph.example/x"}}
	}
	l, _, _, _ := e.signIn(t, "acme-idp", "sub-nia", newcomer)
	e.refused(t, l, "acme-idp", SsoOutcomeRefusedGroupsOverage)
}

func TestJitSettingsChangeIsAudited(t *testing.T) {
	e := newSsoEnv(t)
	e.tenantRole(t, "viewer", "device:read")
	e.jit(t, "acme-idp", true, "viewer")
	require.EqualValues(t, 1, e.auditCount(t, rdb.AuditOpRoleMappingChanged))
}

// The store's own refusal, beneath the sign-in's check: provisioning never adopts an
// identity that already has the address, and a refusal leaves no link behind.
func TestProvisioningNeverAdoptsAnExistingIdentity(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	existing := e.ssoIdentity(t, newcomer, "", "", "acme")
	conn := e.reloadConn(t, "acme-idp")

	err := e.store.CreateProvisionedIdentity(ctx, &iam.Identity{Email: newcomer, Enabled: true},
		&iam.IdentityLink{ConnectionID: conn.ID, Subject: "sub-x", CreatedBy: "test"})
	require.ErrorIs(t, err, iam.ErrEmailTaken)
	links, err := e.store.LinksForIdentity(ctx, existing.ID)
	require.NoError(t, err)
	require.Empty(t, links)
	var n int64
	require.NoError(t, e.sysDB().Model(&iam.IdentityLink{}).Where("subject = ?", "sub-x").Count(&n).Error)
	require.Zero(t, n, "a refused provisioning left a link")
}

// The identity and its link are one write: a link the unique (connection, subject) index
// refuses (the loser of two first sign-ins of one person) leaves no identity behind. The
// harness's in-memory database cannot host a real concurrent race, so this is the race's
// losing write, played in order.
func TestProvisioningIsOneWrite(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	conn := e.reloadConn(t, "acme-idp")
	require.NoError(t, e.store.CreateProvisionedIdentity(ctx, &iam.Identity{Email: newcomer, Enabled: true},
		&iam.IdentityLink{ConnectionID: conn.ID, Subject: "sub-nia", CreatedBy: "test"}))

	other := "other@" + ssoTestDomain
	err := e.store.CreateProvisionedIdentity(ctx, &iam.Identity{Email: other, Enabled: true},
		&iam.IdentityLink{ConnectionID: conn.ID, Subject: "sub-nia", CreatedBy: "test"})
	require.Error(t, err)
	_, err = e.store.IdentityByEmail(ctx, other)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "a refused link left an identity no account can reach")
}
