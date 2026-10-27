// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"errors"
	"time"

	"github.com/devicechain-io/dc-microservice/secrets"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The store methods the sign-in flow and the operator reset need beyond mfa.go's primitives.

// ErrMfaResetNeedsPassword refuses an operator reset while the factor has failures
// outstanding unless the same action sets a new password. A failed code is evidence that
// someone is using the password; removing the factor on its own would hand the account to
// them.
var ErrMfaResetNeedsPassword = errors.New("the second factor has failed attempts outstanding, so it can be reset only together with a new password")

// mfaFailuresOutstanding reports whether the identity's active factor has any failure on
// record: an identity-wide count, a lock, or a per-source row.
func mfaFailuresOutstanding(tx *gorm.DB, factors []MfaFactor) (bool, error) {
	for _, f := range factors {
		if f.State != MfaStateActive {
			continue
		}
		if f.FailedCount > 0 || f.LockedUntil != nil {
			return true, nil
		}
		var n int64
		if err := tx.Model(&MfaSourceFailure{}).Where("factor_id = ? AND failed_count > 0", f.ID).Count(&n).Error; err != nil {
			return false, err
		}
		if n > 0 {
			return true, nil
		}
	}
	return false, nil
}

// ResetMfa is the operator's removal of an identity's second factor, in ONE transaction: the
// factors, recovery codes and sealed seeds go, the session epoch rotates (which kills every
// outstanding sign-in challenge and session), and, when newPasswordHash is not empty, the
// password changes with them.
//
// While the active factor has failures outstanding (MfaFailuresOutstanding) a reset without a
// new password is refused with ErrMfaResetNeedsPassword and NOTHING changes: the failures are
// evidence the password is in use by someone else. The factor row is locked first, as every
// attempt locks it, so a failure cannot slip in between the check and the removal. An identity
// with no factor is ErrMfaFactorNotFound.
func (s *Store) ResetMfa(ctx context.Context, store secrets.SecretStore, id *Identity, newPasswordHash string) error {
	epoch := NewSessionEpoch()
	err := s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		var factors []MfaFactor
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("identity_id = ?", id.ID).Find(&factors).Error; err != nil {
			return err
		}
		if len(factors) == 0 {
			return ErrMfaFactorNotFound
		}
		outstanding, err := mfaFailuresOutstanding(tx, factors)
		if err != nil {
			return err
		}
		if outstanding && newPasswordHash == "" {
			return ErrMfaResetNeedsPassword
		}
		if store == nil {
			return ErrMfaNeedsSecretStore
		}
		sealed, err := secrets.BindTx(store, tx)
		if err != nil {
			return err
		}
		if _, err := removeMfaFactors(ctx, tx, sealed, id.ID, true); err != nil {
			return err
		}
		fields := map[string]any{"session_epoch": epoch}
		if newPasswordHash != "" {
			fields["password_hash"] = newPasswordHash
		}
		return tx.Model(id).Updates(fields).Error
	})
	if err != nil {
		return err
	}
	id.SessionEpoch = epoch
	if newPasswordHash != "" {
		id.PasswordHash = newPasswordHash
	}
	return nil
}

// RecordAuth writes an authentication audit row (best-effort callers ignore the error). The
// label names what the event concerns; it is never a credential.
func (s *Store) RecordAuth(ctx context.Context, operation, actor, tenant, label string) error {
	return s.db.RecordAuthEventLabeled(ctx, operation, actor, tenant, label)
}

// DisableMfa is the person's own removal of their second factor, in ONE transaction: the factors,
// recovery codes and sealed seeds go and the session epoch rotates, which ends every session the
// identity has. An identity with nothing to remove is ErrMfaFactorNotFound.
func (s *Store) DisableMfa(ctx context.Context, store secrets.SecretStore, id *Identity) error {
	epoch := NewSessionEpoch()
	err := s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		removed, err := removeMfaIn(ctx, tx, store, id.ID)
		if err != nil {
			return err
		}
		if !removed {
			return ErrMfaFactorNotFound
		}
		return tx.Model(id).Updates(map[string]any{"session_epoch": epoch}).Error
	})
	if err != nil {
		return err
	}
	id.SessionEpoch = epoch
	return nil
}

// PruneStaleMfaSourceFailures deletes per-source failure rows idle since before cutoff and
// reports how many went. A row is otherwise pruned only when a sign-in succeeds, so a factor
// that is attacked and then left alone would keep its rows forever.
func (s *Store) PruneStaleMfaSourceFailures(ctx context.Context, cutoff time.Time) (int64, error) {
	res := s.sys(ctx).Where("updated_at < ?", cutoff).Delete(&MfaSourceFailure{})
	return res.RowsAffected, res.Error
}

// MfaRequiredAmong returns which of the given tenant tokens effectively require a second factor
// (the tenant's own flag or its tier's floor).
func (s *Store) MfaRequiredAmong(ctx context.Context, tokens []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(tokens) == 0 {
		return out, nil
	}
	var tenants []Tenant
	if err := s.sys(ctx).Preload("Tier").Where("token IN ?", tokens).Find(&tenants).Error; err != nil {
		return nil, err
	}
	for i := range tenants {
		if tenants[i].MfaEffectivelyRequired() {
			out[tenants[i].Token] = true
		}
	}
	return out, nil
}

// ActiveMfaFactorFor returns the identity's ACTIVE factor, or ErrMfaFactorNotFound. A pending
// factor (an enrolment not yet confirmed) is not one: it does not gate a sign-in.
func (s *Store) ActiveMfaFactorFor(ctx context.Context, identityID uint) (*MfaFactor, error) {
	// Find, not First: no factor is the common case on every password sign-in, and First would
	// log it as an error each time.
	var found []MfaFactor
	err := s.sys(ctx).Where("identity_id = ? AND kind = ? AND state = ?", identityID, MfaKindTotp, MfaStateActive).
		Limit(1).Find(&found).Error
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, ErrMfaFactorNotFound
	}
	return &found[0], nil
}

// MfaEnabledAmong returns which of the given identity ids have an active factor.
func (s *Store) MfaEnabledAmong(ctx context.Context, identityIDs []uint) (map[uint]bool, error) {
	out := map[uint]bool{}
	if len(identityIDs) == 0 {
		return out, nil
	}
	var ids []uint
	if err := s.sys(ctx).Model(&MfaFactor{}).
		Where("identity_id IN ? AND state = ?", identityIDs, MfaStateActive).Pluck("identity_id", &ids).Error; err != nil {
		return nil, err
	}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
