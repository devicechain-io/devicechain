// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// alarmSuppressionWindow is this migration's OWN SNAPSHOT, never the live model.
// Mixins are inlined, in storage order, so later model edits cannot rewrite history.
// No TableName: the naming strategy supplies this service's schema and index prefix.
// Targets, slots and keys are opaque jsonb documents. The live-row tenant/token index
// permits token reuse after soft deletion; tenant/ended_at serves evaluator loads.
// creator_scope preserves the full immutable creator scope independently from the
// visibility stamp; CreatorScoped marks that continuing restriction (ADR-085).
type alarmSuppressionWindow struct {
	ID             uint `gorm:"primarykey"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      gorm.DeletedAt `gorm:"index"`
	TenantId       string         `gorm:"index;not null;size:128;index:idx_suppression_tenant_ended,priority:1"`
	Token          string         `gorm:"index;not null;size:128"`
	Name           sql.NullString `gorm:"size:128"`
	Description    sql.NullString `gorm:"size:1024"`
	Metadata       *datatypes.JSON
	Reason         string         `gorm:"type:text;not null"`
	Targets        datatypes.JSON `gorm:"not null"`
	AlarmKeys      datatypes.JSON
	BypassSeverity sql.NullString `gorm:"size:16"`
	Kind           string         `gorm:"size:16;not null"`
	StartTime      *time.Time
	EndTime        *time.Time
	Timezone       sql.NullString `gorm:"size:64"`
	Slots          datatypes.JSON
	ValidFrom      *time.Time
	ValidUntil     *time.Time
	EndedAt        *time.Time `gorm:"index:idx_suppression_tenant_ended,priority:2"`
	EndedBy        sql.NullString
	CreatedBy      sql.NullString
	ScopeAnchors   string `gorm:"type:text[]"`
	CreatorScope   string `gorm:"type:text[]"`
	CreatorScoped  bool   `gorm:"not null;default:false"`
	Version        int64  `gorm:"not null;default:1"`
}

// alarmCycleSuppression is immutable per-cycle history, keyed exactly as dispositions.
// alarm_id has its OWN index for ON DELETE CASCADE; the unique cycle index leads with
// tenant_id. Tokens/names are copies with no window FK, so deleting a window preserves
// history. A manual alarm FK avoids AutoMigrating any snapshot of the alarms table.
// scope_anchors is the alarm cycle's stamp, never the device's later ownership.
type alarmCycleSuppression struct {
	ID           uint `gorm:"primarykey"`
	CreatedAt    time.Time
	TenantId     string         `gorm:"not null;size:128;uniqueIndex:uix_alarm_cycle_suppressions_cycle,priority:1"`
	AlarmId      uint           `gorm:"not null;uniqueIndex:uix_alarm_cycle_suppressions_cycle,priority:2;index:idx_alarm_cycle_suppressions_alarm"`
	RaisedTime   time.Time      `gorm:"not null;uniqueIndex:uix_alarm_cycle_suppressions_cycle,priority:3"`
	AlarmToken   string         `gorm:"not null;size:255"`
	WindowToken  string         `gorm:"not null;size:128"`
	WindowName   sql.NullString `gorm:"size:128"`
	ScopeAnchors string         `gorm:"type:text[]"`
}

// NewAlarmSuppressionSchema appends inert suppression storage; the baseline is frozen.
//
// # Re-runnability
//
// UseTransaction:false can leave partial DDL behind. AutoMigrate and the partial unique
// index are idempotent; the alarm FK is catalog-guarded and revalidated on replay.
// SQLite tests exercise the same table/index DDL; only the Postgres FK is dialect-specific.
//
// # No seed, no backfill
//
// No production writer exists in this slice. Neither table receives seed rows.
//
// # Tenant purge
//
// Both tables carry tenant_id, so the catalog-driven purge covers them automatically.
func NewAlarmSuppressionSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261218120000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&alarmSuppressionWindow{}, &alarmCycleSuppression{}); err != nil {
				return err
			}
			if err := createTenantTokenIndex(tx, &alarmSuppressionWindow{}); err != nil {
				return err
			}
			if tx.Dialector.Name() != "postgres" {
				return nil
			}
			return addForeignKeyIfAbsent(tx, "alarm_cycle_suppressions", "fk_alarm_cycle_suppressions_alarm",
				`ALTER TABLE "device-management"."alarm_cycle_suppressions" `+
					`ADD CONSTRAINT fk_alarm_cycle_suppressions_alarm FOREIGN KEY (alarm_id) `+
					`REFERENCES "device-management"."alarms"(id) ON DELETE CASCADE NOT VALID;`)
		},
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, []string{"alarm_cycle_suppressions", "alarm_suppression_windows"})
		},
	}
}
