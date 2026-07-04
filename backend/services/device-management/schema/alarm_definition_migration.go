// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewAlarmDefinitionSchema adds the alarm_definitions table (ADR-041): alarm rules
// declared on a device profile, structurally parallel to metric_definitions and
// command_definitions. The condition is flat-relational (threshold + optional
// duration/repeat), not a nested document. The snapshot is defined inline so the
// migration is frozen against future model changes; it must produce the same
// columns as model.AlarmDefinition.
func NewAlarmDefinitionSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260704140000",
		Migrate: func(tx *gorm.DB) error {
			// The (device_type_id, alarm_key) lookup index is unique so a profile
			// cannot declare the same alarm twice; tenant isolation is enforced by
			// the app-layer tenant scope on the embedded TenantScoped.
			type AlarmDefinition struct {
				gorm.Model
				rdb.TenantScoped
				rdb.TokenReference
				rdb.NamedEntity
				rdb.MetadataEntity

				DeviceTypeId  uint   `gorm:"not null;index:idx_alarm_definition_key,unique,priority:1"`
				AlarmKey      string `gorm:"not null;size:128;index:idx_alarm_definition_key,unique,priority:2"`
				MetricKey     string `gorm:"not null;size:128"`
				ConditionType string `gorm:"not null;size:16"`
				Operator      string `gorm:"not null;size:8"`
				Severity      string `gorm:"not null;size:16"`

				Threshold     sql.NullFloat64
				ThresholdAttr sql.NullString

				DurationSeconds     sql.NullInt64
				RepeatCount         sql.NullInt64
				RepeatWindowSeconds sql.NullInt64

				Enabled bool `gorm:"not null;default:true"`
			}
			if err := tx.AutoMigrate(&AlarmDefinition{}); err != nil {
				return err
			}
			// ADR-042 P1: per-tenant partial unique index on token.
			return rdb.CreateTenantTokenIndex(tx, &AlarmDefinition{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("alarm_definitions")
		},
	}
}
