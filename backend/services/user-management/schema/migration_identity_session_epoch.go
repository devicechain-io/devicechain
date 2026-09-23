// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"crypto/rand"
	"encoding/base64"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// identitySessionEpochSnapshot is this migration's own snapshot of iam_identities:
// the primary key and the one column it adds. Nothing else.
//
// A SNAPSHOT, not the live model, per the house rule in migrations.go: pointing it
// at iam.Identity would make this migration mean whatever that struct means on the
// day it runs.
//
// 🔴 THE TableName IS LOAD-BEARING. Without it gorm derives the table from the Go
// type name and creates an `identity_session_epoch_snapshots` table — succeeding,
// migrating nothing, and leaving iam_identities without the column every sign-in now
// reads. TestTheSessionEpochMigrationDoesNotCreateAStrayTable is the guard.
type identitySessionEpochSnapshot struct {
	ID uint `gorm:"primarykey"`

	// NOT NULL with an empty-string DEFAULT rather than no default: an INSERT that
	// omits the column — a pod from before this migration during a rolling upgrade, or
	// a raw INSERT — must still succeed. Such a row holds the empty string and cannot
	// be signed into until its password is reset, because an empty epoch is refused at
	// every mint and every redemption; that is the loud outcome, where NOT NULL without
	// a default would turn the old pod's user creation into a failed write.
	SessionEpoch string `gorm:"not null;size:64;default:''"`
}

func (identitySessionEpochSnapshot) TableName() string { return "iam_identities" }

// newSnapshotSessionEpoch is this migration's own epoch generator: 128 bits from
// crypto/rand, base64url. Deliberately a local copy rather than iam.NewSessionEpoch —
// a migration must not change meaning when a live package does.
func newSnapshotSessionEpoch() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// NewIdentitySessionEpochMigration adds the per-identity session epoch: the random
// value every refresh and identity token carries, and that a password reset, a
// disable or a delete changes — which is what ends the sessions minted before it.
//
// It adds the column, then gives every existing row its OWN random value. The
// backfill is done row by row in Go rather than with a dialect-specific volatile
// DEFAULT expression, so it runs the same on Postgres and on the SQLite chain tests.
//
// Re-runnable, as every appended migration must be (UseTransaction:false, replay from
// the top after a failure): AutoMigrate is a no-op once the column exists, and the
// backfill selects only rows still holding the empty string — so a second run neither
// rewrites nor rotates a value an earlier run (or a password reset) already set.
//
// Tokens issued before this migration carry no epoch, so they are refused from the
// first redemption after it: every session is signed out once at the upgrade.
func NewIdentitySessionEpochMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260923120000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&identitySessionEpochSnapshot{}); err != nil {
				return err
			}
			var ids []uint
			if err := tx.Model(&identitySessionEpochSnapshot{}).
				Where("session_epoch = ?", "").
				Pluck("id", &ids).Error; err != nil {
				return err
			}
			for _, id := range ids {
				// The WHERE repeats the empty-string condition so a row that gained a
				// value between the SELECT and this UPDATE is left as it is.
				if err := tx.Model(&identitySessionEpochSnapshot{ID: id}).
					Where("session_epoch = ?", "").
					Update("session_epoch", newSnapshotSessionEpoch()).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropColumn(&identitySessionEpochSnapshot{}, "session_epoch")
		},
	}
}
