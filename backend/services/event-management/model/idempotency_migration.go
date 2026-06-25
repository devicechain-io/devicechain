// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewAltIdIdempotencyIndex adds the partial unique index that makes resolved-event
// ingestion idempotent on the alternateId (ADR-013 alternateId; ADR-022 Wave-2
// redelivery/DLQ machinery makes redelivery a designed-for path, so a redelivered
// resolved event would otherwise double-persist).
//
// The events table is a TimescaleDB hypertable partitioned on occurred_time, and
// TimescaleDB requires every unique index on a hypertable to include the
// partitioning column. The dedup key is therefore (tenant_id, alt_id,
// occurred_time): a redelivered message carries the identical occurred_time, so
// exact redeliveries still collide and are deduped, while the tenant_id keeps the
// key tenant-scoped. The index is partial (alt_id IS NOT NULL) so events without
// an alternateId are unaffected — they simply are not deduplicated.
func NewAltIdIdempotencyIndex() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260625120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_events_tenant_alt_id ` +
				`ON "event-management"."events" (tenant_id, alt_id, occurred_time) ` +
				`WHERE alt_id IS NOT NULL;`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP INDEX IF EXISTS "event-management"."idx_events_tenant_alt_id";`).Error
		},
	}
}
