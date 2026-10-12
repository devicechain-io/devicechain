// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/admin"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// One canonical spelling of an email, at every door. The unicode and punycode spellings of a
// domain are the same domain, so they are the same mailbox, so they are the same identity.

const (
	idnUnicode  = "bücher.example"
	idnPunycode = "xn--bcher-kva.example"
)

func (e *ssoEnv) identityCount(t *testing.T, like string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, e.sysDB().Model(&iam.Identity{}).Where("email LIKE ?", like).Count(&n).Error)
	return n
}

func TestPasswordLoginAndCreateAgreeAcrossIdnaSpellings(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()

	// Created under the unicode spelling and a mixed-case local part...
	created, err := e.admin.CreateIdentity(ctx, admin.CreateIdentityInput{
		Email: "Ann@" + idnUnicode, Password: "pw-ann", Enabled: true,
	})
	require.NoError(t, err)
	require.Equal(t, "ann@"+idnPunycode, created.Email, "stored in the one canonical form")

	// ...every spelling signs in as that one identity...
	for _, spelling := range []string{"ann@" + idnUnicode, "ANN@" + idnPunycode, " Ann@BÜCHER.example. "} {
		got, err := e.m.Login(ctx, spelling, "pw-ann")
		require.NoErrorf(t, err, "login as %q", spelling)
		require.NotEmpty(t, got.IdentityToken)
	}

	// ...and a second creation under the OTHER spelling is a duplicate, not a second identity.
	_, err = e.admin.CreateIdentity(ctx, admin.CreateIdentityInput{
		Email: "ann@" + idnPunycode, Password: "pw-2", Enabled: true,
	})
	require.Error(t, err)
	require.EqualValues(t, 1, e.identityCount(t, "%@"+idnPunycode))
}

func TestInvalidIdnaEmailIsRefusedEverywhere(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	for _, bad := range []string{
		"ann@xn--a.example", // not valid punycode
		"ann@exa mple.com",  // space in the domain
		"ann@exa_mple.com",  // not a hostname character (STD3)
		"ann@@example.com",  // two @
		"ann",               // no @
		"@example.com",      // no local part
		"ann@",              // no domain
		"ann@example.com..", // empty label
		"ann@-bad-.example", // label edge hyphens
		"ann@" + string([]byte{0xff}) + ".example", // not UTF-8
	} {
		_, err := iam.CanonicalEmail(bad)
		require.ErrorIsf(t, err, iam.ErrEmailInvalid, "%q", bad)

		_, err = e.admin.CreateIdentity(ctx, admin.CreateIdentityInput{Email: bad, Password: "x", Enabled: true})
		require.ErrorIsf(t, err, iam.ErrEmailInvalid, "create %q", bad)

		_, err = e.m.Login(ctx, bad, "x")
		require.ErrorIsf(t, err, ErrInvalidCredentials, "login %q", bad)

		require.ErrorIsf(t, e.m.LinkIdentity(ctx, bad, "acme-idp", "sub"), ErrIdpInvalid, "link %q", bad)
		_, err = e.m.RevokeSessions(ctx, bad)
		require.ErrorIsf(t, err, ErrEmailInvalid, "revoke %q", bad)
	}
	// The store is the last line: a caller that forgot to canonicalise cannot insert.
	err := e.store.CreateIdentity(ctx, &iam.Identity{Email: "Ann@" + idnUnicode, Enabled: true})
	require.ErrorIs(t, err, iam.ErrEmailNotCanonical)
	// ...and the same holds on the two other paths that insert an identity.
	err = e.store.CreateProvisionedIdentity(ctx, &iam.Identity{Email: "Ann@" + idnUnicode, Enabled: true}, &iam.IdentityLink{Subject: "sub-ann"})
	require.ErrorIs(t, err, iam.ErrEmailNotCanonical, "provisioning")
	err = e.store.SeedSuperuser(ctx, "Su@"+idnUnicode, "hash", nil, nil)
	require.ErrorIs(t, err, iam.ErrEmailNotCanonical, "superuser seed")
	require.EqualValues(t, 0, e.identityCount(t, "%@"+idnUnicode))
}

func TestSsoCallbackResolvesTheSameIdentityAcrossIdnaSpellings(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	// The connection asserts the domain in its UNICODE spelling; it is stored as punycode.
	e.addConnection(t, "idn-idp", iam.IdpKindTenant, idnUnicode, "acme")
	e.ssoIdentity(t, "kim@"+idnPunycode, "idn-idp", "sub-kim", "acme")

	// The provider sends the address in whichever spelling it likes: same identity each time.
	for _, claim := range []string{"kim@" + idnUnicode, "KIM@" + idnPunycode, "Kim@BÜCHER.EXAMPLE"} {
		got, _ := e.signInOK(t, "idn-idp", "sub-kim", claim)
		require.NotEmptyf(t, got.IdentityToken, "claim %q", claim)
	}
	require.EqualValues(t, 1, e.identityCount(t, "%@"+idnPunycode))

	// The begin step routes by email domain through the same normaliser.
	for _, typed := range []string{"kim@" + idnUnicode, "kim@" + idnPunycode} {
		d, err := emailDomain(typed)
		require.NoError(t, err)
		require.Equal(t, idnPunycode, d)
		conn, err := e.store.IdpConnectionByDomain(ctx, d)
		require.NoError(t, err)
		require.Equal(t, "idn-idp", conn.Token)
	}
}

func TestJitDoesNotCreateASecondIdentityAcrossIdnaSpellings(t *testing.T) {
	e := newSsoEnv(t)
	e.addConnection(t, "idn-idp", iam.IdpKindTenant, idnUnicode, "acme")
	e.jit(t, "idn-idp", true)

	// First sign-in provisions, stored canonically.
	e.signInOK(t, "idn-idp", "sub-lee", "lee@"+idnUnicode)
	require.EqualValues(t, 1, e.identityCount(t, "%@"+idnPunycode))
	_, err := e.store.IdentityByEmail(context.Background(), "lee@"+idnPunycode)
	require.NoError(t, err)

	// The same person arrives under the other spelling: the linked identity, no second row.
	e.signInOK(t, "idn-idp", "sub-lee", "lee@"+idnPunycode)
	require.EqualValues(t, 1, e.identityCount(t, "%@"+idnPunycode))
	require.EqualValues(t, 1, e.auditCount(t, rdb.AuditOpIdentityProvisioned), "provisioned exactly once")

	// A password identity made under one spelling is not shadowed by a JIT identity made
	// under the other: provisioning refuses (link request), it never creates a second one.
	e.ssoIdentity(t, "max@"+idnPunycode, "", "", "acme")
	l, _, _, _ := e.signIn(t, "idn-idp", "sub-max", "max@"+idnUnicode)
	e.refused(t, l, "idn-idp", SsoOutcomeRefusedLinkRequired)
	require.EqualValues(t, 1, e.identityCount(t, "max@%"))
}

func TestLinkRequestMatchesAcrossIdnaSpellings(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.addConnection(t, "idn-idp", iam.IdpKindTenant, idnUnicode, "acme")
	existing := e.ssoIdentity(t, "ola@"+idnPunycode, "", "", "acme")

	l, _, _, _ := e.signIn(t, "idn-idp", "sub-ola", "Ola@"+idnUnicode)
	e.refused(t, l, "idn-idp", SsoOutcomeRefusedLinkRequired)

	views, err := e.m.ListSsoLinkRequests(ctx)
	require.NoError(t, err)
	require.Len(t, views, 1)
	require.Equal(t, "ola@"+idnPunycode, views[0].Request.Email)
	require.True(t, views[0].IdentityExists, "the request found the existing identity")
	require.NoError(t, e.m.ApproveSsoLinkRequest(ctx, views[0].Request.ID, DispositionConvert))
	linked, err := e.store.LinksForIdentity(ctx, existing.ID)
	require.NoError(t, err)
	require.Len(t, linked, 1)

	// An administrator linking by the unicode spelling reaches the same identity.
	e.ssoIdentity(t, "pia@"+idnPunycode, "", "", "acme")
	require.NoError(t, e.m.LinkIdentity(ctx, "PIA@"+idnUnicode, "idn-idp", "sub-pia"))
	_, err = e.m.UnlinkIdentity(ctx, "pia@"+idnUnicode, "idn-idp")
	require.NoError(t, err)
}

// Every door that takes an address from a person -- the two sign-in starts and each
// administrator action -- is driven through its CALLER with the unicode spelling, so a
// caller that computed its own form instead of the canonical one fails here.
func TestEveryDoorReachesTheIdentityByItsUnicodeSpelling(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.addConnection(t, "idn-idp", iam.IdpKindTenant, idnUnicode, "acme")
	e.ssoIdentity(t, "rae@"+idnPunycode, "idn-idp", "sub-rae", "acme")
	_, challenge := verifierPair()

	// Both sign-in starts route a unicode-spelled address to the connection.
	b, err := e.m.BeginSso(ctx, BeginSsoInput{Email: "Rae@BÜCHER.example", ReturnTo: ssoTestReturnTo, CodeChallenge: challenge}, e.source())
	require.NoError(t, err)
	require.NotNil(t, b, "beginSso by the unicode spelling finds the connection")
	e.beginAuthz(t, "rae@"+idnUnicode)

	// Each administrator action finds the one identity.
	_, err = e.m.RevokeSessions(ctx, "Rae@"+idnUnicode)
	require.NoError(t, err, "revoke")
	_, err = e.m.ClearPassword(ctx, "rae@BÜCHER.example")
	require.NoError(t, err, "clear password")
	_, err = e.admin.SetIdentityEnabled(ctx, "RAE@"+idnUnicode, false)
	require.NoError(t, err, "disable")
	deleted, err := e.admin.DeleteIdentity(ctx, "rae@"+idnUnicode)
	require.NoError(t, err)
	require.True(t, deleted, "delete by the unicode spelling removes the identity")
	require.EqualValues(t, 0, e.identityCount(t, "%@"+idnPunycode))
}

func TestVerifiedDomainMatchingUsesTheEmailsCanonicalForm(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.addConnection(t, "idn-idp", iam.IdpKindTenant, idnUnicode, "acme")

	// The one definition: a domain asserted in either spelling is the stored punycode, and the
	// domain of an address in either spelling is that same string.
	for _, spelling := range []string{idnUnicode, idnPunycode, "BÜCHER.Example.", "Xn--BCHER-kva.example"} {
		d, err := NormalizeDomain(spelling)
		require.NoError(t, err)
		require.Equal(t, idnPunycode, d)
		c, err := iam.CanonicalEmail("x@" + spelling)
		require.NoError(t, err)
		require.Equal(t, idnPunycode, iam.EmailDomain(c))
	}

	// Asserting the other spelling for another connection collides: it is ONE assertion.
	e.addTenant(t, "other")
	other := e.addConnection(t, "other-idp", iam.IdpKindTenant, "", "other")
	_, err := e.m.AddIdpDomain(ctx, other.Token, idnPunycode)
	require.Error(t, err, "the second spelling of an asserted domain is refused")

	// An identity provider's claim at the unicode spelling is routed by that connection's domain.
	conn, err := e.store.IdpConnectionByDomain(ctx, idnPunycode)
	require.NoError(t, err)
	require.Equal(t, "idn-idp", conn.Token)
	_, err = e.store.IdentityByEmail(ctx, "nobody@"+idnPunycode)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
