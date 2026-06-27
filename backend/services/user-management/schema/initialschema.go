// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-user-management/model"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// Drop all tables from the list.
func dropTables(tx *gorm.DB, tables []string) error {
	for _, table := range tables {
		if err := tx.Migrator().DropTable(table); err != nil {
			return err
		}
	}
	return nil
}

// Creates the initial schema migration for this functional area. It materializes
// the instance signing-key table (ADR-008). The legacy tenant-bound user/role
// tables it once created are gone (ADR-033 cutover; see NewDropLegacyUserRole).
func NewInitialSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20220101000000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.SigningKey{})
		},
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, []string{"signing_keys"})
		},
	}
}

// NewSigningKeyRetention adds the SigningKey.RetiredAt column that lets signing
// keys be rotated and retained (their public halves stay in the JWKS until past
// the retention window). The column is nullable, so AutoMigrate adds it without
// touching existing rows (ADR-008 rotation follow-up).
func NewSigningKeyRetention() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260624000000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&model.SigningKey{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropColumn(&model.SigningKey{}, "retired_at")
		},
	}
}
