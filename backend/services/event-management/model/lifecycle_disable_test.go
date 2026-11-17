// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Pin the production entry point's lock wiring and error propagation as well as
// applyOne. The integration test proves exclusion across actual server sessions.
func TestRetentionReconcileHoldsLockAndPropagatesDisableFailure(t *testing.T) {
	db, mock := analyticsOwnershipDB(t)
	key := rdb.AdvisoryLockKey("event-management-lifecycle")
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_lock($1)")).
		WithArgs(key).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(strings.ReplaceAll(setChunkIntervalStmt("events"), "?", "$1"))).
		WithArgs(24).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(removeCompressionPolicyStmt("events"))).
		WillReturnResult(sqlmock.NewResult(0, 1))
	denied := errors.New("permission denied removing retention job")
	mock.ExpectExec(regexp.QuoteMeta(removeRetentionPolicyStmt("events"))).WillReturnError(denied)
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).
		WithArgs(key).WillReturnResult(sqlmock.NewResult(0, 1))
	err := ApplyDataLifecyclePolicies(context.Background(), &rdb.RdbManager{Database: db},
		DataLifecyclePolicy{ChunkIntervalHours: 24})
	require.ErrorIs(t, err, denied)
	require.ErrorContains(t, err, "events")
	require.NoError(t, mock.ExpectationsWereMet())
}

// This models only job presence/window. The integration test checks the actual
// Timescale catalog; this control can kill a missing disable Exec without a server.
func TestRetentionJobTransitions(t *testing.T) {
	for _, locationOff := range []bool{false, true} {
		t.Run(map[bool]string{false: "uniform", true: "location-only"}[locationOff], func(t *testing.T) {
			var captured []capturedExec
			db := dryRunDB(t, &captured)
			jobs := map[string]int{}
			require.NoError(t, db.Callback().Raw().After("gorm:raw").Register("test:retention-jobs", func(tx *gorm.DB) {
				for _, table := range LifecycleHypertables {
					switch tx.Statement.SQL.String() {
					case removeRetentionPolicyStmt(table):
						delete(jobs, table)
					case addRetentionPolicyStmt(table):
						require.NotContains(t, jobs, table, "remove must precede replacement")
						jobs[table] = tx.Statement.Vars[0].(int)
					}
				}
			}))
			policy := DataLifecyclePolicy{ChunkIntervalHours: 24, RetentionDays: 30, FailedEventRetentionDays: 7}
			apply := func() {
				for _, table := range LifecycleHypertables {
					require.NoError(t, applyOne(db, table, policy))
				}
			}
			apply()
			require.Len(t, jobs, len(LifecycleHypertables))
			policy.RetentionDays = 7
			apply()
			for _, table := range LifecycleHypertables {
				require.Equal(t, 7, jobs[table], table)
			}
			if locationOff {
				policy.RetentionDays = 30
				policy.LocationRetentionDays = intPtr(0)
			} else {
				policy.RetentionDays = 0
			}
			apply()
			want := map[string]int{FailedEventsHypertable: 7}
			if locationOff {
				for _, table := range LifecycleHypertables {
					if table != LocationHypertable && table != FailedEventsHypertable {
						want[table] = 30
					}
				}
			}
			require.Equal(t, want, jobs)
		})
	}
}

func TestRetentionDisableFailsClosed(t *testing.T) {
	denied := errors.New("permission denied removing retention job")
	for _, table := range LifecycleHypertables {
		t.Run(table, func(t *testing.T) {
			var captured []capturedExec
			db := dryRunDB(t, &captured)
			attempts := 0
			require.NoError(t, db.Callback().Raw().After("gorm:raw").Register("test:deny-retention-remove", func(tx *gorm.DB) {
				if tx.Statement.SQL.String() == removeRetentionPolicyStmt(table) {
					attempts++
					tx.AddError(denied)
				}
			}))
			err := applyOne(db, table, DataLifecyclePolicy{ChunkIntervalHours: 24})
			require.Equal(t, 1, attempts)
			require.ErrorIs(t, err, denied)
			require.ErrorContains(t, err, table)
			// Counterweight: an enabled window retains the existing best-effort
			// behavior. Failing every reconciliation is not the disable guarantee.
			require.NoError(t, applyOne(db, table, DataLifecyclePolicy{
				ChunkIntervalHours: 24, RetentionDays: 30, FailedEventRetentionDays: 7,
			}))
			require.Equal(t, 2, attempts)
		})
	}
}
