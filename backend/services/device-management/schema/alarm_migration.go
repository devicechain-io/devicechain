// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewAlarmSchema adds the alarms table (ADR-041): first-class raised alarms, the
// operational counterpart to alarm_definitions (the rules). The evaluator raises one
// per (originator, alarm_key) and transitions it in place, so there is at most one
// LIVE alarm for that key — enforced by a per-tenant partial unique index. The
// snapshot is defined inline so the migration is frozen against future model changes;
// it must produce the same columns as model.Alarm.
func NewAlarmSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260704150000",
		Migrate: func(tx *gorm.DB) error {
			type Alarm struct {
				gorm.Model
				rdb.TenantScoped
				rdb.TokenReference
				rdb.MetadataEntity

				OriginatorType string `gorm:"not null;size:32"`
				OriginatorId   uint   `gorm:"not null"`

				AlarmKey  string `gorm:"not null;size:128"`
				MetricKey string `gorm:"not null;size:128"`

				State        string `gorm:"not null;size:16"`
				Acknowledged bool   `gorm:"not null;default:false"`
				Severity     string `gorm:"not null;size:16"`

				RaisedTime       time.Time `gorm:"not null"`
				ClearedTime      sql.NullTime
				AcknowledgedTime sql.NullTime
				AcknowledgedBy   sql.NullString `gorm:"size:256"`
				LastValue        sql.NullFloat64
				Message          sql.NullString `gorm:"size:1024"`
			}
			if err := tx.AutoMigrate(&Alarm{}); err != nil {
				return err
			}
			// At most one LIVE alarm per (originator, alarm_key) within a tenant: the
			// evaluator upserts into this row (raise/escalate/re-raise/clear all mutate
			// it), and a soft-deleted alarm frees its slot (WHERE deleted_at IS NULL).
			// tenant_id leads because originator_id is only unique within a tenant.
			if err := rdb.CreatePartialUniqueIndex(tx, &Alarm{}, "uix_alarm_originator_key",
				"tenant_id", "originator_type", "originator_id", "alarm_key"); err != nil {
				return err
			}
			// ADR-042 P1: per-tenant partial unique index on token.
			return rdb.CreateTenantTokenIndex(tx, &Alarm{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("alarms")
		},
	}
}
