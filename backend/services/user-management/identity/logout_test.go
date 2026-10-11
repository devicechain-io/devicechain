// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"crypto/rsa"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// Logout ends one SIGN-IN: the identity token and every refresh chain and OAuth grant
// minted from it, and nothing else. Driven on the real paths (sessionEnv: real issuer and
// validator, refresh state in a real JetStream KV bucket), and every refusal is paired
// with a control showing the same credential working before the logout.

func (e *sessionEnv) auditCount(t *testing.T, op string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, e.db().WithContext(core.WithSystemContext(context.Background())).
		Model(&rdb.AuditEvent{}).Where("operation = ?", op).Count(&n).Error)
	return n
}

// THE DEFECT. A sign-out only cleared the browser, so the refresh token stayed good for
// its whole life, and the identity token (kept beside it) could start a NEW refresh chain
// after the user had signed out. Every credential of the sign-in must die, including
// chains started by switching tenant and the OAuth grants authorized from it.
func TestLogoutEndsEveryCredentialOfThatSignIn(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	e.createMember(t, "pat@example.com", "pw-1")
	idTok, chain1 := e.login(t, "pat@example.com", "pw-1")
	chain2, err := e.m.SelectTenant(ctx, idTok, "acme") // a second chain from the same sign-in
	require.NoError(t, err)
	oauth := e.oauthSession(t, idTok)
	pendingCode, err := e.issueCode(t, idTok)
	require.NoError(t, err)

	// Controls: before the logout every one of these works.
	_, err = e.m.Memberships(ctx, idTok)
	require.NoError(t, err, "control: the identity token lists memberships")
	rotated, err := e.m.Refresh(ctx, chain2.RefreshToken)
	require.NoError(t, err, "control: a refresh token of this sign-in rotates")
	_, err = e.refreshOAuth(oauth.RefreshToken)
	require.NoError(t, err, "control: the OAuth refresh works")
	oauth2 := e.oauthSession(t, idTok)

	require.NoError(t, e.m.Logout(ctx, chain1.RefreshToken))

	_, err = e.m.Refresh(ctx, chain1.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken, "the refresh token that signed out still rotates")
	_, err = e.m.Refresh(ctx, rotated.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken, "a refresh chain started by another tenant selection survived the sign-out")
	_, err = e.m.SelectTenant(ctx, idTok, "acme")
	require.ErrorIs(t, err, ErrInvalidToken, "the identity token started a new refresh chain after sign-out")
	_, err = e.m.Memberships(ctx, idTok)
	require.ErrorIs(t, err, ErrInvalidToken, "the identity token still lists memberships after sign-out")
	_, err = e.issueCode(t, idTok)
	require.ErrorIs(t, err, ErrInvalidToken, "the consent step issued a code for a signed-out identity token")
	_, err = e.redeem(pendingCode)
	assertOAuthErrorCode(t, err, "invalid_grant")
	_, err = e.refreshOAuth(oauth2.RefreshToken)
	assertOAuthErrorCode(t, err, "invalid_grant")
}

// Another sign-in of the same identity is a different session and keeps working, down to
// starting new chains from its own identity token.
func TestLogoutLeavesAnotherSignInOfTheSameIdentityAlone(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	e.createMember(t, "pat@example.com", "pw-1")
	_, laptop := e.login(t, "pat@example.com", "pw-1")
	phoneID, phone := e.login(t, "pat@example.com", "pw-1")
	phoneOAuth := e.oauthSession(t, phoneID)

	require.NoError(t, e.m.Logout(ctx, laptop.RefreshToken))

	_, err := e.m.Refresh(ctx, laptop.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken, "control: the signed-out sign-in is refused")
	_, err = e.m.Refresh(ctx, phone.RefreshToken)
	require.NoError(t, err, "the other sign-in's refresh token was ended by a different sign-in's logout")
	_, err = e.m.SelectTenant(ctx, phoneID, "acme")
	require.NoError(t, err, "the other sign-in's identity token was ended by a different sign-in's logout")
	_, err = e.refreshOAuth(phoneOAuth.RefreshToken)
	require.NoError(t, err, "the other sign-in's OAuth grant was ended by a different sign-in's logout")

	// And the same person can sign in again afterwards.
	_, again := e.login(t, "pat@example.com", "pw-1")
	_, err = e.m.Refresh(ctx, again.RefreshToken)
	require.NoError(t, err)
}

// Signing out is safe to repeat, accepts either token of the sign-in, and refuses a token
// that is not ours.
func TestLogoutIsIdempotentAndAcceptsTheIdentityToken(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	e.createMember(t, "pat@example.com", "pw-1")
	idTok, pair := e.login(t, "pat@example.com", "pw-1")

	require.NoError(t, e.m.Logout(ctx, pair.RefreshToken))
	require.NoError(t, e.m.Logout(ctx, pair.RefreshToken), "a repeated logout must succeed")
	require.NoError(t, e.m.Logout(ctx, idTok), "the identity token of an already-ended sign-in must succeed")

	// A user who has not yet picked a tenant holds only the identity token.
	idOnly, err := e.m.Login(ctx, "pat@example.com", "pw-1")
	require.NoError(t, err)
	_, err = e.m.Memberships(ctx, idOnly.IdentityToken)
	require.NoError(t, err, "control: the new identity token works")
	require.NoError(t, e.m.Logout(ctx, idOnly.IdentityToken))
	_, err = e.m.SelectTenant(ctx, idOnly.IdentityToken, "acme")
	require.ErrorIs(t, err, ErrInvalidToken, "logout with the identity token left the sign-in open")

	require.ErrorIs(t, e.m.Logout(ctx, "not-a-token"), ErrInvalidToken)
	svc, err := e.m.IssueServiceToken("svc", nil)
	require.NoError(t, err)
	require.ErrorIs(t, e.m.Logout(ctx, svc.Token), ErrInvalidToken, "only a refresh or identity token can sign a session out")
}

// A TOKEN WITH NO SESSION ID IS REFUSED, NOT TREATED AS AN OLDER KIND. These are signed
// with the real instance key and (for refresh tokens) registered in the KV, so the
// missing sid is the only thing that can refuse them; the control with a sid, forged the
// same way, is accepted.
func TestATokenWithoutASessionIDIsRefusedEverywhere(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	e.createMember(t, "pat@example.com", "pw-1")
	epoch := e.rowEpoch(t, "pat@example.com")

	_, err := e.m.Refresh(ctx, e.forge(t, tenantRefreshClaims("pat@example.com", epoch), true))
	require.NoError(t, err, "control: a forged refresh token WITH a live sign-in is accepted")
	_, err = e.refreshOAuth(e.forge(t, oauthRefreshClaims("pat@example.com", epoch), true))
	require.NoError(t, err, "control: a forged OAuth refresh token WITH a live sign-in is accepted")
	_, err = e.m.SelectTenant(ctx, e.forge(t, identityClaims("pat@example.com", epoch), false), "acme")
	require.NoError(t, err, "control: a forged identity token WITH a live sign-in is accepted")

	_, err = e.m.Refresh(ctx, e.forgeWithoutSid(t, tenantRefreshClaims("pat@example.com", epoch), true))
	require.ErrorIs(t, err, ErrInvalidToken, "a refresh token with no sid rotated")
	_, err = e.refreshOAuth(e.forgeWithoutSid(t, oauthRefreshClaims("pat@example.com", epoch), true))
	assertOAuthErrorCode(t, err, "invalid_grant")
	noSid := e.forgeWithoutSid(t, identityClaims("pat@example.com", epoch), false)
	_, err = e.m.SelectTenant(ctx, noSid, "acme")
	require.ErrorIs(t, err, ErrInvalidToken, "an identity token with no sid started a refresh chain")
	_, err = e.m.Memberships(ctx, noSid)
	require.ErrorIs(t, err, ErrInvalidToken)
	_, err = e.issueCode(t, noSid)
	require.ErrorIs(t, err, ErrInvalidToken)
	require.ErrorIs(t, e.m.Logout(ctx, noSid), ErrInvalidToken, "logout accepted a token naming no sign-in")
}

// A sid names its owner: another identity's live sign-in cannot carry this one's tokens.
func TestASessionIDOfAnotherIdentityIsRefused(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	e.createMember(t, "pat@example.com", "pw-1")
	e.createMember(t, "sam@example.com", "pw-2")
	epoch := e.rowEpoch(t, "pat@example.com")
	samSid := e.sidOf(t, "sam@example.com")

	claims := tenantRefreshClaims("pat@example.com", epoch)
	claims.SessionID = samSid
	_, err := e.m.Refresh(ctx, e.forgeWithoutSid(t, claims, true))
	require.ErrorIs(t, err, ErrInvalidToken)
}

// The sign-out is audited once per call that ended a sign-in named by a valid token.
func TestLogoutIsAudited(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	e.createMember(t, "pat@example.com", "pw-1")
	_, pair := e.login(t, "pat@example.com", "pw-1")
	require.Equal(t, int64(0), e.auditCount(t, rdb.AuditOpLogout))

	require.NoError(t, e.m.Logout(ctx, pair.RefreshToken))
	require.Equal(t, int64(1), e.auditCount(t, rdb.AuditOpLogout))

	require.ErrorIs(t, e.m.Logout(ctx, "not-a-token"), ErrInvalidToken)
	require.Equal(t, int64(1), e.auditCount(t, rdb.AuditOpLogout), "a refused logout must not be audited as a sign-out")
}

// faultyKV fails the operations a test names on keys with the given prefix and passes
// everything else to the real bucket.
type faultyKV struct {
	nats.KeyValue
	prefix              string
	failGet, failDelete bool
}

var errStoreDown = errors.New("store down")

func (f *faultyKV) Get(key string) (nats.KeyValueEntry, error) {
	if f.failGet && strings.HasPrefix(key, f.prefix) {
		return nil, errStoreDown
	}
	return f.KeyValue.Get(key)
}

func (f *faultyKV) Delete(key string, opts ...nats.DeleteOpt) error {
	if f.failDelete && strings.HasPrefix(key, f.prefix) {
		return errStoreDown
	}
	return f.KeyValue.Delete(key, opts...)
}

// A sign-out the store could not record is NOT a success: the sign-in is still open, so
// the caller must hear a retryable error (and nothing is audited); once the store is back
// the same token signs out and the sign-in ends.
func TestLogoutReportsAStoreFailureAndLeavesTheSignInOpen(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	e.createMember(t, "pat@example.com", "pw-1")
	idTok, pair := e.login(t, "pat@example.com", "pw-1")
	real := e.m.refreshKV
	e.m.refreshKV = &faultyKV{KeyValue: real, prefix: sessionKeyPrefix, failDelete: true}

	require.ErrorIs(t, e.m.Logout(ctx, pair.RefreshToken), ErrLogoutUnavailable)
	require.ErrorIs(t, e.m.Logout(ctx, idTok), ErrLogoutUnavailable)
	require.Equal(t, int64(0), e.auditCount(t, rdb.AuditOpLogout), "a failed sign-out was audited")

	e.m.refreshKV = real
	_, err := e.m.SelectTenant(ctx, idTok, "acme")
	require.NoError(t, err, "the failed sign-out ended the sign-in anyway, yet reported failure")
	require.NoError(t, e.m.Logout(ctx, idTok))
	_, err = e.m.SelectTenant(ctx, idTok, "acme")
	require.ErrorIs(t, err, ErrInvalidToken, "the retried sign-out left the sign-in open")
}

// A session store that cannot be READ is an outage, not a closed session: the refresh is
// retryable and the token is left unconsumed, so it works once the store recovers.
func TestASessionStoreReadFailureIsRetryableNotTerminal(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	e.createMember(t, "pat@example.com", "pw-1")
	_, pair := e.login(t, "pat@example.com", "pw-1")
	real := e.m.refreshKV
	e.m.refreshKV = &faultyKV{KeyValue: real, prefix: sessionKeyPrefix, failGet: true}

	_, err := e.m.Refresh(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, ErrRefreshUnavailable)

	e.m.refreshKV = real
	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.NoError(t, err, "a store outage burned the refresh token")
}

// Logout burns the presented refresh token itself, not only the session record: its jti
// is gone from the store, so a refresh that read the token before the record was deleted
// loses its revision-checked claim.
func TestLogoutBurnsThePresentedRefreshToken(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	e.createMember(t, "pat@example.com", "pw-1")
	_, pair := e.login(t, "pat@example.com", "pw-1")
	claims, err := e.m.validator.ValidateRefresh(pair.RefreshToken)
	require.NoError(t, err)
	_, err = e.m.refreshKV.Get(claims.ID)
	require.NoError(t, err, "control: the refresh jti is in the store before the logout")

	require.NoError(t, e.m.Logout(ctx, pair.RefreshToken))
	_, err = e.m.refreshKV.Get(claims.ID)
	require.ErrorIs(t, err, nats.ErrKeyNotFound, "logout left the presented refresh token's jti in the store")
}

// The owner comparison is EXACT: an identity whose email extends, or is extended by,
// another's cannot carry that one's sign-in. Both directions, each with a control.
func TestASessionIDOfAPrefixSharingIdentityIsRefused(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	const short, long = "pat@example.com", "pat@example.com.au"
	e.createMember(t, short, "pw-1")
	e.createMember(t, long, "pw-2")
	shortSid, longSid := e.sidOf(t, short), e.sidOf(t, long)

	for _, c := range []struct {
		email      string
		own, other auth.SessionID
	}{{short, shortSid, longSid}, {long, longSid, shortSid}} {
		claims := tenantRefreshClaims(c.email, e.rowEpoch(t, c.email))
		claims.SessionID = c.own
		_, err := e.m.Refresh(ctx, e.forgeWithoutSid(t, claims, true))
		require.NoError(t, err, "control: %s with its own sign-in is accepted", c.email)

		claims.SessionID = c.other
		_, err = e.m.Refresh(ctx, e.forgeWithoutSid(t, claims, true))
		require.ErrorIs(t, err, ErrInvalidToken, "%s carried a prefix-sharing identity's sign-in", c.email)
	}
}

// signIdentity signs an identity token for the given sign-in with key (naming the
// instance key's kid whatever key signs it), expiring at exp.
func (e *sessionEnv) signIdentity(t *testing.T, key *rsa.PrivateKey, email string, sid auth.SessionID, exp time.Time) string {
	t.Helper()
	c := identityClaims(email, e.rowEpoch(t, email))
	c.SessionID = sid
	c.ID = uuid.NewString()
	c.Subject = email
	c.IssuedAt = jwt.NewNumericDate(exp.Add(-15 * time.Minute))
	c.ExpiresAt = jwt.NewNumericDate(exp)
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, &c)
	tok.Header["kid"] = auth.Thumbprint(&e.key.PublicKey)
	signed, err := tok.SignedString(key)
	require.NoError(t, err)
	return signed
}

// An identity token that has EXPIRED still names its sign-in, whose refresh chains may be
// live: signing out with it ends them. Only the signature decides whether it is ours.
func TestLogoutWithAnExpiredGenuineIdentityTokenEndsTheSignIn(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	e.createMember(t, "pat@example.com", "pw-1")
	idTok, pair := e.login(t, "pat@example.com", "pw-1")
	idClaims, err := e.m.validator.ParseSignatureOnly(idTok)
	require.NoError(t, err)
	expired := e.signIdentity(t, e.key, "pat@example.com", idClaims.SessionID, time.Now().Add(-time.Minute))
	_, err = e.m.validator.ValidateIdentity(expired)
	require.Error(t, err, "control: the token really is expired")

	require.NoError(t, e.m.Logout(ctx, expired))
	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken, "an expired identity token's sign-out left its refresh chain live")
	require.Equal(t, int64(1), e.auditCount(t, rdb.AuditOpLogout))
}

// A token signed with another key is garbage, expired or not: refused with the same
// error, and the sign-in it names is untouched.
func TestLogoutRefusesAForgedTokenExpiredOrNot(t *testing.T) {
	e := newSessionEnv(t)
	ctx := context.Background()
	e.createMember(t, "pat@example.com", "pw-1")
	idTok, pair := e.login(t, "pat@example.com", "pw-1")
	idClaims, err := e.m.validator.ParseSignatureOnly(idTok)
	require.NoError(t, err)
	other, err := auth.GenerateKeyPair()
	require.NoError(t, err)

	for name, exp := range map[string]time.Time{"expired": time.Now().Add(-time.Minute), "live": time.Now().Add(time.Hour)} {
		forged := e.signIdentity(t, other, "pat@example.com", idClaims.SessionID, exp)
		require.ErrorIs(t, e.m.Logout(ctx, forged), ErrInvalidToken, "a %s forged token", name)
	}
	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.NoError(t, err, "a forged token's sign-out ended a real sign-in")
	require.Equal(t, int64(0), e.auditCount(t, rdb.AuditOpLogout))
}
