// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewStateChangeEventsTable adds the append-only presence history hypertable
// (ADR-067 decision 5, S3). A resolved StateChange was previously ack-skipped in the
// persistence worker — the live device-state projection held the latest presence but
// there was no queryable connect/disconnect timeline. This table is that timeline,
// partitioned on occurred_time alongside the other event hypertables so its chunks
// age out on the same data-lifecycle window (it is added to LifecycleHypertables).
//
// The child rows carry an idempotency UNIQUE index
// (tenant_id, device_token, occurred_time, state, session_id) because a StateChange
// has no AltId (the base-event dedup key never engages): a JetStream redelivery would
// otherwise write a duplicate presence row. A birth+death at one instant differ by
// state and both survive; a late higher-session echo differs by session_id and is
// retained for audit; a true redelivery collides and is dropped. The index includes
// occurred_time, the hypertable partition column, so it is created AFTER
// create_hypertable (a hypertable's unique indexes must include the partition column).
func NewStateChangeEventsTable() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260723120000",
		Migrate: func(tx *gorm.DB) error {
			// Snapshot of the StateChangeEvent shape at this migration (declared locally
			// so a later change to the live model never silently rewrites this migration).
			type StateChangeEvent struct {
				rdb.TenantScoped
				DeviceToken  string            `gorm:"type:varchar(128);not null"`
				EventType    esmodel.EventType `gorm:"not null"`
				OccurredTime time.Time         `gorm:"not null"`
				State        string            `gorm:"type:varchar(16);not null"`
				Reason       string
				SessionId    uint64 `gorm:"not null;default:0"`
			}
			if err := tx.AutoMigrate(&StateChangeEvent{}); err != nil {
				return err
			}
			if err := tx.Exec(`SELECT create_hypertable('event-management.state_change_events', ` +
				`'occurred_time', if_not_exists => TRUE);`).Error; err != nil {
				return err
			}
			// The device token is opaque and only ever exact-matched; force C collation
			// for bytewise comparisons (ADR-044), matching the other event hypertables.
			if err := tx.Exec(`ALTER TABLE "event-management"."state_change_events" ` +
				`ALTER COLUMN device_token TYPE varchar(128) COLLATE "C";`).Error; err != nil {
				return err
			}
			// Tenant-scoped time-series lookup index (a device's presence timeline).
			if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_state_change_events_lookup ` +
				`ON "event-management"."state_change_events" (tenant_id, device_token, occurred_time DESC);`).Error; err != nil {
				return err
			}
			// Idempotency unique index (redelivery dedup — see the doc comment).
			return tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_state_change_events_idem ` +
				`ON "event-management"."state_change_events" (tenant_id, device_token, occurred_time, state, session_id);`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP TABLE IF EXISTS "event-management"."state_change_events";`).Error
		},
	}
}
