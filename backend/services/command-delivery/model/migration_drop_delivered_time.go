// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDropDeliveredTime removes the commands.delivered_time column along with the
// DELIVERED status it existed to timestamp.
//
// Nothing ever wrote either one. Confirming delivery distinctly from a response
// requires a device- or broker-level acknowledgment and no such transport exists,
// so the column was NULL on every row in every install since the table was
// created. Removing it keeps the live model and the schema in agreement, which is
// what migration-diff verifies.
//
// This is expressed as raw DDL rather than Migrator().DropColumn(&Command{}, …)
// on purpose: DropColumn resolves the table through the LIVE model, and the live
// model no longer declares this column at all. A migration must not depend on a
// struct that has moved on without it — that is the drift this repo's first
// migration rule exists to prevent.
//
// The table is schema-qualified even though the connection's search_path already
// resolves it, matching every other raw-DDL migration here. That is not style:
// combined with IF EXISTS, an unqualified name run against an unexpected
// search_path would drop nothing and still report success — a migration that
// cannot fail, which is worse than one that does.
func NewDropDeliveredTime() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260719000000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE "command-delivery"."commands" DROP COLUMN IF EXISTS delivered_time;`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE "command-delivery"."commands" ADD COLUMN IF NOT EXISTS delivered_time timestamptz;`).Error
		},
	}
}
