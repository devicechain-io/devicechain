// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// This file imports none of the service's own packages, for the reason stated in
// baseline.go: a migration that can reach a live model is a migration whose output
// changes when that model does. It declares no struct at all: it changes how three
// existing tables are STORED, not what they hold.
import (
	"fmt"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// projectionStorageTables are the projection tables the StateProcessor updates in place,
// once per event per device: device_states, latest_measurements and latest_locations. They
// are named as literals, not derived from the live models, so the migration's output cannot
// move when a model does.
var projectionStorageTables = []string{"device_states", "latest_measurements", "latest_locations"}

// projectionStorageOptions are the storage parameters set on each of them.
//
//   - fillfactor 75 leaves a quarter of every heap page free for the new version of a row
//     updated in place. A version that fits on its page, with no indexed column changed
//     (none of the columns these projections rewrite per event is indexed), is a HOT update:
//     no index entry is written and pruning reclaims the old version without waiting for
//     vacuum. At the default 100 the page is full, so almost every update moves the row to
//     another page and writes an entry into every index on the table.
//   - autovacuum_vacuum_scale_factor 0 with a fixed threshold turns "vacuum when 20% of the
//     table is dead" into "vacuum every 10,000 dead tuples". The percentage rule waits for
//     200,000 dead rows on a million-device table, a long way past what pruning can hold.
//   - autovacuum_vacuum_cost_limit 2000 lets each of those vacuums run ten times the stock
//     budget before it sleeps. A per-table cost limit takes the table out of the balancing
//     autovacuum does across its workers, which is what is wanted for a table that is
//     always being vacuumed.
//   - autovacuum_analyze_* is scaled the same way, so the planner's statistics follow the
//     table rather than a fifth of it.
//
// 🔴 `ALTER TABLE ... SET (fillfactor = ...)` changes only pages written AFTER it. Rows that
// already exist stay packed until the table is rewritten (VACUUM FULL, CLUSTER, or a
// pg_repack), so an upgraded instance gains the headroom only as its pages are rewritten
// by updates that move rows, which is slowly. A fresh install has it from the first row. A
// projection is rebuilt from the event stream, so a rewrite is optional and offline.
var projectionStorageOptions = "fillfactor = 75, " +
	"autovacuum_vacuum_scale_factor = 0, autovacuum_vacuum_threshold = 10000, " +
	"autovacuum_vacuum_cost_limit = 2000, " +
	"autovacuum_analyze_scale_factor = 0.02, autovacuum_analyze_threshold = 10000"

// projectionStorageStatements returns the DDL the migration runs, one statement per table.
func projectionStorageStatements() []string {
	out := make([]string, 0, len(projectionStorageTables))
	for _, t := range projectionStorageTables {
		out = append(out, fmt.Sprintf(`ALTER TABLE "device-state".%s SET (%s)`, t, projectionStorageOptions))
	}
	return out
}

// NewProjectionStorageSchema tunes the storage of the projection tables for an update-heavy
// load: heap-page headroom for HOT updates and a vacuum that triggers on a count of dead
// tuples rather than a share of the table.
//
// This is an APPENDED migration. The baseline and the migrations before it are frozen and
// are not edited to add these options: a fresh install creates the tables and then applies
// this, landing in exactly the state an upgraded database has.
//
// It is individually re-runnable, which it has to be: migrations run with
// UseTransaction:false, so a half-applied migration is never rolled back and replays from
// the top on the next boot. SET of a storage parameter overwrites its previous value, so a
// replay changes nothing. Each statement takes a SHARE UPDATE EXCLUSIVE lock for the
// catalog change only; it neither rewrites the table nor blocks reads or writes.
func NewProjectionStorageSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261104120000",
		Migrate: func(tx *gorm.DB) error {
			for _, stmt := range projectionStorageStatements() {
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			for _, t := range projectionStorageTables {
				stmt := fmt.Sprintf(`ALTER TABLE "device-state".%s RESET (fillfactor, `+
					`autovacuum_vacuum_scale_factor, autovacuum_vacuum_threshold, autovacuum_vacuum_cost_limit, `+
					`autovacuum_analyze_scale_factor, autovacuum_analyze_threshold)`, t)
				if err := tx.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		},
	}
}
