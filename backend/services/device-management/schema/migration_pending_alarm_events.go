// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// pendingAlarmEvent is this migration's own snapshot, never a live model.
//
// # What the table is
//
// Each row is an alarm state-change event whose transition has committed and whose
// publication the broker has not yet acknowledged. It is written in the transition's own
// transaction, so a crash or a broker outage between the commit and the publish delays the
// event instead of losing it.
//
// # Tenant purge
//
// The table carries tenant_id, so the catalog-driven tenant purge erases it.
type pendingAlarmEvent struct {
	ID            uint64         `gorm:"primaryKey;autoIncrement;index:idx_pending_alarm_events_tenant_id,priority:2"`
	TenantId      string         `gorm:"not null;size:128;index:idx_pending_alarm_events_tenant_id,priority:1"`
	Payload       datatypes.JSON `gorm:"not null"`
	CreatedAt     time.Time      `gorm:"not null"`
	QuarantinedAt *time.Time
	LastError     *string `gorm:"type:text"`
}

// TableName is NOT declared: it would bypass the area's TablePrefix.

// NewPendingAlarmEventsSchema creates the alarm-event outbox.
//
// # No backfill
//
// Transitions made before this release were published inline; there is nothing to recover.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay after a failure. AutoMigrate creates
// the table and its index only when absent, so replay is safe.
func NewPendingAlarmEventsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID:       "20261219120000",
		Migrate:  func(tx *gorm.DB) error { return tx.AutoMigrate(&pendingAlarmEvent{}) },
		Rollback: func(tx *gorm.DB) error { return dropTables(tx, []string{"pending_alarm_events"}) },
	}
}
