// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// These tests run on SQLite over the REAL migration chain with foreign keys enforced. They cover
// what does not depend on the database's clock or row locks: the seed's life with its row, the
// recovery codes, the enrolment states and the purge. The charge-first lock, the replay
// compare-and-set and every concurrency property need a real Postgres and live in
// mfa_integration_test.go.

var mfaSeed = []byte("12345678901234567890")

func secretExists(t *testing.T, store secrets.SecretStore, ref secrets.SecretRef) bool {
	t.Helper()
	ok, err := store.Exists(context.Background(), ref)
	require.NoError(t, err)
	return ok
}

func enrolled(t *testing.T, s *Store, store secrets.SecretStore, email string) (*Identity, *MfaFactor) {
	t.Helper()
	ctx := context.Background()
	id := ssoIdentity(t, s, email)
	f, err := s.BeginMfaEnrollment(ctx, store, id.ID, mfaSeed)
	require.NoError(t, err)
	require.NoError(t, s.ConfirmMfaEnrollment(ctx, store, id.ID, stepVerify(100, true), []string{"d1", "d2", "d3"}, time.Now()))
	return id, f
}

// stepVerify is a scripted confirmation verifier: it ignores the seed and answers (step, ok).
func stepVerify(step int64, ok bool) func([]byte) (int64, bool, error) {
	return func([]byte) (int64, bool, error) { return step, ok, nil }
}

func TestTheSeedIsSealedWithItsRowAndNeverInIt(t *testing.T) {
	ctx := context.Background()
	s, store := newSsoFixture(t)
	id := ssoIdentity(t, s, "a@example.com")

	f, err := s.BeginMfaEnrollment(ctx, store, id.ID, mfaSeed)
	require.NoError(t, err)
	require.Equal(t, MfaStatePending, f.State)
	require.Equal(t, "mfa/totp/"+itoa(f.ID), f.SecretName)

	got, err := store.Resolve(ctx, MfaSeedRef(f.ID))
	require.NoError(t, err)
	require.Equal(t, mfaSeed, got, "the sealed seed must be the one enrolled")

	// The row carries the NAME of the seed, never the seed: no column of the stored row holds it.
	var raw MfaFactor
	require.NoError(t, s.sys(ctx).First(&raw, "id = ?", f.ID).Error)
	require.NotContains(t, raw.SecretName, string(mfaSeed))
	var sealed struct{ Ciphertext []byte }
	require.NoError(t, s.sys(ctx).Raw("SELECT ciphertext FROM secrets WHERE name = ?", f.SecretName).Scan(&sealed).Error)
	require.NotEmpty(t, sealed.Ciphertext)
	require.NotContains(t, string(sealed.Ciphertext), string(mfaSeed), "the secrets table must hold the seed encrypted")
}

func itoa(n uint) string { return strconv.FormatUint(uint64(n), 10) }

// The seed and its row are one fact: rolled back together. This is the property BindTx exists
// for, asserted on THIS store's enrolment, so an enrolment that sealed outside its transaction
// would leave ciphertext nothing refers to.
func TestARolledBackEnrollmentLeavesNoSecretAndNoRow(t *testing.T) {
	ctx := context.Background()
	s, store := newSsoFixture(t)
	id := ssoIdentity(t, s, "a@example.com")
	errRollback := errors.New("roll back")

	var ref secrets.SecretRef
	err := s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		f, err := beginMfaEnrollmentIn(ctx, tx, store, id.ID, mfaSeed)
		require.NoError(t, err)
		ref = MfaSeedRef(f.ID)
		// Inside the transaction the seed IS there: the rollback below removes it, not a Put
		// that silently did nothing.
		bound, err := secrets.BindTx(store, tx)
		require.NoError(t, err)
		ok, err := bound.Exists(ctx, ref)
		require.NoError(t, err)
		require.True(t, ok)
		return errRollback
	})
	require.ErrorIs(t, err, errRollback)

	require.False(t, secretExists(t, store, ref), "a seed sealed in a rolled-back transaction must not survive it")
	has, err := s.HasMfaFactor(ctx, id.ID)
	require.NoError(t, err)
	require.False(t, has, "the factor row must roll back with the seed")
}

func TestARolledBackRemovalKeepsTheSeedAndTheRow(t *testing.T) {
	ctx := context.Background()
	s, store := newSsoFixture(t)
	id, f := enrolled(t, s, store, "a@example.com")
	errRollback := errors.New("roll back")

	err := s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		removed, err := removeMfaIn(ctx, tx, store, id.ID)
		require.NoError(t, err)
		require.True(t, removed)
		return errRollback
	})
	require.ErrorIs(t, err, errRollback)
	require.True(t, secretExists(t, store, MfaSeedRef(f.ID)), "the delete of the seed must roll back with the row")
	_, err = s.MfaFactorFor(ctx, id.ID)
	require.NoError(t, err)
}

func TestEnrollmentStatesAndExpiry(t *testing.T) {
	ctx := context.Background()
	s, store := newSsoFixture(t)
	id := ssoIdentity(t, s, "a@example.com")

	first, err := s.BeginMfaEnrollment(ctx, store, id.ID, mfaSeed)
	require.NoError(t, err)

	// Beginning again REPLACES the pending factor and removes the old seed.
	second, err := s.BeginMfaEnrollment(ctx, store, id.ID, []byte("another seed value 20b"))
	require.NoError(t, err)
	require.NotEqual(t, first.ID, second.ID)
	require.False(t, secretExists(t, store, MfaSeedRef(first.ID)), "the replaced pending factor's seed must go with it")
	var n int64
	require.NoError(t, s.sys(ctx).Model(&MfaFactor{}).Where("identity_id = ?", id.ID).Count(&n).Error)
	require.EqualValues(t, 1, n)

	// An expired pending factor cannot be confirmed; the twin below confirms in time.
	require.ErrorIs(t, s.ConfirmMfaEnrollment(ctx, store, id.ID, stepVerify(5, true), []string{"d"},
		time.Now().Add(MfaEnrollmentTTL+time.Second)), ErrMfaEnrollmentNotPending)
	has, err := s.MfaRecoveryCodesRemaining(ctx, id.ID)
	require.NoError(t, err)
	require.Zero(t, has, "a refused confirmation must store no recovery codes")

	require.NoError(t, s.ConfirmMfaEnrollment(ctx, store, id.ID, stepVerify(5, true), []string{"d1", "d2"}, time.Now()))
	f, err := s.MfaFactorFor(ctx, id.ID)
	require.NoError(t, err)
	require.Equal(t, MfaStateActive, f.State)
	require.EqualValues(t, 5, f.LastStep, "the confirming code's step is recorded so that code cannot sign in")
	require.NotNil(t, f.ConfirmedAt)

	// Confirming twice, and enrolling over an active factor, are both refused.
	require.ErrorIs(t, s.ConfirmMfaEnrollment(ctx, store, id.ID, stepVerify(6, true), []string{"x"}, time.Now()),
		ErrMfaEnrollmentNotPending)
	_, err = s.BeginMfaEnrollment(ctx, store, id.ID, mfaSeed)
	require.ErrorIs(t, err, ErrMfaAlreadyEnrolled)
	require.True(t, secretExists(t, store, MfaSeedRef(second.ID)), "a refused enrolment must not touch the active seed")
}

func TestRecoveryCodesAreSingleUseAndReplaceable(t *testing.T) {
	ctx := context.Background()
	s, store := newSsoFixture(t)
	id, _ := enrolled(t, s, store, "a@example.com")
	other, _ := enrolled(t, s, store, "b@example.com")

	left, err := s.MfaRecoveryCodesRemaining(ctx, id.ID)
	require.NoError(t, err)
	require.EqualValues(t, 3, left)

	ok, err := s.RedeemMfaRecoveryCode(ctx, id.ID, "d1", time.Now())
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = s.RedeemMfaRecoveryCode(ctx, id.ID, "d1", time.Now())
	require.NoError(t, err)
	require.False(t, ok, "a used code must be refused")

	// One identity's digest never redeems another's.
	ok, err = s.RedeemMfaRecoveryCode(ctx, other.ID, "d2", time.Now())
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = s.RedeemMfaRecoveryCode(ctx, id.ID, "d2", time.Now())
	require.NoError(t, err)
	require.True(t, ok, "the first identity still holds its own d2")

	ok, err = s.RedeemMfaRecoveryCode(ctx, id.ID, "", time.Now())
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = s.RedeemMfaRecoveryCode(ctx, id.ID, "nope", time.Now())
	require.NoError(t, err)
	require.False(t, ok)

	// Regenerating replaces them all, used or not.
	require.NoError(t, s.ReplaceMfaRecoveryCodes(ctx, id.ID, []string{"n1", "n2"}))
	ok, err = s.RedeemMfaRecoveryCode(ctx, id.ID, "d3", time.Now())
	require.NoError(t, err)
	require.False(t, ok, "an old code must not survive regeneration")
	ok, err = s.RedeemMfaRecoveryCode(ctx, id.ID, "n1", time.Now())
	require.NoError(t, err)
	require.True(t, ok)

	// A digest is unique per identity.
	require.Error(t, s.ReplaceMfaRecoveryCodes(ctx, id.ID, []string{"dup", "dup"}))

	// No factor, no codes.
	bare := ssoIdentity(t, s, "c@example.com")
	require.ErrorIs(t, s.ReplaceMfaRecoveryCodes(ctx, bare.ID, []string{"x"}), ErrMfaFactorNotFound)
}

// T13 / purge: every removal takes the seed, the codes and the factor, and only that identity's.
func TestRemoveMfaTakesTheSeedTheCodesAndTheFactor(t *testing.T) {
	ctx := context.Background()
	s, store := newSsoFixture(t)
	id, f := enrolled(t, s, store, "a@example.com")
	other, otherF := enrolled(t, s, store, "b@example.com")

	// A per-source failure row, to prove it goes too.
	require.NoError(t, s.sys(ctx).Create(&MfaSourceFailure{FactorID: f.ID, SourceKey: "203.0.113.7", FailedCount: 2, UpdatedAt: time.Now()}).Error)

	removed, err := s.RemoveMfa(ctx, store, id.ID)
	require.NoError(t, err)
	require.True(t, removed)

	require.False(t, secretExists(t, store, MfaSeedRef(f.ID)), "no secrets row may remain for the removed factor")
	has, err := s.HasMfaFactor(ctx, id.ID)
	require.NoError(t, err)
	require.False(t, has)
	left, err := s.MfaRecoveryCodesRemaining(ctx, id.ID)
	require.NoError(t, err)
	require.Zero(t, left)
	var src int64
	require.NoError(t, s.sys(ctx).Model(&MfaSourceFailure{}).Where("factor_id = ?", f.ID).Count(&src).Error)
	require.Zero(t, src)

	// The bystander is untouched (the control for a removal that matched too much).
	require.True(t, secretExists(t, store, MfaSeedRef(otherF.ID)))
	left, err = s.MfaRecoveryCodesRemaining(ctx, other.ID)
	require.NoError(t, err)
	require.EqualValues(t, 3, left)

	// Removing again is a no-op, and needs no secret store.
	removed, err = s.RemoveMfa(ctx, nil, id.ID)
	require.NoError(t, err)
	require.False(t, removed)

	// An identity that HAS a factor is refused a nil store before anything is deleted.
	_, err = s.RemoveMfa(ctx, nil, other.ID)
	require.ErrorIs(t, err, ErrMfaNeedsSecretStore)
	require.True(t, secretExists(t, store, MfaSeedRef(otherF.ID)))
}

func TestDeletingAnIdentityRemovesItsSecondFactorAndItsSeed(t *testing.T) {
	ctx := context.Background()
	s, store := newSsoFixture(t)
	id, f := enrolled(t, s, store, "a@example.com")
	loaded, err := s.IdentityByID(ctx, id.ID)
	require.NoError(t, err)

	// Refused without the store, and refused BEFORE anything is deleted.
	require.ErrorIs(t, s.DeleteIdentity(ctx, nil, loaded), ErrMfaNeedsSecretStore)
	_, err = s.IdentityByID(ctx, id.ID)
	require.NoError(t, err, "a refused delete must leave the identity")
	require.True(t, secretExists(t, store, MfaSeedRef(f.ID)))

	require.NoError(t, s.DeleteIdentity(ctx, store, loaded))
	require.False(t, secretExists(t, store, MfaSeedRef(f.ID)), "deleting the identity must delete its sealed seed")
	var n int64
	require.NoError(t, s.sys(ctx).Model(&MfaFactor{}).Count(&n).Error)
	require.Zero(t, n)
	require.NoError(t, s.sys(ctx).Model(&MfaRecoveryCode{}).Count(&n).Error)
	require.Zero(t, n)

	// An identity with no factor deletes without a store, as before.
	plain := ssoIdentity(t, s, "p@example.com")
	loadedPlain, err := s.IdentityByID(ctx, plain.ID)
	require.NoError(t, err)
	require.NoError(t, s.DeleteIdentity(ctx, nil, loadedPlain))
}

func TestTheTenantFlagAndTheTierFloorCompose(t *testing.T) {
	ctx := context.Background()
	s, _ := newSsoFixture(t)

	tier := &TenantTier{Token: "floor", Config: map[string]any{MfaRequiredConfigKey: true}}
	require.NoError(t, s.CreateTenantTier(ctx, tier))
	open := &TenantTier{Token: "open"}
	require.NoError(t, s.CreateTenantTier(ctx, open))

	floored := &Tenant{Token: "floored", TierID: tier.ID, Enabled: true}
	require.NoError(t, s.CreateTenant(ctx, floored))
	plain := &Tenant{Token: "plain", TierID: open.ID, Enabled: true}
	require.NoError(t, s.CreateTenant(ctx, plain))

	load := func(tok string) *Tenant {
		got, err := s.TenantByToken(ctx, tok)
		require.NoError(t, err)
		return got
	}
	require.False(t, load("plain").MfaEffectivelyRequired(), "nothing set: not required")
	require.True(t, load("floored").MfaEffectivelyRequired(), "the tier floor alone requires it")

	require.NoError(t, s.SetTenantMfaRequired(ctx, plain, true))
	require.True(t, load("plain").MfaEffectivelyRequired(), "the tenant flag alone requires it")
	require.True(t, load("plain").MfaRequired)
	require.False(t, load("plain").MfaRequiredByTier())

	// The tenant cannot subtract from the floor.
	require.NoError(t, s.SetTenantMfaRequired(ctx, floored, false))
	require.True(t, load("floored").MfaEffectivelyRequired(), "clearing the tenant flag must not clear the tier floor")
	require.True(t, load("floored").MfaRequiredByTier())

	require.NoError(t, s.SetTenantMfaRequired(ctx, plain, false))
	require.False(t, load("plain").MfaEffectivelyRequired())
}

func TestTheTierFloorKeyIsStrictlyBoolean(t *testing.T) {
	require.NoError(t, ValidateTierConfig(map[string]any{MfaRequiredConfigKey: true}))
	require.NoError(t, ValidateTierConfig(map[string]any{MfaRequiredConfigKey: false}))
	for _, bad := range []any{"true", 1, 0, nil, 1.0} {
		require.Errorf(t, ValidateTierConfig(map[string]any{MfaRequiredConfigKey: bad}), "%#v must be refused", bad)
	}
	require.Contains(t, TierConfigKeys(), MfaRequiredConfigKey)
	require.Contains(t, TierFlagKeys(), MfaRequiredConfigKey)

	// Read defensively: a junk value parked by an out-of-band write is OFF, never half-on.
	require.False(t, (&TenantTier{Config: map[string]any{MfaRequiredConfigKey: "yes"}}).MfaRequired())
	require.False(t, (*TenantTier)(nil).MfaRequired())
	require.False(t, (&TenantTier{}).MfaRequired())
	require.True(t, (&TenantTier{Config: map[string]any{MfaRequiredConfigKey: true}}).MfaRequired())
}

func TestTheSourceKeyGroupsAnIPv6SubscriberAndUnmapsIPv4(t *testing.T) {
	key := func(s string) string {
		k, err := MfaSourceKey(netip.MustParseAddr(s))
		require.NoError(t, err)
		return k
	}
	require.Equal(t, "203.0.113.7", key("203.0.113.7"))
	require.Equal(t, "203.0.113.7", key("::ffff:203.0.113.7"), "an IPv4-mapped address is the IPv4 address")
	require.Equal(t, key("2001:db8:1:2::1"), key("2001:db8:1:2:ffff:ffff:ffff:ffff"), "one /64 is one source")
	require.NotEqual(t, key("2001:db8:1:2::1"), key("2001:db8:1:3::1"), "a different /64 is a different source")
	require.Equal(t, "2001:db8:1:2::/64", key("2001:db8:1:2::1%eth0"))
	_, err := MfaSourceKey(netip.Addr{})
	require.ErrorIs(t, err, ErrMfaSourceKey)
}

func TestAnAttemptWithoutASourceOrStoreIsRefusedBeforeAnyCharge(t *testing.T) {
	ctx := context.Background()
	s, store := newSsoFixture(t)
	_, f := enrolled(t, s, store, "a@example.com")
	verify := func([]byte) (int64, bool, error) { t.Fatal("must not be evaluated"); return 0, false, nil }

	_, err := s.MfaVerifyTOTP(ctx, store, f.ID, "", verify)
	require.ErrorIs(t, err, ErrMfaSourceKey)
	_, err = s.MfaVerifyTOTP(ctx, nil, f.ID, "203.0.113.7", verify)
	require.Error(t, err)
}

// The policy numbers are the spec's, and every assertion elsewhere is written against LITERALS so
// that changing one is a red test and not a silently shifted expectation. This pins the constants
// to those literals, in one place, so the failure names the policy that moved.
func TestThePolicyNumbersAreThePinnedOnes(t *testing.T) {
	require.Equal(t, 5, MfaSourceLockAfter, "a source locks after 5 failures")
	require.Equal(t, 20, MfaIdentityLockAfter, "the identity locks after 20 failures")
	require.Equal(t, 15*time.Minute, MfaLockCap)
	require.Equal(t, time.Minute, MfaLockBase)
	require.Equal(t, 10*time.Minute, MfaEnrollmentTTL)
}

// mfaLockExpr generates, from the policy constants, the SQL for the lock a failure arms once its
// count reaches threshold. The charge statements are literals (the raw-SQL guard must read them),
// so this is the generator they are held to.
func mfaLockExpr(count string, threshold int) string {
	return fmt.Sprintf(
		"CASE WHEN %[1]s >= %[2]d THEN now() + LEAST(interval '%[3]d seconds', "+
			"interval '%[4]d seconds' * power(2, LEAST(%[1]s - %[2]d, %[5]d))) ELSE NULL END",
		count, threshold, int(MfaLockCap/time.Second), int(MfaLockBase/time.Second), mfaLockMaxDoublings)
}

func TestTheChargeStatementsAreTheLockPolicy(t *testing.T) {
	require.Contains(t, chargeFactorSQL, mfaLockExpr("failed_count + 1", MfaIdentityLockAfter),
		"the identity charge's literal lock expression has drifted from the policy constants")
	require.Contains(t, chargeSourceSQL, mfaLockExpr("iam_mfa_source_failures.failed_count + 1", MfaSourceLockAfter),
		"the source charge's literal lock expression has drifted from the policy constants")
}

// Failed confirmations are charged against the pending enrolment, and the fifth voids it, seed
// included. The verifier runs on the sealed seed, inside the transaction.
func TestAPendingEnrollmentIsVoidAfterFiveWrongConfirmations(t *testing.T) {
	ctx := context.Background()
	s, store := newSsoFixture(t)
	id := ssoIdentity(t, s, "a@example.com")
	f, err := s.BeginMfaEnrollment(ctx, store, id.ID, mfaSeed)
	require.NoError(t, err)

	var saw []byte
	wrong := func(seed []byte) (int64, bool, error) { saw = append([]byte(nil), seed...); return 0, false, nil }
	for i := 1; i <= 4; i++ {
		err := s.ConfirmMfaEnrollment(ctx, store, id.ID, wrong, []string{"d"}, time.Now())
		require.ErrorIs(t, err, ErrMfaCodeInvalid, "wrong code %d", i)
		require.Equal(t, mfaSeed, saw, "the verifier must be handed the sealed seed")
		row := factorRowSQLite(t, s, f.ID)
		require.Equal(t, i, row.FailedCount, "each wrong code is charged")
		require.Equal(t, MfaStatePending, row.State)
	}
	// The twin: a right code before the budget is spent still confirms.
	// (Checked on a second identity so the void below is not pre-empted.)
	id2 := ssoIdentity(t, s, "b@example.com")
	_, err = s.BeginMfaEnrollment(ctx, store, id2.ID, mfaSeed)
	require.NoError(t, err)
	for i := 0; i < 4; i++ {
		require.ErrorIs(t, s.ConfirmMfaEnrollment(ctx, store, id2.ID, wrong, []string{"d"}, time.Now()), ErrMfaCodeInvalid)
	}
	require.NoError(t, s.ConfirmMfaEnrollment(ctx, store, id2.ID, stepVerify(9, true), []string{"d"}, time.Now()),
		"four wrong codes leave the enrolment confirmable")

	// The fifth wrong code voids the first enrolment.
	require.ErrorIs(t, s.ConfirmMfaEnrollment(ctx, store, id.ID, wrong, []string{"d"}, time.Now()), ErrMfaEnrollmentVoid)
	has, err := s.HasMfaFactor(ctx, id.ID)
	require.NoError(t, err)
	require.False(t, has, "a void enrolment is removed")
	require.False(t, secretExists(t, store, MfaSeedRef(f.ID)), "and so is its seed")
	require.ErrorIs(t, s.ConfirmMfaEnrollment(ctx, store, id.ID, stepVerify(9, true), []string{"d"}, time.Now()),
		ErrMfaEnrollmentNotPending, "even the right code cannot confirm a void enrolment")
}

func TestAConfirmationVerifierErrorIsChargedAndReturned(t *testing.T) {
	ctx := context.Background()
	s, store := newSsoFixture(t)
	id := ssoIdentity(t, s, "a@example.com")
	f, err := s.BeginMfaEnrollment(ctx, store, id.ID, mfaSeed)
	require.NoError(t, err)
	boom := errors.New("verifier failed")
	err = s.ConfirmMfaEnrollment(ctx, store, id.ID, func([]byte) (int64, bool, error) { return 0, false, boom },
		[]string{"d"}, time.Now())
	require.ErrorIs(t, err, boom)
	require.Equal(t, 1, factorRowSQLite(t, s, f.ID).FailedCount, "an erroring verifier must not be a free guess")
}

func factorRowSQLite(t *testing.T, s *Store, id uint) MfaFactor {
	t.Helper()
	var f MfaFactor
	require.NoError(t, s.sys(context.Background()).First(&f, "id = ?", id).Error)
	return f
}
