// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm/clause"
)

// Hold the lowest ID, then prove a bulk writer waits without owning the next
// line. Reverse heap order and disable index scans so an unordered bulk UPDATE
// or DELETE cannot accidentally pass by choosing the primary-key index.
func TestFleetBulkLineLockOrderOnPostgres(t *testing.T) {
	for _, path := range []string{"erase", "abort", "purge"} {
		t.Run(path, func(t *testing.T) {
			p := newPostPG(t, "v1")
			db := p.api.RDB.DB(p.ctx)
			low, high := p.lines[0], p.lines[1]
			require.Less(t, low.ID, high.ID)
			if path == "erase" {
				// The same target in two operations: erasure spans operations.
				other := *p.op
				other.ID, other.Token = 0, "erase-other"
				require.NoError(t, db.Create(&other).Error)
				require.NoError(t, db.Model(&high).Updates(map[string]any{"operation_id": other.ID, "target_id": low.TargetId}).Error)
			}
			require.NoError(t, db.Model(&low).Update("detail", "move lowest ID to the heap tail").Error)
			held := db.Begin()
			require.NoError(t, held.Error)
			defer held.Rollback()
			var line FleetOperationLine
			require.NoError(t, held.Clauses(clause.Locking{Strength: "UPDATE"}).First(&line, low.ID).Error)
			var holder int
			require.NoError(t, held.Raw("SELECT pg_backend_pid()").Scan(&holder).Error)
			writer := db.Begin()
			require.NoError(t, writer.Error)
			defer func() {
				_ = held.Rollback() // Release the blocker before waiting for the writer.
				_ = writer.Rollback()
			}()
			require.NoError(t, writer.Exec("SET LOCAL enable_indexscan = off").Error)
			require.NoError(t, writer.Exec("SET LOCAL enable_bitmapscan = off").Error)
			var waiter int
			require.NoError(t, writer.Raw("SELECT pg_backend_pid()").Scan(&waiter).Error)
			done := make(chan error, 1)
			go func() {
				var err error
				switch path {
				case "erase":
					err = redactFleetDeviceLinesOnTx(writer, uint(low.TargetId.Int64), false)
				case "abort":
					_, _, err = fleetAbortChanges(writer, p.op, "test", "", fleetEventAbort)
				default:
					err = fleetPurgeEvidenceChunk(writer, p.op.ID)
				}
				if err == nil {
					err = writer.Commit().Error
				}
				done <- err
			}()
			require.Eventually(t, func() bool {
				var blocked bool
				err := db.Raw("SELECT ? = ANY(pg_blocking_pids(?))", holder, waiter).Scan(&blocked).Error
				return err == nil && blocked
			}, 5*time.Second, 10*time.Millisecond)
			probe := db.Begin()
			require.NoError(t, probe.Error)
			defer probe.Rollback()
			var next FleetOperationLine
			err := probe.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).First(&next, high.ID).Error
			require.NoError(t, probe.Rollback().Error)
			require.NoError(t, held.Commit().Error)
			select {
			case writeErr := <-done:
				require.NoError(t, writeErr)
			case <-p.ctx.Done():
				t.Fatal(p.ctx.Err())
			}
			require.NoError(t, err, "a writer waiting for the first ID must not hold the second ID")
		})
	}
}
