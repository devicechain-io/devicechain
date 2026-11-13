// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The inactivity sweep against a REAL PostgreSQL: it must never WAIT for a row lock. A
// transaction that waits for a lock while holding others can be in a wait-for cycle, and the
// sweep used to be one UPDATE over a sequential scan that locked rows in heap order and
// waited for any row a writer held, while holding every row it had already flipped. A batch
// locks its rows in token order, so the two closed a cycle (40P01 after deadlock_timeout).
//
// Run as projection_integration_test.go describes.
package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var sweepT0 = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

// seedStale creates active INFERRED devices whose last activity is sweepT0, in the order
// given (which is their heap order), under tenant.
func seedStale(t *testing.T, api *Api, tenant string, tokens ...string) {
	t.Helper()
	for _, tok := range tokens {
		ds := &DeviceState{TenantId: tenant, DeviceToken: tok, Active: true, PresenceSource: PresenceSourceInferred,
			LastActivityTime: sql.NullTime{Time: sweepT0, Valid: true}}
		if err := api.RDB.DB(core.WithTenant(context.Background(), tenant)).Create(ds).Error; err != nil {
			t.Fatalf("seed %s/%s: %v", tenant, tok, err)
		}
	}
}

func sweepAt() time.Time { return sweepT0.Add(2 * time.Hour) }

func activeOf(t *testing.T, api *Api, tenant, token string) bool {
	t.Helper()
	var ds DeviceState
	if err := api.RDB.DB(core.WithTenant(context.Background(), tenant)).Where("device_token = ?", token).First(&ds).Error; err != nil {
		t.Fatalf("load %s/%s: %v", tenant, token, err)
	}
	return ds.Active
}

// lockRow holds a row lock on tenant/token in an open transaction until the returned
// function is called.
func lockRow(t *testing.T, api *Api, tenant, token string) (release func(commit bool)) {
	t.Helper()
	tx := api.RDB.DB(core.WithTenant(context.Background(), tenant)).Begin()
	if tx.Error != nil {
		t.Fatalf("begin: %v", tx.Error)
	}
	var rows []DeviceState
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("device_token = ?", token).Find(&rows).Error; err != nil || len(rows) != 1 {
		t.Fatalf("lock %s/%s: %d rows, %v", tenant, token, len(rows), err)
	}
	return func(commit bool) {
		if commit {
			if err := tx.Commit().Error; err != nil {
				t.Errorf("commit: %v", err)
			}
		} else {
			tx.Rollback()
		}
	}
}

// deadlocksOf is the server's deadlock count for this database, after the statistics have
// had a moment to be flushed.
func deadlocksOf(t *testing.T, api *Api) int64 {
	t.Helper()
	time.Sleep(1500 * time.Millisecond)
	sqldb, err := api.RDB.Database.DB()
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	var n int64
	if err := sqldb.QueryRow(`SELECT deadlocks FROM pg_stat_database WHERE datname = current_database()`).Scan(&n); err != nil {
		t.Fatalf("read deadlocks: %v", err)
	}
	return n
}

// A sweep over a row a writer holds does not wait for it: it flips the rows it can lock and
// leaves the held one for the next pass.
func TestTheSweepDoesNotWaitForAHeldRow(t *testing.T) {
	api := newPostgresProjectionApi(t, "nowait")
	seedStale(t, api, "sw", "a", "b")
	release := lockRow(t, api, "sw", "b")

	ctx, cancel := context.WithTimeout(core.WithSystemContext(context.Background()), 3*time.Second)
	defer cancel()
	start := time.Now()
	n, err := api.SweepInactive(ctx, sweepAt())
	if err != nil || n != 1 {
		release(false)
		t.Fatalf("sweep = %d, %v after %v; want 1, nil without waiting for the held row", n, describeDBError(err), time.Since(start))
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("the sweep took %v with a row held; it must not wait", d)
	}
	if activeOf(t, api, "sw", "a") {
		t.Error("the unheld stale device was not flipped")
	}
	release(true)
	if !activeOf(t, api, "sw", "b") {
		t.Error("the held device was flipped though the writer held it")
	}
	if n, err := api.SweepInactive(core.WithSystemContext(context.Background()), sweepAt()); err != nil || n != 1 {
		t.Errorf("the next sweep = %d, %v; want 1, nil (the row left behind)", n, describeDBError(err))
	}
	if activeOf(t, api, "sw", "b") {
		t.Error("the left-behind device was not flipped by the next pass")
	}
}

// THE CYCLE ITSELF. The rows are stored y then x (the sweep's scan order) and sort x then y
// (a batch's lock order). A batch holds x; the sweep, were it to wait, would hold y and wait
// for x; the batch then wants y.
func TestTheSweepAndABatchCannotDeadlock(t *testing.T) {
	api := newPostgresProjectionApi(t, "cycle")
	seedStale(t, api, "dl", "dl-y", "dl-x")
	before := deadlocksOf(t, api)

	// The batch's first lock: x.
	batch := api.RDB.DB(core.WithTenant(context.Background(), "dl")).Begin()
	if err := batch.Model(&DeviceState{}).Where("device_token = ?", "dl-x").Update("session_id", 1).Error; err != nil {
		t.Fatalf("lock x: %v", err)
	}

	var sweepErr error
	var swept int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		swept, sweepErr = api.SweepInactive(core.WithSystemContext(context.Background()), sweepAt())
	}()
	// Wait until the sweep is waiting on a lock, or has finished (after the fix it never waits).
	sqldb, _ := api.RDB.Database.DB()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		var waiting int
		_ = sqldb.QueryRow(`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting)
		select {
		case <-done:
			waiting = 1
		default:
		}
		if waiting > 0 {
			break
		}
	}
	// The batch's second lock: y.
	batchErr := batch.Model(&DeviceState{}).Where("device_token = ?", "dl-y").Update("session_id", 1).Error
	if batchErr == nil {
		batchErr = batch.Commit().Error
	} else {
		batch.Rollback()
	}
	<-done
	if sweepErr != nil {
		t.Errorf("the sweep failed: %v", describeDBError(sweepErr))
	}
	if batchErr != nil {
		t.Errorf("the batch failed: %v", describeDBError(batchErr))
	}
	if d := deadlocksOf(t, api) - before; d != 0 {
		t.Errorf("the server counted %d deadlocks; want 0 (swept %d)", d, swept)
	}
	// The positive control: "no deadlock" is trivially true of a sweep that did nothing. The
	// sweep skipped the row the batch held (x) and flipped the one it did not (y).
	if swept != 1 {
		t.Errorf("the sweep flipped %d rows; want exactly 1 (y, with x held by the batch)", swept)
	}
	if activeOf(t, api, "dl", "dl-y") {
		t.Error("the unheld stale device (y) was not flipped")
	}
	if !activeOf(t, api, "dl", "dl-x") {
		t.Error("the device the batch held (x) was flipped under it")
	}
	// And x was left for the next pass, not lost.
	if n, err := api.SweepInactive(core.WithSystemContext(context.Background()), sweepAt()); err != nil || n != 1 {
		t.Errorf("the next sweep = %d, %v; want 1, nil (x, left behind)", n, describeDBError(err))
	}
	if activeOf(t, api, "dl", "dl-x") {
		t.Error("the left-behind device (x) was not flipped by the next pass")
	}
}

// A deliberate lock cycle proves that the server's deadlock diagnostics survive the driver.
func TestADeadlockErrorCarriesTheServersDetail(t *testing.T) {
	api := newPostgresProjectionApi(t, "dldetail")
	seedStale(t, api, "dl", "dl-x", "dl-y")
	ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), "dl"), 10*time.Second)
	defer cancel()
	tx1 := api.RDB.DB(ctx).Begin()
	if tx1.Error != nil {
		t.Fatalf("begin tx1: %s", describeDBError(tx1.Error))
	}
	defer tx1.Rollback()
	tx2 := api.RDB.DB(ctx).Begin()
	if tx2.Error != nil {
		t.Fatalf("begin tx2: %s", describeDBError(tx2.Error))
	}
	defer tx2.Rollback()
	if err := tx1.Model(&DeviceState{}).Where("device_token = ?", "dl-x").Update("session_id", 1).Error; err != nil {
		t.Fatalf("lock x in tx1: %s", describeDBError(err))
	}
	if err := tx2.Model(&DeviceState{}).Where("device_token = ?", "dl-y").Update("session_id", 1).Error; err != nil {
		t.Fatalf("lock y in tx2: %s", describeDBError(err))
	}

	results := make(chan error, 2)
	update := func(tx *gorm.DB, token string) {
		err := tx.Model(&DeviceState{}).Where("device_token = ?", token).Update("session_id", 2).Error
		// Release the winner's locks too, so both statements can finish without a commit.
		tx.Rollback()
		results <- err
	}
	go update(tx1, "dl-y")
	go update(tx2, "dl-x")
	var losingErr error
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				if losingErr != nil {
					t.Fatalf("both statements failed: %s\n%s", describeDBError(losingErr), describeDBError(err))
				}
				losingErr = err
			}
		case <-ctx.Done():
			t.Fatalf("waiting for the deadlock statements: %v", ctx.Err())
		}
	}
	var pgErr *pgconn.PgError
	if !errors.As(losingErr, &pgErr) || pgErr.Code != "40P01" {
		t.Fatalf("losing statement: %s; want PostgreSQL deadlock (40P01)", describeDBError(losingErr))
	}
	if description := describeDBError(losingErr); !strings.Contains(description, "blocked by process") {
		t.Errorf("deadlock description lacks the server's detail: %s", description)
	}
	if strings.Contains(losingErr.Error(), "blocked by process") {
		t.Errorf("err.Error() unexpectedly includes the server's detail: %s", losingErr)
	}
}

// Two sweeps and several batches over one set of stale rows, for a few seconds: nothing
// errors and the server counts no deadlock. Two sweeps that took their candidates with a
// shared lock and then upgraded them would deadlock each other here.
func TestTwoSweepsAndBatchesDoNotDeadlock(t *testing.T) {
	api := newPostgresProjectionApi(t, "twosweeps")
	const devices = 200
	tokens := make([]string, devices)
	for i := range tokens {
		tokens[i] = fmt.Sprintf("tw-%03d", i)
	}
	seedStale(t, api, "tw", tokens...)
	before := deadlocksOf(t, api)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var sweepErrs, batchErrs, swept atomic.Int64
	var firstErr error
	var firstErrOnce sync.Once
	for s := 0; s < 2; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				n, err := api.SweepInactive(core.WithSystemContext(context.Background()), sweepAt())
				if err != nil {
					sweepErrs.Add(1)
					firstErrOnce.Do(func() { firstErr = err })
				}
				swept.Add(n)
			}
		}()
	}
	for w := 0; w < 4; w++ {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := 0; ; round++ {
				select {
				case <-stop:
					return
				default:
				}
				// 24 devices, in an order that differs per writer, as batches see them.
				updates := make([]ProjectionUpdate, 0, 24)
				for i := 0; i < 24; i++ {
					tok := tokens[(w*37+round*11+i*7)%devices]
					updates = append(updates, ProjectionUpdate{Tenant: "tw", DeviceToken: tok,
						OccurredAt: sweepT0.Add(time.Duration(round+1) * time.Second)})
				}
				if err := api.MergeProjectionBatch(context.Background(), updates); err != nil {
					batchErrs.Add(1)
					firstErrOnce.Do(func() { firstErr = err })
				}
			}
		}()
	}
	time.Sleep(4 * time.Second)
	close(stop)
	wg.Wait()
	if sweepErrs.Load() != 0 || batchErrs.Load() != 0 {
		t.Errorf("%d sweeps and %d batches failed; first: %v", sweepErrs.Load(), batchErrs.Load(), describeDBError(firstErr))
	}
	if d := deadlocksOf(t, api) - before; d != 0 {
		t.Errorf("the server counted %d deadlocks; want 0", d)
	}
	// The positive control: "no deadlock" is trivially true if no work happened. The sweeps
	// must have flipped rows while the batches ran, and once the contention stops one more pass
	// must leave no stale device active (the batches reactivate devices, with an old activity
	// time, so all of them are stale again).
	if swept.Load() == 0 {
		t.Error("the sweeps flipped no row in four seconds over stale devices: they did no work")
	}
	if _, err := api.SweepInactive(core.WithSystemContext(context.Background()), sweepAt()); err != nil {
		t.Fatalf("final sweep: %v", describeDBError(err))
	}
	var stillActive int64
	if err := api.RDB.DB(core.WithTenant(context.Background(), "tw")).Model(&DeviceState{}).
		Where("active = ?", true).Count(&stillActive).Error; err != nil {
		t.Fatalf("count active: %v", err)
	}
	if stillActive != 0 {
		t.Errorf("%d stale devices are still active after an uncontended sweep", stillActive)
	}
}

// The sweep flips by ROW, not by device token: two tenants hold the same token, one stale
// and one fresh, and only the stale one flips. Run in both creation orders (the stale row
// first or last in the heap) and with the planner's nested loop switched off, so neither the
// plan nor the scan order can hide a sweep that matches by token. (With a nested loop the
// locking subquery used to be re-run per outer row, and a row the first run had already
// updated was skipped by the next as self-modified: the pass then flipped too few rows.)
func TestTheSweepFlipsByRowNotByToken(t *testing.T) {
	for _, tc := range []struct {
		name         string
		staleFirst   bool
		noNestedLoop bool
	}{
		{"stale row first", true, false},
		{"fresh row first", false, false},
		{"stale row first, no nested loop", true, true},
		{"fresh row first, no nested loop", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newPostgresProjectionApi(t, "byrow")
			seedFresh := func() {
				fresh := &DeviceState{TenantId: "tenant-b", DeviceToken: "same", Active: true, PresenceSource: PresenceSourceInferred,
					LastActivityTime: sql.NullTime{Time: sweepAt(), Valid: true}}
				if err := api.RDB.DB(core.WithTenant(context.Background(), "tenant-b")).Create(fresh).Error; err != nil {
					t.Fatalf("seed: %v", err)
				}
			}
			if tc.staleFirst {
				seedStale(t, api, "tenant-a", "same")
				seedFresh()
			} else {
				seedFresh()
				seedStale(t, api, "tenant-a", "same")
			}
			ctx := core.WithSystemContext(context.Background())
			if tc.noNestedLoop {
				// One connection for the whole pool, so the setting applies to the sweep.
				sqldb, _ := api.RDB.Database.DB()
				sqldb.SetMaxOpenConns(1)
				if _, err := sqldb.Exec(`SET enable_nestloop = off`); err != nil {
					t.Fatalf("set: %v", err)
				}
			}
			n, err := api.SweepInactive(ctx, sweepAt())
			if err != nil || n != 1 {
				t.Fatalf("sweep = %d, %v; want 1, nil", n, describeDBError(err))
			}
			if activeOf(t, api, "tenant-a", "same") {
				t.Error("the stale device was not flipped")
			}
			if !activeOf(t, api, "tenant-b", "same") {
				t.Error("the fresh device of another tenant with the same token was flipped")
			}
		})
	}
}
