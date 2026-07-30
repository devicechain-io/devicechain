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

// NewBaselineSchema materializes the whole event-processing schema in one step: the DETECT
// engine's snapshot store (ADR-051) and the five durable read-models it rebuilds from at
// startup — the rule projection with its group scope (ADR-062 S4), the dead-man roster and
// its profile-active grace base, the device-attribute values with their deletion fence, and
// the per-rule firing stats.
//
// Every one of those tables exists for the same reason, which is worth stating once: the fact
// streams they are built from have FINITE RETENTION, so anything the engine needs after a
// restart has to be durable here rather than replayed. That is why a flatten of this area
// must not quietly drop a table it cannot see a live reader for.
//
// This REPLACES the former six-migration chain, collapsed pre-GA. Until v1.0.0 there is no
// released version to upgrade FROM, so that chain only ever walked a maintainer's laptop
// forward; the history has no value before there is something to migrate from. Equivalence is
// proven by hack/migration-diff.sh verify against the golden captured from the chain.
//
// One of the six was additive (`ALTER`-style AutoMigrate adding the two scope columns to
// detect_rules) and folds in as FIELDS AT THE END of that snapshot type — see the note in
// baseline_snapshot.go, which is the one place this flatten can go wrong invisibly to a
// reader. None of the six carried data: no seed, no backfill.
//
// Consequence, and it is deliberate: an EXISTING instance is not migrated onto this baseline,
// it is recreated (dcctl destroy + bootstrap).
//
// CHANGING THE SCHEMA FROM HERE ON: append a new migration. Do not touch this one, and do not
// "update" the snapshot types it builds from. Anything appended must be individually
// re-runnable, since migrations run with UseTransaction:false and replay from the top after a
// failure.
func NewBaselineSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260729000000",
		Migrate: func(tx *gorm.DB) error {
			// The SNAPSHOT types in baseline_snapshot.go, never the live models. Every index
			// this area has comes from a struct tag, so there is no explicit DDL here.
			return tx.AutoMigrate(
				&detectSnapshot{},
				&detectRule{},
				&deviceRoster{},
				&profileActive{},
				&deviceAttribute{},
				&deviceAttributeDeletion{},
				&ruleStat{},
			)
		},
		Rollback: func(tx *gorm.DB) error {
			for _, table := range []string{
				"rule_stats",
				"device_attribute_deletions",
				"device_attributes",
				"profile_actives",
				"device_rosters",
				"detect_rules",
				"detect_snapshots",
			} {
				if err := tx.Migrator().DropTable(table); err != nil {
					return err
				}
			}
			return nil
		},
	}
}
