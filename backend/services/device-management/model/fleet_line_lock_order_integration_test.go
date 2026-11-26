// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Two devices have disjoint lines, but share resolutions in two operations.
// Force opposite event scan plans and hold the lowest event: both deletions
// must wait there without owning the higher event, then both must commit.
func TestFleetTwoDeletionsOrderSharedEventLocksOnPostgres(t *testing.T) {
	p := newPostPG(t, "v1")
	cleanupFleetMaintenancePost(t, p)
	db := p.api.RDB.DB(p.ctx)
	other := *p.op
	other.ID, other.Token = 0, "shared-event-other"
	require.NoError(t, db.Create(&other).Error)
	t.Cleanup(func() {
		// other is a hand-made fixture with no runtime row, so it cannot go through
		// AbortFleetOperation; remove its rows so nothing live is left behind.
		require.NoError(t, db.Where("operation_id = ?", other.ID).Delete(&FleetOperationEvent{}).Error)
		require.NoError(t, db.Where("operation_id = ?", other.ID).Delete(&FleetOperationLine{}).Error)
		require.NoError(t, db.Unscoped().Delete(&other).Error)
	})
	otherLines := append([]FleetOperationLine(nil), p.lines...)
	for i := range otherLines {
		otherLines[i].ID, otherLines[i].OperationId = 0, int64(other.ID)
	}
	require.NoError(t, db.Create(&otherLines).Error)
	notes := []FleetOperationEvent{
		{OperationId: int64(p.op.ID), Kind: "RESOLUTION", At: time.Now(), Note: sql.NullString{String: "first shared note", Valid: true}},
		{OperationId: int64(other.ID), Kind: "RESOLUTION", At: time.Now(), Note: sql.NullString{String: "second shared note", Valid: true}},
	}
	require.NoError(t, db.Create(&notes).Error)
	for _, note := range notes {
		require.NoError(t, db.Model(&FleetOperationLine{}).Where("operation_id = ?", note.OperationId).
			Updates(map[string]any{"resolution_id": note.ID, "posting": "POSTED", "settlement": "RESOLVED_CONFIRMED"}).Error)
	}
	// Move the lower event behind the higher one in heap order.
	require.NoError(t, db.Model(&notes[0]).Update("note", "move first event to heap tail").Error)
	held := db.Begin()
	require.NoError(t, held.Error)
	defer held.Rollback()
	var low FleetOperationEvent
	require.NoError(t, held.Clauses(clause.Locking{Strength: "UPDATE"}).First(&low, notes[0].ID).Error)
	var holder int
	require.NoError(t, held.Raw("SELECT pg_backend_pid()").Scan(&holder).Error)

	type deletionRole struct{}
	pids := []chan int{make(chan int, 1), make(chan int, 1)}
	name := "test:shared-event-deletions"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		role, ok := tx.Statement.Context.Value(deletionRole{}).(int)
		if !ok || tx.Error != nil || tx.Statement.Schema == nil || tx.Statement.Schema.Name != "FleetOperationLine" || tx.RowsAffected == 0 {
			return
		}
		connection := tx.Session(&gorm.Session{NewDB: true})
		var pid int
		if err := connection.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
			tx.AddError(err)
			return
		}
		if role == 0 {
			tx.AddError(connection.Exec("SET LOCAL enable_indexscan = off").Error)
		} else {
			tx.AddError(connection.Exec("SET LOCAL enable_seqscan = off").Error)
		}
		tx.AddError(connection.Exec("SET LOCAL enable_bitmapscan = off").Error)
		pids[role] <- pid
	}))
	defer func() { _ = db.Callback().Query().Remove(name) }()
	type deletionResult struct {
		deleted bool
		err     error
	}
	results := make(chan deletionResult, 2)
	var waiters []int
	for i := range p.devs {
		go func(i int) {
			deleted, err := p.api.DeleteDevice(context.WithValue(p.ctx, deletionRole{}, i), p.devs[i].Token)
			results <- deletionResult{deleted, err}
		}(i)
		waiter := deleteRacePID(t, p.ctx, pids[i])
		blocker := holder
		if i != 0 {
			// PostgreSQL may queue the second waiter behind the first one's
			// tuple lock rather than directly behind the external transaction.
			blocker = waiters[0]
		}
		require.Eventually(t, func() bool {
			var blocked bool
			err := db.Raw("SELECT ? = ANY(pg_blocking_pids(?)) OR ? = ANY(pg_blocking_pids(?))", holder, waiter, blocker, waiter).Scan(&blocked).Error
			return err == nil && blocked
		}, 5*time.Second, 10*time.Millisecond)
		waiters = append(waiters, waiter)
	}
	probe := db.Begin()
	require.NoError(t, probe.Error)
	defer probe.Rollback()
	var high FleetOperationEvent
	err := probe.Clauses(clause.Locking{Strength: "UPDATE", Options: "NOWAIT"}).First(&high, notes[1].ID).Error
	require.NoError(t, probe.Rollback().Error)
	require.NoError(t, held.Commit().Error)
	for range p.devs {
		select {
		case result := <-results:
			require.NoError(t, result.err)
			require.True(t, result.deleted)
		case <-p.ctx.Done():
			t.Fatal(p.ctx.Err())
		}
	}
	require.NoError(t, err, "deletions waiting for the first shared event must not hold the second")
	var erased []FleetOperationLine
	require.NoError(t, db.Where("operation_id IN ?", []uint{p.op.ID, other.ID}).Find(&erased).Error)
	require.Len(t, erased, 4)
	for _, line := range erased {
		require.True(t, line.RedactedAt.Valid)
	}
	var redacted []FleetOperationEvent
	require.NoError(t, db.Where("id IN ?", []uint{notes[0].ID, notes[1].ID}).Find(&redacted).Error)
	require.Len(t, redacted, 2)
	for _, note := range redacted {
		require.Empty(t, note.Note.String)
		require.True(t, note.NoteRedactedAt.Valid)
	}
}

// Hold the lowest ID, then prove a bulk writer waits without owning the next
// line. Reverse heap order and disable index scans so an unordered bulk UPDATE
// or DELETE cannot accidentally pass by choosing the primary-key index.
func TestFleetBulkLineLockOrderOnPostgres(t *testing.T) {
	for _, path := range []string{"erase", "abort", "purge"} {
		t.Run(path, func(t *testing.T) {
			p := newPostPG(t, "v1")
			// A running operation left behind counts against the instance-wide
			// dispatching budget every later test in this database starts under.
			cleanupFleetMaintenancePost(t, p)
			db := p.api.RDB.DB(p.ctx)
			low, high := p.lines[0], p.lines[1]
			require.Less(t, low.ID, high.ID)
			if path == "erase" {
				// The same target in two operations: erasure spans operations.
				other := *p.op
				other.ID, other.Token = 0, "erase-other"
				require.NoError(t, db.Create(&other).Error)
				t.Cleanup(func() {
					// A hand-made copy with no runtime row cannot be aborted; remove it.
					require.NoError(t, db.Where("operation_id = ?", other.ID).Delete(&FleetOperationEvent{}).Error)
					require.NoError(t, db.Where("operation_id = ?", other.ID).Delete(&FleetOperationLine{}).Error)
					require.NoError(t, db.Unscoped().Delete(&other).Error)
				})
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
