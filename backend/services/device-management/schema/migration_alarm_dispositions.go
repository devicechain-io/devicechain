// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// alarmDisposition is the SNAPSHOT of an alarm disposition label as of this migration. It is not
// model.AlarmDisposition and must never be replaced by it, for the reason every shape in this
// package gives (see migration_device_replacements.go).
//
// # What the table is
//
// One row per labelled alarm CYCLE: an operator's verdict (TRUE_POSITIVE, FALSE_POSITIVE or
// EXPECTED) on one raise of an alarm, named by the raised_time the alarm had when it was
// labelled. An alarm row is reused across cycles, so the label cannot live on it; keyed by cycle,
// every earlier cycle keeps its own label for the backtests that score rules against them.
//
// # Keys and indexes
//
// (tenant_id, alarm_id, raised_time) is unique: one label per cycle, replaced in place by a
// relabel. It leads with tenant_id, so it is also the tenant index and the column carries no
// index of its own. (tenant_id, raised_time) serves the newest-cycle-first listing, and
// (tenant_id, alarm_token) the listing's alarm facet. alarm_token is copied from the alarm when the
// label is written, so the facet is judged by the label's own scope stamp rather than through the
// alarm, whose stamp is its current cycle's.
//
// The alarm foreign key cascades, so a label goes when its alarm goes (the entity delete removes
// an entity's alarms). It is added by hand below, not declared as a relation field: a relation
// field would make gorm AutoMigrate a snapshot of alarms too, and that is a claim about another
// table this migration has no business making.
//
// No TableName(), for the reason geoFence gives (migration_geofences.go).
type alarmDisposition struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	TenantId string `gorm:"not null;size:128;uniqueIndex:uix_alarm_dispositions_cycle,priority:1;index:idx_alarm_dispositions_tenant_raised,priority:1;index:idx_alarm_dispositions_tenant_alarm_token,priority:1"`

	AlarmId    uint      `gorm:"not null;uniqueIndex:uix_alarm_dispositions_cycle,priority:2"`
	RaisedTime time.Time `gorm:"not null;uniqueIndex:uix_alarm_dispositions_cycle,priority:3;index:idx_alarm_dispositions_tenant_raised,priority:2"`
	AlarmToken string    `gorm:"not null;size:255;index:idx_alarm_dispositions_tenant_alarm_token,priority:2"`

	Disposition  string `gorm:"not null;size:32"`
	Note         sql.NullString
	RecordedBy   sql.NullString
	RecordedTime time.Time `gorm:"not null"`

	ScopeAnchors string `gorm:"type:text[]"` // a text[] in the database; the snapshot only names its type
}

// NewAlarmDispositionsSchema creates the alarm disposition table.
//
// This is an APPENDED migration; the baseline is frozen.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure, so this must
// be individually re-runnable. AutoMigrate creates the table only when it is absent and adds only
// missing columns and indexes, and the foreign key is added only when the catalog does not hold it.
//
// # No seed, no backfill
//
// No cycle has been labelled. Nothing here writes a row, so the DDL-only bar holds.
//
// # Tenant purge
//
// The table carries tenant_id, so the catalog-driven tenant purge classifies and erases it with no
// exemption to register (checked by the coverage gate that runs alongside migration-diff verify).
func NewAlarmDispositionsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261114090000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&alarmDisposition{}); err != nil {
				return err
			}
			return addForeignKeyIfAbsent(tx, "alarm_dispositions", "fk_alarm_dispositions_alarm",
				`ALTER TABLE "device-management"."alarm_dispositions" `+
					`ADD CONSTRAINT fk_alarm_dispositions_alarm FOREIGN KEY (alarm_id) `+
					`REFERENCES "device-management"."alarms"(id) ON DELETE CASCADE NOT VALID;`)
		},
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, []string{"alarm_dispositions"})
		},
	}
}
