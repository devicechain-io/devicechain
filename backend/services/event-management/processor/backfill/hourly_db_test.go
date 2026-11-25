// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backfill

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func hourlyMockDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	conn, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()); _ = conn.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: conn}), &gorm.Config{DisableAutomaticPing: true})
	require.NoError(t, err)
	return db, mock
}

func hourlyTask(mgr *rdb.RdbManager) *HourlyBackfiller {
	ms := &core.Microservice{FunctionalArea: "event-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	return NewHourlyBackfiller(ms, mgr, 0, core.NewNoOpLifecycleCallbacks())
}

func TestHourlyDBRefreshBeforeMarker(t *testing.T) {
	for _, failView := range []string{"", rollupViewHourly} {
		t.Run("fail="+failView, func(t *testing.T) {
			db, mock := hourlyMockDB(t)
			now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
			floor := now.Add(-time.Hour)
			mock.ExpectQuery(`SELECT complete_from`).WillReturnRows(sqlmock.NewRows([]string{"complete_from", "now"}).AddRow(now, now))
			mock.ExpectQuery(`SELECT min\(at\)`).WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(floor))
			boom := errors.New("refresh interrupted")
			for _, view := range []string{rollupViewHourly} {
				mock.ExpectExec(`SELECT set_config`).WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
				expect := mock.ExpectExec(`CALL refresh_continuous_aggregate`).WithArgs(view, floor, now)
				if view == failView {
					expect.WillReturnError(boom)
					break
				}
				expect.WillReturnResult(sqlmock.NewResult(0, 1))
			}
			if failView == "" {
				mock.ExpectExec(`UPDATE "event-management".rollup_levels SET complete_from`).
					WithArgs(floor, now).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			s := hourlyTask(nil)
			s.now = func() time.Time { return now }
			err := s.pass(context.Background(), &hourlyDBProgress{db: db})
			if failView == "" {
				require.NoError(t, err)
				require.Equal(t, float64(floor.Unix()), testutil.ToFloat64(s.completeFrom))
				require.Equal(t, 0.0, testutil.ToFloat64(s.remaining))
				require.Equal(t, 1.0, testutil.ToFloat64(s.windows))
			} else {
				require.ErrorIs(t, err, boom)
				require.Equal(t, float64(now.Unix()), testutil.ToFloat64(s.completeFrom))
				require.Equal(t, 1.0, testutil.ToFloat64(s.remaining))
				require.Equal(t, 0.0, testutil.ToFloat64(s.windows))
			}
		})
	}
}

// Calculated backfills and the tail sweep still need both levels, in order.
// Keep their refresh/error guarantees when the hour-only worker is changed.
func TestRefreshRollupsMaintainsBothLevelsInOrder(t *testing.T) {
	for _, failView := range []string{"", rollupView, rollupViewHourly} {
		t.Run("fail="+failView, func(t *testing.T) {
			db, mock := hourlyMockDB(t)
			end := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
			start := end.Add(-time.Hour)
			boom := errors.New("refresh interrupted")
			for _, view := range []string{rollupView, rollupViewHourly} {
				mock.ExpectExec(`SELECT set_config`).WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
				expect := mock.ExpectExec(`CALL refresh_continuous_aggregate`).WithArgs(view, start, end)
				if view == failView {
					expect.WillReturnError(boom)
					break
				}
				expect.WillReturnResult(sqlmock.NewResult(0, 1))
			}
			err := refreshRollups(db, start, end, time.Second)
			if failView == "" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, boom)
			}
		})
	}
}

func TestHourlyBoundsRespectRetentionAndEmptyStore(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 30, 0, 0, time.UTC)
	marker := now.Truncate(time.Hour)
	old := now.Add(-90 * 24 * time.Hour)
	for _, tc := range []struct {
		name  string
		days  int
		empty bool
		want  time.Time
	}{
		{"all history", 0, false, old.Truncate(time.Hour)},
		{"retention boundary hour", 31, false, now.Add(-31 * 24 * time.Hour).Truncate(time.Hour)},
		{"empty store", 0, true, marker},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := hourlyMockDB(t)
			mock.ExpectQuery(`SELECT complete_from`).WillReturnRows(sqlmock.NewRows([]string{"complete_from", "now"}).AddRow(marker, now))
			rows := sqlmock.NewRows([]string{"min"})
			if tc.empty {
				rows.AddRow(nil)
			} else {
				rows.AddRow(old)
			}
			mock.ExpectQuery(`SELECT min\(at\) FROM \([\s\S]*measurement_rollups[\s\S]*min\(range_start\)[\s\S]*timescaledb_information.chunks[\s\S]*hypertable_schema = 'event-management' AND hypertable_name = 'measurement_events'`).WillReturnRows(rows)
			p := &hourlyDBProgress{db: db, retentionDays: tc.days}
			gotMarker, floor, databaseNow, err := p.bounds(context.Background())
			require.NoError(t, err)
			require.Equal(t, marker, gotMarker)
			require.Equal(t, now, databaseNow)
			require.True(t, tc.want.Equal(floor), "want %v got %v", tc.want, floor)
		})
	}
}

func TestHourlyUsesExistingLock(t *testing.T) {
	db, mock := hourlyMockDB(t)
	mock.ExpectQuery(`SELECT pg_try_advisory_lock`).WithArgs(rdb.AdvisoryLockKey("calc-backfill")).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(false))
	s := hourlyTask(&rdb.RdbManager{Database: db})
	require.ErrorIs(t, s.RunOnce(context.Background()), core.ErrPassSkipped)
}

func TestHourlyRefreshPolicyConflictLeavesWindowOwed(t *testing.T) {
	for _, code := range []string{"55P03", "57014", "XX000"} {
		t.Run(code, func(t *testing.T) {
			db, mock := hourlyMockDB(t)
			now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
			mock.ExpectQuery(`SELECT complete_from`).WillReturnRows(sqlmock.NewRows([]string{"complete_from", "now"}).AddRow(now, now))
			mock.ExpectQuery(`SELECT min\(at\)`).WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(now.Add(-time.Hour)))
			mock.ExpectExec(`SELECT set_config`).WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
			boom := &pgconn.PgError{Code: code, Message: "policy refresh collision"}
			mock.ExpectExec(`CALL refresh_continuous_aggregate`).WithArgs(rollupViewHourly, now.Add(-time.Hour), now).
				WillReturnError(fmt.Errorf("refresh: %w", boom))
			s := hourlyTask(nil)
			s.now = func() time.Time { return now }
			err := s.pass(context.Background(), &hourlyDBProgress{db: db})
			if code == "55P03" {
				require.ErrorIs(t, err, core.ErrPassSkipped, "policy overlap retries next pass without counting a failure")
			} else {
				require.ErrorIs(t, err, boom)
			}
			require.Zero(t, testutil.ToFloat64(s.windows))
			require.Equal(t, float64(now.Unix()), testutil.ToFloat64(s.completeFrom))
		})
	}
}

func TestHourlyProgressGaugesWaitForBounds(t *testing.T) {
	db, mock := hourlyMockDB(t)
	mock.ExpectQuery(`SELECT pg_try_advisory_lock`).WithArgs(rdb.AdvisoryLockKey(lockName)).
		WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(false))
	registry := prometheus.NewRegistry()
	ms := &core.Microservice{FunctionalArea: "event-management"}
	ms.UseMetricsRegistry(registry)
	s := NewHourlyBackfiller(ms, &rdb.RdbManager{Database: db}, 0, core.NewNoOpLifecycleCallbacks())
	require.ErrorIs(t, s.RunOnce(context.Background()), core.ErrPassSkipped)
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		require.NotContains(t, family.GetName(), "rollup_hour_backfill_complete_from")
		require.NotContains(t, family.GetName(), "rollup_hour_backfill_remaining_windows")
	}
	now := time.Now().UTC().Truncate(time.Hour)
	require.NoError(t, s.pass(context.Background(), &memoryHourlyProgress{marker: now, floor: now}))
	require.Equal(t, float64(now.Unix()), testutil.ToFloat64(s.completeFrom))
	require.Zero(t, testutil.ToFloat64(s.remaining))
}

type hourlyCallbackLock struct{ db *gorm.DB }

func (l hourlyCallbackLock) SingletonPassSession(_ context.Context, _ string, pass func(*gorm.DB) error) error {
	return pass(l.db)
}

func TestHourlySourceBoundsStopAfterReachingFloor(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty=%t", empty), func(t *testing.T) {
			db, mock := hourlyMockDB(t)
			now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
			marker := now
			for pass := 0; pass < 3; pass++ {
				mock.ExpectQuery(`SELECT complete_from, now\(\)`).WillReturnRows(sqlmock.NewRows([]string{"complete_from", "now"}).AddRow(marker, now))
				if pass == 0 {
					rows := sqlmock.NewRows([]string{"min"})
					if empty {
						rows.AddRow(nil)
					} else {
						rows.AddRow(now.Add(-time.Hour))
					}
					mock.ExpectQuery(`SELECT min\(at\)`).WillReturnRows(rows)
					if !empty {
						mock.ExpectExec(`SELECT set_config`).WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
						mock.ExpectExec(`CALL refresh_continuous_aggregate`).WithArgs(rollupViewHourly, now.Add(-time.Hour), now).WillReturnResult(sqlmock.NewResult(0, 1))
						mock.ExpectExec(`UPDATE "event-management".rollup_levels`).WithArgs(now.Add(-time.Hour), now).WillReturnResult(sqlmock.NewResult(0, 1))
						marker = now.Add(-time.Hour)
					}
				}
				mock.ExpectExec(`RESET statement_timeout`).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			s := hourlyTask(nil)
			s.rdb = hourlyCallbackLock{db}
			s.now = func() time.Time { return now }
			for pass := 0; pass < 3; pass++ {
				require.NoError(t, s.RunOnce(context.Background()), "no source query after the first pass reached the floor")
			}
			require.Zero(t, testutil.ToFloat64(s.remaining))
		})
	}
}

func TestHourlyOpenHourUsesDatabaseClock(t *testing.T) {
	for _, skew := range []time.Duration{-2 * time.Hour, 2 * time.Hour} {
		t.Run(skew.String(), func(t *testing.T) {
			db, mock := hourlyMockDB(t)
			now := time.Date(2026, 11, 23, 12, 17, 0, 0, time.UTC)
			marker := now.Truncate(time.Hour)
			if skew > 0 {
				marker = marker.Add(time.Hour) // still open in the database
			}
			floor := marker.Add(-time.Hour)
			mock.ExpectQuery(`SELECT complete_from, now\(\)`).WillReturnRows(sqlmock.NewRows([]string{"complete_from", "now"}).AddRow(marker, now))
			mock.ExpectQuery(`SELECT min\(at\)`).WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(floor))
			if skew < 0 { // closed in the DB, even though the pod clock is behind
				mock.ExpectExec(`SELECT set_config`).WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec(`CALL refresh_continuous_aggregate`).WithArgs(rollupViewHourly, floor, marker).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec(`UPDATE "event-management".rollup_levels`).WithArgs(floor, marker).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			s := hourlyTask(nil)
			s.now = func() time.Time { return now.Add(skew) }
			require.NoError(t, s.pass(context.Background(), &hourlyDBProgress{db: db}))
		})
	}
}

func TestHourlyMarkerWriteMustSucceed(t *testing.T) {
	for _, changed := range []int64{0, 1} {
		db, mock := hourlyMockDB(t)
		now := time.Now().UTC().Truncate(time.Hour)
		mock.ExpectExec(`UPDATE "event-management".rollup_levels SET complete_from = \$1[\s\S]*WHERE level = '1h' AND complete_from = \$2`).
			WithArgs(now.Add(-time.Hour), now).WillReturnResult(sqlmock.NewResult(0, changed))
		err := (&hourlyDBProgress{db: db}).advance(context.Background(), now, now.Add(-time.Hour))
		if changed == 0 {
			require.Error(t, err, "a stale worker must not report progress")
		} else {
			require.NoError(t, err)
		}
	}
}

type hourlyDeadlineLock struct{ t *testing.T }

func (l hourlyDeadlineLock) SingletonPassSession(ctx context.Context, key string, _ func(*gorm.DB) error) error {
	deadline, ok := ctx.Deadline()
	require.True(l.t, ok, "the entire pass, including reads and lock acquisition, needs a deadline")
	left := time.Until(deadline)
	require.Positive(l.t, left)
	require.LessOrEqual(l.t, left, hourlyPassBudget)
	require.Equal(l.t, "calc-backfill", key)
	return core.ErrPassSkipped
}

func TestHourlyPassDeadlineIncludesLockAcquisition(t *testing.T) {
	s := &HourlyBackfiller{rdb: hourlyDeadlineLock{t}}
	require.ErrorIs(t, s.RunOnce(context.Background()), core.ErrPassSkipped)
}
