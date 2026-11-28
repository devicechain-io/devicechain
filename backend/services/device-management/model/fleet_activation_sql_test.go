// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Execute the catalog predicates over fixture rows, translating only PostgreSQL
// syntax/functions. The integration suite owns real catalog and lock semantics.
type activationSQLRecorder struct {
	gorm.ConnPool
	calls              []fleetSQLCall
	failLock, failRead error
}

func (r *activationSQLRecorder) ExecContext(_ context.Context, q string, args ...any) (sql.Result, error) {
	r.calls = append(r.calls, fleetSQLCall{q, args})
	return fleetSQLResult{}, r.failLock
}

func (r *activationSQLRecorder) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	r.calls = append(r.calls, fleetSQLCall{q, args})
	if r.failRead != nil {
		return nil, r.failRead
	}
	q = strings.NewReplacer(
		"current_setting('transaction_isolation')", "(SELECT isolation FROM activation_environment)",
		"current_database()", "'activation_db'",
		"t.tgenabled::text", "t.tgenabled",
		"t.tgtype::integer", "t.tgtype",
		"md5(p.prosrc)", "p.prosrc",
		"unnest(s.setconfig) c", "json_each(s.setconfig) c",
		"c LIKE", "c.value LIKE",
	).Replace(q)
	return r.ConnPool.QueryContext(ctx, q, args...)
}

func (r *activationSQLRecorder) Commit() error {
	return r.ConnPool.(gorm.TxCommitter).Commit()
}

func (r *activationSQLRecorder) Rollback() error {
	return r.ConnPool.(gorm.TxCommitter).Rollback()
}

func activationCatalogFixture(t *testing.T) *gorm.DB {
	t.Helper()
	db := sqlitetest.OpenMemory(t, nil)
	for _, q := range []string{
		`ATTACH DATABASE ':memory:' AS pg_catalog`,
		`CREATE TABLE activation_environment (isolation text)`,
		`INSERT INTO activation_environment VALUES ('read committed')`,
		`CREATE TABLE pg_catalog.pg_namespace (oid integer, nspname text)`,
		`INSERT INTO pg_catalog.pg_namespace VALUES (1, 'device-management'), (2, 'other')`,
		`CREATE TABLE pg_catalog.pg_class (oid integer, relnamespace integer, relname text)`,
		`INSERT INTO pg_catalog.pg_class VALUES (3, 1, 'entity_attributes')`,
		`CREATE TABLE pg_catalog.pg_language (oid integer, lanname text)`,
		`INSERT INTO pg_catalog.pg_language VALUES (4, 'plpgsql')`,
		`CREATE TABLE pg_catalog.pg_proc (oid integer, pronamespace integer, proname text, pronargs integer, prosrc text, proconfig text, prolang integer)`,
		`CREATE TABLE pg_catalog.pg_trigger (tgrelid integer, tgfoid integer, tgname text, tgenabled text, tgisinternal boolean, tgtype integer, tgqual text)`,
		`CREATE TABLE pg_catalog.pg_database (oid integer, datname text)`,
		`INSERT INTO pg_catalog.pg_database VALUES (5, 'activation_db'), (6, 'other_db')`,
		`CREATE TABLE pg_catalog.pg_db_role_setting (setdatabase integer, setconfig text)`,
	} {
		require.NoError(t, db.Exec(q).Error)
	}
	require.NoError(t, db.Exec(`INSERT INTO pg_catalog.pg_proc VALUES (7, 1, ?, 0, ?, NULL, 4)`,
		FleetOperationEnforcementFunction, FleetOperationEnforcementFunctionMD5).Error)
	require.NoError(t, db.Exec(`INSERT INTO pg_catalog.pg_trigger VALUES (3, 7, ?, 'A', false, 31, NULL)`,
		FleetOperationEnforcementTrigger).Error)
	return db
}

func recordedActivation(t *testing.T, db *gorm.DB, failLock, failRead error) (error, []fleetSQLCall) {
	t.Helper()
	tx := db.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	cfg := *tx.Config
	tx.Config = &cfg
	tx.Dialector = fleetPGDialect{tx.Dialector}
	r := &activationSQLRecorder{ConnPool: tx.Statement.ConnPool, failLock: failLock, failRead: failRead}
	tx.Statement.ConnPool = r
	return CheckFleetOperationActivation(tx), r.calls
}

func TestActivationUsesOneReadAfterThePerLineLock(t *testing.T) {
	db := activationCatalogFixture(t)
	for line := 0; line < 3; line++ {
		err, calls := recordedActivation(t, db, nil, nil)
		require.NoError(t, err)
		require.Len(t, calls, 2, "each posting transaction needs only LOCK and one facts SELECT (previously four statements)")
		require.Equal(t, `LOCK TABLE "device-management".entity_attributes IN ROW EXCLUSIVE MODE`, calls[0].query)
		for _, fact := range []string{"transaction_isolation", "pg_trigger", "pg_proc", "pg_db_role_setting"} {
			require.Contains(t, calls[1].query, fact)
		}
	}
}

func TestActivationCatalogRefusals(t *testing.T) {
	for _, tc := range []struct{ name, change string }{
		{"repeatable read", `UPDATE activation_environment SET isolation = 'repeatable read'`},
		{"serializable", `UPDATE activation_environment SET isolation = 'serializable'`},
		{"read uncommitted", `UPDATE activation_environment SET isolation = 'read uncommitted'`},
		{"missing trigger", `DELETE FROM pg_catalog.pg_trigger`},
		{"disabled", `UPDATE pg_catalog.pg_trigger SET tgenabled = 'D'`},
		{"ordinary enabled", `UPDATE pg_catalog.pg_trigger SET tgenabled = 'O'`},
		{"replica only", `UPDATE pg_catalog.pg_trigger SET tgenabled = 'R'`},
		{"wrong table", `UPDATE pg_catalog.pg_class SET relname = 'other'`},
		{"wrong table schema", `UPDATE pg_catalog.pg_class SET relnamespace = 2`},
		{"wrong function schema", `UPDATE pg_catalog.pg_proc SET pronamespace = 2`},
		{"wrong function name", `UPDATE pg_catalog.pg_proc SET proname = 'other'`},
		{"function arguments", `UPDATE pg_catalog.pg_proc SET pronargs = 1`},
		{"wrong function body", `UPDATE pg_catalog.pg_proc SET prosrc = 'wrong hash'`},
		{"function setting", `UPDATE pg_catalog.pg_proc SET proconfig = 'devicechain.attr_protocol=1'`},
		{"wrong language", `UPDATE pg_catalog.pg_proc SET prolang = 0`},
		{"wrong trigger name", `UPDATE pg_catalog.pg_trigger SET tgname = 'other'`},
		{"internal trigger", `UPDATE pg_catalog.pg_trigger SET tgisinternal = true`},
		{"conditional", `UPDATE pg_catalog.pg_trigger SET tgqual = 'false'`},
		{"after", `UPDATE pg_catalog.pg_trigger SET tgtype = 29`},
		{"statement level", `UPDATE pg_catalog.pg_trigger SET tgtype = 30`},
		{"missing insert", `UPDATE pg_catalog.pg_trigger SET tgtype = 27`},
		{"missing delete", `UPDATE pg_catalog.pg_trigger SET tgtype = 23`},
		{"missing update", `UPDATE pg_catalog.pg_trigger SET tgtype = 15`},
		{"database default", `INSERT INTO pg_catalog.pg_db_role_setting VALUES (5, '["other=1","devicechain.attr_protocol=1"]')`},
		{"role default", `INSERT INTO pg_catalog.pg_db_role_setting VALUES (0, '["devicechain.attr_protocol=0"]')`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := activationCatalogFixture(t)
			require.NoError(t, db.Exec(tc.change).Error)
			err, _ := recordedActivation(t, db, nil, nil)
			require.ErrorIs(t, err, ErrFleetOperationNotActivated)
		})
	}
	for _, setting := range []string{
		`INSERT INTO pg_catalog.pg_db_role_setting VALUES (6, '["devicechain.attr_protocol=1"]')`,
		`INSERT INTO pg_catalog.pg_db_role_setting VALUES (5, '["other=1"]')`,
	} {
		db := activationCatalogFixture(t)
		require.NoError(t, db.Exec(setting).Error)
		err, _ := recordedActivation(t, db, nil, nil)
		require.NoError(t, err, "unrelated database/settings do not silence this guard")
	}
}

func TestActivationSQLFailuresStayErrors(t *testing.T) {
	failure := errors.New("activation SQL unavailable")
	for _, lockFails := range []bool{true, false} {
		db := activationCatalogFixture(t)
		var lockErr, readErr error
		if lockFails {
			lockErr = failure
		} else {
			readErr = failure
		}
		err, calls := recordedActivation(t, db, lockErr, readErr)
		require.ErrorIs(t, err, failure)
		require.NotErrorIs(t, err, ErrFleetOperationNotActivated, "an unreadable guard is an error, never a decided refusal")
		if lockFails {
			require.Len(t, calls, 1, "never read before acquiring the table lock")
		}
	}
	for _, catalog := range []string{"pg_trigger", "pg_proc", "pg_db_role_setting"} {
		db := activationCatalogFixture(t)
		require.NoError(t, db.Exec("DROP TABLE pg_catalog."+catalog).Error)
		err, _ := recordedActivation(t, db, nil, nil)
		require.Error(t, err)
		require.NotErrorIs(t, err, ErrFleetOperationNotActivated)
	}
}
