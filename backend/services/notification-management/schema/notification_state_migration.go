// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewNotificationStateSchema adds the notification_states table (ADR-017): the
// per-alarm notification/escalation state the dispatcher (N.C) upserts and the
// escalation scheduler (N.D) reads. One row per raised alarm, keyed by alarm
// token; the per-tenant partial unique index on (tenant_id, alarm_token) makes the
// dispatcher's upsert a clean conflict target. Snapshot is inline (frozen) and
// must match model.NotificationState.
func NewNotificationStateSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260705140000",
		Migrate: func(tx *gorm.DB) error {
			type NotificationState struct {
				gorm.Model
				rdb.TenantScoped

				AlarmToken string `gorm:"not null;size:128"`
				AlarmKey   string `gorm:"not null;size:256;index"`
				Severity   string `gorm:"size:16"`

				FirstNotifiedAt sql.NullTime
				LastNotifiedAt  sql.NullTime
				NotifyCount     int `gorm:"not null;default:0"`

				AcknowledgedAt sql.NullTime
				ClearedAt      sql.NullTime

				EscalationLevel int `gorm:"not null;default:0"`
				LastEscalatedAt sql.NullTime
			}
			if err := tx.AutoMigrate(&NotificationState{}); err != nil {
				return err
			}
			// One live state row per raised alarm within a tenant (soft-delete aware).
			return rdb.CreatePartialUniqueIndex(tx, &NotificationState{},
				"uix_notification_states_tenant_alarm_token", "tenant_id", "alarm_token")
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("notification_states")
		},
	}
}
