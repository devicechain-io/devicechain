// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Package concurrencytest parks database workers after their decision snapshot.
package concurrencytest

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type workerKey struct{}
type worker struct {
	unlocked bool
	parked   atomic.Bool
}

// Context marks a single operation. unlocked is a test-only negative control:
// remove row locks, including the account lock that independently protects rotations.
func Context(ctx context.Context, unlocked bool) context.Context {
	return context.WithValue(ctx, workerKey{}, &worker{unlocked: unlocked})
}

// Park installs a test-only database barrier. The holder blocks shared advisory
// locks, so all unlocked workers can read the same snapshot before any can write.
// With production row locks, one worker reaches this barrier and the rest wait
// for that worker's row lock. Both schedules must be visible in pg_stat_activity.
func Park(t *testing.T, db *gorm.DB, update bool, snapshot func(*gorm.DB) bool) func(int) {
	t.Helper()
	const key = int64(741603)
	holder := db.Begin()
	require.NoError(t, holder.Error)
	t.Cleanup(func() { holder.Rollback() })
	require.NoError(t, holder.Exec("SELECT pg_advisory_xact_lock(?)", key).Error)
	name := "test:decision_snapshot"
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if w, ok := tx.Statement.Context.Value(workerKey{}).(*worker); ok && w.unlocked {
			delete(tx.Statement.Clauses, "FOR")
		}
	}))
	park := func(tx *gorm.DB) {
		w, ok := tx.Statement.Context.Value(workerKey{}).(*worker)
		if !ok || tx.Error != nil || !snapshot(tx) || !w.parked.CompareAndSwap(false, true) {
			return
		}
		// NewDB avoids reusing the intercepted statement; Exec uses Raw callbacks.
		tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Exec("SELECT pg_advisory_xact_lock_shared(?)", key).Error)
	}
	if update {
		require.NoError(t, db.Callback().Update().Before("gorm:update").Register(name, park))
	} else {
		require.NoError(t, db.Callback().Query().After("gorm:after_query").Register(name+":park", park))
	}
	t.Cleanup(func() {
		db.Callback().Query().Remove(name)
		if update {
			db.Callback().Update().Remove(name)
		} else {
			db.Callback().Query().Remove(name + ":park")
		}
	})
	return func(n int) {
		t.Helper()
		require.Eventually(t, func() bool {
			var count int64
			err := db.Raw(`SELECT count(*) FROM pg_stat_activity
				WHERE datname = current_database() AND pid <> pg_backend_pid()
				AND state = 'active' AND wait_event_type = 'Lock'
				AND (query LIKE '%pg_advisory_xact_lock_shared%' OR query LIKE '%FOR UPDATE%')`).Scan(&count).Error
			return err == nil && count == int64(n)
		}, 15*time.Second, 10*time.Millisecond, "all workers must overlap on distinct PostgreSQL backends")
		require.NoError(t, holder.Rollback().Error)
	}
}
