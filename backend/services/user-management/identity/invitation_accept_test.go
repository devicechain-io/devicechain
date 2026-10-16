// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

// Accepting an invitation (ADR-081): the code enables the membership it names, once, and
// only for the person it was issued to.

// inviteEnv is a session env with a tenant administrator who can invite into acme.
func inviteEnv(t *testing.T) (*sessionEnv, context.Context) {
	t.Helper()
	e := newSessionEnv(t)
	require.NoError(t, e.db().WithContext(core.WithSystemContext(context.Background())).AutoMigrate(&iam.Invitation{}))
	e.admin.SetViewerBaseline(ViewerAuthorities())
	ctx := context.Background()
	require.NoError(t, e.store.CreateRole(ctx, &iam.Role{Scope: iam.ScopeTenant, Token: iam.TenantAdminRoleToken, Authorities: []string{"*"}}))
	_, err := e.admin.CreateIdentity(ctx, admin.CreateIdentityInput{Email: "boss@example.com", Password: "boss-pw", Enabled: true})
	require.NoError(t, err)
	_, err = e.admin.AddMembership(ctx, "boss@example.com", "acme", []string{iam.TenantAdminRoleToken})
	require.NoError(t, err)
	boss := auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme",
		Username: "boss@example.com", Authorities: []string{"*"}})
	return e, boss
}

func ptr(s string) *string { return &s }

func isInvalidInvitation(err error) bool {
	var r *admin.TenantRefusal
	return errors.As(err, &r) && r.Code() == admin.CodeInvalidInvitation
}

func TestANewPersonAcceptsWithAPasswordAndCanSignIn(t *testing.T) {
	e, boss := inviteEnv(t)
	_, code, err := e.admin.InviteMember(boss, admin.InviteInput{Email: "new@example.com"})
	require.NoError(t, err)

	// Before accepting, the pending identity cannot enter acme.
	_, err = e.m.Login(context.Background(), "new@example.com", "anything")
	require.ErrorIs(t, err, ErrInvalidCredentials)

	ia, err := e.m.AcceptInvitation(context.Background(), code, ptr("new-pw"), nil)
	require.NoError(t, err)
	require.NotEmpty(t, ia.IdentityToken)
	pair, err := e.m.SelectTenant(context.Background(), ia.IdentityToken, "acme")
	require.NoError(t, err, "the accepted membership cannot be entered")
	require.NotEmpty(t, pair.AccessToken)

	// The code works once.
	_, err = e.m.AcceptInvitation(context.Background(), code, ptr("other-pw"), nil)
	require.True(t, isInvalidInvitation(err), "an accepted code was accepted again: %v", err)
}

// 🔴 THE ACCOUNT-TAKEOVER GUARD. The code is held by whoever the administrator gave it to,
// administrator included. An identity that has ever been usable — here one that signs in
// only through an identity provider, so it has NO password — must never have a password
// set through an invitation: the inviter would then sign in as that person everywhere a
// password is accepted. It accepts only by signing in.
func TestAnExistingAccountCannotHaveItsPasswordSetThroughAnInvitation(t *testing.T) {
	e, boss := inviteEnv(t)
	ctx := context.Background()
	// An SSO-only identity in another tenant: no password, one provider link.
	_, err := e.admin.CreateIdentity(ctx, admin.CreateIdentityInput{Email: "sso@example.com", Password: "x", Enabled: true})
	require.NoError(t, err)
	id, err := e.store.IdentityByEmail(ctx, "sso@example.com")
	require.NoError(t, err)
	require.NoError(t, e.db().WithContext(core.WithSystemContext(ctx)).Model(id).Update("password_hash", "").Error)
	require.NoError(t, e.store.CreateIdentityLink(ctx, &iam.IdentityLink{IdentityID: id.ID, ConnectionID: 1, Subject: "sub-1", CreatedBy: "test"}))

	_, code, err := e.admin.InviteMember(boss, admin.InviteInput{Email: "sso@example.com"})
	require.NoError(t, err)
	_, err = e.m.AcceptInvitation(ctx, code, ptr("attacker-chosen"), nil)
	require.True(t, isInvalidInvitation(err), "an invitation set the password of an existing account: %v", err)
	after, err := e.store.IdentityByEmail(ctx, "sso@example.com")
	require.NoError(t, err)
	require.Empty(t, after.PasswordHash, "the existing account's password was set")

	// An operator account with no password (its sign-in is elsewhere) is just as refused.
	require.NoError(t, e.store.CreateRole(ctx, &iam.Role{Scope: iam.ScopeSystem, Token: "operator", Authorities: []string{"user:read"}}))
	_, err = e.admin.CreateIdentity(ctx, admin.CreateIdentityInput{Email: "op@example.com", Password: "x", Enabled: true, SystemRoles: []string{"operator"}})
	require.NoError(t, err)
	op, err := e.store.IdentityByEmail(ctx, "op@example.com")
	require.NoError(t, err)
	require.NoError(t, e.db().WithContext(core.WithSystemContext(ctx)).Model(op).Update("password_hash", "").Error)
	_, opCode, err := e.admin.InviteMember(boss, admin.InviteInput{Email: "op@example.com"})
	require.NoError(t, err)
	_, err = e.m.AcceptInvitation(ctx, opCode, ptr("attacker-chosen"), nil)
	require.True(t, isInvalidInvitation(err), "an invitation set the password of an operator account: %v", err)

	// The same for an account with a password: it must sign in instead.
	e.createMember(t, "has-pw@example.com", "own-pw")
	_, err = e.admin.RemoveMembership(ctx, "has-pw@example.com", "acme")
	require.NoError(t, err)
	_, code2, err := e.admin.InviteMember(boss, admin.InviteInput{Email: "has-pw@example.com"})
	require.NoError(t, err)
	_, err = e.m.AcceptInvitation(ctx, code2, ptr("attacker-chosen"), nil)
	require.True(t, isInvalidInvitation(err), "got %v", err)
	_, err = e.m.Login(ctx, "has-pw@example.com", "own-pw")
	require.NoError(t, err, "the existing password stopped working")
}

func TestAnExistingAccountAcceptsBySigningIn(t *testing.T) {
	e, boss := inviteEnv(t)
	ctx := context.Background()
	_, err := e.admin.CreateIdentity(ctx, admin.CreateIdentityInput{Email: "pat@example.com", Password: "pat-pw", Enabled: true})
	require.NoError(t, err)
	_, code, err := e.admin.InviteMember(boss, admin.InviteInput{Email: "pat@example.com"})
	require.NoError(t, err)
	_, code2, err := e.admin.InviteMember(boss, admin.InviteInput{Email: "other@example.com"})
	require.NoError(t, err)

	ia, err := e.m.Login(ctx, "pat@example.com", "pat-pw")
	require.NoError(t, err)

	// pat's token cannot redeem another person's code.
	_, err = e.m.AcceptInvitation(ctx, code2, nil, &ia.IdentityToken)
	require.True(t, isInvalidInvitation(err), "an identity token accepted someone else's invitation: %v", err)

	res, err := e.m.AcceptInvitation(ctx, code, nil, &ia.IdentityToken)
	require.NoError(t, err)
	found := false
	for _, m := range res.Memberships {
		found = found || m.Tenant == "acme"
	}
	require.True(t, found, "the accepted tenant is not among the memberships returned")
	_, err = e.m.SelectTenant(ctx, ia.IdentityToken, "acme")
	require.NoError(t, err)
}

// Every way a code cannot be used is the ONE answer.
func TestEveryUnusableCodeIsTheSameAnswer(t *testing.T) {
	e, boss := inviteEnv(t)
	ctx := context.Background()

	_, revoked, err := e.admin.InviteMember(boss, admin.InviteInput{Email: "r@example.com"})
	require.NoError(t, err)
	_, err = e.admin.RevokeInvitation(boss, "r@example.com")
	require.NoError(t, err)

	_, expired, err := e.admin.InviteMember(boss, admin.InviteInput{Email: "x@example.com"})
	require.NoError(t, err)
	require.NoError(t, e.db().WithContext(core.WithSystemContext(ctx)).Model(&iam.Invitation{}).
		Where("token_digest = ?", admin.InvitationDigest(expired)).
		Update("expires_at", time.Now().Add(-time.Minute)).Error)

	var msgs []string
	for name, code := range map[string]string{"unknown": "no-such-code", "revoked": revoked, "expired": expired} {
		_, err := e.m.AcceptInvitation(ctx, code, ptr("pw"), nil)
		require.Truef(t, isInvalidInvitation(err), "%s: %v", name, err)
		msgs = append(msgs, err.Error())
	}
	require.Equal(t, msgs[0], msgs[1])
	require.Equal(t, msgs[1], msgs[2])

	// Both or neither proof is a different, input refusal.
	_, err = e.m.AcceptInvitation(ctx, "c", nil, nil)
	require.ErrorIs(t, err, ErrAcceptNeedsOneProof)
}

func TestASsoTenantRefusesThePasswordPath(t *testing.T) {
	e, boss := inviteEnv(t)
	ctx := context.Background()
	_, code, err := e.admin.InviteMember(boss, admin.InviteInput{Email: "new@example.com"})
	require.NoError(t, err)
	require.NoError(t, e.db().WithContext(core.WithSystemContext(ctx)).Model(&iam.Tenant{}).
		Where("token = ?", "acme").Update("sso_enforced", true).Error)

	_, err = e.m.AcceptInvitation(ctx, code, ptr("pw"), nil)
	require.ErrorIs(t, err, ErrSsoRequired)
	id, err := e.store.IdentityByEmail(ctx, "new@example.com")
	require.NoError(t, err)
	require.Empty(t, id.PasswordHash, "an SSO tenant's invitation set a password")
}

// 🔴 "NEVER USED" MEANS NEVER GRANTED ANYTHING EITHER. An identity with no password, no
// system role and no provider link can still carry memberships someone granted it: a
// single-sign-on user whose identity provider connection the operator has since deleted
// (the deletion removes the links and keeps the memberships), or a member a tenant enabled
// without an acceptance. Such an identity has been usable, and a password set on it by
// whoever holds a NEW tenant's invitation code would enter every tenant it belongs to.
// The password path is for an identity whose only memberships are pending invitations.
func TestAnAccountWithGrantsElsewhereCannotHaveItsPasswordSetThroughAnInvitation(t *testing.T) {
	e, boss := inviteEnv(t)
	ctx := context.Background()
	ts, err := e.store.ListTenants(ctx)
	require.NoError(t, err)
	require.NoError(t, e.store.CreateTenant(ctx, &iam.Tenant{Token: "globex", Enabled: true, TierID: ts[0].TierID, PurgeState: iam.PurgeActive}))

	// Formerly SSO-only, enabled member of globex; the connection and its links are gone.
	_, err = e.admin.CreateIdentity(ctx, admin.CreateIdentityInput{Email: "orphan@example.com", Password: "x", Enabled: true})
	require.NoError(t, err)
	_, err = e.admin.AddMembership(ctx, "orphan@example.com", "globex", nil)
	require.NoError(t, err)
	orphan, err := e.store.IdentityByEmail(ctx, "orphan@example.com")
	require.NoError(t, err)
	require.NoError(t, e.db().WithContext(core.WithSystemContext(ctx)).Model(orphan).Update("password_hash", "").Error)

	_, code, err := e.admin.InviteMember(boss, admin.InviteInput{Email: "orphan@example.com"})
	require.NoError(t, err)
	_, err = e.m.AcceptInvitation(ctx, code, ptr("attacker-chosen"), nil)
	require.True(t, isInvalidInvitation(err), "an invitation set the password of an account that is a member elsewhere: %v", err)
	after, err := e.store.IdentityByEmail(ctx, "orphan@example.com")
	require.NoError(t, err)
	require.Empty(t, after.PasswordHash, "the password was set")
	mem, err := e.store.MembershipByIdentityTenant(ctx, after.ID, "acme")
	require.NoError(t, err)
	require.False(t, mem.Enabled, "the membership was enabled by a refused acceptance")

	// The counterweight: two tenants inviting the same new person is the ordinary case, and
	// the first acceptance sets the password while the other invitation is still pending.
	_, codeA, err := e.admin.InviteMember(boss, admin.InviteInput{Email: "twice@example.com"})
	require.NoError(t, err)
	globexBoss := auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "globex",
		Username: "boss@example.com", Authorities: []string{"*"}})
	_, err = e.admin.AddMembership(ctx, "boss@example.com", "globex", []string{iam.TenantAdminRoleToken})
	require.NoError(t, err)
	_, codeB, err := e.admin.InviteMember(globexBoss, admin.InviteInput{Email: "twice@example.com"})
	require.NoError(t, err)
	ia, err := e.m.AcceptInvitation(ctx, codeA, ptr("twice-pw"), nil)
	require.NoError(t, err, "a person invited by two tenants could not accept the first with a password")
	_, err = e.m.AcceptInvitation(ctx, codeB, nil, &ia.IdentityToken)
	require.NoError(t, err, "the second invitation could not be accepted by signing in")

	// An ENABLED membership blocks the password path even while its own invitation is
	// still open — the one state the open-invitation rule alone would admit. Only the admin
	// plane can produce it (the tenant plane refuses enabling a pending member).
	_, codeG, err := e.admin.InviteMember(globexBoss, admin.InviteInput{Email: "enabled-early@example.com"})
	require.NoError(t, err)
	_, err = e.admin.SetMembershipEnabled(ctx, "enabled-early@example.com", "globex", true)
	require.NoError(t, err)
	_, codeA2, err := e.admin.InviteMember(boss, admin.InviteInput{Email: "enabled-early@example.com"})
	require.NoError(t, err)
	_, err = e.m.AcceptInvitation(ctx, codeA2, ptr("attacker-chosen"), nil)
	require.True(t, isInvalidInvitation(err), "a member already enabled elsewhere had a password set through another tenant's invitation: %v", err)
	_ = codeG

	// A DISABLED membership elsewhere whose invitation was revoked blocks it too: nothing is
	// waiting on that membership, so it is a grant someone made, not an invitation in flight.
	_, _, err = e.admin.InviteMember(globexBoss, admin.InviteInput{Email: "revoked-elsewhere@example.com"})
	require.NoError(t, err)
	_, err = e.admin.RevokeInvitation(globexBoss, "revoked-elsewhere@example.com")
	require.NoError(t, err)
	_, codeA3, err := e.admin.InviteMember(boss, admin.InviteInput{Email: "revoked-elsewhere@example.com"})
	require.NoError(t, err)
	_, err = e.m.AcceptInvitation(ctx, codeA3, ptr("attacker-chosen"), nil)
	require.True(t, isInvalidInvitation(err), "a disabled membership with no invitation in flight did not block the password path: %v", err)
}
