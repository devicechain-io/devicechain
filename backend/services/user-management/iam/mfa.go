// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/devicechain-io/dc-microservice/secrets"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Native multi-factor authentication storage (TOTP). This file is the STORE only: the sign-in
// flow, the API and the console build on the methods below.
//
// Three rules shape everything here.
//
//   - The seed is sealed in the instance secret store in the SAME TRANSACTION as the row that
//     names it (secrets.BindTx), and removed in the same transaction as that row. A factor with
//     no seed can never verify, and a seed with no factor is ciphertext nothing can find.
//   - A guess is CHARGED BEFORE IT IS EVALUATED, in one statement that also refuses a locked
//     factor. A read-then-write count can be raced; this one cannot, because concurrent
//     attempts serialize on the factor's row lock and each one increments before any is judged.
//   - A TOTP code is single use: it is accepted only for a step strictly newer than the last
//     one accepted, and the acceptance is the same statement that resets the failure counts.

// The lock policy. A lock arms when a count reaches its threshold and lasts one minute, doubling
// for every failure after it, up to the cap. A source locks itself out long before it can lock
// the person out.
const (
	// MfaSourceLockAfter is the failures from ONE source after which that source is locked out
	// of the factor.
	MfaSourceLockAfter = 5
	// MfaIdentityLockAfter is the failures from ANY sources after which the whole factor is
	// locked.
	MfaIdentityLockAfter = 20
	// MfaLockCap is the longest a lock lasts.
	MfaLockCap = 15 * time.Minute
	// MfaLockBase is the first lock's duration; each further failure doubles it.
	MfaLockBase = time.Minute
	// MfaEnrollmentTTL is how long a pending factor can still be confirmed.
	MfaEnrollmentTTL = 10 * time.Minute
	// MfaSourceFailureRetention is how long an idle per-source row is kept.
	MfaSourceFailureRetention = 24 * time.Hour

	// mfaLockMaxDoublings bounds the exponent in the charge statements so the interval arithmetic
	// can never overflow: 2^10 minutes already exceeds the cap, and an unbounded power() would
	// raise "interval out of range" after a few hundred failures, turning a locked factor into a
	// failing request. Spelled as a literal in the statements; pinned by a test.
	mfaLockMaxDoublings = 10
)

// The factor kind and states.
const (
	MfaKindTotp      = "totp"
	MfaStatePending  = "pending"
	MfaStateActive   = "active"
	mfaSecretPrefix  = "mfa/totp/"
	mfaSourceKeySize = 64
)

var (
	// ErrMfaFactorNotFound: the identity has no (active) factor to act on.
	ErrMfaFactorNotFound = errors.New("no such multi-factor factor")
	// ErrMfaAlreadyEnrolled: the identity already has an active factor; it must be removed first.
	ErrMfaAlreadyEnrolled = errors.New("the identity already has an active second factor")
	// ErrMfaEnrollmentNotPending: confirming a factor that is not pending, or whose enrolment
	// window has passed.
	ErrMfaEnrollmentNotPending = errors.New("there is no pending enrolment to confirm, or it has expired")
	// ErrMfaNeedsSecretStore: a store that can seal and remove seeds was required and absent.
	ErrMfaNeedsSecretStore = errors.New("the identity has a second factor, so the secret store its seed is sealed in is required")
	// ErrMfaSourceKey: an attempt carried no source key to charge.
	ErrMfaSourceKey = errors.New("a second-factor attempt needs a source key")
)

// MfaFactor is one second factor of one identity. Identities are global, so the factor is too:
// it carries no tenant and the tenant purge never touches it.
type MfaFactor struct {
	ID         uint `gorm:"primarykey"`
	IdentityID uint
	Kind       string
	State      string
	// SecretName names the sealed seed (MfaSeedRef); the seed itself is never in this row.
	SecretName string
	// LastStep is the highest TOTP time step accepted.
	LastStep int64
	// FailedCount and LockedUntil are the identity-wide lock.
	FailedCount int
	LockedUntil *time.Time
	CreatedAt   time.Time
	ConfirmedAt *time.Time
	LastUsedAt  *time.Time
}

func (MfaFactor) TableName() string { return "iam_mfa_factors" }

// MfaSourceFailure is the failures charged to one factor from one source.
type MfaSourceFailure struct {
	FactorID    uint   `gorm:"primaryKey;autoIncrement:false"`
	SourceKey   string `gorm:"primaryKey;size:64"`
	FailedCount int
	LockedUntil *time.Time
	UpdatedAt   time.Time
}

func (MfaSourceFailure) TableName() string { return "iam_mfa_source_failures" }

// MfaRecoveryCode is one single-use recovery code, held as its digest only.
type MfaRecoveryCode struct {
	ID         uint `gorm:"primarykey"`
	IdentityID uint
	Digest     string
	UsedAt     *time.Time
}

func (MfaRecoveryCode) TableName() string { return "iam_mfa_recovery_codes" }

// MfaSeedRef is the handle a factor's seed is sealed under.
func MfaSeedRef(factorID uint) secrets.SecretRef {
	return secrets.SecretRef{Scope: secrets.ScopeInstance, Name: fmt.Sprintf("%s%d", mfaSecretPrefix, factorID)}
}

// MfaSourceKey is the per-source lock key of a client address: an IPv4 address as itself, an
// IPv6 address as its /64 (a single subscriber holds the whole /64, so keying on the full
// address would give an attacker 2^64 fresh allowances). An IPv4-mapped IPv6 address is the IPv4
// address. The zero address has no key.
func MfaSourceKey(addr netip.Addr) (string, error) {
	if !addr.IsValid() {
		return "", ErrMfaSourceKey
	}
	addr = addr.Unmap().WithZone("")
	if addr.Is4() {
		return addr.String(), nil
	}
	p, err := addr.Prefix(64)
	if err != nil {
		return "", err
	}
	return p.String(), nil
}

// MfaAttemptOutcome is how a TOTP attempt ended.
type MfaAttemptOutcome int

const (
	// MfaAttemptInvalid: the code did not verify. The charge stands.
	MfaAttemptInvalid MfaAttemptOutcome = iota
	// MfaAttemptSuccess: the code verified, for a step newer than any accepted, and the failure
	// counts were reset in the same statement.
	MfaAttemptSuccess
	// MfaAttemptLocked: the factor or the source is locked, so the code was NOT evaluated. A
	// caller answers it exactly as it answers MfaAttemptInvalid.
	MfaAttemptLocked
	// MfaAttemptReplay: the code verified but its step is not newer than the last accepted one.
	// A caller answers it exactly as it answers MfaAttemptInvalid.
	MfaAttemptReplay
)

func (o MfaAttemptOutcome) String() string {
	switch o {
	case MfaAttemptInvalid:
		return "invalid"
	case MfaAttemptSuccess:
		return "ok"
	case MfaAttemptLocked:
		return "locked"
	case MfaAttemptReplay:
		return "replay"
	}
	return "unknown"
}

// The two charge statements are spelled out as literals, not built from the policy constants: the
// raw-SQL guard reads them statically (a statement it cannot read is refused), and a reader
// should see the whole statement in one place. TestTheChargeStatementsAreTheLockPolicy generates
// them from the constants and fails if the literals drift from the policy.
//
// A lock arms when the NEW count reaches the threshold and lasts one minute, doubling for every
// failure past it, capped at 15 minutes; the exponent is bounded so the interval cannot overflow.

// chargeFactorSQL is the identity-wide charge: ONE statement that counts the attempt, arms the
// lock when the threshold is reached, and refuses (no row) when the factor is already locked or
// is not an active factor.
const chargeFactorSQL = "UPDATE iam_mfa_factors SET failed_count = failed_count + 1, locked_until = " +
	"CASE WHEN failed_count + 1 >= 20 THEN now() + LEAST(interval '900 seconds', " +
	"interval '60 seconds' * power(2, LEAST(failed_count + 1 - 20, 10))) ELSE NULL END " +
	"WHERE id = ? AND state = 'active' AND (locked_until IS NULL OR locked_until <= now()) " +
	"RETURNING failed_count"

// chargeSourceSQL is the per-source charge, the same shape as an upsert: the DO UPDATE's WHERE
// refuses (no row) a source that is already locked.
const chargeSourceSQL = "INSERT INTO iam_mfa_source_failures (factor_id, source_key, failed_count, locked_until, updated_at) " +
	"VALUES (?, ?, 1, NULL, now()) " +
	"ON CONFLICT (factor_id, source_key) DO UPDATE SET " +
	"failed_count = iam_mfa_source_failures.failed_count + 1, " +
	"locked_until = CASE WHEN iam_mfa_source_failures.failed_count + 1 >= 5 THEN now() + LEAST(interval '900 seconds', " +
	"interval '60 seconds' * power(2, LEAST(iam_mfa_source_failures.failed_count + 1 - 5, 10))) ELSE NULL END, " +
	"updated_at = now() " +
	"WHERE iam_mfa_source_failures.locked_until IS NULL OR iam_mfa_source_failures.locked_until <= now() " +
	"RETURNING failed_count"

// replayCASSQL accepts a step only if it is newer than the last accepted one, and resets the
// failure counts in the same statement.
const replayCASSQL = "UPDATE iam_mfa_factors SET last_step = ?, failed_count = 0, locked_until = NULL, " +
	"last_used_at = now() WHERE id = ? AND state = 'active' AND last_step < ?"

// MfaVerifyTOTP runs one TOTP attempt against an active factor, charge first.
//
// In ONE transaction: the identity-wide charge, the per-source charge, then the caller's verify
// against the unsealed seed, then (only on a verified code) the replay compare-and-set. A
// refusal at either charge returns MfaAttemptLocked WITHOUT evaluating the code, and the charge
// that was made stands; a code that does not verify keeps its charge. The transaction holds the
// factor's row lock throughout, which is what serializes concurrent attempts so that a burst of
// N evaluates exactly as many as the thresholds leave free.
//
// verify receives the cleartext seed (zeroed when it returns, so it must not keep it) and
// returns the matched step. It is the caller's to run through the credential Checker; an error
// from it, or from unsealing the seed, rolls the whole attempt back and is returned.
//
// sourceKey is MfaSourceKey of the client address.
func (s *Store) MfaVerifyTOTP(ctx context.Context, store secrets.SecretStore, factorID uint, sourceKey string,
	verify func(seed []byte) (step int64, ok bool, err error)) (MfaAttemptOutcome, error) {
	if sourceKey == "" || len(sourceKey) > mfaSourceKeySize {
		return MfaAttemptInvalid, ErrMfaSourceKey
	}
	if store == nil || verify == nil {
		return MfaAttemptInvalid, errors.New("a TOTP attempt needs the secret store and a verifier")
	}
	outcome := MfaAttemptInvalid
	err := s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		var counts []int
		if err := tx.Raw(chargeFactorSQL, factorID).Scan(&counts).Error; err != nil {
			return err
		}
		if len(counts) == 0 {
			// Refused: locked, or not an active factor. Tell the two apart so a missing factor
			// is a loud error and not a quiet "locked".
			var n int64
			if err := tx.Model(&MfaFactor{}).Where("id = ? AND state = ?", factorID, MfaStateActive).
				Count(&n).Error; err != nil {
				return err
			}
			if n == 0 {
				return ErrMfaFactorNotFound
			}
			outcome = MfaAttemptLocked
			return nil
		}
		counts = counts[:0]
		if err := tx.Raw(chargeSourceSQL, factorID, sourceKey).Scan(&counts).Error; err != nil {
			return err
		}
		if len(counts) == 0 {
			outcome = MfaAttemptLocked
			return nil
		}

		sealed, err := secrets.BindTx(store, tx)
		if err != nil {
			return err
		}
		seed, err := sealed.Resolve(ctx, MfaSeedRef(factorID))
		if err != nil {
			return fmt.Errorf("unsealing the second-factor seed: %w", err)
		}
		step, ok, verr := verify(seed)
		for i := range seed {
			seed[i] = 0
		}
		if verr != nil {
			return verr
		}
		if !ok {
			outcome = MfaAttemptInvalid
			return nil
		}

		cas := tx.Exec(replayCASSQL, step, factorID, step)
		if cas.Error != nil {
			return cas.Error
		}
		if cas.RowsAffected == 0 {
			outcome = MfaAttemptReplay
			return nil
		}
		outcome = MfaAttemptSuccess
		// The source that just proved itself starts clean; idle rows of other sources age out.
		if err := tx.Where("factor_id = ? AND source_key = ?", factorID, sourceKey).
			Delete(&MfaSourceFailure{}).Error; err != nil {
			return err
		}
		return tx.Where("factor_id = ? AND updated_at < ?", factorID, time.Now().Add(-MfaSourceFailureRetention)).
			Delete(&MfaSourceFailure{}).Error
	})
	if err != nil {
		return MfaAttemptInvalid, err
	}
	return outcome, nil
}

// MfaFactorFor returns the identity's factor, or ErrMfaFactorNotFound.
func (s *Store) MfaFactorFor(ctx context.Context, identityID uint) (*MfaFactor, error) {
	var f MfaFactor
	err := s.sys(ctx).Where("identity_id = ? AND kind = ?", identityID, MfaKindTotp).First(&f).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrMfaFactorNotFound
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// HasMfaFactor reports whether the identity has any factor row, pending or active. A factor is
// proof the identity has had a session, which is what an invitation's "never used" test needs.
func (s *Store) HasMfaFactor(ctx context.Context, identityID uint) (bool, error) {
	var n int64
	err := s.sys(ctx).Model(&MfaFactor{}).Where("identity_id = ?", identityID).Count(&n).Error
	return n > 0, err
}

// BeginMfaEnrollment creates a PENDING factor holding seed, sealed in the secret store in the
// same transaction as its row. Any earlier pending factor is replaced (its seed removed with it);
// an active one refuses with ErrMfaAlreadyEnrolled.
//
// The identity row is locked first, so two enrolments for one identity serialize rather than
// racing the unique index.
func (s *Store) BeginMfaEnrollment(ctx context.Context, store secrets.SecretStore, identityID uint,
	seed []byte) (*MfaFactor, error) {
	if store == nil {
		return nil, ErrMfaNeedsSecretStore
	}
	if len(seed) == 0 {
		return nil, errors.New("an enrolment needs a seed")
	}
	var out *MfaFactor
	err := s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		out, err = beginMfaEnrollmentIn(ctx, tx, store, identityID, seed)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// beginMfaEnrollmentIn is BeginMfaEnrollment inside the caller's transaction, which is what lets
// a test (and a later caller that must write something else with it) roll the whole thing back
// and see the sealed seed go with it.
func beginMfaEnrollmentIn(ctx context.Context, tx *gorm.DB, store secrets.SecretStore, identityID uint,
	seed []byte) (*MfaFactor, error) {
	var id Identity
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").
		Where("id = ?", identityID).First(&id).Error; err != nil {
		return nil, err
	}
	sealed, err := secrets.BindTx(store, tx)
	if err != nil {
		return nil, err
	}
	var existing []MfaFactor
	if err := tx.Where("identity_id = ?", identityID).Find(&existing).Error; err != nil {
		return nil, err
	}
	for _, f := range existing {
		if f.State == MfaStateActive {
			return nil, ErrMfaAlreadyEnrolled
		}
	}
	if _, err := removeMfaFactors(ctx, tx, sealed, identityID, false); err != nil {
		return nil, err
	}
	// Created with a placeholder name: the real one carries the id the insert assigns.
	f := &MfaFactor{IdentityID: identityID, Kind: MfaKindTotp, State: MfaStatePending,
		SecretName: "pending", CreatedAt: time.Now()}
	if err := tx.Create(f).Error; err != nil {
		return nil, err
	}
	ref := MfaSeedRef(f.ID)
	if err := tx.Model(f).Update("secret_name", ref.Name).Error; err != nil {
		return nil, err
	}
	f.SecretName = ref.Name
	if err := sealed.Put(ctx, ref, seed); err != nil {
		return nil, fmt.Errorf("sealing the second-factor seed: %w", err)
	}
	return f, nil
}

// ConfirmMfaEnrollment activates a pending factor and stores its first recovery codes, in one
// transaction. step is the TOTP step of the code the person just proved possession with: it is
// recorded as the last accepted, so that same code cannot then sign in. recoveryDigests replace
// any codes the identity held. It refuses (ErrMfaEnrollmentNotPending) a factor that is not
// pending or whose MfaEnrollmentTTL has passed.
func (s *Store) ConfirmMfaEnrollment(ctx context.Context, factorID uint, step int64, recoveryDigests []string,
	now time.Time) error {
	return s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&MfaFactor{ID: factorID}).
			Where("state = ? AND created_at > ?", MfaStatePending, now.Add(-MfaEnrollmentTTL)).
			Updates(map[string]any{
				"state": MfaStateActive, "last_step": step, "confirmed_at": now, "last_used_at": now,
				"failed_count": 0, "locked_until": nil,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrMfaEnrollmentNotPending
		}
		var f MfaFactor
		if err := tx.First(&f, "id = ?", factorID).Error; err != nil {
			return err
		}
		return replaceRecoveryCodes(tx, f.IdentityID, recoveryDigests)
	})
}

// ReplaceMfaRecoveryCodes replaces every recovery code the identity holds, used or not, with the
// given digests. It refuses an identity with no active factor: codes without a factor would be a
// way in that the factor's removal could not close.
func (s *Store) ReplaceMfaRecoveryCodes(ctx context.Context, identityID uint, digests []string) error {
	return s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&MfaFactor{}).Where("identity_id = ? AND state = ?", identityID, MfaStateActive).
			Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			return ErrMfaFactorNotFound
		}
		return replaceRecoveryCodes(tx, identityID, digests)
	})
}

func replaceRecoveryCodes(tx *gorm.DB, identityID uint, digests []string) error {
	if err := tx.Where("identity_id = ?", identityID).Delete(&MfaRecoveryCode{}).Error; err != nil {
		return err
	}
	if len(digests) == 0 {
		return nil
	}
	rows := make([]MfaRecoveryCode, 0, len(digests))
	for _, d := range digests {
		if d == "" {
			return errors.New("a recovery code digest is empty")
		}
		rows = append(rows, MfaRecoveryCode{IdentityID: identityID, Digest: d})
	}
	return tx.Create(&rows).Error
}

// RedeemMfaRecoveryCode spends one recovery code: ONE statement that marks it used only if it is
// still unused, so two concurrent redemptions of one code have exactly one winner. It reports
// whether this call won. Recovery codes are exempt from the TOTP locks (they carry 80 bits), and
// nothing here consults them.
func (s *Store) RedeemMfaRecoveryCode(ctx context.Context, identityID uint, digest string, now time.Time) (bool, error) {
	if digest == "" {
		return false, nil
	}
	res := s.sys(ctx).Model(&MfaRecoveryCode{}).
		Where("identity_id = ? AND digest = ? AND used_at IS NULL", identityID, digest).
		Update("used_at", now)
	return res.RowsAffected == 1, res.Error
}

// MfaRecoveryCodesRemaining counts the identity's unused recovery codes.
func (s *Store) MfaRecoveryCodesRemaining(ctx context.Context, identityID uint) (int64, error) {
	var n int64
	err := s.sys(ctx).Model(&MfaRecoveryCode{}).
		Where("identity_id = ? AND used_at IS NULL", identityID).Count(&n).Error
	return n, err
}

// RemoveMfa removes every factor, recovery code and sealed seed of an identity in ONE
// transaction, reporting whether there was anything to remove. It is the one removal every path
// uses (disable, reset, break-glass, clearing the password), so none of them can forget the seed.
//
// store may be nil only when the identity has nothing to remove; otherwise it is refused before
// anything is deleted.
func (s *Store) RemoveMfa(ctx context.Context, store secrets.SecretStore, identityID uint) (bool, error) {
	removed := false
	err := s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		removed, err = removeMfaIn(ctx, tx, store, identityID)
		return err
	})
	return removed, err
}

// removeMfaIn is RemoveMfa inside the caller's transaction.
func removeMfaIn(ctx context.Context, tx *gorm.DB, store secrets.SecretStore, identityID uint) (bool, error) {
	var n, codes int64
	if err := tx.Model(&MfaFactor{}).Where("identity_id = ?", identityID).Count(&n).Error; err != nil {
		return false, err
	}
	if err := tx.Model(&MfaRecoveryCode{}).Where("identity_id = ?", identityID).Count(&codes).Error; err != nil {
		return false, err
	}
	if n == 0 && codes == 0 {
		return false, nil
	}
	if store == nil {
		return false, ErrMfaNeedsSecretStore
	}
	sealed, err := secrets.BindTx(store, tx)
	if err != nil {
		return false, err
	}
	return removeMfaFactors(ctx, tx, sealed, identityID, true)
}

// removeMfaFactors deletes the identity's factors and their sealed seeds, and (withCodes) its
// recovery codes. The per-source rows go with the factor by cascade and are deleted explicitly
// as well, so the result does not depend on the database enforcing foreign keys.
func removeMfaFactors(ctx context.Context, tx *gorm.DB, sealed secrets.SecretStore, identityID uint,
	withCodes bool) (bool, error) {
	var factors []MfaFactor
	if err := tx.Where("identity_id = ?", identityID).Find(&factors).Error; err != nil {
		return false, err
	}
	for _, f := range factors {
		if err := sealed.Delete(ctx, MfaSeedRef(f.ID)); err != nil {
			return false, fmt.Errorf("removing the second-factor seed: %w", err)
		}
		if err := tx.Where("factor_id = ?", f.ID).Delete(&MfaSourceFailure{}).Error; err != nil {
			return false, err
		}
	}
	if len(factors) > 0 {
		if err := tx.Where("identity_id = ?", identityID).Delete(&MfaFactor{}).Error; err != nil {
			return false, err
		}
	}
	if withCodes {
		if err := tx.Where("identity_id = ?", identityID).Delete(&MfaRecoveryCode{}).Error; err != nil {
			return false, err
		}
	}
	return len(factors) > 0, nil
}

// SetTenantMfaRequired turns the tenant's own "second factor required" flag on or off. It
// writes that one column, so it can neither clobber nor be clobbered by the tenant update path.
// The tenant's EFFECTIVE requirement is MfaEffectivelyRequired: the tier can force it on, and
// this setter cannot clear that floor.
func (s *Store) SetTenantMfaRequired(ctx context.Context, t *Tenant, required bool) error {
	if err := s.sys(ctx).Model(t).Update("mfa_required", required).Error; err != nil {
		return err
	}
	t.MfaRequired = required
	return nil
}

// MfaRequiredByTier reports whether the tenant's tier forces a second factor on. The tenant's
// own flag can add to it and never subtract from it.
func (t *Tenant) MfaRequiredByTier() bool { return t.Tier.MfaRequired() }

// MfaEffectivelyRequired is the requirement the sign-in policy enforces: the tenant's own flag
// OR the tier's floor.
func (t *Tenant) MfaEffectivelyRequired() bool { return t.MfaRequired || t.MfaRequiredByTier() }
