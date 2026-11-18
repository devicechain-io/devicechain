// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package rdb

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Like advisory_lock_integration_test.go, these require Postgres when integration
// is selected: an unavailable server fails rather than silently skipping.
func TestSingletonPassPostgresExclusion(t *testing.T) {
	for _, mode := range []string{"plain", "session", "session terminated"} {
		t.Run(mode, func(t *testing.T) {
			a := &RdbManager{Database: advisoryPool(t)}
			b := &RdbManager{Database: advisoryPool(t)}
			observer := advisoryPool(t)
			ctx := advisoryContext(t)
			name := fmt.Sprintf("%s/%d", t.Name(), time.Now().UnixNano())
			peerName := name // Negative control: append one character to this name.
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			var pid int
			run := func(manager *RdbManager, lockName string, fn func(*gorm.DB) error) error {
				if mode == "plain" {
					return manager.SingletonPass(ctx, lockName, func() error { return fn(nil) })
				}
				return manager.SingletonPassSession(ctx, lockName, fn)
			}
			done := advisoryRun(t, func() advisoryResult {
				err := run(a, name, func(db *gorm.DB) error {
					if db != nil {
						if err := db.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
							return err
						}
					}
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
					if db != nil {
						return db.Exec("SELECT 1").Error
					}
					return nil
				})
				return advisoryResult{err: err}
			})
			advisoryEntered(t, entered)
			called := false
			err := run(b, peerName, func(*gorm.DB) error { called = true; return nil })
			require.ErrorIs(t, err, core.ErrPassSkipped)
			require.False(t, called, "peer ran concurrently while the holder was inside fn")
			if mode == "session terminated" {
				var terminated bool
				require.NoError(t, observer.Raw("SELECT pg_terminate_backend(?)", pid).Scan(&terminated).Error)
				require.True(t, terminated)
				advisoryWait(t, observer, AdvisoryLockKey(name), true, 0)
				// The old callback has not returned, but its connection is gone and a peer
				// can run. Its subsequent statement must fail on that same connection.
				require.NoError(t, run(b, peerName, func(*gorm.DB) error { called = true; return nil }))
				require.True(t, called)
				once.Do(func() { close(release) })
				require.Error(t, advisoryDone(t, done).err)
			} else {
				once.Do(func() { close(release) })
				require.NoError(t, advisoryDone(t, done).err)
				require.NoError(t, run(b, peerName, func(*gorm.DB) error { called = true; return nil }))
				require.True(t, called)
			}
		})
	}
}
