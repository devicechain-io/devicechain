// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewDropAlarmMessageSchema removes alarms.message, a column nothing has ever written.
//
// # What it was
//
// The retired measurement evaluator's human-readable summary. The contributor-set
// integrator that replaced it (ADR-057) has no single rule to phrase a summary from, and
// the raise-alarm request it folds carries no message, so every row has held NULL since.
// The field was nonetheless still served — Alarm.message, AlarmEvent.message, the
// notification email and webhook, the MCP alarm tools — and read null through all of them.
// Every one of those readers is removed in the same change; this drops the storage.
//
// # Appended, not a baseline edit
//
// The baseline snapshot (baseline_snapshot_2.go) still creates the column and stays frozen.
// This runs after it, which is the position a fresh install and an upgraded one both put
// it in, so both converge on the same schema — which is what the golden diff compares.
//
// # Raw DDL rather than a snapshot struct
//
// For the reason NewListOrderIndexesSchema and NewAssetPropertySchemaSchema record: a
// second Go shape for `alarms` needs a TableName(), which bypasses this area's
// TablePrefix. The schema-qualified literal IS the snapshot.
//
// # Re-runnability
//
// Migrations run with UseTransaction:false and replay from the top after a failure, so
// the statement carries IF EXISTS and a replay is a no-op. Dropping a column is a catalog
// change (no table rewrite) under a brief ACCESS EXCLUSIVE lock.
//
// # Rolling upgrade: this is a CONTRACT step taken with the code change
//
// The migration runner's expand/contract discipline (core/rdb) keeps old and new schema
// mutually readable for a rollout window; a DROP of a column the previous release still
// names does not, and that was accepted rather than split over two releases (pre-GA,
// decisive cutover). What a device-management pod still on the previous release sees,
// from the moment the first new pod has migrated until it is replaced:
//
//   - Creating a NEW alarm row fails ("column message does not exist"): its model still
//     INSERTs every column. The raise-alarm consumer treats it as transient and leaves the
//     edge unacked, so AckWait (60s) paces a redelivery — which may land on a new pod, or
//     on another old one. After MaxDeliver (5) attempts the edge is DEAD-LETTERED, and an
//     edge-triggered raise does not re-emit until its condition clears and breaches again
//     (see RaiseAlarmConsumer.retryOrDrop). A rollout that finishes within a few AckWaits
//     loses nothing; one stuck with old pods serving for longer can.
//   - Every cached `SELECT *` on alarms fails ONCE per pooled connection with "cached plan
//     must not change result type" (pgx's statement cache, flushed only by that failure).
//     That covers the integrator's read of an existing alarm (retried as above), and the
//     alarm list, acknowledge and clear served by that pod: an operator's request can fail
//     once and succeeds when repeated.
//
// # Rollback
//
// A downgraded binary against the dropped column fails every new-alarm INSERT, so this
// Rollback is the only route back to a schema the previous release can write. It re-adds
// the column nullable with no default; every value it held was NULL, so no data is lost.
// The column comes back LAST rather than before `contributors`, so a rolled-back schema is
// not byte-identical to the baseline's.
func NewDropAlarmMessageSchema() *gormigrate.Migration {
	const table = `"device-management".alarms`

	return &gormigrate.Migration{
		ID: "20260925120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + table + ` DROP COLUMN IF EXISTS message;`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`ALTER TABLE ` + table +
				` ADD COLUMN IF NOT EXISTS message character varying(1024);`).Error
		},
	}
}
