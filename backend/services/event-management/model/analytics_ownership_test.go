// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func analyticsOwnershipDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqldb, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqldb.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqldb}), &gorm.Config{
		DisableAutomaticPing: true,
	})
	require.NoError(t, err)
	return db, mock
}

// Match the catalog source rather than the SELECT list so this fixture also
// runs against the old query: gorm ignores the additional ownership columns.
func expectAnalyticsOwnershipViews(mock sqlmock.Sqlmock, foreign, inherited string, damagedFirst bool) {
	for i, v := range analyticsViews {
		owner, privileges := "platform", true
		if v.name == inherited {
			owner = "platform_owners"
		}
		if v.name == foreign {
			owner, privileges = "foreign_owner", false
		}
		def := "SELECT declared_columns FROM source " + expectedViewPredicate()
		if damagedFirst && i == 0 {
			def = "SELECT declared_columns FROM source;"
		}
		mock.ExpectQuery("FROM pg_class c").WithArgs(AnalyticsSchema, v.name).
			WillReturnRows(sqlmock.NewRows([]string{"opts", "def", "cols", "owner", "platform_role", "owner_privileges"}).
				AddRow("security_barrier=true", def, strings.Join(v.columns, ","), owner, "platform", privileges))
	}
}

func expectAnalyticsOwnershipFunction(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("FROM pg_proc p").WithArgs(AnalyticsSchema).
		WillReturnRows(sqlmock.NewRows([]string{"src", "cfg", "secdef", "vol"}).
			AddRow(readerTenantBody(), readerTenantSearchPath, false, "s"))
}

func TestAnalyticsViewOwnership(t *testing.T) {
	for _, tc := range []struct {
		name      string
		foreign   string
		inherited string
	}{
		{name: "platform owned"},
		{name: "inherited owner privileges", inherited: "measurement_rollups"},
		{name: "foreign owner", foreign: "measurement_rollups"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock := analyticsOwnershipDB(t)
			expectAnalyticsOwnershipViews(mock, tc.foreign, tc.inherited, false)
			expectAnalyticsOwnershipFunction(mock)

			intact, reason, err := analyticsSurfaceIntact(db)
			if tc.foreign == "" {
				require.NoError(t, err)
				require.True(t, intact, reason)
			} else {
				// On the old implementation this is true, despite the extra catalog
				// columns: the regression fails by value, not by SQL mismatch.
				require.False(t, intact, "a foreign-owned view must prevent the healthy path")
				require.ErrorContains(t, err, tc.foreign)
				require.ErrorContains(t, err, "ownership")
				require.ErrorContains(t, err, "foreign_owner")
				require.ErrorContains(t, err, "platform")
				require.ErrorContains(t, err, "operator")
				// The ownership failure precedes the function check. Read it here
				// to verify that the rest of the fixture really is intact too.
				ok, why, fnErr := functionIntact(db)
				require.NoError(t, fnErr)
				require.True(t, ok, why)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestAnalyticsOwnershipStopsReconciliation(t *testing.T) {
	for _, damagedFirst := range []bool{false, true} {
		name := "otherwise intact"
		if damagedFirst {
			name = "earlier view needs repair"
		}
		t.Run(name, func(t *testing.T) {
			db, mock := analyticsOwnershipDB(t)
			key := rdb.AdvisoryLockKey(analyticsLockName)
			mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_lock($1)")).
				WithArgs(key).WillReturnResult(sqlmock.NewResult(0, 1))
			// The existing identity-function refresh precedes view inspection.
			for _, stmt := range []string{createAnalyticsSchemaStmt(), createReaderTenantFnStmt()} {
				mock.ExpectExec(regexp.QuoteMeta(stmt)).WillReturnResult(sqlmock.NewResult(0, 0))
			}
			expectAnalyticsOwnershipViews(mock, "measurement_rollups", "", damagedFirst)
			// No repair or grant DDL is allowed after the ownership diagnosis.
			mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).
				WithArgs(key).WillReturnResult(sqlmock.NewResult(0, 1))

			err := ReconcileAnalyticsSurface(context.Background(), &rdb.RdbManager{Database: db})
			require.ErrorContains(t, err, "measurement_rollups")
			require.ErrorContains(t, err, "ownership")
			require.ErrorContains(t, err, "operator")
			require.NotContains(t, err.Error(), "rebuilding")
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
