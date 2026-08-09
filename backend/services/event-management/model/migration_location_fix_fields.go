// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// locationFixColumns are the three columns that complete a GPS fix, in the order
// they are appended to the table.
//
// The types are written as LITERALS here rather than derived from the live
// LocationEvent model, for the same reason the baseline builds from frozen
// snapshot types: a migration is a snapshot of a point in time. If this read the
// live model, the day someone widens LocationEvent.Heading every EXISTING database
// would keep the old column while every FRESH install silently got the new one —
// the two diverge, and the install that looks healthy is the one that is wrong.
//
// Widths: accuracy and speed are metres and metres-per-second, so they take
// elevation's decimal(12,4). Heading is a compass bearing bounded by its own
// semantics to [0, 360), so decimal(7,4) holds every legal value at 0.36 mm-arc
// resolution with nothing to spare for a units bug — which is the point.
var locationFixColumns = []struct{ name, sqlType string }{
	{"accuracy", "decimal(12,4)"},
	{"speed", "decimal(12,4)"},
	{"heading", "decimal(7,4)"},
}

// NewLocationFixFieldsSchema adds accuracy, speed and heading to location_events.
//
// 🔴 This is the FIRST migration appended after the GA baseline, so it is also the
// worked example. Three properties are load-bearing and none of them is optional:
//
//  1. **It is individually re-runnable.** This area's DDL is non-transactional
//     (Timescale forbids wrapping it), so a half-applied migration is never rolled
//     back and replays from the top on the next boot. Hence ADD COLUMN IF NOT
//     EXISTS, and hence nothing here depends on a previous statement having run.
//
//  2. **It declares its own types and never touches the baseline.** The baseline's
//     snapshot struct for this table stays frozen without these columns, so a fresh
//     install creates the table without them and then appends them here — landing
//     them LAST, in exactly the position an upgraded database has them. Physical
//     column order is part of what the golden schema diff compares, so editing the
//     baseline instead would make fresh and upgraded installs disagree.
//
//  3. 🔴 **NO DEFAULT, and that is a data-safety property rather than a style
//     choice.** `ADD COLUMN ... DEFAULT <const>` is tempting: it is fast, the size
//     does not change, and every historical row reads the default. But the value is
//     not stored anywhere — it lives in PostgreSQL's attmissingval catalog state,
//     and on a compressed hypertable the compressed relations hold NULL. A logical
//     dump and restore loses it silently: measured on this schema, 96.7 % of rows
//     came back NULL with no error at all. This repo's restore drills are
//     logical-dump-based, so a default here would be a silent-data-loss shape.
//     Nullable with no default is free (ADD COLUMN on a hypertable with compressed
//     chunks is metadata-only, ~15 ms, old chunks untouched and reading NULL) and
//     it is the only safe form.
//
// Note what this migration deliberately does NOT do: there is no backfill. A
// table-wide UPDATE on a compressed hypertable fails outright at default settings
// and, forced through, permanently decompresses every chunk it touches — a ~10x
// growth that needs an explicit recompress plus VACUUM FULL to recover. A backfill
// here would be an outage-shaped event sized by the largest hypertable. Old rows
// have no accuracy, speed or heading because the devices that wrote them never
// reported any, and NULL says that honestly.
//
// All of the above was MEASURED against the pinned TimescaleDB image, on a
// compressed hypertable built from this exact schema (20k rows / 3 chunks), rather
// than reasoned from the docs:
//
//	both passes applied      second pass skipped every column, no error
//	per statement            4-14 ms
//	column position          10, 11, 12 — last, matching the struct
//	column_default           empty, so no attmissingval state to lose on restore
//	chunks fully compressed  3/3 before, 3/3 after
//	hypertable size          672 kB before, 672 kB after
//	historical rows          20000 total, 0 with a value — NULL, as intended
//
// And the negative control, because a health check nobody has seen fail is not a
// health check: running the backfill this migration refuses to run flips the same
// counters to 0/3 compressed and 6080 kB — 9x growth. 🔑 Note which instrument
// stayed silent through that: timescaledb_information.chunks.is_compressed still
// reported true for all three chunks. It answers "does a compressed counterpart
// relation exist", not "are these rows compressed". Verify with
// _timescaledb_catalog.chunk.status (bit 8 = has uncompressed rows) instead.
func NewLocationFixFieldsSchema() *gormigrate.Migration {
	const table = `"event-management"."location_events"`

	return &gormigrate.Migration{
		ID: "20260809000000",
		Migrate: func(tx *gorm.DB) error {
			for _, col := range locationFixColumns {
				if err := tx.Exec(fmt.Sprintf(
					`ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s %s;`,
					table, col.name, col.sqlType)).Error; err != nil {
					return fmt.Errorf("add location_events.%s: %w", col.name, err)
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			for _, col := range locationFixColumns {
				if err := tx.Exec(fmt.Sprintf(
					`ALTER TABLE %s DROP COLUMN IF EXISTS %s;`,
					table, col.name)).Error; err != nil {
					return fmt.Errorf("drop location_events.%s: %w", col.name, err)
				}
			}
			return nil
		},
	}
}
