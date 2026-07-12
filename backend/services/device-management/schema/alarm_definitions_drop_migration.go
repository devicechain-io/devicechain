// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDropAlarmDefinitionsSchema drops the alarm_definitions table. The AlarmDefinition rule
// model was retired: authoring moved to DetectionRule (ADR-057) and the 6d cutover deleted the
// measurement evaluator that read the frozen rules, leaving the table with no reader or writer.
// This removes the dead table so the schema matches the code (a fresh deploy runs the create
// migration then this drop; an existing deploy just drops it). The GA migration squash will
// later collapse the create/reshape/drop into a baseline that never had the table.
func NewDropAlarmDefinitionsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260712180000",
		Migrate: func(tx *gorm.DB) error {
			if tx.Migrator().HasTable("alarm_definitions") {
				return tx.Migrator().DropTable("alarm_definitions")
			}
			return nil
		},
		// No rollback: the table's schema lived in the now-retired create migration, and there is
		// no model to AutoMigrate it back. Rolling forward past this point is one-way (pre-GA).
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
