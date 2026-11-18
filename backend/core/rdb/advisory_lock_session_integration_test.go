// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package rdb

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestTryAdvisoryLockSessionPostgresPinnedAndContended(t *testing.T) {
	a := &RdbManager{Database: advisoryPool(t)}
	b := &RdbManager{Database: advisoryPool(t)}
	observer := advisoryPool(t)
	ctx := advisoryContext(t)
	key := AdvisoryLockKey(fmt.Sprintf("%s/%d", t.Name(), time.Now().UnixNano()))
	ran, err := a.TryAdvisoryLockSession(ctx, key, func(db *gorm.DB) error {
		var lockPID int
		require.NoError(t, observer.WithContext(ctx).Raw(`SELECT pid FROM pg_locks
			WHERE locktype = 'advisory' AND objsubid = 1 AND granted
			AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
			AND classid::bigint = ((?::bigint >> 32) & 4294967295)
			AND objid::bigint = (?::bigint & 4294967295)`, key, key).Scan(&lockPID).Error)
		require.Positive(t, lockPID, "the callback must run while the lock is granted")
		checkPID := func(handle *gorm.DB) {
			var pid int
			require.NoError(t, handle.Raw("SELECT pg_backend_pid()").Scan(&pid).Error)
			require.Equal(t, lockPID, pid, "every statement must use the lock's backend")
		}
		checkPID(db)
		require.NoError(t, db.Exec("SELECT 1").Error)
		checkPID(db)
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			checkPID(tx)
			return nil
		}))
		checkPID(db)

		peerCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		called := false
		peerRan, peerErr := b.TryAdvisoryLockSession(peerCtx, key, func(*gorm.DB) error {
			called = true
			return nil
		})
		require.NoError(t, peerErr)
		require.False(t, peerRan)
		require.False(t, called, "a contender must not enter its callback")
		return nil
	})
	require.NoError(t, err)
	require.True(t, ran)
	require.Zero(t, advisoryRows(t, observer, key, true), "lock leaked into the idle pool")
}

func TestTryAdvisoryLockSessionPostgresStatementErrorIsNotSticky(t *testing.T) {
	a := &RdbManager{Database: advisoryPool(t)}
	ctx := advisoryContext(t)
	key := AdvisoryLockKey(fmt.Sprintf("%s/%d", t.Name(), time.Now().UnixNano()))
	ran, err := a.TryAdvisoryLockSession(ctx, key, func(db *gorm.DB) error {
		// Outside a transaction, Postgres accepts the next statement after this
		// error. A getInstance-derived GORM handle retains Error and never sends it.
		var pgErr *pgconn.PgError
		require.ErrorAs(t, db.Exec("SELECT 1 / 0").Error, &pgErr)
		require.Equal(t, "22012", pgErr.Code)
		require.NoError(t, db.Exec("SELECT 1").Error, "the failed statement must not poison the handle")
		var answer int
		require.NoError(t, db.Raw("SELECT 42").Scan(&answer).Error)
		require.Equal(t, 42, answer)
		return nil
	})
	require.True(t, ran)
	require.NoError(t, err)
}

func TestTryAdvisoryLockSessionPostgresReleasesOnCancelAndError(t *testing.T) {
	for _, cancelInside := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%v", cancelInside), func(t *testing.T) {
			a := &RdbManager{Database: advisoryPool(t)}
			b := &RdbManager{Database: advisoryPool(t)}
			observer := advisoryPool(t)
			ctx, cancel := context.WithCancel(advisoryContext(t))
			defer cancel()
			key := AdvisoryLockKey(fmt.Sprintf("%s/%d", t.Name(), time.Now().UnixNano()))
			want := errors.New("session callback failed")
			var lockPID int
			ran, err := a.TryAdvisoryLockSession(ctx, key, func(db *gorm.DB) error {
				require.EqualValues(t, 1, advisoryRows(t, observer, key, true))
				require.NoError(t, db.Exec("SELECT 1").Error)
				require.NoError(t, db.Raw("SELECT pg_backend_pid()").Scan(&lockPID).Error)
				if cancelInside {
					cancel()
					return ctx.Err()
				}
				return want
			})
			require.True(t, ran, "callback errors must still report ran=true")
			if cancelInside {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.ErrorIs(t, err, want)
			}
			// Observe before closing a's pool: ending the session would hide a
			// missing unlock, including an unlock attempted on the cancelled ctx.
			require.Zero(t, advisoryRows(t, observer, key, true), "lock leaked into the idle pool")
			// The unlock ran, rather than the lock dying with a discarded connection: an
			// unlock sent on the cancelled ctx is refused by the driver as a bad connection,
			// and database/sql then closes the backend, which also frees the lock. Only the
			// fresh unlock context leaves the backend alive in the idle pool.
			time.Sleep(200 * time.Millisecond)
			var alive int64
			require.NoError(t, observer.WithContext(advisoryContext(t)).Raw(
				"SELECT count(*) FROM pg_stat_activity WHERE pid = ?", lockPID).Scan(&alive).Error)
			require.EqualValues(t, 1, alive, "the lock's connection was discarded instead of unlocked")
			called := false
			ran, err = b.TryAdvisoryLockSession(advisoryContext(t), key, func(*gorm.DB) error {
				called = true
				return nil
			})
			require.NoError(t, err)
			require.True(t, ran)
			require.True(t, called)
		})
	}
}
