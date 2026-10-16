// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

// Scoped memberships at the mint (ADR-081): the scope rides the ACCESS token as "scp" on
// both paths that mint one for a member — selecting a tenant (and refreshing) and the OAuth
// grant MCP clients hold — with the scoped lifetime, and never rides a refresh token.

// scopedEnv is a session env whose issuer has the DEFAULT lifetimes, so the scoped access
// lifetime (5 minutes) is visibly shorter than the unscoped one (15).
func scopedEnv(t *testing.T) *sessionEnv {
	t.Helper()
	e := newSessionEnv(t)
	e.m.issuer = auth.NewIssuer(e.key, "https://as.example.com", auth.DefaultAccessTTL, time.Hour)
	return e
}

func (e *sessionEnv) scopeMember(t *testing.T, email string, customers ...string) {
	t.Helper()
	anchors := make([]admin.ScopeAnchorInput, 0, len(customers))
	for _, c := range customers {
		anchors = append(anchors, admin.ScopeAnchorInput{Kind: auth.AnchorKindCustomer, Token: c})
	}
	_, err := e.admin.SetMembershipScope(context.Background(), email, "acme", anchors)
	require.NoError(t, err)
}

func TestScopedTokenCarriesScpAtSelectAndRefresh(t *testing.T) {
	e := scopedEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	e.scopeMember(t, "pat@example.com", "acme-corp", "initech")

	_, pair := e.login(t, "pat@example.com", "pw-1")
	access, err := e.validator.Validate(pair.AccessToken)
	require.NoError(t, err)
	require.Equal(t, []string{"customer:acme-corp", "customer:initech"}, access.ScopeAnchors,
		"the access token minted at selectTenant does not carry the membership's scope")
	require.WithinDuration(t, time.Now().Add(auth.DefaultScopedAccessTTL), access.ExpiresAt.Time, 5*time.Second,
		"a scoped access token must live for the scoped lifetime, not the unscoped one")

	refresh, err := e.validator.ValidateRefresh(pair.RefreshToken)
	require.NoError(t, err)
	require.Nil(t, refresh.ScopeAnchors, "a refresh token carries a scope nothing reads")

	again, err := e.m.Refresh(context.Background(), pair.RefreshToken)
	require.NoError(t, err)
	access2, err := e.validator.Validate(again.AccessToken)
	require.NoError(t, err)
	require.Equal(t, []string{"customer:acme-corp", "customer:initech"}, access2.ScopeAnchors,
		"the access token minted at refresh does not carry the membership's scope")
}

// The negative control: an unscoped membership's token carries no scope and the ordinary
// lifetime, and a superuser is never scoped even where it holds a scoped membership.
func TestAnUnscopedMemberAndASuperuserCarryNoScope(t *testing.T) {
	e := scopedEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	_, pair := e.login(t, "pat@example.com", "pw-1")
	access, err := e.validator.Validate(pair.AccessToken)
	require.NoError(t, err)
	require.Nil(t, access.ScopeAnchors)
	require.WithinDuration(t, time.Now().Add(auth.DefaultAccessTTL), access.ExpiresAt.Time, 5*time.Second)

	e.makeSuperuser(t, "root@example.com", "pw-2")
	_, err = e.admin.AddMembership(context.Background(), "root@example.com", "acme", nil)
	require.NoError(t, err)
	e.scopeMember(t, "root@example.com", "acme-corp")
	_, rootPair := e.login(t, "root@example.com", "pw-2")
	rootAccess, err := e.validator.Validate(rootPair.AccessToken)
	require.NoError(t, err)
	require.Nil(t, rootAccess.ScopeAnchors, "a superuser entering a tenant was scoped")
	require.True(t, rootAccess.ActingAsSuperuser)
}

// The OAuth grant is the second mint path: an MCP client's access token carries the
// member's scope, and expires_in reports the token's own (scoped) lifetime — not the
// unscoped default the client would otherwise refresh ten minutes too late against.
func TestScopedTokenCarriesScpOnTheOAuthGrant(t *testing.T) {
	e := scopedEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	e.scopeMember(t, "pat@example.com", "acme-corp")
	idTok, _ := e.login(t, "pat@example.com", "pw-1")

	toks := e.oauthSession(t, idTok)
	access, err := e.validator.Validate(toks.AccessToken)
	require.NoError(t, err)
	require.Equal(t, []string{"customer:acme-corp"}, access.ScopeAnchors,
		"the OAuth access token MCP forwards does not carry the member's scope")
	require.NotEmpty(t, access.Scope, "the OAuth scope string must still be there, apart from scp")
	require.InDelta(t, auth.DefaultScopedAccessTTL.Seconds(), float64(toks.ExpiresIn), 2,
		"expires_in must describe the scoped token it is returned with")

	refreshed, err := e.refreshOAuth(toks.RefreshToken)
	require.NoError(t, err)
	access2, err := e.validator.Validate(refreshed.AccessToken)
	require.NoError(t, err)
	require.Equal(t, []string{"customer:acme-corp"}, access2.ScopeAnchors)
	require.InDelta(t, auth.DefaultScopedAccessTTL.Seconds(), float64(refreshed.ExpiresIn), 2)

	// Unscoped, expires_in is the unscoped lifetime — the counterweight that shows the value
	// above is read from the token rather than fixed.
	e.createMember(t, "sam@example.com", "pw-3")
	samTok, _ := e.login(t, "sam@example.com", "pw-3")
	samToks := e.oauthSession(t, samTok)
	require.InDelta(t, auth.DefaultAccessTTL.Seconds(), float64(samToks.ExpiresIn), 2)
}

// A narrowing takes effect at the member's NEXT token exchange, and does not end the
// session (ADR-081 decision 8, as amended 2026-10-16). Every exchange re-reads the
// membership and mints from it, so the refresh token issued before the narrowing still
// refreshes — and what it mints is the NARROWED grant, never a copy of the old token. The
// access token already issued is the old token until it expires: nothing on the data plane
// re-reads the membership.
func TestANarrowedMembersNextRefreshCarriesTheNarrowedScope(t *testing.T) {
	e := scopedEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	_, before := e.login(t, "pat@example.com", "pw-1")

	e.scopeMember(t, "pat@example.com", "acme-corp")

	old, err := e.validator.Validate(before.AccessToken)
	require.NoError(t, err, "the already-issued access token stopped authenticating — the documented window is "+
		"its lifetime, and nothing on the data plane re-reads the membership")
	require.Nil(t, old.ScopeAnchors, "the old token is the old token: unscoped until it expires")

	next, err := e.m.Refresh(context.Background(), before.RefreshToken)
	require.NoError(t, err, "narrowing a scope signed the member out instead of narrowing their next token")
	access, err := e.validator.Validate(next.AccessToken)
	require.NoError(t, err)
	require.Equal(t, []string{"customer:acme-corp"}, access.ScopeAnchors,
		"the refresh after a narrowing minted a grant wider than the membership now holds")
	require.WithinDuration(t, time.Now().Add(auth.DefaultScopedAccessTTL), access.ExpiresAt.Time, 5*time.Second,
		"the narrowed token must carry the scoped lifetime")

	// Narrowing an already-scoped membership further is the same: the next token is the
	// narrower one.
	e.scopeMember(t, "pat@example.com", "acme-corp", "initech")
	wide, err := e.m.Refresh(context.Background(), next.RefreshToken)
	require.NoError(t, err)
	e.scopeMember(t, "pat@example.com", "initech")
	narrow, err := e.m.Refresh(context.Background(), wide.RefreshToken)
	require.NoError(t, err)
	access, err = e.validator.Validate(narrow.AccessToken)
	require.NoError(t, err)
	require.Equal(t, []string{"customer:initech"}, access.ScopeAnchors)
}

// The OAuth refresh (an MCP client's session) re-reads the membership the same way.
func TestANarrowedMembersNextOAuthRefreshCarriesTheNarrowedScope(t *testing.T) {
	e := scopedEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	idTok, _ := e.login(t, "pat@example.com", "pw-1")
	toks := e.oauthSession(t, idTok)

	e.scopeMember(t, "pat@example.com", "acme-corp")

	refreshed, err := e.refreshOAuth(toks.RefreshToken)
	require.NoError(t, err, "narrowing a scope ended the member's OAuth session")
	access, err := e.validator.Validate(refreshed.AccessToken)
	require.NoError(t, err)
	require.Equal(t, []string{"customer:acme-corp"}, access.ScopeAnchors,
		"the OAuth refresh after a narrowing minted a grant wider than the membership now holds")
	require.InDelta(t, auth.DefaultScopedAccessTTL.Seconds(), float64(refreshed.ExpiresIn), 2)
}

// Taking an authority away is the same rule for the authorities: the next refresh no longer
// carries it.
func TestARemovedAuthorityIsGoneFromTheNextRefresh(t *testing.T) {
	e := scopedEnv(t)
	ctx := context.Background()
	_, err := e.admin.CreateRole(ctx, admin.RoleInput{Scope: "tenant", Token: "operator", Authorities: []string{"command:write"}})
	require.NoError(t, err)
	e.createMember(t, "pat@example.com", "pw-1")
	_, err = e.admin.SetMembershipRoles(ctx, "pat@example.com", "acme", []string{"operator"})
	require.NoError(t, err)
	_, before := e.login(t, "pat@example.com", "pw-1")
	held, err := e.validator.Validate(before.AccessToken)
	require.NoError(t, err)
	require.Contains(t, held.Authorities, "command:write", "the fixture never granted the authority it removes")

	_, err = e.admin.SetMembershipRoles(ctx, "pat@example.com", "acme", nil)
	require.NoError(t, err)

	next, err := e.m.Refresh(ctx, before.RefreshToken)
	require.NoError(t, err, "removing an authority signed the member out instead of narrowing their next token")
	access, err := e.validator.Validate(next.AccessToken)
	require.NoError(t, err)
	require.NotContains(t, access.Authorities, "command:write",
		"the refresh after an authority was removed still carries it")
	require.NotContains(t, access.Roles, "operator")
}

// 🔴 A NARROWING IN ONE TENANT DOES NOT TOUCH THE PERSON'S SESSIONS IN ANOTHER. Disabling or
// removing a membership ends access to THAT tenant at the next exchange — the refresh token
// for it is refused — while the same person's session in a tenant the change does not
// concern keeps refreshing. Rotating the per-identity session epoch did the opposite: it
// signed the person out of every tenant, so one tenant's administrator could repeatedly
// sign a multi-tenant user out of tenants they do not administer.
func TestNarrowingInOneTenantLeavesTheOtherTenantsSessionAlone(t *testing.T) {
	for _, change := range []struct {
		name string
		do   func(e *sessionEnv) error
	}{
		{"disabled", func(e *sessionEnv) error {
			_, err := e.admin.SetMembershipEnabled(context.Background(), "pat@example.com", "acme", false)
			return err
		}},
		{"removed", func(e *sessionEnv) error {
			_, err := e.admin.RemoveMembership(context.Background(), "pat@example.com", "acme")
			return err
		}},
		{"scoped", func(e *sessionEnv) error {
			_, err := e.admin.SetMembershipScope(context.Background(), "pat@example.com", "acme",
				[]admin.ScopeAnchorInput{{Kind: auth.AnchorKindCustomer, Token: "acme-corp"}})
			return err
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			e := scopedEnv(t)
			ctx := context.Background()
			tier := &iam.TenantTier{Token: "gold"}
			require.NoError(t, e.store.CreateTenantTier(ctx, tier))
			require.NoError(t, e.store.CreateTenant(ctx, &iam.Tenant{Token: "globex", Enabled: true, TierID: tier.ID, PurgeState: iam.PurgeActive}))
			e.createMember(t, "pat@example.com", "pw-1")
			_, err := e.admin.AddMembership(ctx, "pat@example.com", "globex", nil)
			require.NoError(t, err)
			idTok, acme := e.login(t, "pat@example.com", "pw-1")
			globex, err := e.m.SelectTenant(ctx, idTok, "globex")
			require.NoError(t, err)

			require.NoError(t, change.do(e))

			next, err := e.m.Refresh(ctx, globex.RefreshToken)
			require.NoErrorf(t, err, "%s in acme signed the person out of globex", change.name)
			access, err := e.validator.Validate(next.AccessToken)
			require.NoError(t, err)
			require.Equal(t, "globex", access.Tenant)
			require.Nil(t, access.ScopeAnchors, "a change in acme reached the globex grant")
			_, err = e.m.SelectTenant(ctx, idTok, "globex")
			require.NoErrorf(t, err, "%s in acme ended the person's identity token", change.name)

			_, err = e.m.Refresh(ctx, acme.RefreshToken)
			if change.name == "scoped" {
				require.NoError(t, err, "a narrowed — not ended — membership must keep refreshing")
			} else {
				require.ErrorIsf(t, err, ErrInvalidToken, "a %s membership still refreshes into acme", change.name)
			}
		})
	}
}

// Widening is not a revocation: the refresh token survives, and the next one it mints
// carries the wider scope — read from the membership, never copied from the old token.
func TestWideningKeepsTheSessionAndRefreshPicksItUp(t *testing.T) {
	e := scopedEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	e.scopeMember(t, "pat@example.com", "acme-corp")
	_, pair := e.login(t, "pat@example.com", "pw-1")

	e.scopeMember(t, "pat@example.com", "acme-corp", "initech")
	next, err := e.m.Refresh(context.Background(), pair.RefreshToken)
	require.NoError(t, err, "widening a scope signed the member out")
	access, err := e.validator.Validate(next.AccessToken)
	require.NoError(t, err)
	require.Equal(t, []string{"customer:acme-corp", "customer:initech"}, access.ScopeAnchors)

	e.scopeMember(t, "pat@example.com")
	last, err := e.m.Refresh(context.Background(), next.RefreshToken)
	require.NoError(t, err)
	unscoped, err := e.validator.Validate(last.AccessToken)
	require.NoError(t, err)
	require.Nil(t, unscoped.ScopeAnchors, "clearing the scope left the member scoped")
}

// A stored scope that does not parse is a DENIAL, never the whole tenant.
func TestAnUnreadableStoredScopeIsRefusedNotMintedUnscoped(t *testing.T) {
	e := scopedEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	require.NoError(t, e.db().Exec(`UPDATE iam_memberships SET scope = ? WHERE tenant_id = 'acme'`, `["tenant:globex"]`).Error)

	ia, err := e.m.Login(context.Background(), "pat@example.com", "pw-1")
	require.NoError(t, err)
	_, err = e.m.SelectTenant(context.Background(), ia.IdentityToken, "acme")
	require.ErrorIs(t, err, ErrInvalidCredentials, "a membership whose scope cannot be read was let in")
}
