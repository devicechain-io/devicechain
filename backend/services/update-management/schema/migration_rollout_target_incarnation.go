// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// RolloutTargetIncarnationID is the frozen-target identity migration's frozen ID.
const RolloutTargetIncarnationID = "20261118000100"

// NewRolloutTargetIncarnationSchema adds a nullable identity stamp so old pods can
// still insert targets during a rolling upgrade. Its own snapshot survives replay.
func NewRolloutTargetIncarnationSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: RolloutTargetIncarnationID,
		Migrate: func(tx *gorm.DB) error {
			type updateRolloutTarget struct {
				ID                uint          `gorm:"primarykey"`
				DeviceIncarnation sql.NullInt64 `gorm:"type:bigint"`
			}
			if tx.Migrator().HasColumn(&updateRolloutTarget{}, "DeviceIncarnation") {
				return nil
			}
			return tx.Migrator().AddColumn(&updateRolloutTarget{}, "DeviceIncarnation")
		},
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
