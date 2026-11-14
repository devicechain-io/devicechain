// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestReplaceDeviceConcurrentOnPostgres(t *testing.T) {
	for _, skipLock := range []bool{false, true} {
		t.Run(fmt.Sprintf("skipLock=%t", skipLock), func(t *testing.T) {
			f := newPgFixture(t)
			// Session kicks are covered separately; this test measures the replacement transaction.
			f.api.SessionKicker = nil
			ctx, cancel := context.WithTimeout(f.ctx, 45*time.Second)
			defer cancel()
			device := seedReplacementDevice(t, f.api, ctx, "replacement-race")
			seedCredential(t, f.api, ctx, device.Token, "original", "original-bearer", true, nil)
			pool, err := f.mgr.Database.DB()
			require.NoError(t, err)
			pool.SetMaxOpenConns(25) // twenty transactions plus the lock observer

			const writers = 20
			allStarted, allRead, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			firstRead := make(chan struct{}, 1)
			var started, read atomic.Int32
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			before, after := "test:replacement-lock", "test:replacement-read"
			require.NoError(t, f.mgr.Database.Callback().Query().Before("gorm:query").Register(before, func(db *gorm.DB) {
				if _, ok := db.Statement.Dest.(*Device); !ok {
					return
				}
				// Test-only seam: run the actual ReplaceDevice transaction without FOR UPDATE.
				if skipLock {
					delete(db.Statement.Clauses, "FOR")
				}
				if started.Add(1) == writers {
					close(allStarted)
				}
				select {
				case <-allStarted:
				case <-ctx.Done():
					db.AddError(ctx.Err())
				}
			}))
			require.NoError(t, f.mgr.Database.Callback().Query().After("gorm:query").Register(after, func(db *gorm.DB) {
				if _, ok := db.Statement.Dest.(*[]*DeviceCredential); !ok || db.Error != nil {
					return
				}
				select {
				case firstRead <- struct{}{}:
				default:
				}
				if read.Add(1) == writers {
					close(allRead)
				}
				select {
				case <-release:
				case <-ctx.Done():
					db.AddError(ctx.Err())
				}
			}))
			var workers sync.WaitGroup
			defer func() {
				unblock()
				cancel()
				workers.Wait()
				_ = f.mgr.Database.Callback().Query().Remove(before)
				_ = f.mgr.Database.Callback().Query().Remove(after)
			}()
			errs := make(chan error, writers)
			for i := 0; i < writers; i++ {
				workers.Add(1)
				go func() {
					defer workers.Done()
					_, err := f.api.ReplaceDevice(ctx, &DeviceReplaceRequest{DeviceToken: device.Token}, "operator", time.Now())
					errs <- err
				}()
			}
			awaitSignal(t, firstRead, "the first enabled-credential read")
			if skipLock {
				// Force every transaction to read the original set before any can mint.
				awaitSignal(t, allRead, "all unlocked credential reads")
			} else {
				require.Eventually(t, func() bool {
					var blocked int64
					err := f.mgr.Database.WithContext(ctx).Raw(`SELECT count(*) FROM pg_stat_activity
						WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&blocked).Error
					return err == nil && blocked >= writers-1
				}, 10*time.Second, 20*time.Millisecond, "all other replacements must wait for the device lock")
			}
			unblock()
			for i := 0; i < writers; i++ {
				require.NoError(t, <-errs)
			}
			// Remove the scheduling callbacks before reading the committed state.
			require.NoError(t, f.mgr.Database.Callback().Query().Remove(before))
			require.NoError(t, f.mgr.Database.Callback().Query().Remove(after))
			var credentials []DeviceCredential
			var journal []DeviceReplacement
			require.NoError(t, f.api.RDB.DB(ctx).Where("device_id = ?", device.ID).Find(&credentials).Error)
			require.NoError(t, f.api.RDB.DB(ctx).Where("device_id = ?", device.ID).Find(&journal).Error)
			require.Len(t, credentials, writers+1)
			require.Len(t, journal, writers)
			live := 0
			retired, disabled := map[string]bool{}, map[string]bool{}
			for _, row := range journal {
				for _, token := range row.RetiredCredentialTokenList() {
					retired[token] = true
				}
			}
			for _, cred := range credentials {
				if cred.Enabled {
					live++
				} else {
					disabled[cred.Token] = true
				}
			}
			if skipLock {
				require.GreaterOrEqual(t, live, 2, "negative control must expose duplicate live credentials")
			} else {
				require.Equal(t, 1, live)
				require.Equal(t, disabled, retired, "journal union is every credential except the survivor")
			}
		})
	}
}
