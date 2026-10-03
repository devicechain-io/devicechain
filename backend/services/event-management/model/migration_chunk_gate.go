// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// eventStoreMaxChunks is the most chunks one hypertable may have for a startup migration that
// locks every chunk of it (NewIndexTrimSchema, NewTimeLeadingKeysSchema) to go ahead. It is HALF
// the shape both were first measured at on the pinned image, 1,000 chunks with 950 compressed.
// There, rebuilding events_pkey took 10.8 s of its 40 s allowance when the rebuild was written,
// then 21.6 s, 23.7 s and 28.2 s on development workstations (WSL2, Docker) while this gate was
// reviewed: under 1.5x of room on a slow host, and the time had already varied more than 2x
// between hosts. DROP INDEX took 0.54 s of its 5 s bound, and the default lock table held both.
// Running out of the allowance is the one outcome to avoid, because it marks the key too slow
// and refuses every later start until the instance is recreated, so the ceiling is set where
// slower storage still fits: at 500 chunks the same rebuild took 9.5 s on the workstation that
// took 28.2 s at 1,000, about 4x of room. Above it a start refuses, changing and marking nothing.
// TestIntegrationTimeLeadingKeysRefuseTooManyChunksBeforeLocking re-measures the rebuild at
// this ceiling on every run and fails if it no longer fits one table's allowance.
//
// It is a count of chunks, not of days: one chunk covers lifecycle.chunkIntervalHours (24 h by
// default), so a shorter interval reaches it with less history. The event-time floor
// (eventtime.MaxAge) bounds how far back a device can date a reading in DAYS, and so keeps no
// table under this count by itself; this gate is what protects the migrations.
const eventStoreMaxChunks = 500

// chunkCountTimeout bounds the chunk count (SET LOCAL statement_timeout). The count reads one
// catalog table, so it takes milliseconds even at hundreds of thousands of chunks.
const chunkCountTimeout = 10 * time.Second

// eventStoreTables lists every event hypertable, in the order the remedy names them.
var eventStoreTables = []string{"events", "measurement_events", "location_events", "alert_events",
	"event_anchors", "state_change_events"}

// chunkListQuery is the query both refusals hand the operator to see what is there.
const chunkListQuery = "SELECT hypertable_name, count(*) AS chunks, min(range_start) AS oldest, " +
	"max(range_end) AS newest FROM timescaledb_information.chunks " +
	"WHERE hypertable_schema = 'event-management' GROUP BY 1 ORDER BY 1"

// eventStoreChunksMessage is the refusal: what refused, the tables over the ceiling with
// their counts, the ceiling, what this start changed, the list query, then the remedy.
const eventStoreChunksMessage = "%s: %s more than %d chunks, the most this release will lock and rebuild " +
	"within one start (half what it has been measured to do, to leave room for slower storage), so this " +
	"start changed nothing (%s) rather than risk a rebuild " +
	"that cannot finish and then blocks every later start. Nothing is marked: every start counts again. " +
	"A chunk covers one chunk interval of one event table (a day by default, lifecycle.chunkIntervalHours); " +
	"this many usually means events dated far in the past. List them with: %s. To upgrade in place, " +
	"first stop whatever sent them (revoke the device's credential), or the chunks come back; then remove " +
	"the chunks you do not need, with the same cutoff on all six event tables, using the batched drop_chunks " +
	"procedure in this release's upgrade notes (under \"The event store's keys lead with time\"). That " +
	"deletes every tenant's events in those chunks for good; the date the instance was installed is a cutoff " +
	"that removes only events dated before it existed. Then restart event-management. Otherwise, " +
	recreateAdvice + ". The previous event-management keeps storing events until then."

// chunksPerTable counts the chunks of each of tables, from the catalog alone, under its own
// statement_timeout, inside a transaction so the SET LOCAL ends with it. It takes no table
// lock: it reads _timescaledb_catalog.chunk, one row per chunk, joined to its hypertable,
// rather than the timescaledb_information.chunks view, which joins every chunk to its
// dimension slices and would itself slow with the count it is guarding against. A table with
// no chunks is absent from the result (zero).
func chunksPerTable(db *gorm.DB, tables []string, timeout time.Duration) (map[string]int64, error) {
	counts := map[string]int64{}
	err := db.Transaction(func(t *gorm.DB) error {
		setting, err := timeoutSetting(timeout)
		if err != nil {
			return err
		}
		if err := t.Exec(`SET LOCAL statement_timeout = '` + setting + `'`).Error; err != nil {
			return err
		}
		var rows []struct {
			Hypertable string
			Chunks     int64
		}
		if err := t.Raw(`SELECT h.table_name AS hypertable, count(*) AS chunks
			FROM _timescaledb_catalog.chunk c
			JOIN _timescaledb_catalog.hypertable h ON h.id = c.hypertable_id
			WHERE h.schema_name = 'event-management' AND h.table_name IN ?
			GROUP BY h.table_name`, tables).Scan(&rows).Error; err != nil {
			return err
		}
		for _, r := range rows {
			counts[r.Hypertable] = r.Chunks
		}
		return nil
	})
	return counts, err
}

// overChunkCeiling renders "events (1834), measurement_events (1834) have" for every table of
// tables, in tables' order, whose count is over limit, or "" when none is. Over is strict:
// limit chunks is allowed.
func overChunkCeiling(counts map[string]int64, tables []string, limit int64) string {
	var over []string
	for _, table := range tables {
		if n := counts[table]; n > limit {
			over = append(over, fmt.Sprintf("%s (%d)", table, n))
		}
	}
	switch len(over) {
	case 0:
		return ""
	case 1:
		return over[0] + " has"
	default:
		return strings.Join(over, ", ") + " have"
	}
}

// checkChunkCeiling refuses, before any lock, a start whose tables include one with more than
// limit chunks. what leads the message and progress says what this start has changed.
func checkChunkCeiling(db *gorm.DB, what, progress string, tables []string, limit int64, timeout time.Duration) error {
	if len(tables) == 0 {
		return nil
	}
	counts, err := chunksPerTable(db, tables, timeout)
	if err != nil {
		return fmt.Errorf("%s: counting the chunks of %s failed, so nothing was changed (%s); the next start "+
			"tries again. If it fails the same way on every start, list the chunks with: %s; and see the batched "+
			"drop_chunks procedure in this release's upgrade notes. Error: %w",
			what, strings.Join(tables, ", "), progress, chunkListQuery, err)
	}
	if over := overChunkCeiling(counts, tables, limit); over != "" {
		return fmt.Errorf(eventStoreChunksMessage, what, over, limit, progress, chunkListQuery)
	}
	return nil
}
