// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// Literal snapshot owned by this migration. Tenant columns match the baseline and
// the usage migrations: varchar(128), with the database's default collation.
// Each statement can replay after a partial run with UseTransaction:false.
var entityDeletionWatermarksDDL = []string{
	`CREATE TABLE IF NOT EXISTS "event-management".entity_deletion_watermarks (
		tenant_id varchar(128) NOT NULL,
		entity_type varchar(64) NOT NULL,
		entity_token varchar(128) NOT NULL,
		deleted_time timestamptz NOT NULL,
		resweep_after timestamptz NULL,
		armed_at timestamptz NULL,
		PRIMARY KEY (tenant_id, entity_type, entity_token)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_entity_deletion_watermarks_resweep ON "event-management".entity_deletion_watermarks
		(resweep_after) WHERE resweep_after IS NOT NULL`,
}

func NewEntityDeletionWatermarksSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261119000000",
		Migrate: func(tx *gorm.DB) error {
			for i, stmt := range entityDeletionWatermarksDDL {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("entity deletion watermark DDL statement %d: %w", i+1, err)
				}
			}
			return nil
		},
	}
}
