// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewInactivityAlarmTimeRestoreFinalPassSchema finishes the repair that
// NewInactivityAlarmTimeRestoreSchema (20261106120000) starts, and proves it finished.
//
// That migration batches with FOR UPDATE SKIP LOCKED so it never waits on a row lock a v1.1 writer
// holds, and stops at the first batch that touches nothing. A row a v1.1 writer held while the
// migration ran is therefore skipped, and the migration is recorded as applied, so it never runs
// again: the rows it skipped stayed wrong for good. This one runs the same three statements in the
// same order, then counts what still matches and tries again after a pause, up to
// restoreFinalPasses times, and returns an error naming the count when rows remain. An error leaves
// the migration unrecorded, so it replays from the top at the next start.
//
// IT DOES NOT WAIT FOR A LOCK EITHER. Waiting for a row a v1.1 batch writer holds is the cycle
// documented at SweepInactive (the writer locks its rows in token order, the waiter in heap order,
// and the pair deadlocks), so the passes keep SKIP LOCKED and the lock is waited out by time:
// a writer's locks last one batch.
//
// WHAT IT DOES NOT AND CANNOT COVER is a v1.1 pod that sweeps after the last pass. v1.1 pods run
// for as long as a rolling upgrade lasts and their SweepInactive stamps last_disconnect_time and
// not this column. Those rows are answered by the resolver (graphql.sweptByV11), which reads the
// stamp itself, so the deprecated field is right for them without any write.
//
// The third statement differs from the original in one condition: it does not write a value that
// predates the last connect. The second statement would clear it again and the count could never
// reach zero, and the resolver answers null for such a value, so nothing is lost.
//
// Data only, no DDL, and no snapshot struct: it reads and writes the table by literal column names
// in SQL, as the migration it follows does, so no live model can move under it.
func NewInactivityAlarmTimeRestoreFinalPassSchema() *gormigrate.Migration {
	return newInactivityAlarmTimeRestoreFinalPass(restoreFinalPasses, restoreFinalPause)
}

const (
	// restoreFinalPasses and restoreFinalPause bound how long a migration waits for the rows a
	// writer holds: a v1.1 batch holds its locks for a transaction, so seconds are plenty.
	restoreFinalPasses = 6
	restoreFinalPause  = 5 * time.Second
)

// restoreStatements are the three repairs, each as the column it sets and the rows it matches.
var restoreStatements = []struct{ set, where string }{
	{`inactivity_alarm_time = NULL`, `active AND inactivity_alarm_time IS NOT NULL`},
	{`inactivity_alarm_time = NULL`, `inactivity_alarm_time IS NOT NULL AND last_connect_time IS NOT NULL` +
		` AND inactivity_alarm_time < last_connect_time`},
	{`inactivity_alarm_time = last_disconnect_time`, `NOT active AND presence_source <> 'ASSERTED'` +
		` AND inactivity_alarm_time IS NULL AND last_disconnect_time IS NOT NULL` +
		` AND (presence_time IS NULL OR last_disconnect_time > presence_time)` +
		` AND (last_connect_time IS NULL OR last_disconnect_time >= last_connect_time)`},
}

func newInactivityAlarmTimeRestoreFinalPass(passes int, pause time.Duration) *gormigrate.Migration {
	const states = `"device-state".device_states`

	return &gormigrate.Migration{
		ID: "20261107120000",
		Migrate: func(tx *gorm.DB) error {
			remaining := 0
			for pass := 0; pass < passes; pass++ {
				if pass > 0 {
					time.Sleep(pause)
				}
				for _, stmt := range restoreStatements {
					for {
						res := tx.Exec(`UPDATE ` + states + ` SET ` + stmt.set + ` WHERE id IN (SELECT id FROM ` + states +
							` WHERE ` + stmt.where + ` LIMIT ` + migrationBatch + ` FOR UPDATE SKIP LOCKED);`)
						if res.Error != nil {
							return res.Error
						}
						if res.RowsAffected == 0 {
							break
						}
					}
				}
				// Count with the same predicates, locked or not: a row left only because a writer held
				// it still matches.
				query := `SELECT count(*) FROM ` + states + ` WHERE FALSE`
				for _, stmt := range restoreStatements {
					query += ` OR (` + stmt.where + `)`
				}
				if err := tx.Raw(query).Scan(&remaining).Error; err != nil {
					return err
				}
				if remaining == 0 {
					return nil
				}
			}
			return fmt.Errorf("inactivity alarm time restore: %d device_states rows were still held by writers after %d passes; "+
				"the migration is not recorded and runs again at the next start", remaining, passes)
		},
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
