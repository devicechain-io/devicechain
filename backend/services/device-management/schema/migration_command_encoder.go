// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// commandEncoderSnapshot documents only this migration's nullable addition.
// Never substitute a live model here.
type commandEncoderSnapshot struct{ Encoder *datatypes.JSON }

// NewCommandEncoderSchema is individually re-runnable after a partial upgrade.
func NewCommandEncoderSchema() *gormigrate.Migration {
	const table = `"device-management"."command_definitions"`
	return &gormigrate.Migration{
		ID: "20261109000100",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + table + ` ADD COLUMN IF NOT EXISTS encoder jsonb;`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + table + ` DROP COLUMN IF EXISTS encoder;`).Error
		},
	}
}
