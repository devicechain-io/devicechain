// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewInitialSchema creates the initial schema for the event-processing service:
// the DETECT engine's snapshot store (ADR-051). Unlike event-management's schema
// this is a plain relational table (one row per partition), not a hypertable —
// bounded by the number of single-writer partitions (one per Instance at GA), so
// it never grows with telemetry volume.
func NewInitialSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260709000000",
		Migrate: func(tx *gorm.DB) error {
			// One durable checkpoint per partition. partition_id is the natural primary
			// key (a partition has exactly one live checkpoint, overwritten in place on
			// each commit); the payload holds the serialized engine state and stream_seq
			// the highest applied JetStream sequence captured in it (ack-on-checkpoint).
			// No tenant_id: the engine is a per-Instance singleton spanning all tenants.
			type DetectSnapshot struct {
				PartitionId string    `gorm:"primaryKey;size:128;not null"`
				StreamSeq   int64     `gorm:"not null"`
				Watermark   time.Time `gorm:"not null"`
				Payload     []byte    `gorm:"not null"`
				UpdatedAt   time.Time
			}

			return tx.AutoMigrate(&DetectSnapshot{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("detect_snapshots")
		},
	}
}
