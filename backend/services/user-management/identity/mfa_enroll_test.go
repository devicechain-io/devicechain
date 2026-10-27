// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"encoding/base32"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// T12. Enrolment, confirmation, regeneration and disabling all need the PASSWORD (identity
// tokens travel), and the last three need a CODE as well. Every refusal changes nothing, and each
// is paired with the same call succeeding with the right credentials.

func (e *mfaEnv) factorState(t *testing.T, email string) (hasRow bool, state string, codesLeft int64, sealed bool) {
	t.Helper()
	ctx := context.Background()
	id, err := e.store.IdentityByEmail(ctx, email)
	require.NoError(t, err)
	hasRow, err = e.store.HasMfaFactor(ctx, id.ID)
	require.NoError(t, err)
	if hasRow {
		f, err := e.store.MfaFactorFor(ctx, id.ID)
		require.NoError(t, err)
		state = f.State
		ok, err := e.m.secrets.Exists(ctx, iam.MfaSeedRef(f.ID))
		require.NoError(t, err)
		sealed = ok
	}
	codesLeft, err = e.store.MfaRecoveryCodesRemaining(ctx, id.ID)
	require.NoError(t, err)
	return hasRow, state, codesLeft, sealed
}

func TestEnrollmentNeedsThePasswordAndTheRightCode(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	ctx := context.Background()
	ia, err := e.m.Login(ctx, "pat@example.com", "pw-1")
	require.NoError(t, err)

	// A garbage token and a wrong password: refused, nothing written.
	_, err = e.m.BeginTotpEnrollment(ctx, "not-a-token", "pw-1")
	require.ErrorIs(t, err, ErrInvalidToken)
	_, err = e.m.BeginTotpEnrollment(ctx, ia.IdentityToken, "wrong")
	require.ErrorIs(t, err, ErrInvalidCredentials)
	has, _, _, _ := e.factorState(t, "pat@example.com")
	require.False(t, has, "a wrong password wrote a factor row")

	// The twin begins.
	enr, err := e.m.BeginTotpEnrollment(ctx, ia.IdentityToken, "pw-1")
	require.NoError(t, err)
	require.Contains(t, enr.OtpauthURI, "otpauth://totp/")
	require.Contains(t, enr.OtpauthURI, "secret="+enr.Secret)
	require.Contains(t, enr.OtpauthURI, "issuer="+DefaultMfaIssuer)
	require.WithinDuration(t, time.Now().Add(iam.MfaEnrollmentTTL), enr.ExpiresAt, time.Minute)
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enr.Secret)
	require.NoError(t, err)
	has, state, _, sealed := e.factorState(t, "pat@example.com")
	require.True(t, has)
	require.Equal(t, iam.MfaStatePending, state)
	require.True(t, sealed)
	// A pending factor gates no sign-in.
	_, err = e.m.Login(ctx, "pat@example.com", "pw-1")
	require.NoError(t, err)

	good := credential.GenerateTOTP(seed, time.Now())
	// Right code, wrong password: the code is not even evaluated.
	_, err = e.m.ConfirmTotpEnrollment(ctx, ia.IdentityToken, "wrong", good)
	require.ErrorIs(t, err, ErrInvalidCredentials)
	// Right password, wrong code, and a malformed one.
	wrong := "000000"
	if good == wrong {
		wrong = "111111"
	}
	_, err = e.m.ConfirmTotpEnrollment(ctx, ia.IdentityToken, "pw-1", wrong)
	require.ErrorIs(t, err, ErrMfaInvalidCode)
	_, err = e.m.ConfirmTotpEnrollment(ctx, ia.IdentityToken, "pw-1", "abc")
	require.ErrorIs(t, err, ErrMfaInvalidCode)
	_, state, codes, _ := e.factorState(t, "pat@example.com")
	require.Equal(t, iam.MfaStatePending, state, "a refused confirmation activated the factor")
	require.Zero(t, codes)

	// The twin confirms, once, and gets ten recovery codes.
	recovery, err := e.m.ConfirmTotpEnrollment(ctx, ia.IdentityToken, "pw-1", good)
	require.NoError(t, err)
	require.Len(t, recovery, 10)
	_, state, codes, _ = e.factorState(t, "pat@example.com")
	require.Equal(t, iam.MfaStateActive, state)
	require.EqualValues(t, 10, codes)
	require.Equal(t, int64(1), e.auditCount(t, rdb.AuditOpMfaEnrolled))

	// The session that enrolled is NOT upgraded: its token still says password only.
	require.Equal(t, auth.AmrPasswordOnly(), e.claims(t, ia.IdentityToken).Amr)
	// The proving code cannot sign in (its step is recorded as used), and a second begin is refused.
	_, err = e.m.BeginTotpEnrollment(ctx, ia.IdentityToken, "pw-1")
	require.True(t, isCode(err, CodeMfaAlreadyEnrolled), "got %v", err)

	status, err := e.m.MfaStatusFor(ctx, ia.IdentityToken)
	require.NoError(t, err)
	require.True(t, status.TotpEnabled)
	require.EqualValues(t, 10, status.RecoveryCodesRemaining)
	require.NotNil(t, status.EnrolledAt)
	plain, err := e.m.MfaStatusFor(ctx, e.signInWith(t, "pat@example.com", auth.AmrPasswordOnly()))
	require.NoError(t, err)
	require.True(t, plain.TotpEnabled)
}

func TestFiveWrongConfirmationCodesCancelThePendingEnrollment(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	ctx := context.Background()
	ia, err := e.m.Login(ctx, "pat@example.com", "pw-1")
	require.NoError(t, err)
	enr, err := e.m.BeginTotpEnrollment(ctx, ia.IdentityToken, "pw-1")
	require.NoError(t, err)
	seed, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enr.Secret)
	wrong := "000000"
	if credential.GenerateTOTP(seed, time.Now()) == wrong {
		wrong = "111111"
	}
	for i := 0; i < iam.MfaConfirmBudget-1; i++ {
		_, err = e.m.ConfirmTotpEnrollment(ctx, ia.IdentityToken, "pw-1", wrong)
		require.ErrorIs(t, err, ErrMfaInvalidCode)
	}
	_, err = e.m.ConfirmTotpEnrollment(ctx, ia.IdentityToken, "pw-1", wrong)
	require.ErrorIs(t, err, iam.ErrMfaEnrollmentVoid)
	has, _, _, sealed := e.factorState(t, "pat@example.com")
	require.False(t, has)
	require.False(t, sealed, "a voided enrolment left its seed behind")
}

func TestRegeneratingRecoveryCodesNeedsThePasswordAndACode(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	_, recovery := e.enroll(t, "pat@example.com", "pw-1")
	ctx := context.Background()
	tok := e.signInWith(t, "pat@example.com", auth.AmrPasswordOTP())
	unchanged := func() {
		t.Helper()
		_, _, left, _ := e.factorState(t, "pat@example.com")
		require.EqualValues(t, 10, left)
	}

	_, err := e.m.RegenerateRecoveryCodes(ctx, tok, "wrong", recovery[0], "203.0.113.5")
	require.ErrorIs(t, err, ErrInvalidCredentials)
	_, err = e.m.RegenerateRecoveryCodes(ctx, tok, "pw-1", "", "203.0.113.5")
	require.ErrorIs(t, err, ErrMfaInvalidCode)
	_, err = e.m.RegenerateRecoveryCodes(ctx, tok, "pw-1", "ABCD-EFGH-JKMN-PQRS", "203.0.113.5")
	require.ErrorIs(t, err, ErrMfaInvalidCode)
	unchanged()
	require.Zero(t, e.auditCount(t, rdb.AuditOpMfaRecoveryCodesRegenerated))

	fresh, err := e.m.RegenerateRecoveryCodes(ctx, tok, "pw-1", recovery[0], "203.0.113.5")
	require.NoError(t, err)
	require.Len(t, fresh, 10)
	require.NotEqual(t, recovery, fresh)
	unchanged() // the old set (one spent) was replaced by ten fresh ones
	require.Equal(t, int64(1), e.auditCount(t, rdb.AuditOpMfaRecoveryCodesRegenerated))

	// The old codes are dead; a new one signs in.
	ch := e.challenge(t, "pat@example.com", "pw-1")
	_, err = e.m.VerifyMfa(ctx, ch.Challenge, recovery[1], "203.0.113.5")
	require.ErrorIs(t, err, ErrMfaInvalidCode, "a replaced recovery code still worked")
	_, err = e.m.VerifyMfa(ctx, ch.Challenge, fresh[0], "203.0.113.5")
	require.NoError(t, err)
}

func TestDisablingTheSecondFactorNeedsThePasswordAndACodeAndEndsEverySession(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	_, recovery := e.enroll(t, "pat@example.com", "pw-1")
	ctx := context.Background()
	tok := e.signInWith(t, "pat@example.com", auth.AmrPasswordOTP())

	require.ErrorIs(t, e.m.DisableTotp(ctx, tok, "wrong", recovery[0], "203.0.113.5"), ErrInvalidCredentials)
	require.ErrorIs(t, e.m.DisableTotp(ctx, tok, "pw-1", "", "203.0.113.5"), ErrMfaInvalidCode)
	require.ErrorIs(t, e.m.DisableTotp(ctx, tok, "pw-1", "ABCD-EFGH-JKMN-PQRS", "203.0.113.5"), ErrMfaInvalidCode)
	has, state, left, sealed := e.factorState(t, "pat@example.com")
	require.True(t, has)
	require.Equal(t, iam.MfaStateActive, state)
	require.EqualValues(t, 10, left)
	require.True(t, sealed)
	require.Zero(t, e.auditCount(t, rdb.AuditOpMfaDisabled))

	// The twin disables -- as ONE re-authentication: a request that allows one credential check
	// is enough for the pair.
	epochBefore := e.rowEpoch(t, "pat@example.com")
	oneCheck := credential.WithRequestBudget(ctx, 1)
	require.NoError(t, e.m.DisableTotp(oneCheck, tok, "pw-1", recovery[0], "203.0.113.5"))
	has, _, left, sealed = e.factorState(t, "pat@example.com")
	require.False(t, has)
	require.Zero(t, left)
	require.False(t, sealed, "disabling left the seed behind")
	require.Equal(t, int64(1), e.auditCount(t, rdb.AuditOpMfaDisabled))
	require.NotEqual(t, epochBefore, e.rowEpoch(t, "pat@example.com"), "disabling rotates the session epoch")

	// Every session ended: the identity token is dead, and a password sign-in needs no code now.
	_, err := e.m.Memberships(ctx, tok)
	require.ErrorIs(t, err, ErrInvalidToken)
	ia, err := e.m.Login(ctx, "pat@example.com", "pw-1")
	require.NoError(t, err)
	require.Equal(t, auth.AmrPasswordOnly(), e.claims(t, ia.IdentityToken).Amr)
}

// The pair is ONE unit of the request budget: a second copy in the same request is refused before
// anything is read, so aliasing the mutation buys nothing.
func TestAliasedRegenerationsInOneRequestSpendOnePair(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	_, recovery := e.enroll(t, "pat@example.com", "pw-1")
	tok := e.signInWith(t, "pat@example.com", auth.AmrPasswordOTP())

	ctx := credential.WithRequestBudget(context.Background(), 1)
	_, err := e.m.RegenerateRecoveryCodes(ctx, tok, "pw-1", recovery[0], "203.0.113.5")
	require.NoError(t, err)
	_, err = e.m.RegenerateRecoveryCodes(ctx, tok, "pw-1", recovery[1], "203.0.113.5")
	require.ErrorIs(t, err, credential.ErrRequestBudgetExhausted)
	_, err = e.m.RegenerateRecoveryCodes(ctx, tok, "wrong", recovery[1], "203.0.113.5")
	require.ErrorIs(t, err, credential.ErrRequestBudgetExhausted, "a wrong password is not evaluated either once the budget is spent")
}

// The login row is written when the sign-in COMPLETES and says how: pwd, pwd+otp or pwd+recovery.
func TestTheLoginAuditRowSaysHowTheSignInCompleted(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	e.createMember(t, "sam@example.com", "pw-2")
	_, recovery := e.enroll(t, "pat@example.com", "pw-1") // its own password-only sign-in first
	ctx := context.Background()
	_, err := e.m.Login(ctx, "sam@example.com", "pw-2")
	require.NoError(t, err)
	ch := e.challenge(t, "pat@example.com", "pw-1")
	_, err = e.m.VerifyMfa(ctx, ch.Challenge, recovery[0], "203.0.113.5")
	require.NoError(t, err)

	labels := func(actor string) []string {
		var rows []rdb.AuditEvent
		require.NoError(t, e.db().WithContext(core.WithSystemContext(ctx)).
			Where("operation = ? AND actor = ?", rdb.AuditOpLogin, actor).Order("id").Find(&rows).Error)
		var out []string
		for _, r := range rows {
			out = append(out, r.EntityLabel)
		}
		return out
	}
	require.Equal(t, []string{"pwd"}, labels("sam@example.com"))
	require.Equal(t, []string{"pwd", "pwd+recovery"}, labels("pat@example.com"),
		"the enrolling sign-in is a password one; the challenged one is recorded when it completed")
}

// T10. A service account never goes near the second factor: a tenant and a tier that require one
// still mint its client_credentials token, which carries no amr, and a token that CLAIMS one is
// refused by the validator.
func TestServiceAccountsAreUntouchedByTheRequirement(t *testing.T) {
	cc := newCCEnv(t)
	ctx := context.Background()
	tn, err := cc.store.TenantByToken(ctx, "acme")
	require.NoError(t, err)
	require.NoError(t, cc.store.SetTenantMfaRequired(ctx, tn, true))
	tier := tn.Tier
	tier.Config = map[string]any{iam.MfaRequiredConfigKey: true}
	require.NoError(t, cc.store.UpdateTenantTier(ctx, tier))
	tn, err = cc.store.TenantByToken(ctx, "acme")
	require.NoError(t, err)
	require.True(t, tn.MfaRequiredByTier() && tn.MfaRequired, "precondition: the tenant AND its tier require the factor")

	id, secret := cc.account(t, "acme", "ci", nil, "device:read")
	rec := cc.grant(t, id, secret)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	claims, err := cc.validator.Validate(decode(t, rec).AccessToken)
	require.NoError(t, err)
	require.Nil(t, claims.Amr, "a service account's token never carries amr")

	// A token that claims one is refused outright.
	e := newMfaEnv(t)
	forged := auth.Claims{Tenant: "acme", Username: auth.ServiceAccountSubject("ci"), ClientId: "dcsa_ci_acme",
		Authorities: []string{"device:read"}, TokenType: auth.TokenTypeAccess, SessionEpoch: "x", SessionID: auth.SessionID(uuid.NewString())}
	_, err = e.validator.Validate(e.forgeWithoutSid(t, forged, false))
	require.NoError(t, err, "the control: the same token without amr is valid")
	forged.Amr = auth.AmrPasswordOTP()
	_, err = e.validator.Validate(e.forgeWithoutSid(t, forged, false))
	require.ErrorIs(t, err, auth.ErrMalformedAmr)
}

// ---- operator reset (T17) ------------------------------------------------------------------

func (e *mfaEnv) operator(email string) context.Context {
	return auth.WithClaims(context.Background(), &auth.Claims{Username: email, Email: email, TokenType: auth.TokenTypeIdentity})
}

func TestAResetWithFailuresOutstandingNeedsANewPasswordAndChangesNothingWithout(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	e.enroll(t, "pat@example.com", "pw-1")
	e.createMember(t, "ops@example.com", "pw-ops")
	ctx := e.operator("ops@example.com")
	before, err := e.store.IdentityByEmail(ctx, "pat@example.com")
	require.NoError(t, err)

	// Failures on record: a wrong code was charged.
	require.NoError(t, e.db().Exec("UPDATE iam_mfa_factors SET failed_count = 3").Error)

	_, err = e.m.ResetIdentityMfa(ctx, "pat@example.com", "")
	require.True(t, isCode(err, CodeMfaResetNeedsPassword), "got %v", err)
	after, err := e.store.IdentityByEmail(ctx, "pat@example.com")
	require.NoError(t, err)
	require.Equal(t, before.PasswordHash, after.PasswordHash)
	require.Equal(t, before.SessionEpoch, after.SessionEpoch, "a refused reset changed nothing")
	has, _, left, sealed := e.factorState(t, "pat@example.com")
	require.True(t, has && sealed)
	require.EqualValues(t, 10, left)
	require.Zero(t, e.auditCount(t, rdb.AuditOpMfaReset))

	// The twin: with a new password the factor goes and the password changes together.
	_, err = e.m.ResetIdentityMfa(ctx, "pat@example.com", "pw-new")
	require.NoError(t, err)
	after, err = e.store.IdentityByEmail(ctx, "pat@example.com")
	require.NoError(t, err)
	require.NotEqual(t, before.PasswordHash, after.PasswordHash)
	require.NotEqual(t, before.SessionEpoch, after.SessionEpoch)
	has, _, left, sealed = e.factorState(t, "pat@example.com")
	require.False(t, has)
	require.Zero(t, left)
	require.False(t, sealed, "the reset left the seed behind")
	_, err = e.m.Login(context.Background(), "pat@example.com", "pw-1")
	require.ErrorIs(t, err, ErrInvalidCredentials, "the old password must not work")
	ia, err := e.m.Login(context.Background(), "pat@example.com", "pw-new")
	require.NoError(t, err, "the new password signs in with no code")
	require.NotEmpty(t, ia.IdentityToken)
	require.Equal(t, int64(1), e.auditCount(t, rdb.AuditOpMfaReset))
}

func TestEveryKindOfOutstandingFailureRequiresTheNewPassword(t *testing.T) {
	for name, arrange := range map[string]string{
		"identity count": "UPDATE iam_mfa_factors SET failed_count = 1",
		"identity lock":  "UPDATE iam_mfa_factors SET locked_until = '2999-01-01 00:00:00'",
		"source row":     "INSERT INTO iam_mfa_source_failures (factor_id, source_key, failed_count, updated_at) SELECT id, '203.0.113.5', 2, CURRENT_TIMESTAMP FROM iam_mfa_factors",
	} {
		t.Run(name, func(t *testing.T) {
			e := newMfaEnv(t)
			e.createMember(t, "pat@example.com", "pw-1")
			e.enroll(t, "pat@example.com", "pw-1")
			e.createMember(t, "ops@example.com", "pw-ops")
			require.NoError(t, e.db().Exec(arrange).Error)
			_, err := e.m.ResetIdentityMfa(e.operator("ops@example.com"), "pat@example.com", "")
			require.True(t, isCode(err, CodeMfaResetNeedsPassword), "got %v", err)
		})
	}
}

func TestAResetWithNoFailuresNeedsNoPasswordButEndsTheSessions(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	e.enroll(t, "pat@example.com", "pw-1")
	e.createMember(t, "ops@example.com", "pw-ops")
	tok := e.signInWith(t, "pat@example.com", auth.AmrPasswordOTP())

	_, err := e.m.ResetIdentityMfa(e.operator("ops@example.com"), "pat@example.com", "")
	require.NoError(t, err)
	_, err = e.m.Memberships(context.Background(), tok)
	require.ErrorIs(t, err, ErrInvalidToken, "the reset ends every session")
	ia, err := e.m.Login(context.Background(), "pat@example.com", "pw-1")
	require.NoError(t, err, "the person's own password still works, with no code now")
	require.NotEmpty(t, ia.IdentityToken)

	_, err = e.m.ResetIdentityMfa(e.operator("ops@example.com"), "pat@example.com", "")
	require.ErrorIs(t, err, ErrMfaNotEnrolled, "resetting an identity with no factor is an error, not a silent success")
	_, err = e.m.ResetIdentityMfa(e.operator("ops@example.com"), "nobody@example.com", "")
	require.ErrorIs(t, err, ErrIdentityNotFound)
}

// Targeting an identity that holds a system role needs a superuser caller; the twin is the same
// call by a superuser.
func TestOnlyASuperuserMayResetAnIdentityThatHoldsASystemRole(t *testing.T) {
	e := newMfaEnv(t)
	ctx := context.Background()
	e.seedSuperuser(t, "root@example.com")
	e.createMember(t, "ops@example.com", "pw-ops") // an operator with user:write but no system role of its own
	roles, err := e.store.CatalogueRolesByScopeTokens(ctx, iam.ScopeSystem, []string{iam.SuperuserRoleToken})
	require.NoError(t, err)
	require.NoError(t, e.store.CreateIdentity(ctx, &iam.Identity{Email: "root2@example.com", Enabled: true,
		PasswordHash: "x", SystemRoles: roles}))

	// Enrol the second superuser's factor directly through the store.
	target, err := e.store.IdentityByEmail(ctx, "root2@example.com")
	require.NoError(t, err)
	_, err = e.store.BeginMfaEnrollment(ctx, e.m.secrets, target.ID, []byte("12345678901234567890"))
	require.NoError(t, err)
	require.NoError(t, e.store.ConfirmMfaEnrollment(ctx, e.m.secrets, target.ID,
		func([]byte) (int64, bool, error) { return 5, true, nil }, []string{"d1"}, time.Now()))

	_, err = e.m.ResetIdentityMfa(e.operator("ops@example.com"), "root2@example.com", "")
	require.ErrorIs(t, err, auth.ErrForbidden)
	has, err := e.store.HasMfaFactor(ctx, target.ID)
	require.NoError(t, err)
	require.True(t, has, "a refused reset removed the factor")
	_, err = e.m.ResetIdentityMfa(context.Background(), "root2@example.com", "")
	require.ErrorIs(t, err, auth.ErrForbidden, "a request with no caller is refused too")

	_, err = e.m.ResetIdentityMfa(e.operator("root@example.com"), "root2@example.com", "")
	require.NoError(t, err)
	has, err = e.store.HasMfaFactor(ctx, target.ID)
	require.NoError(t, err)
	require.False(t, has)
}

// ---- the stale per-source row sweep ---------------------------------------------------------

func TestTheSweepPrunesIdleSourceRowsAndOnlyThose(t *testing.T) {
	e := newMfaEnv(t)
	e.createMember(t, "pat@example.com", "pw-1")
	e.enroll(t, "pat@example.com", "pw-1")
	ctx := core.WithSystemContext(context.Background())
	id, err := e.store.IdentityByEmail(ctx, "pat@example.com")
	require.NoError(t, err)
	f, err := e.store.MfaFactorFor(ctx, id.ID)
	require.NoError(t, err)
	old := time.Now().Add(-iam.MfaSourceFailureRetention - time.Hour)
	require.NoError(t, e.db().Create(&iam.MfaSourceFailure{FactorID: f.ID, SourceKey: "198.51.100.1", FailedCount: 3, UpdatedAt: old}).Error)
	require.NoError(t, e.db().Create(&iam.MfaSourceFailure{FactorID: f.ID, SourceKey: "198.51.100.2", FailedCount: 1, UpdatedAt: time.Now()}).Error)

	ms := &core.Microservice{FunctionalArea: "user-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	sweeper := NewMfaSourceFailureSweeper(ms, e.store, time.Hour, core.NewNoOpLifecycleCallbacks())
	require.NoError(t, sweeper.RunOnce(ctx))

	var rows []iam.MfaSourceFailure
	require.NoError(t, e.db().Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, "198.51.100.2", rows[0].SourceKey, "the idle row went and the recent one stayed")
}
