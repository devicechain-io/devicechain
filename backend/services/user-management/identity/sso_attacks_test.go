// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/sourcelimit"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/golang-jwt/jwt/v5"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// Every attack the design names, driven through the real callback handler against a stub
// provider. Each refusal below is paired with a control: the same sign-in, unmodified,
// completes. Without it a refusal proves nothing -- a flow that is broken for an unrelated
// reason refuses too.

const linkedEmail = "pat@" + ssoTestDomain

// refused asserts a callback ended in a refusal: a redirect carrying an error class and no
// login code, counted under outcome.
func (e *ssoEnv) refused(t *testing.T, l ssoLanding, conn, outcome string) {
	t.Helper()
	require.Equal(t, http.StatusFound, l.Status)
	require.NotEmpty(t, l.Error, "the sign-in was not refused")
	require.Empty(t, l.Code, "a refused sign-in must not mint a login code")
	require.GreaterOrEqual(t, e.metrics.count(conn, outcome), 1, "outcome %s was not counted", outcome)
}

func (e *ssoEnv) linkedPat(t *testing.T) {
	t.Helper()
	e.ssoIdentity(t, linkedEmail, "acme-idp", "sub-pat", "acme")
}

// ---- the ID token ----------------------------------------------------------------------

func tokenAttack(t *testing.T, name string, mutate func(c jwt.MapClaims), configure func(op *stubOP)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		e := newSsoEnv(t)
		e.linkedPat(t)

		// Control: the unmodified token signs in.
		e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)

		e.op.mutate = mutate
		if configure != nil {
			configure(e.op)
		}
		l, _, _, _ := e.signIn(t, "acme-idp", "sub-pat", linkedEmail)
		e.refused(t, l, "acme-idp", SsoOutcomeRefusedTokenInvalid)
	})
}

func TestCallbackRejectsWrongIss(t *testing.T) {
	tokenAttack(t, "wrong issuer", func(c jwt.MapClaims) { c["iss"] = "https://evil.example" }, nil)
	tokenAttack(t, "issuer with a trailing slash", func(c jwt.MapClaims) { c["iss"] = c["iss"].(string) + "/" }, nil)
}

func TestCallbackRejectsNonceMismatch(t *testing.T) {
	tokenAttack(t, "another sign-in's nonce", func(c jwt.MapClaims) { c["nonce"] = "nonce-of-someone-else" }, nil)
	tokenAttack(t, "no nonce", func(c jwt.MapClaims) { delete(c, "nonce") }, nil)
}

func TestCallbackRejectsAzpMismatch(t *testing.T) {
	tokenAttack(t, "azp is another client", func(c jwt.MapClaims) {
		c["aud"] = []string{ssoTestClientID, "other-client"}
		c["azp"] = "other-client"
	}, nil)
	tokenAttack(t, "azp names another client on a single audience", func(c jwt.MapClaims) { c["azp"] = "other-client" }, nil)
	tokenAttack(t, "several audiences and no azp", func(c jwt.MapClaims) {
		c["aud"] = []string{ssoTestClientID, "other-client"}
	}, nil)
}

func TestAudienceMustContainTheClient(t *testing.T) {
	tokenAttack(t, "audience is another client", func(c jwt.MapClaims) { c["aud"] = "other-client" }, nil)
}

func TestAlgNoneRefused(t *testing.T) {
	tokenAttack(t, "alg none", nil, func(op *stubOP) { op.method = jwt.SigningMethodNone })
	// The shared client secret is known to both ends; a token MACed with it must not verify.
	tokenAttack(t, "HS256 with the client secret", nil, func(op *stubOP) { op.method = jwt.SigningMethodHS256 })
	tokenAttack(t, "signed by a key the provider does not publish", nil, func(op *stubOP) {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)
		op.signKey = k
	})
}

func TestExpiredIDTokenRefused(t *testing.T) {
	tokenAttack(t, "expired", func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Minute).Unix() }, nil)
	tokenAttack(t, "not yet valid", func(c jwt.MapClaims) { c["nbf"] = time.Now().Add(time.Hour).Unix() }, nil)
	tokenAttack(t, "no subject", func(c jwt.MapClaims) { delete(c, "sub") }, nil)
}

func TestNoIDTokenInTheResponseIsRefused(t *testing.T) {
	e := newSsoEnv(t)
	e.linkedPat(t)
	e.op.omitIDToken = true
	l, _, _, _ := e.signIn(t, "acme-idp", "sub-pat", linkedEmail)
	e.refused(t, l, "acme-idp", SsoOutcomeRefusedTokenInvalid)
}

func TestRfc9207IssuerParameterMustMatch(t *testing.T) {
	e := newSsoEnv(t)
	e.linkedPat(t)
	verifier, challenge := verifierPair()
	begin, err := e.m.BeginSso(context.Background(), BeginSsoInput{Connection: "acme-idp", ReturnTo: ssoTestReturnTo, CodeChallenge: challenge}, e.source())
	require.NoError(t, err)
	state, code := e.op.authorize(begin.AuthorizationURL, "sub-pat", linkedEmail)
	l := e.callback(t, "acme-idp", url.Values{"state": {state}, "code": {code}, "iss": {"https://evil.example"}})
	e.refused(t, l, "acme-idp", SsoOutcomeRefusedIssuerMismatch)

	// Control: the connection's own issuer in the parameter passes.
	begin, err = e.m.BeginSso(context.Background(), BeginSsoInput{Connection: "acme-idp", ReturnTo: ssoTestReturnTo, CodeChallenge: challenge}, e.source())
	require.NoError(t, err)
	state, code = e.op.authorize(begin.AuthorizationURL, "sub-pat", linkedEmail)
	l = e.callback(t, "acme-idp", url.Values{"state": {state}, "code": {code}, "iss": {e.op.url()}})
	require.NotEmpty(t, l.Code)
	_, err = e.m.CompleteSso(context.Background(), l.Code, verifier)
	require.NoError(t, err)
}

func TestProviderErrorIsReportedNotSignedIn(t *testing.T) {
	e := newSsoEnv(t)
	_, challenge := verifierPair()
	begin, err := e.m.BeginSso(context.Background(), BeginSsoInput{Connection: "acme-idp", ReturnTo: ssoTestReturnTo, CodeChallenge: challenge}, e.source())
	require.NoError(t, err)
	state, _ := e.op.authorize(begin.AuthorizationURL, "sub", linkedEmail)
	l := e.callback(t, "acme-idp", url.Values{"state": {state}, "error": {"access_denied"}})
	require.Equal(t, "idp_error", l.Error)
	require.Empty(t, l.Code)
	require.Equal(t, 1, e.metrics.count("acme-idp", SsoOutcomeIdpError))
}

// ---- the state -------------------------------------------------------------------------

func TestStateIsSingleUse(t *testing.T) {
	e := newSsoEnv(t)
	e.linkedPat(t)
	l, _, state, code := e.signIn(t, "acme-idp", "sub-pat", linkedEmail)
	require.NotEmpty(t, l.Code, "control: the first delivery signs in")

	replay := e.callback(t, "acme-idp", url.Values{"state": {state}, "code": {code}})
	require.Equal(t, http.StatusBadRequest, replay.Status, "a replayed callback has nowhere trustworthy to go")
	require.Nil(t, replay.Location)
	require.Equal(t, 1, e.metrics.count("unknown", SsoOutcomeStateInvalid))
	require.Equal(t, int32(1), e.op.tokenHits.Load(), "a replayed state must not reach the provider again")
}

func TestStateForConnectionXRejectedOnPathY(t *testing.T) {
	e := newSsoEnv(t)
	e.linkedPat(t)
	e.addConnection(t, "other-idp", iam.IdpKindTenant, "other-corp.com", "acme")

	_, challenge := verifierPair()
	begin, err := e.m.BeginSso(context.Background(), BeginSsoInput{Connection: "acme-idp", ReturnTo: ssoTestReturnTo, CodeChallenge: challenge}, e.source())
	require.NoError(t, err)
	state, code := e.op.authorize(begin.AuthorizationURL, "sub-pat", linkedEmail)

	// The redirect_uri registered for acme-idp is its own path; delivering its state at
	// other-idp's path is a mix-up and finds nothing usable.
	l := e.callback(t, "other-idp", url.Values{"state": {state}, "code": {code}})
	require.Equal(t, http.StatusBadRequest, l.Status)
	require.Equal(t, 1, e.metrics.count("other-idp", SsoOutcomeStateInvalid))
	require.Zero(t, e.op.tokenHits.Load(), "the code must never be exchanged for a mismatched connection")

	// The state was consumed by the mismatch: the right path cannot use it afterwards.
	l = e.callback(t, "acme-idp", url.Values{"state": {state}, "code": {code}})
	require.Equal(t, http.StatusBadRequest, l.Status)
}

func TestUnknownConnectionPathIsNeverALabel(t *testing.T) {
	e := newSsoEnv(t)
	l := e.callback(t, "attacker-chosen-name", url.Values{"state": {"x"}, "code": {"y"}})
	require.Equal(t, http.StatusBadRequest, l.Status)
	require.Equal(t, 1, e.metrics.count("unknown", SsoOutcomeStateInvalid))
	require.Zero(t, e.metrics.count("attacker-chosen-name", SsoOutcomeStateInvalid),
		"a path segment is request text; using it as a metric label would be unbounded")
}

// ---- the login code --------------------------------------------------------------------

func TestCompleteRequiresVerifier(t *testing.T) {
	e := newSsoEnv(t)
	e.linkedPat(t)

	// A code planted in another browser (login CSRF): that browser holds a different secret.
	l, verifier, _, _ := e.signIn(t, "acme-idp", "sub-pat", linkedEmail)
	attackerSecret, _ := verifierPair()
	_, err := e.m.CompleteSso(context.Background(), l.Code, attackerSecret)
	require.ErrorIs(t, err, ErrSsoCodeInvalid)
	// The attempt burned the code, so the right browser cannot finish either: one try.
	_, err = e.m.CompleteSso(context.Background(), l.Code, verifier)
	require.ErrorIs(t, err, ErrSsoCodeInvalid)

	// Control: with the verifier, once.
	l, verifier, _, _ = e.signIn(t, "acme-idp", "sub-pat", linkedEmail)
	_, err = e.m.CompleteSso(context.Background(), l.Code, verifier)
	require.NoError(t, err)
	_, err = e.m.CompleteSso(context.Background(), l.Code, verifier)
	require.ErrorIs(t, err, ErrSsoCodeInvalid, "a code works once")
}

func TestSsoCodeRefusedByOAuthRedeemerAndTheReverse(t *testing.T) {
	e := newSsoEnv(t)
	e.linkedPat(t)
	ctx := context.Background()

	l, verifier, _, _ := e.signIn(t, "acme-idp", "sub-pat", linkedEmail)
	require.NotEmpty(t, l.Code)
	// The SSO login code, presented as an OAuth authorization code.
	_, err := e.m.RedeemAuthorizationCode(ctx, l.Code, sessClient, sessRedirect, sessVerifier)
	assertOAuthErrorCode(t, err, "invalid_grant")
	// ...and the same code is still there for its own redeemer (it was a different bucket).
	_, err = e.m.CompleteSso(ctx, l.Code, verifier)
	require.NoError(t, err)

	// An OAuth authorization code, presented as a login code.
	e.createMember(t, "oauth@example.com", "pw-1")
	ia, err := e.m.Login(ctx, "oauth@example.com", "pw-1")
	require.NoError(t, err)
	oauthCode, err := e.issueCode(t, ia.IdentityToken)
	require.NoError(t, err)
	_, err = e.m.CompleteSso(ctx, oauthCode, sessVerifier)
	require.ErrorIs(t, err, ErrSsoCodeInvalid)
}

// If a record of the OTHER kind ever reached a bucket (a misconfigured deployment pointing
// both at one store, a future bug), the redeemer must still refuse it: the two share field
// names, and a record read as the wrong kind would be the type confusion.
func TestEachRedeemerChecksTheRecordKindFirst(t *testing.T) {
	e := newSsoEnv(t)
	e.linkedPat(t)
	ctx := context.Background()

	l, verifier, _, _ := e.signIn(t, "acme-idp", "sub-pat", linkedEmail)
	require.NotEmpty(t, l.Code)
	entry, err := e.m.ssoCodesKV.Get(l.Code)
	require.NoError(t, err)
	// Plant the SSO login code record where OAuth codes live.
	_, err = e.m.codesKV.Create(l.Code, entry.Value())
	require.NoError(t, err)
	_, err = e.m.RedeemAuthorizationCode(ctx, l.Code, sessClient, sessRedirect, verifier)
	assertOAuthErrorCode(t, err, "invalid_grant")

	// And the OAuth record where SSO codes live.
	e.createMember(t, "oauth@example.com", "pw-1")
	ia, err := e.m.Login(ctx, "oauth@example.com", "pw-1")
	require.NoError(t, err)
	oauthCode, err := e.issueCode(t, ia.IdentityToken)
	require.NoError(t, err)
	oentry, err := e.m.codesKV.Get(oauthCode)
	require.NoError(t, err)
	_, err = e.m.ssoCodesKV.Create(oauthCode, oentry.Value())
	require.NoError(t, err)
	_, err = e.m.CompleteSso(ctx, oauthCode, sessVerifier)
	require.ErrorIs(t, err, ErrSsoCodeInvalid)
}

// ---- the return address ----------------------------------------------------------------

func TestReturnToRejectsDotSegmentsAndProtocolRelative(t *testing.T) {
	e := newSsoEnv(t)
	_, challenge := verifierPair()
	begin := func(returnTo string) error {
		_, err := e.m.BeginSso(context.Background(), BeginSsoInput{Connection: "acme-idp", ReturnTo: returnTo, CodeChallenge: challenge}, e.source())
		return err
	}
	require.NoError(t, begin("https://dc.example.com/login/sso/complete"), "control")
	require.NoError(t, begin("https://dc.example.com/dash/sso/complete"), "control")

	for _, bad := range []string{
		"https://evil.example/login/sso/complete",
		"//evil.example/login/sso/complete",
		"/login/sso/complete",
		"http://dc.example.com/login/sso/complete",
		"https://dc.example.com:8443/login/sso/complete",
		"https://dc.example.com/login/sso/complete/",
		"https://dc.example.com/login/sso/complete/../../evil",
		"https://dc.example.com/login/sso/%2e%2e/complete",
		"https://dc.example.com/login/sso/%2E%2E/complete",
		"https://dc.example.com/login/sso/complete?next=https://evil.example",
		"https://dc.example.com/login/sso/complete#frag",
		"https://dc.example.com\\@evil.example/login/sso/complete",
		"https://dc.example.com/login\\sso/complete",
		"https://user@dc.example.com/login/sso/complete",
		"https://dc.example.com@evil.example/login/sso/complete",
		"https://dc.example.com/login/sso/complete\r\nSet-Cookie: x=y",
		"https://dc.example.com/login/sso/complete ",
		"javascript:alert(1)",
		"https://dc.example.com/other",
		"",
	} {
		require.ErrorIs(t, begin(bad), ErrSsoInvalidRequest, "return address %q must be refused", bad)
	}
}

func TestBeginRefusesMalformedChallengeAndAmbiguousSelectors(t *testing.T) {
	e := newSsoEnv(t)
	_, challenge := verifierPair()
	ctx := context.Background()
	for _, in := range []BeginSsoInput{
		{Connection: "acme-idp", ReturnTo: ssoTestReturnTo, CodeChallenge: "short"},
		{Connection: "acme-idp", ReturnTo: ssoTestReturnTo, CodeChallenge: strings.Repeat("!", 43)},
		{ReturnTo: ssoTestReturnTo, CodeChallenge: challenge},
		{Connection: "acme-idp", Email: linkedEmail, ReturnTo: ssoTestReturnTo, CodeChallenge: challenge},
		{Email: strings.Repeat("a", 300) + "@x.com", ReturnTo: ssoTestReturnTo, CodeChallenge: challenge},
		{Email: "not-an-email", ReturnTo: ssoTestReturnTo, CodeChallenge: challenge},
	} {
		_, err := e.m.BeginSso(ctx, in, e.source())
		require.ErrorIs(t, err, ErrSsoInvalidRequest, "%+v", in)
	}
}

func TestBeginByEmailFindsTheConnectionByVerifiedDomainOnly(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	_, challenge := verifierPair()

	b, err := e.m.BeginSso(ctx, BeginSsoInput{Email: "Anyone@" + strings.ToUpper(ssoTestDomain), ReturnTo: ssoTestReturnTo, CodeChallenge: challenge}, e.source())
	require.NoError(t, err)
	require.NotNil(t, b, "an email in a verified domain finds its connection, case-insensitively")

	for _, email := range []string{"x@unknown-corp.com", "x@sub." + ssoTestDomain, "x@" + ssoTestDomain + ".evil.example"} {
		b, err := e.m.BeginSso(ctx, BeginSsoInput{Email: email, ReturnTo: ssoTestReturnTo, CodeChallenge: challenge}, e.source())
		require.NoError(t, err)
		require.Nil(t, b, "%s is not in a verified domain: no connection, sign in another way", email)
	}
}

// A domain asserted for a connection that cannot begin a sign-in (switched off, or its
// discovery snapshot awaiting approval) answers by email exactly as a domain with no
// connection does: null, nothing written. The sign-in screen asks this for every address,
// so the answer must not tell a claimed-but-unusable domain from an unclaimed one. Named by
// TOKEN the connection still refuses out loud, and the operator's counter still sees it.
func TestBeginByEmailAnswersAnUnusableConnectionAsNone(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	_, challenge := verifierPair()
	byEmail := BeginSsoInput{Email: "someone@" + ssoTestDomain, ReturnTo: ssoTestReturnTo, CodeChallenge: challenge}
	stateKeys := func() int {
		keys, err := e.m.ssoStateKV.Keys()
		if errors.Is(err, nats.ErrNoKeysFound) {
			return 0
		}
		require.NoError(t, err)
		return len(keys)
	}

	_, err := e.m.UpdateIdpConnection(ctx, "acme-idp", &IdpConnectionUpdateRequest{Enabled: optBool(false)})
	require.NoError(t, err)
	b, err := e.m.BeginSso(ctx, byEmail, e.source())
	require.NoError(t, err, "a disabled connection is not a different answer from no connection")
	require.Nil(t, b)
	require.Equal(t, 0, stateKeys(), "nothing is begun for a connection that cannot be used")
	require.Equal(t, 1, e.metrics.count("acme-idp", SsoOutcomeRefusedConnectionUnusable))

	_, err = e.m.BeginSso(ctx, BeginSsoInput{Connection: "acme-idp", ReturnTo: ssoTestReturnTo, CodeChallenge: challenge}, e.source())
	require.ErrorIs(t, err, ErrSsoUnavailable, "named by token, it still refuses")

	// Enabled again, but with an endpoint move awaiting approval: the same null.
	_, err = e.m.UpdateIdpConnection(ctx, "acme-idp", &IdpConnectionUpdateRequest{Enabled: optBool(true)})
	require.NoError(t, err)
	e.op.authorizePath = "/moved/authorize"
	_, err = e.m.RefreshIdpDiscovery(ctx, "acme-idp")
	require.NoError(t, err)
	b, err = e.m.BeginSso(ctx, byEmail, e.source())
	require.NoError(t, err)
	require.Nil(t, b)
	require.Equal(t, 0, stateKeys())

	// Control: a usable connection by the same email begins.
	moved, err := e.m.IdpConnection(ctx, "acme-idp")
	require.NoError(t, err)
	_, err = e.m.ApproveIdpDiscovery(ctx, "acme-idp", moved.Fingerprint)
	require.NoError(t, err)
	b, err = e.m.BeginSso(ctx, byEmail, e.source())
	require.NoError(t, err)
	require.NotNil(t, b)
	require.Equal(t, 1, stateKeys())
}

// ---- the discovery snapshot -------------------------------------------------------------

func TestUnapprovedDiscoveryRefusesBegin(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	d, err := e.m.CreateIdpConnection(ctx, IdpConnectionInput{
		Token: "fresh-idp", Name: "fresh", Kind: iam.IdpKindTenant, Issuer: e.op.url(), ClientID: ssoTestClientID,
	})
	require.NoError(t, err)
	require.False(t, d.Connection.Enabled, "a connection is created disabled")
	require.Nil(t, d.Connection.DiscoveryApprovedAt)

	_, challenge := verifierPair()
	_, err = e.m.BeginSso(ctx, BeginSsoInput{Connection: "fresh-idp", ReturnTo: ssoTestReturnTo, CodeChallenge: challenge}, e.source())
	require.ErrorIs(t, err, ErrSsoUnavailable)

	enabled := true
	_, err = e.m.UpdateIdpConnection(ctx, "fresh-idp", &IdpConnectionUpdateRequest{Enabled: optBool(enabled)})
	require.ErrorIs(t, err, ErrIdpNotApproved, "enabling needs an approved snapshot")

	_, err = e.m.ApproveIdpDiscovery(ctx, "fresh-idp", "not-the-fingerprint")
	require.ErrorIs(t, err, ErrIdpFingerprint)
}

func TestChangedDiscoveryRequiresReapproval(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	before, err := e.m.IdpConnection(ctx, "acme-idp")
	require.NoError(t, err)
	require.NotNil(t, before.Connection.DiscoveryApprovedAt)

	// Unchanged document: nothing to re-approve.
	same, err := e.m.RefreshIdpDiscovery(ctx, "acme-idp")
	require.NoError(t, err)
	require.NotNil(t, same.Connection.DiscoveryApprovedAt, "an unchanged document keeps its approval")

	// The provider moves an endpoint. The authorization endpoint now points elsewhere.
	e.op.authorizePath = "/moved/authorize"
	moved, err := e.m.RefreshIdpDiscovery(ctx, "acme-idp")
	require.NoError(t, err)
	require.Nil(t, moved.Connection.DiscoveryApprovedAt, "changed endpoints withdraw the approval")
	require.NotEqual(t, before.Fingerprint, moved.Fingerprint)

	_, challenge := verifierPair()
	_, err = e.m.BeginSso(ctx, BeginSsoInput{Connection: "acme-idp", ReturnTo: ssoTestReturnTo, CodeChallenge: challenge}, e.source())
	require.ErrorIs(t, err, ErrSsoUnavailable, "no sign-in is redirected to an endpoint nobody reviewed")

	// Approving the snapshot the administrator reviewed BEFORE the move is refused.
	_, err = e.m.ApproveIdpDiscovery(ctx, "acme-idp", before.Fingerprint)
	require.ErrorIs(t, err, ErrIdpFingerprint)
	_, err = e.m.ApproveIdpDiscovery(ctx, "acme-idp", moved.Fingerprint)
	require.NoError(t, err)
	b, err := e.m.BeginSso(ctx, BeginSsoInput{Connection: "acme-idp", ReturnTo: ssoTestReturnTo, CodeChallenge: challenge}, e.source())
	require.NoError(t, err)
	require.Contains(t, b.AuthorizationURL, e.op.url()+"/moved/authorize?")
}

// ---- identity: system roles ------------------------------------------------------------

func (e *ssoEnv) grantSuperuser(t *testing.T, email string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, e.store.EnsureRole(ctx, iam.ScopeSystem, iam.SuperuserRoleToken, "Superuser", []string{"*"}))
	_, err := e.admin.SetSystemRoles(ctx, email, []string{iam.SuperuserRoleToken})
	require.NoError(t, err)
}

func TestTenantConnectionCannotReachSystemRoleIdentity(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	// The operator's own address is in a domain a tenant asserted (the operator's company
	// is itself a tenant). The tenant's IdP administrator creates this person at their
	// provider and signs in as them.
	e.ssoIdentity(t, "ops@"+ssoTestDomain, "", "", "acme")
	e.grantSuperuser(t, "ops@"+ssoTestDomain)

	// No link: verified email in the connection's domain. It must NOT link by email.
	l, _, _, _ := e.signIn(t, "acme-idp", "attacker-sub", "ops@"+ssoTestDomain)
	e.refused(t, l, "acme-idp", SsoOutcomeRefusedLinkRequired)
	var n int64
	require.NoError(t, e.m.db.Database.Raw("SELECT COUNT(*) FROM iam_identity_links").Scan(&n).Error)
	require.Zero(t, n, "no link row was created")

	// The superuser cannot be linked to a tenant connection by anyone either.
	err := e.m.LinkIdentity(ctx, "ops@"+ssoTestDomain, "acme-idp", "attacker-sub")
	require.ErrorIs(t, err, ErrIdpInvalid)
}

func TestPreexistingLinkStopsWhenSystemRoleGranted(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.linkedPat(t)

	// Control: linked, no system role -> in.
	got, _ := e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
	pair, err := e.m.SelectTenant(ctx, got.IdentityToken, "acme")
	require.NoError(t, err)
	_ = pair

	// The role is granted AFTER the link was made. The link exists and is unchanged.
	e.grantSuperuser(t, linkedEmail)

	// A new sign-in is refused at the callback...
	l, _, _, _ := e.signIn(t, "acme-idp", "sub-pat", linkedEmail)
	e.refused(t, l, "acme-idp", SsoOutcomeRefusedSystemRole)
	// ...and the session already open stops at its next admission, by the same rule.
	_, err = e.m.SelectTenant(ctx, got.IdentityToken, "acme")
	require.ErrorIs(t, err, ErrInvalidToken)
	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken)
}

// The window between the callback and completeSso: a role granted in that minute.
func TestSystemRoleGrantedBetweenCallbackAndCompleteIsRefused(t *testing.T) {
	e := newSsoEnv(t)
	e.linkedPat(t)
	l, verifier, _, _ := e.signIn(t, "acme-idp", "sub-pat", linkedEmail)
	require.NotEmpty(t, l.Code)
	e.grantSuperuser(t, linkedEmail)
	_, err := e.m.CompleteSso(context.Background(), l.Code, verifier)
	require.ErrorIs(t, err, ErrSsoCodeInvalid)
}

func TestFederatedTenantIdentityTokenHasNoSystemAuthorities(t *testing.T) {
	e := newSsoEnv(t)
	e.linkedPat(t)
	got, _ := e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)

	claims, err := e.m.validator.ValidateIdentity(got.IdentityToken)
	require.NoError(t, err)
	require.Empty(t, claims.Authorities, "a tenant connection mints no system authorities")
	require.Empty(t, claims.Roles)
	require.False(t, claims.HasAuthority(auth.UserWrite))
	require.NotEmpty(t, claims.SessionID, "the token names the sign-in whose terms bind it")

	// Contrast: an instance connection's token for a linked superuser carries them.
	e.addConnection(t, "ops-idp", iam.IdpKindInstance, "")
	e.ssoIdentity(t, "root@ops.example", "", "", "acme")
	e.grantSuperuser(t, "root@ops.example")
	require.NoError(t, e.m.LinkIdentity(context.Background(), "root@ops.example", "ops-idp", "sub-root"))
	op, _ := e.signInOK(t, "ops-idp", "sub-root", "root@ops.example")
	oclaims, err := e.m.validator.ValidateIdentity(op.IdentityToken)
	require.NoError(t, err)
	require.True(t, oclaims.HasAuthority(auth.UserWrite))
	require.True(t, op.Superuser)
}

// ---- identity: explicit linking only ---------------------------------------------------

func TestExistingEmailNeverAutoLinks(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	// pat has a password identity and a manual membership. Nothing links an SSO account to it.
	e.createMember(t, linkedEmail, "pw-1")

	l, _, _, _ := e.signIn(t, "acme-idp", "sub-self-asserted", linkedEmail)
	e.refused(t, l, "acme-idp", SsoOutcomeRefusedLinkRequired)
	require.Equal(t, "link_required", l.Error, "the person is told an administrator must link the account")

	views, err := e.m.ListSsoLinkRequests(ctx)
	require.NoError(t, err)
	require.Len(t, views, 1)
	require.Equal(t, linkedEmail, views[0].Request.Email)
	require.Equal(t, "sub-self-asserted", views[0].Request.Subject)
	require.True(t, views[0].IdentityExists)
	require.False(t, views[0].SubjectChanged)
	require.Equal(t, []string{"acme"}, views[0].ManualMemberships)
	var n int64
	require.NoError(t, e.m.db.Database.Raw("SELECT COUNT(*) FROM iam_identity_links").Scan(&n).Error)
	require.Zero(t, n)

	// The same refused sign-in twice is ONE request.
	e.signIn(t, "acme-idp", "sub-self-asserted", linkedEmail)
	views, err = e.m.ListSsoLinkRequests(ctx)
	require.NoError(t, err)
	require.Len(t, views, 1)

	// Approval needs a disposition for the manual membership, then links and converts it.
	require.ErrorIs(t, e.m.ApproveSsoLinkRequest(ctx, views[0].Request.ID, ""), ErrDispositionRequired)
	require.NoError(t, e.m.ApproveSsoLinkRequest(ctx, views[0].Request.ID, DispositionConvert))
	id, err := e.store.IdentityByEmail(ctx, linkedEmail)
	require.NoError(t, err)
	require.NotNil(t, id.Memberships[0].ManagedBy, "the membership is now managed by the connection")
	got, _ := e.signInOK(t, "acme-idp", "sub-self-asserted", linkedEmail)
	require.Len(t, got.Memberships, 1)
	views, err = e.m.ListSsoLinkRequests(ctx)
	require.NoError(t, err)
	require.Empty(t, views, "an approved request is gone")
}

func TestRecycledSubjectFlagged(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.linkedPat(t) // linked as sub-pat

	// A different subject presents the same address: the departed employee's address, reissued.
	l, _, _, _ := e.signIn(t, "acme-idp", "sub-newcomer", linkedEmail)
	e.refused(t, l, "acme-idp", SsoOutcomeRefusedLinkRequired)
	views, err := e.m.ListSsoLinkRequests(ctx)
	require.NoError(t, err)
	require.Len(t, views, 1)
	require.True(t, views[0].SubjectChanged, "an identity already linked under another subject is flagged")

	// Approving it cannot silently add a second subject: the store refuses.
	require.ErrorIs(t, e.m.ApproveSsoLinkRequest(ctx, views[0].Request.ID, DispositionKeepManual), iam.ErrIdentityAlreadyLinked)
}

func TestLinkRequestDoesNotOutliveItsIdentity(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.createMember(t, linkedEmail, "pw-1")
	e.signIn(t, "acme-idp", "sub-x", linkedEmail)
	views, err := e.m.ListSsoLinkRequests(ctx)
	require.NoError(t, err)
	require.Len(t, views, 1)

	// The identity is deleted: its pending request goes with it...
	removed, err := e.admin.DeleteIdentity(ctx, linkedEmail)
	require.NoError(t, err)
	require.True(t, removed)
	views, err = e.m.ListSsoLinkRequests(ctx)
	require.NoError(t, err)
	require.Empty(t, views)

	// ...and a request that survived some other way still cannot be approved onto whoever
	// holds the email NEXT: approval re-resolves the identity by email at that moment.
	require.NoError(t, e.store.UpsertLinkRequest(ctx, &iam.SsoLinkRequest{
		ConnectionID: e.reloadConn(t, "acme-idp").ID, Subject: "sub-x", Email: linkedEmail,
	}))
	reqs, err := e.store.ListSsoLinkRequests(ctx)
	require.NoError(t, err)
	err = e.m.ApproveSsoLinkRequest(ctx, reqs[0].ID, "")
	require.ErrorIs(t, err, ErrIdpInvalid, "no identity has that email now")
}

func TestUserInitiatedLinkRequiresPasswordSid(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.createMember(t, linkedEmail, "pw-1")
	ia, err := e.m.Login(ctx, linkedEmail, "pw-1")
	require.NoError(t, err)

	begin := func(token string) (*SsoBegin, error) {
		return e.m.BeginSsoLink(ctx, token, ssoTestReturnTo, e.source())
	}
	link := func(b *SsoBegin, sub, email string) ssoLanding {
		state, code := e.op.authorize(b.AuthorizationURL, sub, email)
		return e.callback(t, "acme-idp", url.Values{"state": {state}, "code": {code}})
	}

	// An attacker begins a link from THEIR session, then tricks a victim (different email)
	// into signing in at the provider: the victim's account must not bind to the attacker.
	b, err := begin(ia.IdentityToken)
	require.NoError(t, err)
	l := link(b, "victim-sub", "victim@"+ssoTestDomain)
	e.refused(t, l, "acme-idp", SsoOutcomeRefusedLinkMismatch)
	require.Equal(t, "link_email_mismatch", l.Error)

	// An unverified address is refused here as well.
	e.op.mutate = func(c jwt.MapClaims) { c["email_verified"] = false }
	b, err = begin(ia.IdentityToken)
	require.NoError(t, err)
	l = link(b, "sub-pat", linkedEmail)
	e.refused(t, l, "acme-idp", SsoOutcomeRefusedUnverifiedEmail)
	require.Equal(t, "link_unverified_email", l.Error)
	e.op.mutate = nil

	// A session that ended between begin and callback cannot complete the link.
	b, err = begin(ia.IdentityToken)
	require.NoError(t, err)
	other, err := e.m.Login(ctx, linkedEmail, "pw-1")
	require.NoError(t, err)
	require.NoError(t, e.m.Logout(ctx, ia.IdentityToken))
	l = link(b, "sub-pat", linkedEmail)
	e.refused(t, l, "acme-idp", SsoOutcomeRefusedLinkSession)
	require.Equal(t, "link_session_ended", l.Error)
	// ...and the ended sign-in cannot begin another.
	_, err = begin(ia.IdentityToken)
	require.ErrorIs(t, err, ErrSsoLinkSessionRequired)
	ia = other

	// A provider account already linked to SOMEONE ELSE is refused at the return.
	e.ssoIdentity(t, "lee@"+ssoTestDomain, "acme-idp", "sub-taken", "acme")
	b, err = begin(ia.IdentityToken)
	require.NoError(t, err)
	l = link(b, "sub-taken", linkedEmail)
	e.refused(t, l, "acme-idp", SsoOutcomeRefusedAlreadyLinked)
	require.Equal(t, "link_already_linked", l.Error)

	// Control: the owner's own account, verified, in the domain, from a live password sign-in.
	b, err = begin(ia.IdentityToken)
	require.NoError(t, err)
	l = link(b, "sub-pat", linkedEmail)
	require.True(t, l.Linked, "the owner's own verified account links")
	require.Empty(t, l.Error)

	// A second link on the same connection is refused up front.
	_, err = begin(ia.IdentityToken)
	require.ErrorIs(t, err, ErrSsoAlreadyLinked)

	// The new link signs in; and an SSO session cannot begin a link (its only proof of who
	// it is, is the provider).
	got, _ := e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
	_, err = begin(got.IdentityToken)
	require.ErrorIs(t, err, ErrSsoLinkNeedsPassword)

	// A token that is not one at all.
	_, err = begin("not-a-token")
	require.ErrorIs(t, err, ErrSsoLinkSessionRequired)
}

// The connection a person links is the one their OWN email domain names; nothing else.
func TestUserInitiatedLinkPicksTheConnectionByDomain(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	login := func(email string) string {
		t.Helper()
		e.createMember(t, email, "pw-1")
		ia, err := e.m.Login(ctx, email, "pw-1")
		require.NoError(t, err)
		return ia.IdentityToken
	}
	begin := func(token string) error {
		_, err := e.m.BeginSsoLink(ctx, token, ssoTestReturnTo, e.source())
		return err
	}

	// A domain no connection was verified for.
	require.ErrorIs(t, begin(login("kim@elsewhere-corp.com")), ErrSsoLinkNoConnection)

	// A domain verified for an INSTANCE connection: those link only by superuser action.
	e.addConnection(t, "ops-idp", iam.IdpKindInstance, "ops-corp.com")
	require.ErrorIs(t, begin(login("ops@ops-corp.com")), ErrSsoLinkNoConnection)

	// An identity with a system role is never linked by itself.
	e.createMember(t, "root@"+ssoTestDomain, "pw-1")
	e.grantSuperuser(t, "root@"+ssoTestDomain)
	su, err := e.m.Login(ctx, "root@"+ssoTestDomain, "pw-1")
	require.NoError(t, err)
	require.ErrorIs(t, begin(su.IdentityToken), ErrSsoLinkNotAllowed)

	// The domain's tenant connection, disabled: no connection to link. Enabled again: the
	// flow goes to THAT connection's provider.
	pat := login(linkedEmail)
	off, on := false, true
	_, err = e.m.UpdateIdpConnection(ctx, "acme-idp", &IdpConnectionUpdateRequest{Enabled: dcgraphql.OptionalBool{Set: true, Value: &off}})
	require.NoError(t, err)
	require.ErrorIs(t, begin(pat), ErrSsoLinkNoConnection)
	_, err = e.m.UpdateIdpConnection(ctx, "acme-idp", &IdpConnectionUpdateRequest{Enabled: dcgraphql.OptionalBool{Set: true, Value: &on}})
	require.NoError(t, err)
	b, err := e.m.BeginSsoLink(ctx, pat, ssoTestReturnTo, e.source())
	require.NoError(t, err)
	state, code := e.op.authorize(b.AuthorizationURL, "sub-pat", linkedEmail)
	l := e.callback(t, "acme-idp", url.Values{"state": {state}, "code": {code}})
	require.True(t, l.Linked, "the domain's own connection links: %+v", l)

	// Every refusal is typed for the client to branch on.
	var typed *SsoLinkError
	require.ErrorAs(t, begin(pat), &typed)
	require.Equal(t, map[string]any{"code": CodeSsoLinkAlreadyLinked}, typed.Extensions())
}

// ---- identity: what the provider must vouch for ----------------------------------------

func TestEmailVerifiedFalseRefused(t *testing.T) {
	for name, v := range map[string]any{"false": false, "the string false": "false", "absent": nil} {
		t.Run(name, func(t *testing.T) {
			e := newSsoEnv(t)
			e.createMember(t, linkedEmail, "pw-1")
			e.op.mutate = func(c jwt.MapClaims) {
				if v == nil {
					delete(c, "email_verified")
				} else {
					c["email_verified"] = v
				}
			}
			l, _, _, _ := e.signIn(t, "acme-idp", "sub-x", linkedEmail)
			e.refused(t, l, "acme-idp", SsoOutcomeRefusedUnverifiedEmail)
			views, err := e.m.ListSsoLinkRequests(context.Background())
			require.NoError(t, err)
			require.Empty(t, views, "an unverified address does not even earn a request")
		})
	}
}

func TestAddressOutsideTheConnectionsDomainsIsRefused(t *testing.T) {
	e := newSsoEnv(t)
	e.addConnection(t, "other-idp", iam.IdpKindTenant, "other-corp.com", "acme")
	// A verified address in ANOTHER connection's domain, presented through this one.
	l, _, _, _ := e.signIn(t, "acme-idp", "sub-x", "someone@other-corp.com")
	e.refused(t, l, "acme-idp", SsoOutcomeRefusedUnverifiedDomain)
	// And one in no connection's domain.
	l, _, _, _ = e.signIn(t, "acme-idp", "sub-y", "someone@nobody.example")
	e.refused(t, l, "acme-idp", SsoOutcomeRefusedUnverifiedDomain)
}

func TestFreemailAndPublicSuffixDomainsCannotBeAsserted(t *testing.T) {
	e := newSsoEnv(t)
	for _, d := range []string{"gmail.com", "outlook.com", "co.uk", "com", "GMAIL.COM"} {
		_, err := e.m.AddIdpDomain(context.Background(), "acme-idp", d)
		require.Error(t, err, d)
	}
	_, err := e.m.AddIdpDomain(context.Background(), "acme-idp", "Example-Corp.COM")
	require.NoError(t, err, "control")
}

func TestRequiredClaimsAndAssuranceAreEnforcedForEverySignIn(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.linkedPat(t)
	_, err := e.m.SetIdpRequiredClaims(ctx, "acme-idp", map[string]string{"org": "acme"})
	require.NoError(t, err)

	// Linked identity or not, a token without the claim is refused...
	l, _, _, _ := e.signIn(t, "acme-idp", "sub-pat", linkedEmail)
	e.refused(t, l, "acme-idp", SsoOutcomeRefusedRequiredClaim)
	e.op.mutate = func(c jwt.MapClaims) { c["org"] = "someone-else" }
	l, _, _, _ = e.signIn(t, "acme-idp", "sub-pat", linkedEmail)
	e.refused(t, l, "acme-idp", SsoOutcomeRefusedRequiredClaim)
	// ...and with it, in.
	e.op.mutate = func(c jwt.MapClaims) { c["org"] = "acme" }
	e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)

	// Required authentication methods: any one of them.
	enabled := true
	_ = enabled
	_, err = e.m.UpdateIdpConnection(ctx, "acme-idp", &IdpConnectionUpdateRequest{RequiredAmr: optList("mfa", "hwk")})
	require.NoError(t, err)
	l, _, _, _ = e.signIn(t, "acme-idp", "sub-pat", linkedEmail)
	e.refused(t, l, "acme-idp", SsoOutcomeRefusedRequiredClaim)
	e.op.mutate = func(c jwt.MapClaims) { c["org"] = "acme"; c["amr"] = []string{"pwd", "hwk"} }
	e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
}

func TestEntraWithoutXmsEdovRefused(t *testing.T) {
	entra := &iam.IdpConnection{Issuer: "https://login.microsoftonline.com/6fe8a8b1-6fce-4c36-a6e4-0a1b2c3d4e5f/v2.0"}
	require.False(t, providerVouchesForEmail(entra, map[string]any{"email_verified": true}),
		"Entra does not send email_verified; a token that claims it anyway is not evidence")
	require.False(t, providerVouchesForEmail(entra, map[string]any{}))
	require.False(t, providerVouchesForEmail(entra, map[string]any{"xms_edov": false}))
	require.True(t, providerVouchesForEmail(entra, map[string]any{"xms_edov": true}))
	require.True(t, providerVouchesForEmail(entra, map[string]any{"xms_edov": "true"}))

	other := &iam.IdpConnection{Issuer: "https://idp.example.com"}
	require.True(t, providerVouchesForEmail(other, map[string]any{"email_verified": true}))
	require.False(t, providerVouchesForEmail(other, map[string]any{"xms_edov": true}))
}

func TestGoogleConsumerAccountOnWorkspaceDomainRefused(t *testing.T) {
	google := &iam.IdpConnection{
		Issuer: "https://accounts.google.com", RequiredClaims: map[string]string{"hd": ssoTestDomain},
	}
	claims := func(raw map[string]any) *ssoClaims {
		return &ssoClaims{Raw: raw, Domain: ssoTestDomain}
	}
	// A consumer Google account registered on a corporate address: verified, no hd.
	require.Error(t, checkSsoClaims(google, claims(map[string]any{"email_verified": true})))
	// A Workspace account of ANOTHER domain.
	require.Error(t, checkSsoClaims(google, claims(map[string]any{"hd": "other.example"})))
	// The Workspace domain the address is in.
	require.NoError(t, checkSsoClaims(google, claims(map[string]any{"hd": ssoTestDomain})))
	// hd on the token, but the address is not in that domain.
	require.Error(t, checkSsoClaims(google, &ssoClaims{Raw: map[string]any{"hd": ssoTestDomain}, Domain: "elsewhere.example"}))
}

// ---- confinement -----------------------------------------------------------------------

func (e *ssoEnv) twoTenantSignIn(t *testing.T) *IdentityAuth {
	t.Helper()
	e.addTenant(t, "globex")
	e.ssoIdentity(t, linkedEmail, "acme-idp", "sub-pat", "acme", "globex")
	got, _ := e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
	return got
}

func TestSsoSessionCannotSelectForeignTenant(t *testing.T) {
	ctx := context.Background()

	t.Run("the memberships offered are the connection's tenants", func(t *testing.T) {
		e := newSsoEnv(t)
		got := e.twoTenantSignIn(t)
		require.Len(t, got.Memberships, 1)
		require.Equal(t, "acme", got.Memberships[0].Tenant)
		ms, err := e.m.Memberships(ctx, got.IdentityToken)
		require.NoError(t, err)
		require.Len(t, ms, 1)
	})

	t.Run("selectTenant", func(t *testing.T) {
		e := newSsoEnv(t)
		got := e.twoTenantSignIn(t)
		_, err := e.m.SelectTenant(ctx, got.IdentityToken, "acme")
		require.NoError(t, err, "control")
		_, err = e.m.SelectTenant(ctx, got.IdentityToken, "globex")
		require.ErrorIs(t, err, ErrInvalidCredentials, "a membership in a tenant the connection does not serve is not enough")
	})

	t.Run("refresh", func(t *testing.T) {
		e := newSsoEnv(t)
		got := e.twoTenantSignIn(t)
		pair, err := e.m.SelectTenant(ctx, got.IdentityToken, "acme")
		require.NoError(t, err)
		// The connection stops serving acme (an administrator edit) after the pair was issued.
		_, err = e.m.SetIdpConnectionTenants(ctx, "acme-idp", []string{"globex"})
		require.NoError(t, err)
		_, err = e.m.Refresh(ctx, pair.RefreshToken)
		require.ErrorIs(t, err, ErrInvalidToken)
	})

	t.Run("authorize (the MCP consent step)", func(t *testing.T) {
		e := newSsoEnv(t)
		got := e.twoTenantSignIn(t)
		client, err := e.m.ResolveAuthorizeClient(ctx, sessClient, sessRedirect)
		require.NoError(t, err)
		subject, err := e.m.IdentitySubject(got.IdentityToken)
		require.NoError(t, err)
		params := AuthorizeParams{
			ResponseType: "code", ClientID: sessClient, RedirectURI: sessRedirect,
			Scope: auth.ScopeReadOnly, CodeChallenge: pkceChallenge(sessVerifier), CodeChallengeMethod: "S256",
		}
		_, err = e.m.IssueAuthorizationCode(ctx, client, params, subject, "acme")
		require.NoError(t, err, "control")
		_, err = e.m.IssueAuthorizationCode(ctx, client, params, subject, "globex")
		require.ErrorIs(t, err, errTenantAccessDenied)
	})

	t.Run("OAuth redemption and refresh", func(t *testing.T) {
		e := newSsoEnv(t)
		got := e.twoTenantSignIn(t)
		toks := e.oauthSession(t, got.IdentityToken)
		_, err := e.refreshOAuth(toks.RefreshToken)
		require.NoError(t, err, "control")

		// A code authorized while acme was served is redeemed after it stops being.
		code, err := e.issueCode(t, got.IdentityToken)
		require.NoError(t, err)
		_, err = e.m.SetIdpConnectionTenants(ctx, "acme-idp", []string{"globex"})
		require.NoError(t, err)
		_, err = e.redeem(code)
		assertOAuthErrorCode(t, err, "invalid_grant")
		_, err = e.refreshOAuth(toks.RefreshToken)
		assertOAuthErrorCode(t, err, "invalid_grant")
	})
}

func TestInstanceSessionEntersOnlyViaBreakGlass(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.addConnection(t, "ops-idp", iam.IdpKindInstance, "")

	// An ordinary member linked to the OPERATOR's provider gets no tenant from it.
	e.ssoIdentity(t, "member@ops.example", "", "", "acme")
	require.NoError(t, e.m.LinkIdentity(ctx, "member@ops.example", "ops-idp", "sub-member"))
	got, _ := e.signInOK(t, "ops-idp", "sub-member", "member@ops.example")
	require.Empty(t, got.Memberships)
	_, err := e.m.SelectTenant(ctx, got.IdentityToken, "acme")
	require.ErrorIs(t, err, ErrInvalidCredentials)

	// A superuser breaks glass.
	e.ssoIdentity(t, "root@ops.example", "", "", "acme")
	e.grantSuperuser(t, "root@ops.example")
	require.NoError(t, e.m.LinkIdentity(ctx, "root@ops.example", "ops-idp", "sub-root"))
	root, _ := e.signInOK(t, "ops-idp", "sub-root", "root@ops.example")
	require.True(t, root.Superuser)
	pair, err := e.m.SelectTenant(ctx, root.IdentityToken, "acme")
	require.NoError(t, err)
	claims, err := e.validator.Validate(pair.AccessToken)
	require.NoError(t, err)
	require.True(t, claims.ActingAsSuperuser)
}

func TestInstanceConnectionNeverAutoLinks(t *testing.T) {
	e := newSsoEnv(t)
	e.addConnection(t, "ops-idp", iam.IdpKindInstance, "ops.example")
	e.ssoIdentity(t, "root@ops.example", "", "", "acme")
	e.grantSuperuser(t, "root@ops.example")
	// The address is verified and in the connection's domain; the identity exists; there is
	// no link. It must not sign in.
	l, _, _, _ := e.signIn(t, "ops-idp", "some-sub", "root@ops.example")
	e.refused(t, l, "ops-idp", SsoOutcomeRefusedLinkRequired)
}

func TestDisabledConnectionEndsSessions(t *testing.T) {
	for name, end := range map[string]func(t *testing.T, e *ssoEnv){
		"disabled": func(t *testing.T, e *ssoEnv) {
			_, err := e.m.UpdateIdpConnection(context.Background(), "acme-idp", &IdpConnectionUpdateRequest{Enabled: optBool(false)})
			require.NoError(t, err)
		},
		"deleted": func(t *testing.T, e *ssoEnv) {
			ok, err := e.m.DeleteIdpConnection(context.Background(), "acme-idp")
			require.NoError(t, err)
			require.True(t, ok)
		},
		"identity unlinked": func(t *testing.T, e *ssoEnv) {
			ok, err := e.m.UnlinkIdentity(context.Background(), linkedEmail, "acme-idp")
			require.NoError(t, err)
			require.True(t, ok)
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newSsoEnv(t)
			ctx := context.Background()
			e.linkedPat(t)
			got, _ := e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
			pair, err := e.m.SelectTenant(ctx, got.IdentityToken, "acme")
			require.NoError(t, err)
			_, err = e.m.Refresh(ctx, pair.RefreshToken)
			require.NoError(t, err, "control: the session works before")

			end(t, e)
			pair2, err := e.m.SelectTenant(ctx, got.IdentityToken, "acme")
			require.ErrorIs(t, err, ErrInvalidToken)
			require.Nil(t, pair2)
		})
	}
}

func TestDisabledConnectionRefusesRefresh(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	e.linkedPat(t)
	got, _ := e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
	pair, err := e.m.SelectTenant(ctx, got.IdentityToken, "acme")
	require.NoError(t, err)
	_, err = e.m.UpdateIdpConnection(ctx, "acme-idp", &IdpConnectionUpdateRequest{Enabled: optBool(false)})
	require.NoError(t, err)
	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken)
}

// ---- secrets, rate limits, outages ------------------------------------------------------

func TestSecretNeverReturned(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	d, err := e.m.IdpConnection(ctx, "acme-idp")
	require.NoError(t, err)
	require.True(t, d.HasSecret)
	require.NotContains(t, strings.ToLower(spew(d)), strings.ToLower(ssoTestSecret))
	require.Equal(t, "sso/connection/1", d.Connection.ClientSecretRef, "only the handle's name is stored with the row")

	// And in the database the secret is sealed.
	var rows []struct{ Ciphertext []byte }
	require.NoError(t, e.m.db.Database.Raw("SELECT ciphertext FROM secrets").Scan(&rows).Error)
	require.NotEmpty(t, rows)
	for _, r := range rows {
		require.NotContains(t, string(r.Ciphertext), ssoTestSecret)
	}

	// Replacing it keeps the handle and still returns nothing.
	require.NoError(t, e.m.SetIdpConnectionSecret(ctx, "acme-idp", "a-new-secret"))
	d, err = e.m.IdpConnection(ctx, "acme-idp")
	require.NoError(t, err)
	require.NotContains(t, spew(d), "a-new-secret")
}

func TestBeginIsRateLimitedPerSource(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	_, challenge := verifierPair()
	in := BeginSsoInput{Connection: "acme-idp", ReturnTo: ssoTestReturnTo, CodeChallenge: challenge}
	for i := 0; i < ssoBeginPerSourceBurst; i++ {
		_, err := e.m.BeginSso(ctx, in, "203.0.113.9")
		require.NoError(t, err)
	}
	_, err := e.m.BeginSso(ctx, in, "203.0.113.9")
	require.ErrorIs(t, err, ErrSsoRateLimited)
	require.Equal(t, 1, e.metrics.count("unknown", SsoOutcomeRateLimited))

	// Another source is unaffected: one sender cannot exhaust the others' allowance.
	_, err = e.m.BeginSso(ctx, in, "203.0.113.10")
	require.NoError(t, err)
}

func TestGlobalBeginAllowanceHoldsAcrossSources(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := newBeginLimiter(sourcelimit.WithClock(func() time.Time { return now }))
	ok := 0
	for i := 0; i < 5*ssoBeginGlobalBurst; i++ {
		if l.allow("10.0." + itoa(i/250) + "." + itoa(i%250)) {
			ok++
		}
	}
	require.Equal(t, ssoBeginGlobalBurst, ok, "a sender who varies the source still shares one global allowance")
	now = now.Add(time.Duration(ssoBeginGlobalBurst) * ssoBeginGlobalEvery)
	require.True(t, l.allow("fresh-source"), "the allowance refills")
}

// A sender already over its OWN allowance must not drain the global one: the per-source
// bucket is charged first, so its refused attempts never reach the shared bucket. Charging
// the global bucket first would let one noisy source starve every other sender.
func TestASenderOverItsOwnAllowanceDoesNotDrainTheGlobalBucket(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := newBeginLimiter(sourcelimit.WithClock(func() time.Time { return now }))
	for i := 0; i < ssoBeginPerSourceBurst; i++ {
		require.True(t, l.allow("203.0.113.9"))
	}
	for i := 0; i < 300; i++ {
		require.False(t, l.allow("203.0.113.9"), "attempt %d is over the source's own allowance", i)
	}
	require.True(t, l.allow("198.51.100.1"), "one source's refused attempts drained the shared global bucket")
}

func TestStateStoreFullFailsClosedAndPasswordSignInSurvives(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	tiny, err := e.js.CreateKeyValue(&nats.KeyValueConfig{Bucket: "sso-state-tiny", MaxBytes: 4096, TTL: SsoStateTTL})
	require.NoError(t, err)
	e.m.ssoStateKV = tiny

	_, challenge := verifierPair()
	in := BeginSsoInput{Connection: "acme-idp", ReturnTo: ssoTestReturnTo, CodeChallenge: challenge}
	var failure error
	for i := 0; i < 200 && failure == nil; i++ {
		_, failure = e.m.BeginSso(ctx, in, e.source())
	}
	require.ErrorIs(t, failure, ErrSsoUnavailable, "a full state bucket makes begin fail closed")
	require.GreaterOrEqual(t, e.metrics.count("acme-idp", SsoOutcomeStateStoreFull), 1,
		"the SsoStateStoreFull alert watches this outcome")

	// Password sign-in does not use the bucket.
	e.createMember(t, "pw@example.com", "pw-1")
	_, err = e.m.Login(ctx, "pw@example.com", "pw-1")
	require.NoError(t, err)
}

func TestSsoIsOffWithoutAPublicIssuerOrTheBuckets(t *testing.T) {
	e := newSsoEnv(t)
	ctx := context.Background()
	_, challenge := verifierPair()
	in := BeginSsoInput{Connection: "acme-idp", ReturnTo: ssoTestReturnTo, CodeChallenge: challenge}

	issuer := e.m.issuerUrl
	e.m.issuerUrl = ""
	_, err := e.m.BeginSso(ctx, in, e.source())
	require.ErrorIs(t, err, ErrSsoUnavailable)
	_, err = e.m.CreateIdpConnection(ctx, IdpConnectionInput{Token: "x", Name: "x", Kind: iam.IdpKindTenant, Issuer: e.op.url(), ClientID: "c"})
	require.ErrorIs(t, err, ErrSsoNoPublicIssuer, "the administrator is told why")
	res := e.m.HandleSsoCallback(ctx, "acme-idp", url.Values{"state": {"x"}})
	require.Empty(t, res.Location)
	e.m.issuerUrl = issuer

	stateKV := e.m.ssoStateKV
	e.m.ssoStateKV = nil
	_, err = e.m.BeginSso(ctx, in, e.source())
	require.ErrorIs(t, err, ErrSsoUnavailable)
	e.m.ssoStateKV = stateKV
	_, err = e.m.BeginSso(ctx, in, e.source())
	require.NoError(t, err, "control")
}

// ---- the provider's key set -------------------------------------------------------------

func TestOversizedKeySetIsRefusedAndCounted(t *testing.T) {
	e := newSsoEnv(t)
	e.linkedPat(t)
	e.op.jwks = func() []byte {
		return []byte(`{"keys":[],"padding":"` + strings.Repeat("a", maxIdpResponseBytes+1024) + `"}`)
	}
	l, _, _, _ := e.signIn(t, "acme-idp", "sub-pat", linkedEmail)
	e.refused(t, l, "acme-idp", SsoOutcomeIdpError)
	require.Equal(t, 1, e.metrics.fetch["acme-idp"], "an unreachable or hostile key set is counted for the SsoIdpUnreachable alert")
}

func TestKeyRotationIsFollowedOnAnUnknownKid(t *testing.T) {
	e := newSsoEnv(t)
	e.linkedPat(t)
	e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
	hits := e.op.jwksHits.Load()

	k, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	e.op.key, e.op.kid = k, "op-key-2"
	e.signInOK(t, "acme-idp", "sub-pat", linkedEmail)
	require.Greater(t, e.op.jwksHits.Load(), hits, "an unknown kid triggers one refetch")
}

// An ID token minted under the legacy issuer still validates after the issuer URL is set:
// the platform validator selects keys by kid and pins neither iss nor aud, so deriving
// issuerUrl from the ingress cannot break a token already in a browser.
func TestTokenMintedUnderTheOldIssuerStillValidates(t *testing.T) {
	key, err := auth.GenerateKeyPair()
	require.NoError(t, err)
	legacy := auth.NewIssuer(key, "dc-user-management:inst-1", time.Minute, time.Hour)
	modern := auth.NewIssuer(key, ssoTestIssuerURL, time.Minute, time.Hour)
	v := auth.NewValidator(&key.PublicKey)

	old, err := legacy.IssueAccess("acme", "pat@example.com", nil, []string{"device:read"}, "j1")
	require.NoError(t, err)
	fresh, err := modern.IssueAccess("acme", "pat@example.com", nil, []string{"device:read"}, "j2")
	require.NoError(t, err)
	for _, tok := range []string{old.Token, fresh.Token} {
		_, err := v.Validate(tok)
		require.NoError(t, err)
	}
}

func optBool(b bool) dcgraphql.OptionalBool { return dcgraphql.OptionalBool{Set: true, Value: &b} }

func optList(v ...string) dcgraphql.OptionalStringList {
	return dcgraphql.OptionalStringList{Set: true, Value: v}
}

func spew(v any) string { return fmt.Sprintf("%+v", v) }

func itoa(i int) string { return strconv.Itoa(i) }
