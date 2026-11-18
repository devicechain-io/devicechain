// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// WAL permits a separate writer to commit while the candidate reader holds its
// snapshot. An in-memory SQLite DB cannot exercise this interleaving.
func TestHistorySnapshotInterleaving(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.ToSlash(filepath.Join(t.TempDir(), "history.db"))), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(4)
	t.Cleanup(func() { require.NoError(t, pool.Close()) })
	require.NoError(t, db.Exec("PRAGMA journal_mode=WAL").Error)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&NotificationState{}, &NotificationAttempt{}))
	assertHistorySnapshotInterleaving(t, NewApi(&rdb.RdbManager{Database: db}, nil), tenantCtx("A"))
}

// Shared by SQLite and the real Postgres migration/tenant-callback harness.
// The synchronous post-COUNT callback is a barrier: the downgrade commits on a
// separate connection before the page and distinct-token queries may execute.
func assertHistorySnapshotInterleaving(t *testing.T, api *Api, ctx context.Context) {
	t.Helper()
	for _, read := range []string{"states", "attempts"} {
		t.Run(read, func(t *testing.T) {
			no := false
			token := "snapshot-" + read
			require.NoError(t, api.RDB.DB(ctx).Create(&NotificationState{
				AlarmToken: token, AlarmKey: token, PositionDerived: &no,
			}).Error)
			require.NoError(t, api.RecordNotificationAttempt(ctx, &NotificationAttempt{AlarmToken: token}))
			other := core.WithTenant(ctx, "snapshot-other")
			require.NoError(t, api.RDB.DB(other).Create(&NotificationState{
				AlarmToken: token, AlarmKey: token, PositionDerived: &no,
			}).Error)
			require.NoError(t, api.RecordNotificationAttempt(other, &NotificationAttempt{AlarmToken: token}))

			caller := auth.WithClaims(ctx, &auth.Claims{Authorities: []string{string(auth.NotificationRead)}})
			counts, downgrades := 0, 0
			const hook = "test:history_snapshot_count"
			require.NoError(t, api.RDB.Database.Callback().Query().After("gorm:query").Register(hook, func(q *gorm.DB) {
				count, ok := q.Statement.Dest.(*int64)
				if !ok || q.Error != nil {
					return
				}
				counts++
				if counts != 1 {
					return
				}
				require.Equal(t, int64(1), *count, "fixture must count the unpaged token and exclude the other tenant")
				// This cannot use q: the concurrent repair must commit outside
				// the reader's snapshot before it continues.
				update := api.RDB.DB(ctx).Model(&NotificationState{}).
					Where("alarm_token = ? AND position_derived IS FALSE", token).
					Update("position_derived", nil)
				require.NoError(t, update.Error)
				require.EqualValues(t, 1, update.RowsAffected)
				downgrades++
			}))
			t.Cleanup(func() { require.NoError(t, api.RDB.Database.Callback().Query().Remove(hook)) })

			var asked []string
			api.PositionOracle = positionOracleFunc(func(_ context.Context, tokens []string) (map[string]bool, error) {
				asked = append(asked, tokens...)
				return map[string]bool{}, nil
			})
			page := rdb.Pagination{PageNumber: 2, PageSize: 1}
			if read == "states" {
				result, err := api.NotificationStates(caller, NotificationStateSearchCriteria{AlarmKey: &token, Pagination: page})
				require.NoError(t, err)
				require.Empty(t, result.Results)
				require.Zero(t, result.Pagination.TotalRecords, "count disclosed an unverified withheld token")
			} else {
				result, err := api.NotificationAttempts(caller, NotificationAttemptSearchCriteria{AlarmToken: &token, Pagination: page})
				require.NoError(t, err)
				require.Empty(t, result.Results)
				require.Zero(t, result.Pagination.TotalRecords, "count disclosed an unverified withheld token")
			}
			require.Equal(t, 1, downgrades, "the interleaved write must have committed")
			require.Equal(t, 2, counts, "repair must retry on a fresh snapshot")
			require.Equal(t, []string{token}, asked, "a counted but unpaged token must reach verification")

			// Internal repair must retain tenant scope even after removing claims.
			var state NotificationState
			require.NoError(t, api.RDB.DB(other).Where("alarm_token = ?", token).First(&state).Error)
			require.Equal(t, &no, state.PositionDerived)
		})
	}
}

// Even a query/transaction failure after COUNT must discard the candidate count.
func TestHistorySnapshotErrorsFailClosed(t *testing.T) {
	for _, stage := range []string{"count", "page", "tokens"} {
		t.Run(stage, func(t *testing.T) {
			api := newTestApi(t)
			ctx := tenantCtx("A")
			no := false
			require.NoError(t, api.RDB.DB(ctx).Create(&NotificationState{AlarmToken: "snapshot", PositionDerived: &no}).Error)
			caller := auth.WithClaims(ctx, &auth.Claims{Authorities: []string{string(auth.NotificationRead)}})
			calls := 0
			api.PositionOracle = positionOracleFunc(func(context.Context, []string) (map[string]bool, error) {
				t.Fatal("failed snapshot reached the oracle")
				return nil, nil
			})
			require.NoError(t, api.RDB.Database.Callback().Query().Before("gorm:query").Register("test:history_snapshot_error", func(q *gorm.DB) {
				calls++
				if calls == map[string]int{"count": 1, "page": 2, "tokens": 3}[stage] {
					q.AddError(sql.ErrConnDone)
				}
			}))
			result, err := api.NotificationStates(caller, NotificationStateSearchCriteria{})
			require.ErrorIs(t, err, sql.ErrConnDone)
			require.Nil(t, result)
		})
	}
}
