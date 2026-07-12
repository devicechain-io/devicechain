// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// alarmContributorsCol is the frozen partial Alarm snapshot for the additive contributor columns —
// gorm.Model gives AutoMigrate the table (alarms) and primary key so it adds just the new columns.
type alarmContributorsCol struct {
	gorm.Model
	Contributors       datatypes.JSON `gorm:"type:jsonb"`
	ContributorVersion uint           `gorm:"not null;default:0"`
}

func (alarmContributorsCol) TableName() string { return "alarms" }

// NewAlarmContributorsSchema adds the alarms.contributors JSONB column + contributor_version optimistic
// guard (ADR-057): the DETECT+REACT alarm-object integrator reference-counts the rules currently
// raising an alarm in the contributor set, deriving the alarm's state (cleared when the set empties)
// and severity (max over the active set), and CAS-writes on contributor_version so a concurrent fold
// cannot clobber the accumulator. Both are additive — a legacy measurement-evaluator-raised alarm has
// a NULL set and version 0, and the integrator populates them on the first edge it applies. The columns
// are defined inline (frozen against future model changes) and match model.Alarm.
func NewAlarmContributorsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260712120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&alarmContributorsCol{})
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Migrator().DropColumn(&alarmContributorsCol{}, "Contributors"); err != nil {
				return err
			}
			return tx.Migrator().DropColumn(&alarmContributorsCol{}, "ContributorVersion")
		},
	}
}
