// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// Like the migrations beside it, this file imports none of the service's own packages. A
// migration that can reach a live model is a migration whose behaviour changes when that
// model does.
import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewCommandCauseSchema adds the record of WHY the platform sent a command: the detection that
// fired and, when the same rule raised one, the alarm (see Command.CauseKind).
//
// Every column is NULLable with no default and no backfill. NULL is the true reading of every
// command that predates the columns, of every command a person or a batch issued, and of every
// command an older event-processing (or this one, falling back from an older command-delivery
// during a rolling upgrade) enqueued without a cause — and it is what an old pod's insert, which
// names none of these columns, writes. cause_kind IS NULL is "no cause"; the other columns are
// written with it, by the same insert, and never rewritten.
//
// cause_detected_at is TEXT, not timestamptz, on purpose: it is the detection's identity time,
// which event-processing anchors at nanoseconds, and Postgres keeps microseconds. A stored instant
// rounded to microseconds would no longer name the detection it came from.
//
// No snapshot struct, by the rule the neighbouring column migrations follow: columns added to an
// EXISTING table are literal, schema-qualified DDL, since AutoMigrate over a whole-row snapshot
// would re-assert every other column. Every statement is individually re-runnable, because
// migrations run with UseTransaction:false and replay from the top after a failure. No index: the
// cause is read with its row, never searched.
func NewCommandCauseSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261120120000",
		Migrate: func(tx *gorm.DB) error {
			for _, ddl := range []string{
				`ALTER TABLE "command-delivery".commands ADD COLUMN IF NOT EXISTS cause_kind varchar(16);`,
				`ALTER TABLE "command-delivery".commands ADD COLUMN IF NOT EXISTS cause_rule_id text;`,
				`ALTER TABLE "command-delivery".commands ADD COLUMN IF NOT EXISTS cause_detection_kind varchar(64);`,
				`ALTER TABLE "command-delivery".commands ADD COLUMN IF NOT EXISTS cause_detected_at varchar(64);`,
				`ALTER TABLE "command-delivery".commands ADD COLUMN IF NOT EXISTS cause_value double precision;`,
				`ALTER TABLE "command-delivery".commands ADD COLUMN IF NOT EXISTS cause_alarm_key text;`,
				`ALTER TABLE "command-delivery".commands ADD COLUMN IF NOT EXISTS cause_position_derived boolean;`,
			} {
				if err := tx.Exec(ddl).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			for _, column := range []string{
				"cause_position_derived", "cause_alarm_key", "cause_value", "cause_detected_at",
				"cause_detection_kind", "cause_rule_id", "cause_kind",
			} {
				if err := tx.Exec(`ALTER TABLE "command-delivery".commands DROP COLUMN IF EXISTS ` + column + `;`).Error; err != nil {
					return err
				}
			}
			return nil
		},
	}
}
