// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// This migration owns its literal snapshot. Nullable/defaulted columns let old
// pods keep writing; each statement can replay after a partial nontransactional run.
var holderCheckBackoffDDL = []string{
	`ALTER TABLE "event-management".entity_deletion_watermarks ADD COLUMN IF NOT EXISTS holder_checked_deletion timestamptz NULL`,
	`ALTER TABLE "event-management".entity_deletion_watermarks ADD COLUMN IF NOT EXISTS holder_check_after timestamptz NULL`,
	`ALTER TABLE "event-management".entity_deletion_watermarks ADD COLUMN IF NOT EXISTS holder_check_delay_seconds integer NOT NULL DEFAULT 0`,
}

func NewHolderCheckBackoffSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261120010000",
		Migrate: func(tx *gorm.DB) error {
			for i, stmt := range holderCheckBackoffDDL {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("holder check backoff DDL statement %d: %w", i+1, err)
				}
			}
			return nil
		},
	}
}
