// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"gorm.io/gorm"

	"github.com/go-gormigrate/gormigrate/v2"
)

// tierColorNullableSnapshot is this migration's own snapshot of iam_tenant_tiers: the
// primary key and the one column it changes, plus the two indexed columns SQLite's table
// rebuild would otherwise lose (see alterTierColor). The pinned TableName is load-bearing;
// see migration_tenant_locale.go for why.
type tierColorNullableSnapshot struct {
	ID        uint           `gorm:"primarykey"`
	DeletedAt gorm.DeletedAt `gorm:"index"`
	Token     string         `gorm:"uniqueIndex;not null;size:128"`
	Color     *string        `gorm:"size:32"`
}

func (tierColorNullableSnapshot) TableName() string { return "iam_tenant_tiers" }

// tierColorNotNullSnapshot is the shape before this migration, used only by Rollback.
type tierColorNotNullSnapshot struct {
	ID        uint           `gorm:"primarykey"`
	DeletedAt gorm.DeletedAt `gorm:"index"`
	Token     string         `gorm:"uniqueIndex;not null;size:128"`
	Color     string         `gorm:"not null;default:'';size:32"`
}

func (tierColorNotNullSnapshot) TableName() string { return "iam_tenant_tiers" }

// NewTierColorNullableMigration makes a tier's "no pill" a NULL rather than an empty
// string.
//
// The colour column was the one text column in this area that could not hold NULL (NOT
// NULL with an empty-string default), so "no colour" was the empty string, and a partial
// update had to fold an explicit null onto it through a private helper that core
// deliberately does not provide. With the column nullable the colour is ordinary nullable text: null, "" and
// whitespace all clear it to NULL, and the admin API reads NULL back as `color: null`.
//
// It drops the NOT NULL and the default, then converts every stored empty colour to NULL —
// in one transaction, so a failure leaves neither half applied. Individually re-runnable, as the
// chain requires: both DDL clauses and the UPDATE are no-ops the second time.
func NewTierColorNullableMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260926120000",
		Migrate: func(db *gorm.DB) error {
			return db.Transaction(func(tx *gorm.DB) error {
				if err := alterTierColor(tx, &tierColorNullableSnapshot{},
					"ALTER TABLE iam_tenant_tiers ALTER COLUMN color DROP NOT NULL, ALTER COLUMN color DROP DEFAULT"); err != nil {
					return err
				}
				return tx.Exec("UPDATE iam_tenant_tiers SET color = NULL WHERE color = ''").Error
			})
		},
		Rollback: func(db *gorm.DB) error {
			return db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Exec("UPDATE iam_tenant_tiers SET color = '' WHERE color IS NULL").Error; err != nil {
					return err
				}
				return alterTierColor(tx, &tierColorNotNullSnapshot{},
					"ALTER TABLE iam_tenant_tiers ALTER COLUMN color SET DEFAULT '', ALTER COLUMN color SET NOT NULL")
			})
		},
	}
}

// alterTierColor changes the colour column's nullability, one way per engine.
//
// 🔴 THE TWO ENGINES DO NOT TAKE ONE CODE PATH, AND THAT IS STATED RATHER THAN HIDDEN.
// Postgres runs the given raw DDL: each clause is idempotent, and nothing needs
// introspecting. SQLite — only ever the schema tests' engine — cannot alter a column in
// place, so gorm rebuilds the table from its CREATE statement; that rebuild does not carry
// the separately created indexes across, so the token's UNIQUE index and the deleted_at
// index are re-created here from the snapshot. Without that, every SQLite test after this
// migration would run against a tier table weaker than production's.
func alterTierColor(tx *gorm.DB, snapshot any, postgresDDL string) error {
	if tx.Dialector.Name() == "postgres" {
		return tx.Exec(postgresDDL).Error
	}
	if err := tx.Migrator().AlterColumn(snapshot, "Color"); err != nil {
		return err
	}
	for _, index := range []string{"idx_iam_tenant_tiers_token", "idx_iam_tenant_tiers_deleted_at"} {
		if tx.Migrator().HasIndex(snapshot, index) {
			continue
		}
		if err := tx.Migrator().CreateIndex(snapshot, index); err != nil {
			return err
		}
	}
	return nil
}
