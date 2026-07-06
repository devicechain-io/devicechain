// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewEventAnchorsTable moves an event's relationship anchors from a single
// denormalized (anchor_type, anchor_id) pair on the base event to a *set* in a
// sibling event_anchors table (ADR-013 addendum 2026-07-01). One event now has
// zero or more anchor rows, so a device assigned to several targets (a customer
// *and* an area *and* an asset) is queryable by each dimension for the same
// reading — which the single-pair schema could not express.
//
// event_anchors is a TimescaleDB hypertable partitioned on occurred_time, so its
// rows partition and age out alongside the events they index. Both the source
// device and the anchor target are named by their stable per-tenant tokens
// (ADR-044), never device-management row ids. The lookup index
// (tenant_id, anchor_type, anchor_token, occurred_time DESC) serves "events for
// customer X / area Y, most recent first"; the events keyed by an anchor are
// fetched by their natural key (device_token, event_type, occurred_time).
func NewEventAnchorsTable() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260701130000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&EventAnchor{}); err != nil {
				return err
			}
			if err := tx.Exec(`SELECT create_hypertable('event-management.event_anchors', ` +
				`'occurred_time', if_not_exists => TRUE);`).Error; err != nil {
				return err
			}
			// The device and anchor tokens are opaque and only ever exact-matched;
			// force C collation for bytewise comparisons and a tighter lookup index
			// (ADR-044). The index created below inherits these column collations.
			for _, col := range []string{"device_token", "anchor_token"} {
				if err := tx.Exec(`ALTER TABLE "event-management"."event_anchors" ` +
					`ALTER COLUMN ` + col + ` TYPE varchar(128) COLLATE "C";`).Error; err != nil {
					return err
				}
			}
			if err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_event_anchors_lookup ` +
				`ON "event-management"."event_anchors" (tenant_id, anchor_type, anchor_token, occurred_time DESC);`).Error; err != nil {
				return err
			}
			// The base event no longer carries a single denormalized anchor — the set
			// lives in event_anchors — so drop the old columns and their index.
			if err := tx.Exec(`DROP INDEX IF EXISTS ` +
				`"event-management"."events_anchor_type_anchor_id_occurred_time_idx";`).Error; err != nil {
				return err
			}
			if err := tx.Exec(`ALTER TABLE "event-management"."events" DROP COLUMN IF EXISTS anchor_type;`).Error; err != nil {
				return err
			}
			return tx.Exec(`ALTER TABLE "event-management"."events" DROP COLUMN IF EXISTS anchor_id;`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Exec(`ALTER TABLE "event-management"."events" ` +
				`ADD COLUMN IF NOT EXISTS anchor_type text, ADD COLUMN IF NOT EXISTS anchor_id bigint;`).Error; err != nil {
				return err
			}
			return tx.Exec(`DROP TABLE IF EXISTS "event-management"."event_anchors";`).Error
		},
	}
}
