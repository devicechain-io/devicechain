// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// This file imports none of the service's own packages, and that is a property worth
// keeping rather than an accident. A migration that can reach a live model, a live
// vocabulary or a live validator is a migration whose behaviour changes when those do.
import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewBaselineSchema materializes the whole device-state schema in one step: the live
// current-state projection per device, including the authoritative-presence columns
// (ADR-067), and the latest-measurement projection with its denormalized metric binding
// (ADR-016).
//
// This REPLACES the former six-migration chain, collapsed pre-GA. Until v1.0.0 there is no
// released version to upgrade FROM, so that chain only ever walked a maintainer's laptop
// forward one column at a time; the history has no value before there is something to
// migrate from. Its equivalence to this migration is proven by hack/migration-diff.sh
// verify against the golden captured from the chain.
//
// Four of those six migrations were raw `ALTER TABLE ... ADD COLUMN`, and they fold in as
// FIELDS AT THE END of the snapshot types — see the field-order note in
// baseline_snapshot.go, which is the one place this flatten can go wrong invisibly to a
// reader. None of the six carried data: no seed, no backfill, so nothing is dropped and
// nothing needs a row-level test.
//
// Consequence, and it is deliberate: an EXISTING instance is not migrated onto this
// baseline, it is recreated (dcctl destroy + bootstrap).
//
// CHANGING THE SCHEMA FROM HERE ON: append a new migration. Do not touch this one, and do
// not "update" the snapshot types it builds from. Anything appended must be individually
// re-runnable, since migrations run with UseTransaction:false and replay from the top after
// a failure.
func NewBaselineSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260729000000",
		Migrate: func(tx *gorm.DB) error {
			// The SNAPSHOT types in baseline_snapshot.go, never the live models.
			if err := tx.AutoMigrate(&deviceState{}, &latestMeasurement{}); err != nil {
				return err
			}
			// One row per (tenant, device, measurement name). The unique index backs the
			// tenant-scoped per-device read and guards against duplicate creates racing
			// across pods (a losing concurrent create errors and is redelivered).
			//
			// Raw DDL rather than a struct tag because the index is created against the
			// area-qualified table, and literal here rather than through a core helper for
			// the reason the snapshot rule exists: an index name is schema, and core must
			// not be able to change this migration's output.
			return tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_latest_measurement_tenant_device_name ` +
				`ON "device-state".latest_measurements (tenant_id, device_token, name);`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			for _, table := range []string{"latest_measurements", "device_states"} {
				if err := tx.Migrator().DropTable(table); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
