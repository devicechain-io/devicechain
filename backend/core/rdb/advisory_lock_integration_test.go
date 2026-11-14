// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package rdb

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Each handle opens its own pool. Keep idle sessions alive: otherwise closing a
// leaked lock's session would hide a missing pg_advisory_unlock.
func advisoryPool(t *testing.T) *gorm.DB {
	t.Helper()
	host, port := itServer(t)
	dsn := postgresKeywordDSN("postgres", "postgres", host, int32(port), "postgres", "disable",
		map[string]string{"connect_timeout": "5"})
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(4)
	pool.SetMaxIdleConns(4)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	return db
}

func advisoryContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// Bigint advisory keys are exposed as two unsigned halves in pg_locks.
func queryAdvisoryRows(db *gorm.DB, key int64, granted bool) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var n int64
	err := db.WithContext(ctx).Raw(`SELECT count(*) FROM pg_locks
		WHERE locktype = 'advisory' AND objsubid = 1 AND granted = ?
		AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
		AND classid::bigint = ((?::bigint >> 32) & 4294967295)
		AND objid::bigint = (?::bigint & 4294967295)`, granted, key, key).Scan(&n).Error
	return n, err
}

func advisoryRows(t *testing.T, db *gorm.DB, key int64, granted bool) int64 {
	t.Helper()
	n, err := queryAdvisoryRows(db, key, granted)
	require.NoError(t, err)
	return n
}

func advisoryWait(t *testing.T, db *gorm.DB, key int64, granted bool, want int64) {
	t.Helper()
	// Keep running after a missing waiter so the migration negative control also
	// reports the competing startup's non-idempotent INSERT failure.
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		n, err := queryAdvisoryRows(db, key, granted)
		require.NoError(c, err)
		require.Equal(c, want, n)
	}, 5*time.Second, 10*time.Millisecond, "unexpected pg_locks rows for key %d, granted=%v", key, granted)
}

type advisoryResult struct {
	ran bool
	err error
}

// Join workers before their pools and schema are cleaned up, including when an
// assertion aborts the test. The holder's release is deferred by each caller.
func advisoryRun(t *testing.T, fn func() advisoryResult) <-chan advisoryResult {
	t.Helper()
	done := make(chan advisoryResult, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		done <- fn()
	}()
	t.Cleanup(func() {
		select {
		case <-finished:
		case <-time.After(20 * time.Second):
			t.Error("advisory worker did not stop before cleanup")
		}
	})
	return done
}

func advisoryDone(t *testing.T, done <-chan advisoryResult) advisoryResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("advisory operation did not finish promptly")
		return advisoryResult{}
	}
}

func advisoryEntered(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("holder did not enter callback")
	}
}

func TestTryAdvisoryLockPostgresContention(t *testing.T) {
	a := &RdbManager{Database: advisoryPool(t)}
	b := &RdbManager{Database: advisoryPool(t)}
	observer := advisoryPool(t)
	ctx := advisoryContext(t)
	key := AdvisoryLockKey(fmt.Sprintf("%s/%d", t.Name(), time.Now().UnixNano()))
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	done := advisoryRun(t, func() advisoryResult {
		ran, err := a.TryAdvisoryLock(ctx, key, func() error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		return advisoryResult{ran, err}
	})
	advisoryEntered(t, entered)
	require.EqualValues(t, 1, advisoryRows(t, observer, key, true))
	// A deadline bounds a mutant that accidentally uses the blocking acquire.
	peerCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	called := false
	start := time.Now()
	ran, err := b.TryAdvisoryLock(peerCtx, key, func() error { called = true; return nil })
	require.NoError(t, err)
	require.False(t, ran)
	require.False(t, called)
	require.Less(t, time.Since(start), time.Second)
	once.Do(func() { close(release) })
	result := advisoryDone(t, done)
	require.True(t, result.ran)
	require.NoError(t, result.err)
	require.Zero(t, advisoryRows(t, observer, key, true))
	ran, err = b.TryAdvisoryLock(ctx, key, func() error { called = true; return nil })
	require.NoError(t, err)
	require.True(t, ran)
	require.True(t, called)
}

func TestTryAdvisoryLockPostgresReleasesOnCancelAndError(t *testing.T) {
	for _, cancelInside := range []bool{true, false} {
		t.Run(fmt.Sprintf("cancel=%v", cancelInside), func(t *testing.T) {
			a := &RdbManager{Database: advisoryPool(t)}
			b := &RdbManager{Database: advisoryPool(t)}
			observer := advisoryPool(t)
			ctx, cancel := context.WithCancel(advisoryContext(t))
			defer cancel()
			key := AdvisoryLockKey(fmt.Sprintf("%s/%d", t.Name(), time.Now().UnixNano()))
			want := errors.New("callback failed")
			ran, err := a.TryAdvisoryLock(ctx, key, func() error {
				if cancelInside {
					cancel()
					return ctx.Err()
				}
				return want
			})
			require.True(t, ran)
			if cancelInside {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.ErrorIs(t, err, want)
			}
			require.Zero(t, advisoryRows(t, observer, key, true), "lock leaked into the idle pool")
			called := false
			ran, err = b.TryAdvisoryLock(advisoryContext(t), key, func() error { called = true; return nil })
			require.NoError(t, err)
			require.True(t, ran)
			require.True(t, called)
		})
	}
}

func TestWithAdvisoryLockPostgresReleasesOnCancelAndError(t *testing.T) {
	for _, cancelInside := range []bool{true, false} {
		t.Run(fmt.Sprintf("cancel=%v", cancelInside), func(t *testing.T) {
			a := &RdbManager{Database: advisoryPool(t)}
			b := &RdbManager{Database: advisoryPool(t)}
			observer := advisoryPool(t)
			ctx, cancel := context.WithCancel(advisoryContext(t))
			defer cancel()
			key := AdvisoryLockKey(fmt.Sprintf("%s/%d", t.Name(), time.Now().UnixNano()))
			want := errors.New("callback failed")
			err := a.WithAdvisoryLock(ctx, key, func() error {
				if cancelInside {
					cancel()
					return ctx.Err()
				}
				return want
			})
			if cancelInside {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.ErrorIs(t, err, want)
			}
			require.Zero(t, advisoryRows(t, observer, key, true))
			called := false
			err = b.WithAdvisoryLock(advisoryContext(t), key, func() error { called = true; return nil })
			require.NoError(t, err)
			require.True(t, called)
		})
	}
}

// Use a fresh, empty area schema, pre-created to keep schema creation (which
// precedes the startup lock) out of the race. ExecuteInitialize still runs all
// core-owned DDL and gormigrate bookkeeping on the production startup path.
func advisoryStartup(t *testing.T, migration func(*gorm.DB) error) (*RdbManager, *RdbManager, *gorm.DB, int64, string) {
	t.Helper()
	observer := advisoryPool(t)
	area := fmt.Sprintf("advisory_%d", time.Now().UnixNano())
	require.NoError(t, observer.Exec(fmt.Sprintf(`CREATE SCHEMA %q`, area)).Error)
	t.Cleanup(func() { require.NoError(t, observer.Exec(fmt.Sprintf(`DROP SCHEMA %q CASCADE`, area)).Error) })
	host, port := itServer(t)
	makeManager := func() *RdbManager {
		mgr := &RdbManager{
			Microservice: &core.Microservice{InstanceId: "postgres", FunctionalArea: area},
			InstanceConfig: config.DatastoreConfiguration{Type: "postgres", Configuration: map[string]interface{}{
				"hostname": host, "port": port, "username": "postgres", "password": "postgres", "sslMode": "disable",
			}},
			Migrations: []*gormigrate.Migration{{ID: "20261114000000", Migrate: migration}},
		}
		t.Cleanup(func() {
			if mgr.Database != nil {
				pool, err := mgr.Database.DB()
				require.NoError(t, err)
				require.NoError(t, pool.Close())
			}
		})
		return mgr
	}
	return makeManager(), makeManager(), observer, AdvisoryLockKey(MigrationTableName(area)), area
}

func TestMigrationStartupAdvisoryLockPostgresContention(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	ctx := advisoryContext(t)
	a, b, observer, key, area := advisoryStartup(t, func(tx *gorm.DB) error {
		enteredOnce.Do(func() { close(entered) })
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		if err := tx.Exec("SELECT pg_sleep(1)").Error; err != nil {
			return err
		}
		return tx.Exec("INSERT INTO startup_counter (id) VALUES (1)").Error
	})
	require.NoError(t, observer.Exec(fmt.Sprintf(`CREATE TABLE %q.startup_counter (id integer PRIMARY KEY)`, area)).Error)
	first := advisoryRun(t, func() advisoryResult { return advisoryResult{err: a.ExecuteInitialize(ctx)} })
	advisoryEntered(t, entered)
	second := advisoryRun(t, func() advisoryResult { return advisoryResult{err: b.ExecuteInitialize(ctx)} })
	advisoryWait(t, observer, key, false, 1)
	require.EqualValues(t, 1, advisoryRows(t, observer, key, true))
	releaseOnce.Do(func() { close(release) })
	require.NoError(t, advisoryDone(t, first).err)
	require.NoError(t, advisoryDone(t, second).err)
	var n int64
	require.NoError(t, observer.Raw(fmt.Sprintf(`SELECT count(*) FROM %q.startup_counter`, area)).Scan(&n).Error)
	require.EqualValues(t, 1, n)
	require.Zero(t, advisoryRows(t, observer, key, true))
}

func TestMigrationStartupAdvisoryLockPostgresCancelledWaiter(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	ctx := advisoryContext(t)
	a, b, observer, key, _ := advisoryStartup(t, func(tx *gorm.DB) error {
		enteredOnce.Do(func() { close(entered) })
		select {
		case <-release:
			return tx.Exec("SELECT pg_sleep(1)").Error
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	first := advisoryRun(t, func() advisoryResult { return advisoryResult{err: a.ExecuteInitialize(ctx)} })
	advisoryEntered(t, entered)
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	second := advisoryRun(t, func() advisoryResult { return advisoryResult{err: b.ExecuteInitialize(waitCtx)} })
	advisoryWait(t, observer, key, false, 1)
	cancel()
	start := time.Now()
	require.Error(t, advisoryDone(t, second).err)
	require.Less(t, time.Since(start), time.Second)
	require.EqualValues(t, 1, advisoryRows(t, observer, key, true), "cancelling a waiter must not disturb the holder")
	releaseOnce.Do(func() { close(release) })
	require.NoError(t, advisoryDone(t, first).err)
	advisoryWait(t, observer, key, false, 0)
	require.Zero(t, advisoryRows(t, observer, key, true))
}
