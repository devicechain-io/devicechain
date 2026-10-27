// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The tenant policy: "this tenant requires a second factor". It is applied in ONE place,
// admitPasswordSession, which every grant point already goes through -- tenant selection,
// refresh, the OAuth code issue and redemption, the OAuth refresh -- so each is driven here.

// signInWith opens a password sign-in whose record carries amr (nil for a record written by a
// build that predates amr) and returns its identity token, WITHOUT passing through Login: the
// way to hold a session of a chosen kind when the point is what the policy does with it.
func (e *mfaEnv) signInWith(t *testing.T, email string, amr []string) string {
	t.Helper()
	sid, err := e.m.startSession(email, amr)
	require.NoError(t, err)
	var opts []auth.MintOption
	if len(amr) > 0 {
		opts = append(opts, auth.WithAmr(amr))
	}
	tok, err := e.m.issuer.IssueIdentity(email, auth.SessionEpoch(e.rowEpoch(t, email)), sid, nil, nil, uuid.NewString(), opts...)
	require.NoError(t, err)
	return tok.Token
}

func (e *mfaEnv) requireMfa(t *testing.T, tenant string, required bool) {
	t.Helper()
	ctx := context.Background()
	tn, err := e.store.TenantByToken(ctx, tenant)
	require.NoError(t, err)
	require.NoError(t, e.store.SetTenantMfaRequired(ctx, tn, required))
}

func TestATenantThatRequiresASecondFactorAdmitsOnlyASessionThatUsedOne(t *testing.T) {
	e := newMfaEnv(t)
	ctx := context.Background()
	e.createMember(t, "plain@example.com", "pw-1")  // no factor
	e.createMember(t, "holder@example.com", "pw-2") // has a factor
	_, recovery := e.enroll(t, "holder@example.com", "pw-2")
	e.createMember(t, "outsider@example.com", "pw-3")
	require.NoError(t, e.db().Exec("DELETE FROM iam_memberships WHERE identity_id = (SELECT id FROM iam_identities WHERE email = ?)", "outsider@example.com").Error)

	// The control: the tenant does not require it, and a password-only session enters.
	_, err := e.m.SelectTenant(ctx, e.signInWith(t, "plain@example.com", auth.AmrPasswordOnly()), "acme")
	require.NoError(t, err)

	e.requireMfa(t, "acme", true)

	// No factor to present: it must enrol first.
	_, err = e.m.SelectTenant(ctx, e.signInWith(t, "plain@example.com", auth.AmrPasswordOnly()), "acme")
	require.True(t, isCode(err, CodeMfaEnrollmentRequired), "got %v", err)
	// A factor, but this sign-in did not use it: sign in again.
	_, err = e.m.SelectTenant(ctx, e.signInWith(t, "holder@example.com", auth.AmrPasswordOnly()), "acme")
	require.True(t, isCode(err, CodeMfaRequired), "got %v", err)
	// An older build's record has no amr at all, which reads as no second factor.
	_, err = e.m.SelectTenant(ctx, e.signInWith(t, "holder@example.com", nil), "acme")
	require.True(t, isCode(err, CodeMfaRequired), "a record without amr must be refused, got %v", err)
	require.Equal(t, int64(3), e.auditCount(t, rdb.AuditOpLoginFailed), "each refusal is a failed login")

	// A session that used one is admitted: the real flow, and a crafted authenticator session.
	ch := e.challenge(t, "holder@example.com", "pw-2")
	ia, err := e.m.VerifyMfa(ctx, ch.Challenge, recovery[0], "203.0.113.5")
	require.NoError(t, err)
	require.False(t, ia.Memberships[0].MfaRequired, "the sign-in that used a second factor is not told it needs one")
	pair, err := e.m.SelectTenant(ctx, ia.IdentityToken, "acme")
	require.NoError(t, err)
	access, err := e.validator.Validate(pair.AccessToken)
	require.NoError(t, err)
	require.Equal(t, auth.AmrPasswordRecovery(), access.Amr)
	_, err = e.m.SelectTenant(ctx, e.signInWith(t, "holder@example.com", auth.AmrPasswordOTP()), "acme")
	require.NoError(t, err)

	// The membership list says which tenants this sign-in cannot enter, as it does for SSO.
	memberships, err := e.m.Memberships(ctx, e.signInWith(t, "plain@example.com", auth.AmrPasswordOnly()))
	require.NoError(t, err)
	require.True(t, memberships[0].MfaRequired)

	// A non-member never learns the tenant requires one: the membership check comes first.
	_, err = e.m.SelectTenant(ctx, e.signInWith(t, "outsider@example.com", auth.AmrPasswordOnly()), "acme")
	require.ErrorIs(t, err, ErrInvalidCredentials)
	require.False(t, isCode(err, CodeMfaRequired) || isCode(err, CodeMfaEnrollmentRequired))
}

// A refresh re-applies the requirement: the tenant started requiring it under a session that
// did not use one. The token is CONSUMED, like any definite denial.
func TestARefreshIsRefusedWhenTheTenantStartsRequiringASecondFactor(t *testing.T) {
	e := newMfaEnv(t)
	ctx := context.Background()
	e.createMember(t, "plain@example.com", "pw-1")
	pair, err := e.m.SelectTenant(ctx, e.signInWith(t, "plain@example.com", auth.AmrPasswordOnly()), "acme")
	require.NoError(t, err)

	e.requireMfa(t, "acme", true)
	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.True(t, isCode(err, CodeMfaEnrollmentRequired), "got %v", err)
	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.ErrorIs(t, err, ErrInvalidToken, "the refused refresh token was consumed")

	// The twin: the requirement off again and a fresh session refreshes.
	e.requireMfa(t, "acme", false)
	pair, err = e.m.SelectTenant(ctx, e.signInWith(t, "plain@example.com", auth.AmrPasswordOnly()), "acme")
	require.NoError(t, err)
	_, err = e.m.Refresh(ctx, pair.RefreshToken)
	require.NoError(t, err)
}

// The OAuth grants: the authorize step issues no code to a session without the factor, a code
// issued before the tenant started requiring it is not redeemable, and the OAuth refresh is
// refused. A session with the factor does all three.
func TestTheOAuthGrantPointsApplyTheRequirement(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "plain@example.com", "pw-1")
	plain := e.signInWith(t, "plain@example.com", auth.AmrPasswordOnly())

	// Before the requirement: a code issues, redeems and refreshes.
	toks := e.oauthSession(t, plain)
	// A code issued now will be redeemed after the requirement starts.
	code, err := e.issueCode(t, plain)
	require.NoError(t, err)

	e.requireMfa(t, "acme", true)

	_, err = e.issueCode(t, plain)
	require.ErrorIs(t, err, errTenantAccessDenied, "no code is issued to a session without the factor")
	_, err = e.redeem(code)
	require.Error(t, err, "a code issued before the requirement is not redeemable without the factor")
	_, err = e.refreshOAuth(toks.RefreshToken)
	require.Error(t, err, "the OAuth refresh is refused")

	// The twin: a session with the factor passes all three, and its tokens carry the amr.
	with := e.signInWith(t, "plain@example.com", auth.AmrPasswordOTP())
	good := e.oauthSession(t, with)
	access, err := e.validator.Validate(good.AccessToken)
	require.NoError(t, err)
	require.Equal(t, auth.AmrPasswordOTP(), access.Amr, "OAuth tokens carry the amr of the sign-in they descend from")
	renewed, err := e.refreshOAuth(good.RefreshToken)
	require.NoError(t, err)
	access, err = e.validator.Validate(renewed.AccessToken)
	require.NoError(t, err)
	require.Equal(t, auth.AmrPasswordOTP(), access.Amr)
}

// The tier floor: a tenant whose own flag is OFF is still required to hold the factor when its
// tier says so, and a tenant administrator cannot turn that off.
func TestTheTierFloorBindsAndATenantAdministratorCannotLowerIt(t *testing.T) {
	e := newMfaEnv(t)
	ctx := context.Background()
	tier := &iam.TenantTier{Token: "regulated", Config: map[string]any{iam.MfaRequiredConfigKey: true}}
	require.NoError(t, e.store.CreateTenantTier(ctx, tier))
	require.NoError(t, e.store.CreateTenant(ctx, &iam.Tenant{Token: "floored", Enabled: true, TierID: tier.ID, PurgeState: iam.PurgeActive}))
	e.createMember(t, "plain@example.com", "pw-1")
	_, err := e.admin.AddMembership(ctx, "plain@example.com", "floored", nil)
	require.NoError(t, err)
	tn, err := e.store.TenantByToken(ctx, "floored")
	require.NoError(t, err)
	require.False(t, tn.MfaRequired, "precondition: the tenant's own flag is off")

	_, err = e.m.SelectTenant(ctx, e.signInWith(t, "plain@example.com", auth.AmrPasswordOnly()), "floored")
	require.True(t, isCode(err, CodeMfaEnrollmentRequired), "the tier floor binds with the tenant flag off, got %v", err)

	// The tenant administrator turns the flag off (it already is) and then on and off: the
	// effective requirement does not move.
	caller := auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "floored",
		Username: "plain@example.com", Email: "plain@example.com"})
	off, err := e.admin.SetOwnTenantMfaRequired(caller, false)
	require.NoError(t, err)
	require.False(t, off.MfaRequired)
	require.True(t, off.MfaEffectivelyRequired(), "a tenant setting cannot clear the tier floor")
	require.True(t, off.MfaRequiredByTier())
	_, err = e.m.SelectTenant(ctx, e.signInWith(t, "plain@example.com", auth.AmrPasswordOnly()), "floored")
	require.True(t, isCode(err, CodeMfaEnrollmentRequired), "still refused after the tenant flag was set off, got %v", err)

	// The twin: a tenant with no floor is controlled by its own flag.
	on, err := e.admin.SetOwnTenantMfaRequired(auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme",
		Username: "plain@example.com", Email: "plain@example.com"}), true)
	require.NoError(t, err)
	require.True(t, on.MfaEffectivelyRequired())
	require.Equal(t, int64(1), e.auditCount(t, rdb.AuditOpTenantMfaRequiredChanged))
}

// A scoped caller may not change the requirement, and neither may a caller who is no longer a
// member (the plane re-reads the live grant).
func TestTheTenantPlaneSetterRefusesAScopedCaller(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "plain@example.com", "pw-1")
	scoped := auth.WithClaims(context.Background(), &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme",
		Username: "plain@example.com", Email: "plain@example.com", ScopeAnchors: []string{"customer:c1"}})
	_, err := e.admin.SetOwnTenantMfaRequired(scoped, true)
	require.ErrorIs(t, err, auth.ErrScopeUnsupported)

	stranger := auth.WithClaims(context.Background(), &auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: "acme",
		Username: "nobody@example.com", Email: "nobody@example.com"})
	_, err = e.admin.SetOwnTenantMfaRequired(stranger, true)
	require.ErrorIs(t, err, auth.ErrForbidden)
	tn, err := e.store.TenantByToken(context.Background(), "acme")
	require.NoError(t, err)
	require.False(t, tn.MfaRequired)
}

// A superuser with a password-only session is admitted to a tenant that requires the factor, and
// each entry is audited. The twin is the same session for an ordinary member.
func TestASuperuserIsAdmittedWithoutTheFactorAndItIsAudited(t *testing.T) {
	e := newMfaEnv(t)
	ctx := context.Background()
	e.seedSuperuser(t, "root@example.com")
	e.createMember(t, "plain@example.com", "pw-1")
	e.requireMfa(t, "acme", true)

	_, err := e.m.SelectTenant(ctx, e.signInWith(t, "plain@example.com", auth.AmrPasswordOnly()), "acme")
	require.Error(t, err, "the control: an ordinary member is refused")
	require.Zero(t, e.auditCount(t, rdb.AuditOpMfaEnforcementBypassed))

	pair, err := e.m.SelectTenant(ctx, e.signInWith(t, "root@example.com", auth.AmrPasswordOnly()), "acme")
	require.NoError(t, err)
	access, err := e.validator.Validate(pair.AccessToken)
	require.NoError(t, err)
	require.True(t, access.ActingAsSuperuser)
	require.Equal(t, int64(1), e.auditCount(t, rdb.AuditOpMfaEnforcementBypassed))
	// Not told the tenant needs a factor: they are let in.
	memberships, err := e.m.Memberships(ctx, e.signInWith(t, "root@example.com", auth.AmrPasswordOnly()))
	require.NoError(t, err)
	for _, m := range memberships {
		require.False(t, m.MfaRequired)
	}
}

// T11. `amr` comes from the SESSION RECORD, never from a token the caller holds. A refresh token
// that CLAIMS a second factor, on a session that only ever used a password, yields tokens that
// say the password alone. The twin is a genuine pwd+otp session, whose refresh keeps its amr.
func TestAForgedAmrOnARefreshTokenIsNotBelieved(t *testing.T) {
	e := newMfaEnv(t)
	ctx := context.Background()
	e.createMember(t, "plain@example.com", "pw-1")
	email := "plain@example.com"
	epoch := auth.SessionEpoch(e.rowEpoch(t, email))

	pwdSid, err := e.m.startSession(email, auth.AmrPasswordOnly())
	require.NoError(t, err)
	claims := tenantRefreshClaims(email, epoch)
	claims.SessionID = pwdSid
	claims.Amr = auth.AmrPasswordOTP() // the lie
	forged := e.forge(t, claims, true)

	pair, err := e.m.Refresh(ctx, forged)
	require.NoError(t, err)
	access, err := e.validator.Validate(pair.AccessToken)
	require.NoError(t, err)
	require.Equal(t, auth.AmrPasswordOnly(), access.Amr, "the forged refresh token's amr must not be believed")
	refresh, err := e.validator.ValidateRefresh(pair.RefreshToken)
	require.NoError(t, err)
	require.Equal(t, auth.AmrPasswordOnly(), refresh.Amr)

	// And the forged claim buys nothing at a tenant that requires the factor.
	e.requireMfa(t, "acme", true)
	forged2 := e.forge(t, claims, true)
	_, err = e.m.Refresh(ctx, forged2)
	require.True(t, isCode(err, CodeMfaEnrollmentRequired), "got %v", err)

	// Twin: a session that really used one keeps its amr across a refresh.
	otpSid, err := e.m.startSession(email, auth.AmrPasswordOTP())
	require.NoError(t, err)
	genuine := tenantRefreshClaims(email, epoch)
	genuine.SessionID = otpSid
	genuine.Amr = auth.AmrPasswordOTP()
	pair, err = e.m.Refresh(ctx, e.forge(t, genuine, true))
	require.NoError(t, err)
	access, err = e.validator.Validate(pair.AccessToken)
	require.NoError(t, err)
	require.Equal(t, auth.AmrPasswordOTP(), access.Amr)

	// The same for the OAuth refresh: a forged claim on a password record is not believed.
	e.requireMfa(t, "acme", false)
	oauth := oauthRefreshClaims(email, epoch)
	oauth.SessionID = pwdSid
	oauth.Amr = auth.AmrPasswordOTP()
	toks, err := e.refreshOAuth(e.forge(t, oauth, true))
	require.NoError(t, err)
	access, err = e.validator.Validate(toks.AccessToken)
	require.NoError(t, err)
	require.Equal(t, auth.AmrPasswordOnly(), access.Amr)
}

// An older build's record has no amr, and its tokens carry none rather than a guess.
func TestARecordWithoutAmrMintsTokensWithoutOne(t *testing.T) {
	e := newMfaEnv(t)
	ctx := context.Background()
	e.createMember(t, "plain@example.com", "pw-1")
	pair, err := e.m.SelectTenant(ctx, e.signInWith(t, "plain@example.com", nil), "acme")
	require.NoError(t, err)
	access, err := e.validator.Validate(pair.AccessToken)
	require.NoError(t, err)
	require.Nil(t, access.Amr)
}

// The userinfo endpoint restates the token's amr.
func TestUserinfoReturnsTheTokensAmr(t *testing.T) {
	e := newMfaEnv(t)
	ctx := context.Background()
	e.createMember(t, "plain@example.com", "pw-1")
	pair, err := e.m.SelectTenant(ctx, e.signInWith(t, "plain@example.com", auth.AmrPasswordRecovery()), "acme")
	require.NoError(t, err)
	claims, err := e.validator.Validate(pair.AccessToken)
	require.NoError(t, err)
	rec := userinfoOf(t, claims)
	require.Equal(t, auth.AmrPasswordRecovery(), rec.Amr)
}

// userinfoOf runs the userinfo endpoint over claims and decodes its answer.
func userinfoOf(t *testing.T, claims *auth.Claims) userinfoResponse {
	t.Helper()
	h := UserinfoHandler(func(string) (*auth.Claims, error) { return claims, nil })
	req := httptest.NewRequest(http.MethodGet, "/oauth/userinfo", nil)
	req.Header.Set("Authorization", "Bearer x")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var out userinfoResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}
