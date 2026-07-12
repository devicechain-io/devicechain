// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// alarmContributorsCol is the frozen partial Alarm snapshot for the additive contributors column —
// gorm.Model gives AutoMigrate the table (alarms) and primary key so it adds just the column.
type alarmContributorsCol struct {
	gorm.Model
	Contributors datatypes.JSON `gorm:"type:jsonb"`
}

func (alarmContributorsCol) TableName() string { return "alarms" }

// NewAlarmContributorsSchema adds the alarms.contributors JSONB column (ADR-057): the DETECT+REACT
// alarm-object integrator reference-counts the rules currently raising an alarm in this contributor
// set, deriving the alarm's state (cleared when the set empties) and severity (max over the active
// set). It is nullable — a legacy measurement-evaluator-raised alarm has none, and the integrator
// populates it on the first edge it applies. The column is defined inline (frozen against future model
// changes) and matches model.Alarm.Contributors.
func NewAlarmContributorsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260712120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&alarmContributorsCol{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropColumn(&alarmContributorsCol{}, "Contributors")
		},
	}
}
