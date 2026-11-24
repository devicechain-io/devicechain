// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAnalyticsNewerGenerationLeavesSurfaceUntouched(t *testing.T) {
	db, mock := analyticsOwnershipDB(t)
	key := rdb.AdvisoryLockKey(analyticsLockName)
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_lock($1)")).
		WithArgs(key).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT generation FROM "event-management".analytics_surface_generation WHERE id = 1`).
		WillReturnRows(sqlmock.NewRows([]string{"generation"}).AddRow(2147483647))
	// No function/view/grant DDL, integrity inspection, or generation write is
	// permitted: a future surface must remain completely untouched.
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).
		WithArgs(key).WillReturnResult(sqlmock.NewResult(0, 1))
	metrics := newTestAnalyticsMetrics()
	output := logSink.Capture(t)
	require.NoError(t, ReconcileAnalyticsSurface(context.Background(), &rdb.RdbManager{Database: db}, metrics))
	require.NoError(t, mock.ExpectationsWereMet())
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.NewerSurfaceSkips))
	var entry map[string]interface{}
	for _, line := range strings.Split(output.String(), "\n") {
		if strings.Contains(line, "storedGeneration") {
			require.NoError(t, json.Unmarshal([]byte(line), &entry))
		}
	}
	require.NotNil(t, entry, "the newer-surface skip must be logged")
	require.Equal(t, "info", entry["level"])
	require.Equal(t, float64(2147483647), entry["storedGeneration"])
	require.Equal(t, float64(AnalyticsSurfaceGeneration), entry["buildGeneration"])
}

func expectAnalyticsGenerationLock(mock sqlmock.Sqlmock) {
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_lock($1)")).
		WithArgs(rdb.AdvisoryLockKey(analyticsLockName)).WillReturnResult(sqlmock.NewResult(0, 1))
}

func expectAnalyticsGenerationUnlock(mock sqlmock.Sqlmock) {
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).
		WithArgs(rdb.AdvisoryLockKey(analyticsLockName)).WillReturnResult(sqlmock.NewResult(0, 1))
}

func TestAnalyticsEqualLowerAndUnrecordedGenerationReconcile(t *testing.T) {
	for _, stored := range []int{-1, 0, AnalyticsSurfaceGeneration} {
		for _, damaged := range []bool{false, true} {
			t.Run(fmt.Sprintf("stored_%d_damaged_%v", stored, damaged), func(t *testing.T) {
				db, mock := analyticsOwnershipDB(t)
				expectAnalyticsGenerationLock(mock)
				rows := sqlmock.NewRows([]string{"generation"})
				if stored >= 0 {
					rows.AddRow(stored)
				}
				mock.ExpectQuery(`SELECT generation FROM "event-management".analytics_surface_generation WHERE id = 1`).WillReturnRows(rows)
				for _, stmt := range []string{createAnalyticsSchemaStmt(), createReaderTenantFnStmt()} {
					mock.ExpectExec(regexp.QuoteMeta(stmt)).WillReturnResult(sqlmock.NewResult(0, 0))
				}
				expectAnalyticsOwnershipViews(mock, "", "", damaged)
				if damaged {
					for _, stmt := range analyticsSurfaceStatements() {
						mock.ExpectExec(regexp.QuoteMeta(stmt)).WillReturnResult(sqlmock.NewResult(0, 0))
					}
					expectAnalyticsOwnershipViews(mock, "", "", false)
				}
				expectAnalyticsOwnershipFunction(mock)
				mock.ExpectQuery("WITH RECURSIVE").WillReturnRows(sqlmock.NewRows([]string{"rolname", "refuse", "superuser"}))
				for _, stmt := range convergeStatements(analyticsGrantees(nil)[0]) {
					mock.ExpectExec(regexp.QuoteMeta(stmt)).WillReturnResult(sqlmock.NewResult(0, 0))
				}
				mock.ExpectExec(`INSERT INTO "event-management".analytics_surface_generation`).
					WithArgs(AnalyticsSurfaceGeneration).WillReturnResult(sqlmock.NewResult(0, 1))
				expectAnalyticsGenerationUnlock(mock)
				metrics := newTestAnalyticsMetrics()
				require.NoError(t, ReconcileAnalyticsSurface(context.Background(), &rdb.RdbManager{Database: db}, metrics))
				require.NoError(t, mock.ExpectationsWereMet())
				require.Zero(t, testutil.ToFloat64(metrics.NewerSurfaceSkips))
			})
		}
	}
}

// A failure anywhere refuses the boot and leaves the generation unwritten, but it
// must NOT roll back what the reconcile already applied: each statement is a
// tenant-isolation repair (a rebuilt view, a revoked grant) that has to survive a
// later step refusing the boot. sqlmock fails any BEGIN/ROLLBACK it was not told
// to expect, so a transaction around the reconcile turns every case here red.
func TestAnalyticsGenerationFailuresKeepAppliedRepairs(t *testing.T) {
	for _, stage := range []string{"read", "surface", "later", "store", "lowered"} {
		t.Run(stage, func(t *testing.T) {
			db, mock := analyticsOwnershipDB(t)
			expectAnalyticsGenerationLock(mock)
			failure := errors.New("injected " + stage + " failure")
			read := mock.ExpectQuery(`SELECT generation FROM "event-management".analytics_surface_generation WHERE id = 1`)
			if stage == "read" {
				read.WillReturnError(failure)
			} else {
				read.WillReturnRows(sqlmock.NewRows([]string{"generation"}).AddRow(0))
				ddl := mock.ExpectExec("surface DDL")
				if stage == "surface" {
					ddl.WillReturnError(failure)
				} else {
					ddl.WillReturnResult(sqlmock.NewResult(0, 0))
					later := mock.ExpectExec("later DDL")
					if stage == "later" {
						later.WillReturnError(failure)
					} else {
						later.WillReturnResult(sqlmock.NewResult(0, 0))
						store := mock.ExpectExec(`INSERT INTO "event-management".analytics_surface_generation`).WithArgs(AnalyticsSurfaceGeneration)
						switch stage {
						case "store":
							store.WillReturnError(failure)
						case "lowered":
							store.WillReturnResult(sqlmock.NewResult(0, 0))
						}
					}
				}
			}
			expectAnalyticsGenerationUnlock(mock)
			metrics := newTestAnalyticsMetrics()
			err := withAnalyticsGeneration(context.Background(), &rdb.RdbManager{Database: db}, AnalyticsSurfaceGeneration, metrics, func(db *gorm.DB) error {
				if err := db.Exec("surface DDL").Error; err != nil {
					return err
				}
				return db.Exec("later DDL").Error
			})
			if stage == "lowered" {
				require.ErrorContains(t, err, "refusing to lower")
			} else {
				require.ErrorIs(t, err, failure)
			}
			require.NoError(t, mock.ExpectationsWereMet())
			require.Zero(t, testutil.ToFloat64(metrics.NewerSurfaceSkips))
		})
	}
}

// SQLite exercises the actual guarded upsert and migration replay, not a SQL
// expectation. Advisory-lock concurrency is covered separately on Postgres.
func TestAnalyticsGenerationStoreIsMonotonicAndMigrationReplays(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	require.NoError(t, db.Exec(`ATTACH DATABASE ':memory:' AS "event-management"`).Error)
	migration := NewAnalyticsGenerationSchema()
	require.Equal(t, migration.ID, Migrations[len(Migrations)-1].ID)
	require.False(t, rdb.MigrationOptions("event-management").UseTransaction)
	require.NoError(t, migration.Migrate(db))
	var count int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM "event-management".analytics_surface_generation WHERE id = 1 AND generation = 0`).Scan(&count).Error)
	require.Equal(t, int64(1), count, "the migration must persist its literal unversioned seed")
	read := func(want int) {
		t.Helper()
		var stored int
		require.NoError(t, db.Raw(`SELECT generation FROM "event-management".analytics_surface_generation WHERE id = 1`).Scan(&stored).Error)
		require.Equal(t, want, stored)
	}
	read(0)
	for _, generation := range []int{1, 1, 2} {
		require.NoError(t, storeAnalyticsGeneration(db, generation))
		read(generation)
	}
	require.Error(t, storeAnalyticsGeneration(db, 1), "a stale write must fail and leave the newer row intact")
	read(2)
	require.NoError(t, migration.Migrate(db))
	read(2)
	require.Error(t, db.Exec(`INSERT INTO "event-management".analytics_surface_generation VALUES (2, 1)`).Error)
	require.Error(t, db.Exec(`UPDATE "event-management".analytics_surface_generation SET generation = -1`).Error)
	require.NoError(t, db.Exec(`DELETE FROM "event-management".analytics_surface_generation`).Error)
	require.NoError(t, storeAnalyticsGeneration(db, 3))
	read(3)
}
