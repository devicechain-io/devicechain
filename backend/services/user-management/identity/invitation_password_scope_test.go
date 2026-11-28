// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

// 🔴 A PASSWORD SET THROUGH AN INVITATION CODE PROVES ONLY THAT ITS SETTER HELD THE CODE. The
// inviting administrator holds that code too, and identities are global by address, so the
// password must open only the tenants that vouched for whoever set it — never a tenant that
// later attaches a membership to the same address.

// secondTenant adds tenant globex with its own administrator (boss@example.com, already
// acme's administrator) and returns that administrator's tenant-plane context.
func secondTenant(t *testing.T, e *sessionEnv) context.Context {
	t.Helper()
	ctx := context.Background()
	ts, err := e.store.ListTenants(ctx)
	require.NoError(t, err)
	require.NoError(t, e.store.CreateTenant(ctx, &iam.Tenant{Token: "globex", Enabled: true, TierID: ts[0].TierID, PurgeState: iam.PurgeActive}))
	_, err = e.admin.AddMembership(ctx, "boss@example.com", "globex", []string{iam.TenantAdminRoleToken})
	require.NoError(t, err)
	return auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "globex",
		Username: "boss@example.com", Authorities: []string{"*"}})
}

func hasMembership(ia *IdentityAuth, tenant string) bool {
	for _, m := range ia.Memberships {
		if m.Tenant == tenant {
			return true
		}
	}
	return false
}

// The cross-tenant scenario: acme's administrator invites an address nobody holds yet and
// redeems the code with a password of their own. globex later invites the same address, the
// real person cannot accept, and globex enables the membership by hand. The password set
// through acme's code must not enter globex.
func TestAnInvitationPasswordDoesNotEnterATenantThatDidNotVouchForIt(t *testing.T) {
	e, acmeBoss := inviteEnv(t)
	globexBoss := secondTenant(t, e)
	ctx := context.Background()

	_, code, err := e.admin.InviteMember(acmeBoss, admin.InviteInput{Email: "ceo@victim.example"})
	require.NoError(t, err)
	_, err = e.m.AcceptInvitation(ctx, code, ptr("inviter-chosen"), nil)
	require.NoError(t, err)

	_, globexCode, err := e.admin.InviteMember(globexBoss, admin.InviteInput{Email: "ceo@victim.example"})
	require.NoError(t, err)
	_, err = e.m.AcceptInvitation(ctx, globexCode, ptr("real-person"), nil)
	require.True(t, isInvalidInvitation(err), "got %v", err)
	_, err = e.admin.RevokeInvitation(globexBoss, "ceo@victim.example")
	require.NoError(t, err)
	_, err = e.admin.SetMemberEnabled(globexBoss, "ceo@victim.example", true)
	require.NoError(t, err)

	ia, err := e.m.Login(ctx, "ceo@victim.example", "inviter-chosen")
	require.NoError(t, err)
	require.True(t, hasMembership(ia, "acme"), "the vouching tenant is not listed")
	require.False(t, hasMembership(ia, "globex"), "a tenant that did not vouch for the password is listed")
	_, err = e.m.SelectTenant(ctx, ia.IdentityToken, "globex")
	require.Error(t, err, "a password set through acme's invitation entered globex")
	_, err = e.m.SelectTenant(ctx, ia.IdentityToken, "acme")
	require.NoError(t, err, "the vouching tenant can no longer be entered")

	// An operator setting the password vouches for it everywhere: the person the operator
	// gave it to enters every tenant they belong to.
	_, err = e.admin.SetPassword(ctx, "ceo@victim.example", "operator-set")
	require.NoError(t, err)
	_, err = e.m.Login(ctx, "ceo@victim.example", "inviter-chosen")
	require.ErrorIs(t, err, ErrInvalidCredentials)
	ia, err = e.m.Login(ctx, "ceo@victim.example", "operator-set")
	require.NoError(t, err)
	require.True(t, hasMembership(ia, "globex"))
	_, err = e.m.SelectTenant(ctx, ia.IdentityToken, "globex")
	require.NoError(t, err, "an operator-set password does not enter the member's tenant")
}

// The other order: globex's invitation is open first, then acme's administrator invites
// the same address and redeems their own code (allowed: the identity has never been used).
// globex's later hand-enabling, or an operator adding a membership, does not admit it.
func TestAnInvitationPasswordSetWhileAnotherInvitationIsOpenStaysScoped(t *testing.T) {
	e, acmeBoss := inviteEnv(t)
	globexBoss := secondTenant(t, e)
	ctx := context.Background()
	ts, err := e.store.ListTenants(ctx)
	require.NoError(t, err)
	require.NoError(t, e.store.CreateTenant(ctx, &iam.Tenant{Token: "initech", Enabled: true, TierID: ts[0].TierID, PurgeState: iam.PurgeActive}))

	_, _, err = e.admin.InviteMember(globexBoss, admin.InviteInput{Email: "cfo@victim.example"})
	require.NoError(t, err)
	_, code, err := e.admin.InviteMember(acmeBoss, admin.InviteInput{Email: "cfo@victim.example"})
	require.NoError(t, err)
	_, err = e.m.AcceptInvitation(ctx, code, ptr("inviter-chosen"), nil)
	require.NoError(t, err)
	_, err = e.admin.RevokeInvitation(globexBoss, "cfo@victim.example")
	require.NoError(t, err)
	_, err = e.admin.SetMemberEnabled(globexBoss, "cfo@victim.example", true)
	require.NoError(t, err)
	_, err = e.admin.AddMembership(ctx, "cfo@victim.example", "initech", nil)
	require.NoError(t, err)

	ia, err := e.m.Login(ctx, "cfo@victim.example", "inviter-chosen")
	require.NoError(t, err)
	for _, tenant := range []string{"globex", "initech"} {
		require.Falsef(t, hasMembership(ia, tenant), "%s is listed", tenant)
		_, err = e.m.SelectTenant(ctx, ia.IdentityToken, tenant)
		require.Errorf(t, err, "a password set through acme's invitation entered %s", tenant)
	}

	// A single sign-on link does not widen it either: the provider vouches for its own
	// sign-ins, not for a password someone else set.
	id, err := e.store.IdentityByEmail(ctx, "cfo@victim.example")
	require.NoError(t, err)
	require.NoError(t, e.store.CreateIdentityLink(ctx, &iam.IdentityLink{IdentityID: id.ID, ConnectionID: 7, Subject: "cfo-sub", CreatedBy: "test"}))
	ia, err = e.m.Login(ctx, "cfo@victim.example", "inviter-chosen")
	require.NoError(t, err)
	_, err = e.m.SelectTenant(ctx, ia.IdentityToken, "globex")
	require.Error(t, err, "a provider link widened a password set through an invitation")
}

// The counterweight: a person invited by two tenants accepts the first with a password and
// the second by signing in with it. Holding the second tenant's code is that tenant's
// vouching, so the password enters both. A sign-in through an identity provider that
// accepts an invitation vouches for nothing about the password.
func TestAcceptingBySigningInWithTheInvitationPasswordVouchesForThatTenant(t *testing.T) {
	e, acmeBoss := inviteEnv(t)
	globexBoss := secondTenant(t, e)
	ctx := context.Background()

	_, codeA, err := e.admin.InviteMember(acmeBoss, admin.InviteInput{Email: "twice@example.com"})
	require.NoError(t, err)
	_, codeB, err := e.admin.InviteMember(globexBoss, admin.InviteInput{Email: "twice@example.com"})
	require.NoError(t, err)
	ia, err := e.m.AcceptInvitation(ctx, codeA, ptr("twice-pw"), nil)
	require.NoError(t, err)
	_, err = e.m.AcceptInvitation(ctx, codeB, nil, &ia.IdentityToken)
	require.NoError(t, err)

	ia, err = e.m.Login(ctx, "twice@example.com", "twice-pw")
	require.NoError(t, err)
	for _, tenant := range []string{"acme", "globex"} {
		require.Truef(t, hasMembership(ia, tenant), "%s is not listed", tenant)
		_, err = e.m.SelectTenant(ctx, ia.IdentityToken, tenant)
		require.NoErrorf(t, err, "%s cannot be entered", tenant)
	}

	// The flag and the vouches are what the store holds, not an artefact of this sign-in.
	id, err := e.store.IdentityByEmail(ctx, "twice@example.com")
	require.NoError(t, err)
	require.True(t, id.PasswordFromInvitation)
	var n int64
	require.NoError(t, e.db().WithContext(core.WithSystemContext(ctx)).Model(&iam.Membership{}).
		Where("identity_id = ? AND admits_invited_password", id.ID).Count(&n).Error)
	require.EqualValues(t, 2, n)
}

// An account whose password an operator set is untouched: every tenant it belongs to admits it.
func TestAnOperatorPasswordIsNotScoped(t *testing.T) {
	e, acmeBoss := inviteEnv(t)
	globexBoss := secondTenant(t, e)
	ctx := context.Background()
	_, err := e.admin.CreateIdentity(ctx, admin.CreateIdentityInput{Email: "pat@example.com", Password: "pat-pw", Enabled: true})
	require.NoError(t, err)
	_, err = e.admin.AddMembership(ctx, "pat@example.com", "acme", nil)
	require.NoError(t, err)
	_, _, err = e.admin.InviteMember(globexBoss, admin.InviteInput{Email: "pat@example.com"})
	require.NoError(t, err)
	_, err = e.admin.RevokeInvitation(globexBoss, "pat@example.com")
	require.NoError(t, err)
	_, err = e.admin.SetMemberEnabled(globexBoss, "pat@example.com", true)
	require.NoError(t, err)
	_ = acmeBoss
	ia, err := e.m.Login(ctx, "pat@example.com", "pat-pw")
	require.NoError(t, err)
	for _, tenant := range []string{"acme", "globex"} {
		_, err = e.m.SelectTenant(ctx, ia.IdentityToken, tenant)
		require.NoErrorf(t, err, "%s cannot be entered", tenant)
	}
}
