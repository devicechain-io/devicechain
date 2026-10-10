// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewProfileConfigurationDeclarationSchema adds a device profile's DRAFT configuration
// declaration: the shared attribute keys a device of that profile may see. Publish
// freezes it into the version snapshot (which is JSON inside an existing column), so
// this single draft column is the only schema change.
//
// This is an APPENDED migration, not a baseline edit, and it declares no snapshot
// struct: it is one ALTER, so there is no table shape to copy. Like the location
// declaration it follows, the column is a nullable JSONB document with NO default —
// `ADD COLUMN ... DEFAULT <const>` lives in PostgreSQL's attmissingval catalog state
// and is silently lost by a logical dump/restore. NULL is the real answer for a profile
// that declares nothing, so there is no backfill either.
//
// Re-runnability: migrations run with UseTransaction:false and replay from the top after
// a failure, so this is one statement using ADD COLUMN IF NOT EXISTS.
func NewProfileConfigurationDeclarationSchema() *gormigrate.Migration {
	const table = `"device-management"."device_profiles"`

	return &gormigrate.Migration{
		ID: "20261010120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + table +
				` ADD COLUMN IF NOT EXISTS configuration_declaration jsonb;`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + table +
				` DROP COLUMN IF EXISTS configuration_declaration;`).Error
		},
	}
}
