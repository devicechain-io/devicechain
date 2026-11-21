// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

const RolloutSafetyID = "20261121000100"

// NewRolloutSafetySchema appends opt-in safety fields. Literal local snapshots
// deliberately exclude live models; each column addition survives partial replay.
func NewRolloutSafetySchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: RolloutSafetyID,
		Migrate: func(tx *gorm.DB) error {
			type updateRollout struct {
				ID                  uint           `gorm:"primarykey"`
				CohortPlan          sql.NullString `gorm:"type:text"`
				CurrentCohort       int            `gorm:"not null;default:0"`
				FailureRatePlan     sql.NullString `gorm:"type:text"`
				HaltSequence        int            `gorm:"not null;default:0"`
				HaltFailures        int            `gorm:"not null;default:0"`
				HaltSamples         int            `gorm:"not null;default:0"`
				RateOverrideAt      sql.NullTime
				HaltFailureEvidence sql.NullString `gorm:"type:text"`
			}
			for _, name := range []string{"CohortPlan", "CurrentCohort", "FailureRatePlan", "HaltSequence", "HaltFailures", "HaltSamples", "RateOverrideAt", "HaltFailureEvidence"} {
				if !tx.Migrator().HasColumn(&updateRollout{}, name) {
					if err := tx.Migrator().AddColumn(&updateRollout{}, name); err != nil {
						return err
					}
				}
			}
			type updateRolloutTarget struct {
				ID     uint `gorm:"primarykey"`
				Cohort int  `gorm:"not null;default:0"`
			}
			if !tx.Migrator().HasColumn(&updateRolloutTarget{}, "Cohort") {
				return tx.Migrator().AddColumn(&updateRolloutTarget{}, "Cohort")
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
