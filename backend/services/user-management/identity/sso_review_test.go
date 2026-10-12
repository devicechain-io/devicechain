// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"testing"

	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

// Properties the first round of tests left to other checks, found by mutation: each test
// here fails when the one check it names is removed, because nothing else in the flow
// refuses the same input.

// The kind check is the ONLY thing that refuses a record whose every other field is right.
// A record moved between buckets is also refused by its missing client or challenge, which
// is why planting a foreign record does not show that the kind is read at all: here the
// record is the redeemer's own, valid in every field, relabelled.
func TestRedeemersRefuseAMislabelledRecordOnKindAlone(t *testing.T) {
	e := newSsoEnv(t)
	e.linkedPat(t)
	ctx := context.Background()

	relabel := func(t *testing.T, raw []byte, kind string) []byte {
		t.Helper()
		var rec map[string]any
		require.NoError(t, json.Unmarshal(raw, &rec))
		rec["kind"] = kind
		b, err := json.Marshal(rec)
		require.NoError(t, err)
		return b
	}

	// The OAuth redeemer, handed its own authorization code labelled as a login code.
	e.createMember(t, "oauth@example.com", "pw-1")
	ia, err := e.m.Login(ctx, "oauth@example.com", "pw-1")
	require.NoError(t, err)
	code, err := e.issueCode(t, ia.IdentityToken)
	require.NoError(t, err)
	entry, err := e.m.codesKV.Get(code)
	require.NoError(t, err)
	copyKey := b64(randBytes(32))
	_, err = e.m.codesKV.Create(copyKey, relabel(t, entry.Value(), ssoKindLogin))
	require.NoError(t, err)
	_, err = e.redeem(copyKey)
	assertOAuthErrorCode(t, err, "invalid_grant")
	_, err = e.redeem(code)
	require.NoError(t, err, "control: the same record under its own kind redeems")

	// The single sign-on redeemer, handed its own login code labelled as another kind.
	l, verifier, _, _ := e.signIn(t, "acme-idp", "sub-pat", linkedEmail)
	require.NotEmpty(t, l.Code)
	sentry, err := e.m.ssoCodesKV.Get(l.Code)
	require.NoError(t, err)
	for _, kind := range []string{ssoKindAuthorize, ssoKindLink, authCodeKind} {
		k := b64(randBytes(32))
		_, err = e.m.ssoCodesKV.Create(k, relabel(t, sentry.Value(), kind))
		require.NoError(t, err)
		_, err = e.m.CompleteSso(ctx, k, verifier)
		require.ErrorIs(t, err, ErrSsoCodeInvalid, "kind %q", kind)
	}
	_, err = e.m.CompleteSso(ctx, l.Code, verifier)
	require.NoError(t, err, "control: the same record under its own kind completes")
}

// An instance connection is a credential for the whole instance, so it must carry a
// multi-factor rule from creation on, keep it, and apply it to EVERY sign-in through it.
func TestInstanceConnectionMfaRuleIsRequiredKeptAndEnforced(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	base := IdpConnectionInput{
		Name: "ops", Kind: iam.IdpKindInstance, Issuer: e.op.url(), ClientID: ssoTestClientID,
		ClientSecret: ssoTestSecret,
	}

	// Created with neither required methods nor the acknowledgement: refused.
	bare := base
	bare.Token = "ops-bare"
	_, err := e.m.CreateIdpConnection(ctx, bare)
	require.ErrorIs(t, err, ErrIdpInvalid)
	// A TENANT connection has no such rule (control: the refusal above is the instance rule).
	tenant := base
	tenant.Token, tenant.Kind = "tenant-bare", iam.IdpKindTenant
	_, err = e.m.CreateIdpConnection(ctx, tenant)
	require.NoError(t, err)

	// Created with a required method: accepted, and the method cannot be cleared.
	mfa := base
	mfa.Token, mfa.RequiredAmr = "ops-mfa", []string{"mfa"}
	d, err := e.m.CreateIdpConnection(ctx, mfa)
	require.NoError(t, err)
	_, err = e.m.UpdateIdpConnection(ctx, "ops-mfa", &IdpConnectionUpdateRequest{RequiredAmr: optList()})
	require.ErrorIs(t, err, ErrIdpInvalid, "an instance connection cannot drop its multi-factor rule")

	_, err = e.m.ApproveIdpDiscovery(ctx, "ops-mfa", d.Fingerprint)
	require.NoError(t, err)
	_, err = e.m.UpdateIdpConnection(ctx, "ops-mfa", &IdpConnectionUpdateRequest{Enabled: optBool(true)})
	require.NoError(t, err)
	e.ssoIdentity(t, "root@ops.example", "", "")
	e.grantSuperuser(t, "root@ops.example")
	require.NoError(t, e.m.LinkIdentity(ctx, "root@ops.example", "ops-mfa", "sub-root"))

	// A linked operator whose provider did not report the method is refused...
	l, _, _, _ := e.signIn(t, "ops-mfa", "sub-root", "root@ops.example")
	e.refused(t, l, "ops-mfa", SsoOutcomeRefusedRequiredClaim)
	e.op.mutate = func(c jwt.MapClaims) { c["amr"] = []string{"pwd"} }
	l, _, _, _ = e.signIn(t, "ops-mfa", "sub-root", "root@ops.example")
	e.refused(t, l, "ops-mfa", SsoOutcomeRefusedRequiredClaim)
	// ...and signs in when it did.
	e.op.mutate = func(c jwt.MapClaims) { c["amr"] = []string{"pwd", "mfa"} }
	got, _ := e.signInOK(t, "ops-mfa", "sub-root", "root@ops.example")
	require.True(t, got.Superuser)
}

// A sign-in begun against an approved snapshot must not finish against one nobody approved:
// the callback judges the connection as it is when the provider redirects back, and a
// refresh that moved an endpoint in between withdraws the approval the begin relied on.
func TestDiscoveryChangedMidSignInRefusesTheCallback(t *testing.T) {
	e := newSsoEnv(t)
	e.linkedPat(t)
	ctx := context.Background()

	verifier, challenge := verifierPair()
	_ = verifier
	begin, err := e.m.BeginSso(ctx, BeginSsoInput{Connection: "acme-idp", ReturnTo: ssoTestReturnTo, CodeChallenge: challenge}, e.source())
	require.NoError(t, err)
	state, code := e.op.authorize(begin.AuthorizationURL, "sub-pat", linkedEmail)

	e.op.authorizePath = "/moved/authorize"
	moved, err := e.m.RefreshIdpDiscovery(ctx, "acme-idp")
	require.NoError(t, err)
	require.Nil(t, moved.Connection.DiscoveryApprovedAt)
	require.True(t, e.reloadConn(t, "acme-idp").Enabled, "the connection stays enabled; only its approval is withdrawn")

	hits := e.op.tokenHits.Load()
	l := e.callback(t, "acme-idp", url.Values{"state": {state}, "code": {code}})
	e.refused(t, l, "acme-idp", SsoOutcomeRefusedConnectionUnusable)
	require.Equal(t, hits, e.op.tokenHits.Load(), "no code is exchanged at an endpoint from an unapproved snapshot")

	// Control: once the new snapshot is approved, a fresh sign-in completes.
	_, err = e.m.ApproveIdpDiscovery(ctx, "acme-idp", moved.Fingerprint)
	require.NoError(t, err)
	e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
}

// The login code outlives the callback by up to a minute. A connection disabled, or an
// identity unlinked, in that minute starts no session -- for an instance connection that
// session's identity token would carry system authorities to the admin plane, which
// admitTenant never sees.
func TestCompleteRejudgesTheConnectionAndTheLink(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		revoke func(e *ssoEnv) error
	}{
		{"connection disabled", func(e *ssoEnv) error {
			_, err := e.m.UpdateIdpConnection(ctx, "ops-idp", &IdpConnectionUpdateRequest{Enabled: optBool(false)})
			return err
		}},
		{"identity unlinked", func(e *ssoEnv) error {
			ok, err := e.m.UnlinkIdentity(ctx, "root@ops.example", "ops-idp")
			if err == nil && !ok {
				err = errors.New("nothing was unlinked")
			}
			return err
		}},
		{"discovery approval withdrawn", func(e *ssoEnv) error {
			e.op.authorizePath = "/moved/authorize"
			_, err := e.m.RefreshIdpDiscovery(ctx, "ops-idp")
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newSsoEnv(t)
			e.addConnection(t, "ops-idp", iam.IdpKindInstance, "")
			e.ssoIdentity(t, "root@ops.example", "", "")
			e.grantSuperuser(t, "root@ops.example")
			require.NoError(t, e.m.LinkIdentity(ctx, "root@ops.example", "ops-idp", "sub-root"))

			// Control: the same flow, unrevoked, completes.
			e.signInOK(t, "ops-idp", "sub-root", "root@ops.example")

			l, verifier, _, _ := e.signIn(t, "ops-idp", "sub-root", "root@ops.example")
			require.NotEmpty(t, l.Code)
			require.NoError(t, tc.revoke(e))
			_, err := e.m.CompleteSso(ctx, l.Code, verifier)
			require.ErrorIs(t, err, ErrSsoCodeInvalid)
		})
	}
}
