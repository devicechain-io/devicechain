// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A separate manager is a separate pool, as on another service replica. Reuse
// the migrated database without the fixture helper's table truncation.
func batchLockReplica(t *testing.T, base *Api) *Api {
	t.Helper()
	mgr := &rdb.RdbManager{
		Microservice: base.RDB.Microservice, Migrations: Migrations,
		InstanceConfig: base.RDB.InstanceConfig, MicroserviceConfig: base.RDB.MicroserviceConfig,
	}
	require.NoError(t, mgr.ExecuteInitialize(context.Background()))
	pool, err := mgr.Database.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	return NewApi(mgr)
}

func TestConcurrentBatchesNeverOvershootTheTenantLimitOnPostgres(t *testing.T) {
	for _, bypass := range []bool{false, true} {
		t.Run(map[bool]string{false: "guarded", true: "without-admission-lock-overshoots"}[bypass], func(t *testing.T) {
			const k, m, ceiling = 4, 4, 10
			base := newPostgresApi(t)
			ctx, cancel := context.WithTimeout(machineryCtx(core.WithTenant(context.Background(), "batch-race")), 15*time.Second)
			defer cancel()
			pgCommand(t, base, ctx, "existing-held", "existing-device", CommandHeld, time.Time{}, time.Time{}, "")
			replicas := []*Api{base}
			for i := 1; i < k; i++ {
				replicas = append(replicas, batchLockReplica(t, base))
			}
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			var counted atomic.Int32
			for _, api := range replicas {
				api.DefaultHeldCommandCeiling = ceiling
				db := api.RDB.Database
				if bypass {
					original := db.Callback().Raw().Get("gorm:raw")
					require.NoError(t, db.Callback().Raw().Replace("gorm:raw", func(tx *gorm.DB) {
						if strings.Contains(tx.Statement.SQL.String(), "pg_advisory_xact_lock") {
							return
						}
						original(tx)
					}))
				}
				require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:park-headroom", func(tx *gorm.DB) {
					if _, ok := tx.Statement.Dest.(*int64); ok && tx.Statement.Table == "commands" && tx.Error == nil {
						counted.Add(1)
						select {
						case <-release:
						case <-ctx.Done():
							tx.AddError(ctx.Err())
						}
					}
				}))
			}
			results := make(chan error, k)
			start := make(chan struct{})
			for i, api := range replicas {
				go func(i int, api *Api) {
					<-start
					_, err := api.CreateCommandBatch(ctx, batchRequest(fmt.Sprintf("race-%d", i), deviceTokens(m)))
					results <- err
				}(i, api)
			}
			close(start)
			hash := fnv.New64a()
			_, _ = hash.Write([]byte("command-batch:batch-race"))
			key := hash.Sum64()
			// Observe actual backend lock waiters, rather than inferring concurrency
			// from elapsed time. With the lock removed, all K snapshots reach the barrier.
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			ready := false
			for !ready {
				var waiters int64
				require.NoError(t, base.RDB.Database.WithContext(core.WithSystemContext(ctx)).Raw(
					"SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted AND classid = ? AND objid = ? AND objsubid = 1",
					int64(key>>32), int64(key&0xffffffff)).Scan(&waiters).Error)
				ready = counted.Load() == k || (counted.Load() == 1 && waiters == k-1)
				if !ready {
					select {
					case <-ticker.C:
					case <-ctx.Done():
						t.Fatal("batches neither overlapped their counts nor waited on the admission lock")
					}
				}
			}
			if bypass {
				require.EqualValues(t, k, counted.Load(), "negative control needs all pre-insert snapshots")
			} else {
				require.EqualValues(t, 1, counted.Load(), "only the holder may count before release")
			}
			unblock()
			successes, refusals := 0, 0
			for i := 0; i < k; i++ {
				select {
				case err := <-results:
					if err == nil {
						successes++
						continue
					}
					var rejection *BatchRejected
					require.True(t, errors.As(err, &rejection), "unexpected admission error: %v", err)
					require.Equal(t, RejectHeldCeilingExceeded, rejection.Code)
					refusals++
				case <-ctx.Done():
					t.Fatal("batch admission did not finish")
				}
			}
			var undelivered int64
			require.NoError(t, base.RDB.DB(ctx).Model(&Command{}).Where("status IN ?", undeliveredStatusStrings()).Count(&undelivered).Error)
			if bypass {
				require.Equal(t, k, successes)
				require.EqualValues(t, 1+k*m, undelivered)
				require.Greater(t, undelivered, int64(ceiling), "negative control did not expose overshoot")
			} else {
				require.Equal(t, 2, successes)
				require.Equal(t, k-2, refusals)
				require.EqualValues(t, 9, undelivered)
				require.LessOrEqual(t, undelivered, int64(ceiling))
			}
		})
	}
}

func TestPostgresPassLocksExcludePeersAndRelease(t *testing.T) {
	a := newPostgresApi(t)
	b := batchLockReplica(t, a)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i, holder := range passLockers(a) {
		entered, release := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		// Cleanup also unblocks the holder on an assertion failure.
		t.Cleanup(unblock)
		go func() {
			ran, err := holder(ctx, func() error {
				close(entered)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			if err == nil && !ran {
				err = fmt.Errorf("holder pass %d did not acquire", i)
			}
			done <- err
		}()
		select {
		case <-entered:
		case err := <-done:
			t.Fatalf("holder failed: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		for j, peer := range passLockers(b) {
			called := false
			ran, err := peer(ctx, func() error { called = true; return nil })
			require.NoError(t, err)
			require.Equal(t, i != j, ran, "holder %d, contender %d", i, j)
			require.Equal(t, ran, called)
		}
		unblock()
		require.NoError(t, <-done)
		called := false
		ran, err := passLockers(b)[i](ctx, func() error { called = true; return nil })
		require.NoError(t, err)
		require.True(t, ran)
		require.True(t, called)
	}
}
