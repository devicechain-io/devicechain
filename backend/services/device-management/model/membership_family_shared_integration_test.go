// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type membershipDeviceWriterKey struct{}
type membershipDeviceWriterPID struct{ writer, pid int }

// Both holders must have a GRANTED ShareLock on precisely the same family key.
// Checking goroutine completion alone would miss an accidental exclusive lock.
func TestMembershipDifferentDeviceWritersShareFamilyPostgres(t *testing.T) {
	for _, create := range []bool{false, true} {
		name := "attributes"
		if create {
			name = "creation"
		}
		t.Run(name, func(t *testing.T) {
			api, ctx := membershipPostgres(t)
			membershipRaceEntity(t, api, ctx, "device", "first")
			membershipRaceEntity(t, api, ctx, "device", "second")
			g, version := membershipRaceGroup(t, api, ctx, "device")
			ready := make(chan membershipDeviceWriterPID, 2)
			releases := []chan struct{}{make(chan struct{}), make(chan struct{})}
			var releaseOnce [2]sync.Once
			var parked [2]sync.Once
			unblock := func(i int) { releaseOnce[i].Do(func() { close(releases[i]) }) }
			require.NoError(t, api.RDB.Database.Callback().Raw().After("gorm:raw").Register("test:shared-family-holders", func(db *gorm.DB) {
				writer, ok := db.Statement.Context.Value(membershipDeviceWriterKey{}).(int)
				if !ok || db.Error != nil || !strings.Contains(db.Statement.SQL.String(), "pg_advisory_xact_lock") {
					return
				}
				parked[writer].Do(func() {
					var pid int
					if err := db.Session(&gorm.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
						db.AddError(err)
						return
					}
					ready <- membershipDeviceWriterPID{writer: writer, pid: pid}
					select {
					case <-releases[writer]:
					case <-ctx.Done():
						db.AddError(ctx.Err())
					}
				})
			}))
			t.Cleanup(func() { api.RDB.Database.Callback().Raw().Remove("test:shared-family-holders") })
			var workers sync.WaitGroup
			defer func() { unblock(0); unblock(1); workers.Wait() }()
			done := []chan error{make(chan error, 1), make(chan error, 1)}
			for i, token := range []string{"first", "second"} {
				workers.Add(1)
				go func(i int, token string) {
					defer workers.Done()
					writerCtx := context.WithValue(ctx, membershipDeviceWriterKey{}, i)
					if create {
						_, err := api.CreateDevice(writerCtx, &DeviceCreateRequest{Token: token + "-new", DeviceTypeToken: token + "-type"})
						done[i] <- err
					} else {
						done[i] <- membershipRaceWrite(api, writerCtx, "device", token, "a", "x")
					}
				}(i, token)
			}
			var pids [2]int
			for range 2 {
				select {
				case holder := <-ready:
					pids[holder.writer] = holder.pid
				case err := <-done[0]:
					t.Fatalf("first writer ended before overlap: %v", err)
				case err := <-done[1]:
					t.Fatalf("second writer ended before overlap: %v", err)
				case <-ctx.Done():
					t.Fatal("both device writers must acquire the family lock concurrently: ", ctx.Err())
				}
			}
			require.NotEqual(t, pids[0], pids[1])
			var shared int64
			require.NoError(t, api.RDB.Database.WithContext(ctx).Raw(`SELECT count(*) FROM pg_locks a JOIN pg_locks b
				ON a.locktype = b.locktype AND a.database = b.database
				AND a.classid = b.classid AND a.objid = b.objid AND a.objsubid = b.objsubid
				WHERE a.pid = ? AND b.pid = ? AND a.locktype = 'advisory'
				AND a.mode = 'ShareLock' AND b.mode = 'ShareLock' AND a.granted AND b.granted`, pids[0], pids[1]).Scan(&shared).Error)
			require.EqualValues(t, 1, shared, "both writers must hold the same granted shared family lock")
			registered := make(chan error, 1)
			workers.Add(1)
			go func() {
				defer workers.Done()
				registered <- api.RegisterGroupVersionForScoping(ctx, g.Token, version)
			}()
			waitOn := func(pid int) {
				t.Helper()
				require.Eventually(t, func() bool {
					n, err := membershipWaiters(api.RDB.Database.WithContext(ctx), pid)
					return err == nil && n == 1
				}, 10*time.Second, 10*time.Millisecond, "Register must wait for each shared holder")
			}
			waitOn(pids[0])
			unblock(0)
			membershipWriterDone(t, ctx, done[0])
			waitOn(pids[1])
			select {
			case err := <-registered:
				t.Fatalf("Register passed the remaining holder: %v", err)
			default:
			}
			unblock(1)
			membershipWriterDone(t, ctx, done[1])
			membershipWriterDone(t, ctx, registered)
		})
	}
}

func TestMembershipRegisterBlocksBothDeviceWritersPostgres(t *testing.T) {
	api, ctx := membershipPostgres(t)
	membershipRaceEntity(t, api, ctx, "device", "first")
	membershipRaceEntity(t, api, ctx, "device", "second")
	g, version := membershipRaceGroup(t, api, ctx, "device")
	ready, unblock := parkMembershipHolder(t, api, true)
	var workers sync.WaitGroup
	defer func() { unblock(); workers.Wait() }()
	registered := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		registered <- api.RegisterGroupVersionForScoping(context.WithValue(ctx, membershipRaceHolder{}, true), g.Token, version)
	}()
	pid := membershipHolderPID(t, ctx, ready, registered)
	done := make(chan error, 2)
	for _, token := range []string{"first", "second"} {
		workers.Add(1)
		go func(token string) {
			defer workers.Done()
			done <- membershipRaceWrite(api, ctx, "device", token, "a", "x")
		}(token)
	}
	require.Eventually(t, func() bool {
		n, err := membershipWaiters(api.RDB.Database.WithContext(ctx), pid)
		return err == nil && n == 2
	}, 10*time.Second, 10*time.Millisecond, "Register's exclusive family lock must block both device writers")
	unblock()
	membershipWriterDone(t, ctx, registered)
	membershipWriterDone(t, ctx, done)
	membershipWriterDone(t, ctx, done)
}

func TestMembershipSharedRejectsRolledBackDeviceLockPostgres(t *testing.T) {
	api, ctx := membershipPostgres(t)
	id := membershipRaceEntity(t, api, ctx, "device", "member")
	require.NoError(t, api.runDeviceAttributeTx(ctx, "set", func(tx *gorm.DB) error {
		require.NoError(t, tx.SavePoint("before_owner").Error)
		require.NoError(t, lockDeviceAttributes(ctx, tx, id))
		require.NoError(t, tx.RollbackTo("before_owner").Error)
		// The Go owner state survived, but the database lock did not.
		err := api.lockMembershipWriterFamily(ctx, tx, "device", id)
		require.ErrorContains(t, err, "requires K_dev")
		require.NoError(t, lockDeviceAttributes(ctx, tx, id))
		return api.lockMembershipWriterFamily(ctx, tx, "device", id)
	}))
}

// The ownership assertion runs on every eligible device write, the path shared mode exists to
// speed up. pg_locks copies the server's whole lock table under every lock-partition lock, so
// the assertion must read the transaction-local owner setting instead. The shared-lock count
// keeps this from passing vacuously (a write that never asserted would also read no pg_locks).
func TestMembershipSharedOwnerCheckDoesNotScanPgLocksPostgres(t *testing.T) {
	api, ctx := membershipPostgres(t)
	membershipRaceEntity(t, api, ctx, "device", "member")
	type writerKey struct{}
	var mu sync.Mutex
	scans, shared := 0, 0
	count := func(db *gorm.DB) {
		if db.Statement.Context.Value(writerKey{}) != true {
			return
		}
		sql := db.Statement.SQL.String()
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(sql, "pg_locks") {
			scans++
		}
		if strings.Contains(sql, "pg_advisory_xact_lock_shared(") {
			shared++
		}
	}
	require.NoError(t, api.RDB.Database.Callback().Raw().After("gorm:raw").Register("test:owner-check-raw", count))
	require.NoError(t, api.RDB.Database.Callback().Row().After("gorm:row").Register("test:owner-check-row", count))
	t.Cleanup(func() {
		api.RDB.Database.Callback().Raw().Remove("test:owner-check-raw")
		api.RDB.Database.Callback().Row().Remove("test:owner-check-row")
	})
	writerCtx := context.WithValue(ctx, writerKey{}, true)
	require.NoError(t, membershipRaceWrite(api, writerCtx, "device", "member", "a", "x"))
	_, err := api.CreateDevice(writerCtx, &DeviceCreateRequest{Token: "created", DeviceTypeToken: "member-type"})
	require.NoError(t, err)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 2, shared, "the write and the create each take the family lock shared")
	require.Zero(t, scans, "the K_dev assertion must not read pg_locks")
}

// The counterweight to the rolled-back case: a savepoint rolled back AFTER K_dev was taken
// leaves both the lock and the owner setting, so the writer is not refused.
func TestMembershipSharedKeepsOwnerAcrossLaterSavepointPostgres(t *testing.T) {
	api, ctx := membershipPostgres(t)
	id := membershipRaceEntity(t, api, ctx, "device", "member")
	other := membershipRaceEntity(t, api, ctx, "device", "other")
	require.NoError(t, api.runDeviceAttributeTx(ctx, "set", func(tx *gorm.DB) error {
		require.NoError(t, lockDeviceAttributes(ctx, tx, id))
		require.NoError(t, tx.SavePoint("after_owner").Error)
		require.NoError(t, tx.RollbackTo("after_owner").Error)
		require.NoError(t, api.lockMembershipWriterFamily(ctx, tx, "device", id))
		// The setting names one device: it is no proof for another.
		var held bool
		require.NoError(t, tx.Raw("SELECT COALESCE(current_setting('"+deviceOwnerSetting+"', true), '') = CAST(CAST(? AS bigint) AS text)", other).Scan(&held).Error)
		require.False(t, held)
		return nil
	}))
	// The setting is transaction-local: the next transaction on a pooled connection starts without it.
	require.NoError(t, api.RDB.DB(ctx).Transaction(func(tx *gorm.DB) error {
		var held bool
		require.NoError(t, tx.Raw("SELECT COALESCE(current_setting('"+deviceOwnerSetting+"', true), '') = CAST(CAST(? AS bigint) AS text)", id).Scan(&held).Error)
		require.False(t, held)
		return nil
	}))
}

// A bulk create keeps one exclusive family lock and takes no per-device K_dev: shared mode
// would hold a K_dev per created device (up to MaxBulkDeviceCount in one transaction).
func TestMembershipBulkCreateKeepsOneExclusiveFamilyLockPostgres(t *testing.T) {
	api, ctx := membershipPostgres(t)
	_, err := api.CreateDeviceType(ctx, &DeviceTypeCreateRequest{Token: "bulk-type"})
	require.NoError(t, err)
	type bulkKey struct{}
	var mu sync.Mutex
	var devLocks, exclusive, shared int
	require.NoError(t, api.RDB.Database.Callback().Raw().After("gorm:raw").Register("test:bulk-create-locks", func(db *gorm.DB) {
		if db.Statement.Context.Value(bulkKey{}) != true {
			return
		}
		sql := db.Statement.SQL.String()
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.Contains(sql, "dc_entity_attr_lock"):
			devLocks++
		case strings.Contains(sql, "pg_advisory_xact_lock_shared("):
			shared++
		case strings.Contains(sql, "pg_advisory_xact_lock("):
			exclusive++
		}
	}))
	t.Cleanup(func() { api.RDB.Database.Callback().Raw().Remove("test:bulk-create-locks") })
	requests := make([]*DeviceCreateRequest, 0, 3)
	for i := range 3 {
		requests = append(requests, &DeviceCreateRequest{Token: fmt.Sprintf("bulk-%d", i), DeviceTypeToken: "bulk-type"})
	}
	created, err := api.CreateDevices(context.WithValue(ctx, bulkKey{}, true), requests)
	require.NoError(t, err)
	require.Len(t, created, 3)
	mu.Lock()
	defer mu.Unlock()
	require.Zero(t, devLocks, "a bulk create takes no per-device K_dev")
	require.Zero(t, shared)
	require.Equal(t, 1, exclusive, "one exclusive family lock for the whole batch")
}
