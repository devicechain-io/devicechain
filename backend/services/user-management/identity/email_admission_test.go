// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// An identity whose STORED email is not canonical (written before the canonical form
// existed) authenticates nowhere, and its existing sessions end at their next exchange --
// while an administrator can still find, revoke and delete it.

const legacyEmail = "old@" + idnUnicode // the pre-canonical spelling: unicode domain

// seedLegacyIdentity inserts an identity around the store's canonical-email guard, the way
// a row written by an older build looks, with a password, a membership in acme and a link
// on idn-idp.
func (e *ssoEnv) seedLegacyIdentity(t *testing.T) *iam.Identity {
	t.Helper()
	ctx := context.Background()
	conn := e.addConnection(t, "idn-idp", iam.IdpKindTenant, idnUnicode, "acme")
	hash, err := bcrypt.GenerateFromPassword([]byte("pw-old"), bcrypt.MinCost)
	require.NoError(t, err)
	id := &iam.Identity{Email: legacyEmail, Enabled: true, PasswordHash: string(hash)}
	require.NoError(t, e.sysDB().Create(id).Error, "raw insert, bypassing the store's guard")
	require.NoError(t, e.store.CreateIdentityLink(ctx, &iam.IdentityLink{
		IdentityID: id.ID, ConnectionID: conn.ID, Subject: "sub-old", CreatedBy: "test",
	}))
	// Adding the membership goes through the administrator's lookup: already repairable.
	_, err = e.admin.AddMembership(ctx, legacyEmail, "acme", nil)
	require.NoError(t, err)
	got, err := e.store.IdentityByEmail(ctx, legacyEmail)
	require.NoError(t, err)
	return got
}

// legacySession opens a session for the legacy identity as a build from before the
// canonical form would have: an identity token and a tenant refresh pair.
func (e *ssoEnv) legacySession(t *testing.T, id *iam.Identity) (identityToken string, pair *TokenPair) {
	t.Helper()
	sid, err := e.m.startSession(id.Email, auth.AmrPasswordOnly())
	require.NoError(t, err)
	tok, err := e.m.issuer.IssueIdentity(id.Email, auth.SessionEpoch(id.SessionEpoch), sid, nil, nil, uuid.NewString())
	require.NoError(t, err)
	pair, err = e.m.issueTenantTokens("acme", id.Email, auth.SessionEpoch(id.SessionEpoch), sid, nil, nil, auth.Scope{}, false, nil)
	require.NoError(t, err)
	return tok.Token, pair
}

func TestNonCanonicalStoredEmailAuthenticatesNowhere(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	id := e.seedLegacyIdentity(t)
	identityToken, pair := e.legacySession(t, id)

	// Password sign-in, by the stored spelling and by the canonical one: refused like any
	// other failed sign-in, never saying whether the password was right.
	for _, spelling := range []string{legacyEmail, "old@" + idnPunycode} {
		_, err := e.m.Login(ctx, spelling, "pw-old")
		require.ErrorIsf(t, err, ErrInvalidCredentials, "login as %q", spelling)
	}

	// Single sign-on through its link: the callback resolves it by subject, the completion
	// refuses it with the typed error.
	l, verifier, _, _ := e.signIn(t, "idn-idp", "sub-old", legacyEmail)
	require.NotEmpty(t, l.Code)
	_, err := e.m.CompleteSso(ctx, l.Code, verifier)
	require.ErrorIs(t, err, ErrIdentityEmailNotCanonical, "sso complete")

	// Its existing session ends at the next exchange, with the typed refusal.
	_, err = e.m.SelectTenant(ctx, identityToken, "acme")
	require.ErrorIs(t, err, ErrIdentityEmailNotCanonical, "select tenant")
	_, err = e.m.Memberships(ctx, identityToken)
	require.ErrorIs(t, err, ErrIdentityEmailNotCanonical, "memberships")
	// Linking says what is wrong, not "sign in with a password", which cannot help.
	_, err = e.m.BeginSsoLink(ctx, identityToken, ssoTestReturnTo, e.source())
	require.ErrorIs(t, err, ErrIdentityEmailNotCanonical, "begin sso link")
	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, ErrIdentityEmailNotCanonical, "refresh")
	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken, "the refused refresh token was burned")

	ext, ok := ErrIdentityEmailNotCanonical.(interface{ Extensions() map[string]any })
	require.True(t, ok)
	require.Equal(t, CodeIdentityEmailNotCanonical, ext.Extensions()["code"])
}

func TestNonCanonicalStoredEmailIsRepairableByAnAdministrator(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.seedLegacyIdentity(t)

	// Found by the stored spelling, in any case.
	_, err := e.admin.SetIdentityEnabled(ctx, "OLD@BÜCHER.example", true)
	require.NoError(t, err, "find")
	_, err = e.m.RevokeSessions(ctx, legacyEmail)
	require.NoError(t, err, "revoke")
	_, err = e.m.ClearPassword(ctx, legacyEmail)
	require.NoError(t, err, "clear password")
	removed, err := e.m.UnlinkIdentity(ctx, legacyEmail, "idn-idp")
	require.NoError(t, err)
	require.True(t, removed, "unlink frees the subject for the replacement identity")
	deleted, err := e.admin.DeleteIdentity(ctx, legacyEmail)
	require.NoError(t, err)
	require.True(t, deleted, "delete")
	require.EqualValues(t, 0, e.identityCount(t, "old@%"))

	// Recreated in canonical form, the person signs in again through the same link.
	e.ssoIdentity(t, "old@"+idnPunycode, "idn-idp", "sub-old", "acme")
	got, _ := e.signInOK(t, "idn-idp", "sub-old", legacyEmail)
	require.NotEmpty(t, got.IdentityToken)
	tp, err := e.m.SelectTenant(ctx, got.IdentityToken, "acme")
	require.NoError(t, err, "a canonical identity is unaffected")
	_, err = e.m.Refresh(ctx, tp.RefreshToken)
	require.NoError(t, err)
}
