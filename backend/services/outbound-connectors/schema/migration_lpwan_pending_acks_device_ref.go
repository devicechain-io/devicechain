// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"fmt"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewLpwanPendingAcksDeviceRefSchema adds the nullable column a downlink to The Things Stack names
// its device by (the application and device id), beside the DevEUI a ChirpStack downlink uses. NULL
// is the true reading of every existing row, and a replica of the previous version neither reads nor
// writes the column, so a rolling upgrade shares the table safely.
//
// The table name is DERIVED from the migration's own snapshot (lpwanPendingAck) rather than written
// out, because only a derived name takes the area's table prefix. Re-runnable: the column is added
// only when it is missing.
func NewLpwanPendingAcksDeviceRefSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261110120000",
		Migrate: func(tx *gorm.DB) error {
			stmt := &gorm.Statement{DB: tx}
			if err := stmt.Parse(&lpwanPendingAck{}); err != nil {
				return fmt.Errorf("parse the pending-acks snapshot: %w", err)
			}
			if tx.Migrator().HasColumn(stmt.Table, "device_ref") {
				return nil
			}
			return tx.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN device_ref varchar(256)", stmt.Quote(stmt.Table))).Error
		},
		// Nothing is dropped: a rollback undoes a failed forward migration rather than erasing rows
		// that were written after it.
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
