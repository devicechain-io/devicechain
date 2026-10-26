// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// mfaIdentityRef names iam_identities, a table this migration does not own: the primary key
// only, so the foreign keys below can reference it. AutoMigrate on an existing table with a
// struct this thin adds nothing and drops nothing.
type mfaIdentityRef struct {
	ID uint `gorm:"primarykey"`
}

func (mfaIdentityRef) TableName() string { return "iam_identities" }

// mfaFactorSnapshot is this migration's snapshot of iam_mfa_factors: one second factor of one
// identity. The factor is per IDENTITY (identities are global), so the table carries no
// tenant column and the tenant purge never touches it.
//
//   - secret_name is the NAME of the sealed seed in the instance secret store, never the seed.
//     The seed is written in the same transaction as the row that names it.
//   - state is 'pending' until the person proves possession with a code, then 'active'.
//   - last_step is the highest TOTP time step ever accepted. A code is accepted only for a step
//     strictly newer than it, which is what makes a code single use.
//   - failed_count and locked_until are the identity-wide lock. They are charged BEFORE a code
//     is evaluated, so a burst of concurrent guesses cannot outrun the count.
//   - (identity_id, kind) is unique: one factor of a kind per identity. There is no soft
//     delete, so the index is plain.
//
// The foreign key CASCADES, so deleting an identity removes its factors. It cannot reach the
// secrets table: the store removes the sealed seed itself, in the same transaction.
type mfaFactorSnapshot struct {
	ID          uint            `gorm:"primarykey"`
	IdentityID  uint            `gorm:"not null;uniqueIndex:idx_iam_mfa_factors_identity_kind,priority:1"`
	Identity    *mfaIdentityRef `gorm:"foreignKey:IdentityID;constraint:OnDelete:CASCADE"`
	Kind        string          `gorm:"not null;size:16;uniqueIndex:idx_iam_mfa_factors_identity_kind,priority:2;check:chk_iam_mfa_factors_kind,kind IN ('totp')"`
	State       string          `gorm:"not null;size:16;check:chk_iam_mfa_factors_state,state IN ('pending','active')"`
	SecretName  string          `gorm:"not null;size:128"`
	LastStep    int64           `gorm:"not null;default:0"`
	FailedCount int             `gorm:"not null;default:0"`
	LockedUntil *time.Time
	CreatedAt   time.Time
	ConfirmedAt *time.Time
	LastUsedAt  *time.Time
}

func (mfaFactorSnapshot) TableName() string { return "iam_mfa_factors" }

// mfaSourceFailureSnapshot is the per-source half of the lock: the failures charged to one
// factor from one source address (an IPv4 address or an IPv6 /64), so a single source locks
// itself out long before it can lock the person out. Primary key (factor_id, source_key). The
// foreign key CASCADES.
type mfaSourceFailureSnapshot struct {
	FactorID    uint               `gorm:"primaryKey;autoIncrement:false"`
	Factor      *mfaFactorSnapshot `gorm:"foreignKey:FactorID;constraint:OnDelete:CASCADE"`
	SourceKey   string             `gorm:"primaryKey;size:64"`
	FailedCount int                `gorm:"not null;default:0"`
	LockedUntil *time.Time
	UpdatedAt   time.Time `gorm:"not null;index:idx_iam_mfa_source_failures_updated_at"`
}

func (mfaSourceFailureSnapshot) TableName() string { return "iam_mfa_source_failures" }

// mfaRecoveryCodeSnapshot is one single-use recovery code. digest is hex(SHA-256(domain ||
// identity id || code)): the codes carry 80 bits, so the digest needs no salt and no key, and
// the lookup is an equality on the unique (identity_id, digest) index. used_at is set by the one
// statement that redeems the code. The identity foreign key CASCADES.
type mfaRecoveryCodeSnapshot struct {
	ID         uint            `gorm:"primarykey"`
	IdentityID uint            `gorm:"not null;uniqueIndex:idx_iam_mfa_recovery_codes_identity_digest,priority:1"`
	Identity   *mfaIdentityRef `gorm:"foreignKey:IdentityID;constraint:OnDelete:CASCADE"`
	Digest     string          `gorm:"not null;size:64;uniqueIndex:idx_iam_mfa_recovery_codes_identity_digest,priority:2"`
	UsedAt     *time.Time
}

func (mfaRecoveryCodeSnapshot) TableName() string { return "iam_mfa_recovery_codes" }

// mfaTenantSnapshot is this migration's snapshot of iam_tenants: the primary key and the one
// column it adds. NOT NULL DEFAULT false, so a row inserted by a pod from before this migration
// reads as "MFA not required", which is what every existing tenant is.
//
// 🔴 THE TableName IS LOAD-BEARING. Without it gorm derives the table from the Go type name and
// creates a stray `mfa_tenant_snapshots` table, succeeding and migrating nothing.
// TestTheMfaMigrationAddsTheTenantColumnAndNoStrayTable is the guard.
type mfaTenantSnapshot struct {
	ID          uint `gorm:"primarykey"`
	MfaRequired bool `gorm:"not null;default:false"`
}

func (mfaTenantSnapshot) TableName() string { return "iam_tenants" }

// NewMfaMigration adds native multi-factor authentication storage: the factor, its per-source
// failure rows, the recovery codes, and the per-tenant "MFA required" flag. It seeds nothing.
//
// Individually re-runnable, as every appended migration must be (UseTransaction:false, replay
// from the top after a failure): AutoMigrate creates each table and column when absent and
// converges nothing when present.
func NewMfaMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261026120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(
				&mfaFactorSnapshot{}, &mfaSourceFailureSnapshot{}, &mfaRecoveryCodeSnapshot{},
				&mfaTenantSnapshot{},
			)
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Migrator().DropColumn(&mfaTenantSnapshot{}, "mfa_required"); err != nil {
				return err
			}
			return tx.Migrator().DropTable("iam_mfa_source_failures", "iam_mfa_recovery_codes", "iam_mfa_factors")
		},
	}
}
