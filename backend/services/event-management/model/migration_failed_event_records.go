// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// failedEventRecordsDDL is this migration's own snapshot of the failed-events index: the
// statements that create the hypertable and its indexes, written as literals. Nothing here
// reads the baseline, a live model or another migration, so the live FailedEventRecord struct
// can change without rewriting what a fresh install builds; a later change to the table is a
// new migration.
//
// What a row is. One row indexes ONE record on the failed-events stream: which tenant, when
// the broker stored it, which area wrote it and why, the device token the sender CLAIMED, the
// event type when the record carried one, a bounded error, the correlation id, and
// record_seq - the record's position on the stream. The payload is NOT here, and that is the
// design rather than an omission: it stays on the stream, located by record_seq, so the
// relational store never holds an archived device payload and an expired one reads as expired.
//
// row_id is the row's opaque public identity (random, minted by the writer): the API serves it
// instead of record_seq, which is a position on a stream every tenant shares. payload_retained
// says whether the record's envelope carried an archived event at all, decided when the record
// was indexed; it is what lets "the original was never kept" read differently from "the stream
// has rolled past it".
//
// suppressed counts records the flood bound folded into this row instead of inserting (see
// FailedEventGate). It is the only column ever UPDATEd.
//
// A hypertable partitioned on occurred_time, like every other table in this schema, so the
// data-lifecycle reconciler governs it and a retention window drops whole chunks. Timescale
// requires a unique index to include the partitioning column, so the idempotency key is
// (tenant_id, record_seq, occurred_time): a redelivery carries the same stream position and
// the same broker timestamp, and collides.
//
// device_token is C-collated like every other token column here: tokens are only ever
// exact-matched, and an index inherits its column's collation. An absent device is the empty
// string rather than NULL, so the gate's (tenant, reason, device) key has one spelling for it.
//
// Every statement is re-runnable (IF NOT EXISTS, if_not_exists => TRUE): this area's
// migrations run with UseTransaction:false, and create_hypertable cannot run in a
// transaction at all, so a half-applied run replays from the top.
var failedEventRecordsDDL = []string{
	`CREATE TABLE IF NOT EXISTS "event-management".failed_event_records (
		tenant_id varchar(128) NOT NULL,
		row_id varchar(32) NOT NULL,
		occurred_time timestamptz NOT NULL,
		record_seq bigint NOT NULL,
		source_area varchar(64) NOT NULL,
		reason integer NOT NULL,
		device_token varchar(128) COLLATE "C" NOT NULL DEFAULT '',
		event_type bigint,
		error varchar(512) NOT NULL DEFAULT '',
		correlation varchar(128) NOT NULL DEFAULT '',
		payload_retained boolean NOT NULL,
		suppressed bigint NOT NULL DEFAULT 0
	)`,
	// create_default_indexes => FALSE: Timescale's own (occurred_time DESC) btree would be a
	// fourth index on a table the three below already serve (every read is tenant-first and
	// every write is the unique arbiter), paid for on every insert.
	`SELECT create_hypertable('"event-management".failed_event_records', 'occurred_time', create_default_indexes => FALSE, if_not_exists => TRUE)`,
	// A record's identity, and the arbiter that makes a redelivery add nothing.
	`CREATE UNIQUE INDEX IF NOT EXISTS uq_failed_event_records_seq
		ON "event-management".failed_event_records (tenant_id, record_seq, occurred_time)`,
	// The tenant's list, newest first.
	`CREATE INDEX IF NOT EXISTS idx_failed_event_records_tenant_time
		ON "event-management".failed_event_records (tenant_id, occurred_time DESC)`,
	// "this device, this reason": the flood gate's hydration and the device filter.
	`CREATE INDEX IF NOT EXISTS idx_failed_event_records_device
		ON "event-management".failed_event_records (tenant_id, device_token, reason, occurred_time DESC)`,
}

// NewFailedEventRecordsSchema adds the failed-events index (hypertable) the failed-events
// reader writes and the tenant's failedEvents query reads.
//
// It is inert until a record arrives: no seed rows, nothing written.
func NewFailedEventRecordsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261102000000",
		Migrate: func(tx *gorm.DB) error {
			for i, stmt := range failedEventRecordsDDL {
				if err := tx.Exec(stmt).Error; err != nil {
					return fmt.Errorf("failed_event_records DDL statement %d: %w", i+1, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP TABLE IF EXISTS "event-management".failed_event_records`).Error
		},
	}
}
