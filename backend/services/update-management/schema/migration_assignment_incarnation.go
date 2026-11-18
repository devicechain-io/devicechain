// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// AssignmentIncarnationID is the device identity migration's frozen ID.
const AssignmentIncarnationID = "20261115000100"

// NewAssignmentIncarnationSchema adds a nullable identity stamp so older pods can
// continue inserting assignments during a rolling upgrade. Replay adds nothing twice.
func NewAssignmentIncarnationSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: AssignmentIncarnationID,
		Migrate: func(tx *gorm.DB) error {
			type updateAssignment struct {
				ID                uint          `gorm:"primarykey"`
				DeviceIncarnation sql.NullInt64 `gorm:"type:bigint"`
			}
			if tx.Migrator().HasColumn(&updateAssignment{}, "DeviceIncarnation") {
				return nil
			}
			return tx.Migrator().AddColumn(&updateAssignment{}, "DeviceIncarnation")
		},
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
