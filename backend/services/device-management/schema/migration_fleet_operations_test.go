// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var fleetTables = []string{
	"fleet_operations", "fleet_operation_runtime", "fleet_operation_contracts",
	"fleet_operation_lines", "fleet_operation_events",
}

const insertOperation = `INSERT INTO fleet_operations (tenant_id, token, kind, name, dispatch, request_digest, intent,
 target_kind, target_count, max_posts_per_second, eff_posts_per_second, settle_deadline_s, initiator, driver_protocol %s)
 VALUES ('acme', '%s', 'config.set', 'n', '%s', 'sha256:x', '{}', 'DEVICE_LIST', 1, 10, 10, 60, 'u', 1 %s)`

// protocolInsert states the protocol itself, for the tests of the column.
const protocolInsert = `INSERT INTO fleet_operations (tenant_id, token, kind, name, dispatch, request_digest, intent,
 target_kind, target_count, max_posts_per_second, eff_posts_per_second, settle_deadline_s, initiator, driver_protocol)
 VALUES ('acme', '%s', 'config.set', 'n', 'READY', 'sha256:x', '{}', 'DEVICE_LIST', 1, 10, 10, 60, 'u', %s)`

func insertOp(db *gorm.DB, token, dispatch string) error {
	return db.Exec(fmt.Sprintf(insertOperation, "", token, dispatch, "")).Error
}

const insertLine = `INSERT INTO fleet_operation_lines (tenant_id, operation_id, ordinal, target_id, posting %s)
 VALUES ('acme', %d, %d, %s, '%s' %s)`

func insertLn(db *gorm.DB, op, ordinal int, target, posting string) error {
	return db.Exec(fmt.Sprintf(insertLine, "", op, ordinal, target, posting, "")).Error
}

func TestFleetOperationsMigrationCreatesTheJournalsAndReplays(t *testing.T) {
	db := sqlitetest.OpenMemory(t, nil)
	migration := NewFleetOperationsSchema()
	require.NoError(t, migration.Migrate(db))
	for _, table := range fleetTables {
		require.True(t, db.Migrator().HasTable(table), table)
	}
	require.NoError(t, insertOp(db, "t1", "READY"))
	// A second run over a populated database changes nothing and loses nothing.
	require.NoError(t, migration.Migrate(db))
	var n int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM fleet_operations`).Scan(&n).Error)
	require.Equal(t, int64(1), n)
}

func TestFleetOperationsMigrationRecoversFromEveryPartialFailure(t *testing.T) {
	// Migrations run without a transaction and replay from the top, so a failure after any
	// statement must leave a state the replay completes. Drop each extra index in turn, and
	// then every table, and re-run: nothing may be left missing or doubled.
	for _, ix := range fleetOperationPartialIndexes {
		t.Run(ix.Name, func(t *testing.T) {
			db := sqlitetest.OpenMemory(t, nil)
			migration := NewFleetOperationsSchema()
			require.NoError(t, migration.Migrate(db))
			require.NoError(t, db.Exec(fmt.Sprintf(`DROP INDEX "%s"`, ix.Name)).Error)
			require.NoError(t, migration.Migrate(db))
			require.True(t, indexExists(t, db, ix.Name), "the replay did not rebuild %s", ix.Name)
		})
	}
	for _, table := range fleetTables {
		t.Run("missing "+table, func(t *testing.T) {
			db := sqlitetest.OpenMemory(t, nil)
			migration := NewFleetOperationsSchema()
			require.NoError(t, migration.Migrate(db))
			require.NoError(t, db.Exec(`DROP TABLE "`+table+`"`).Error)
			require.NoError(t, migration.Migrate(db))
			require.True(t, db.Migrator().HasTable(table))
			for _, ix := range fleetOperationPartialIndexes {
				require.True(t, indexExists(t, db, ix.Name), ix.Name)
			}
		})
	}
}

func indexExists(t *testing.T, db *gorm.DB, name string) bool {
	t.Helper()
	var n int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name = ?`, name).Scan(&n).Error)
	return n == 1
}

func TestFleetOperationsDriverProtocolIsStatedAndAtLeastOne(t *testing.T) {
	db := sqlitetest.OpenMemory(t, nil)
	require.NoError(t, NewFleetOperationsSchema().Migrate(db))
	require.NoError(t, insertOp(db, "t1", "READY"))
	var protocol int
	require.NoError(t, db.Raw(`SELECT driver_protocol FROM fleet_operations WHERE token='t1'`).Scan(&protocol).Error)
	require.Equal(t, 1, protocol)
	// There is no default: an insert that does not state the protocol is refused, so a creator
	// that forgets cannot silently produce a protocol-1 operation a later feature needed 2 for.
	const noProtocol = `INSERT INTO fleet_operations (tenant_id, token, kind, name, dispatch, request_digest, intent,
 target_kind, target_count, max_posts_per_second, eff_posts_per_second, settle_deadline_s, initiator)
 VALUES ('acme', 'np', 'config.set', 'n', 'READY', 'sha256:x', '{}', 'DEVICE_LIST', 1, 10, 10, 60, 'u')`
	require.Error(t, db.Exec(noProtocol).Error)
	// Negative controls: any stated protocol of at least 1 is fine; zero and below are refused.
	for i, p := range []string{"2", "7"} {
		require.NoError(t, db.Exec(fmt.Sprintf(protocolInsert, fmt.Sprintf("p%d", i), p)).Error, p)
	}
	for i, p := range []string{"0", "-1"} {
		require.Error(t, db.Exec(fmt.Sprintf(protocolInsert, fmt.Sprintf("bad%d", i), p)).Error, p)
	}
}

func TestFleetOperationsVocabularyChecks(t *testing.T) {
	db := sqlitetest.OpenMemory(t, nil)
	require.NoError(t, NewFleetOperationsSchema().Migrate(db))
	for i, dispatch := range []string{"PLANNING", "READY", "RUNNING", "PAUSED", "DISPATCHED", "ABORTED", "DRAFT"} {
		require.NoError(t, insertOp(db, fmt.Sprintf("ok%d", i), dispatch), dispatch)
	}
	require.Error(t, insertOp(db, "bad1", "ready"))
	require.Error(t, insertOp(db, "bad2", ""))
	require.Error(t, insertOp(db, "bad3", "DONE"))
	require.Error(t, db.Exec(fmt.Sprintf(insertOperation, ", evidence", "bad4", "READY", ", 'MAYBE'")).Error)
	require.NoError(t, db.Exec(fmt.Sprintf(insertOperation, ", evidence", "ok-closed", "READY", ", 'CLOSED'")).Error)

	for i, posting := range []string{"PLANNED", "POSTED", "REFUSED", "SKIPPED", "CANCELLED", "DISPATCHING", "POSTING_UNKNOWN"} {
		require.NoError(t, insertLn(db, 1, i, fmt.Sprint(100+i), posting), posting)
	}
	require.Error(t, insertLn(db, 1, 50, "150", "planned"))
	require.Error(t, insertLn(db, 1, 51, "151", "DONE"))
}

func TestFleetOperationLineStructuralChecks(t *testing.T) {
	db := sqlitetest.OpenMemory(t, nil)
	require.NoError(t, NewFleetOperationsSchema().Migrate(db))
	line := func(ordinal int, posting, extraCols, extraVals string) error {
		return db.Exec(fmt.Sprintf(insertLine, extraCols, 1, ordinal, fmt.Sprint(1000+ordinal), posting, extraVals)).Error
	}
	// A settlement exists only on a POSTED line, and only from the vocabulary.
	require.NoError(t, line(1, "POSTED", ", settlement", ", 'CONFIRMED'"))
	for i, s := range []string{"FAILED", "SUPERSEDED", "AMBIGUOUS", "RESOLVED_CONFIRMED", "RESOLVED_FAILED"} {
		require.NoError(t, line(10+i, "POSTED", ", settlement", ", '"+s+"'"), s)
	}
	require.Error(t, line(2, "PLANNED", ", settlement", ", 'CONFIRMED'"))
	require.Error(t, line(3, "REFUSED", ", settlement", ", 'FAILED'"))
	require.Error(t, line(4, "POSTED", ", settlement", ", 'DONE'"))
	// A before-image exists only on a POSTED line (a NULL one is allowed anywhere).
	require.NoError(t, line(5, "POSTED", ", prior", ", '{}'"))
	require.Error(t, line(6, "PLANNED", ", prior", ", '{}'"))
	require.Error(t, line(7, "CANCELLED", ", prior", ", '{}'"))
	require.NoError(t, line(8, "PLANNED", "", ""))
}

func TestFleetOperationsUniqueness(t *testing.T) {
	db := sqlitetest.OpenMemory(t, nil)
	require.NoError(t, NewFleetOperationsSchema().Migrate(db))
	// (tenant, token) is unique and NOT partial: the header has no deleted_at to hide behind.
	require.NoError(t, insertOp(db, "t1", "READY"))
	require.Error(t, insertOp(db, "t1", "READY"))
	require.NoError(t, insertOp(db, "t2", "READY"))
	require.NoError(t, db.Exec(`UPDATE fleet_operations SET tenant_id='globex' WHERE token='t2'`).Error)
	require.NoError(t, insertOp(db, "t2", "READY"), "the same token under another tenant is a different operation")

	// One line per ordinal and one per device in an operation; another operation is free.
	require.NoError(t, insertLn(db, 1, 1, "7", "PLANNED"))
	require.Error(t, insertLn(db, 1, 1, "8", "PLANNED"), "duplicate ordinal")
	require.Error(t, insertLn(db, 1, 2, "7", "PLANNED"), "duplicate device")
	require.NoError(t, insertLn(db, 2, 1, "7", "PLANNED"))
	// Unresolved inputs have no target id and may repeat.
	require.NoError(t, insertLn(db, 1, 3, "NULL", "REFUSED"))
	require.NoError(t, insertLn(db, 1, 4, "NULL", "REFUSED"))

	// One successful contra line per reversed line, counted only once POSTED.
	contra := func(op, ordinal, target int, posting string) error {
		return db.Exec(fmt.Sprintf(insertLine, ", reverses_line_id", op, ordinal, fmt.Sprint(target), posting, ", 1")).Error
	}
	require.NoError(t, contra(3, 1, 20, "POSTED"))
	require.Error(t, contra(3, 2, 21, "POSTED"))
	require.NoError(t, contra(3, 3, 22, "REFUSED"), "a refused contra does not hold the slot")

	// One contradiction marker per line.
	event := func(line int, kind string) error {
		return db.Exec(fmt.Sprintf(`INSERT INTO fleet_operation_events (tenant_id, operation_id, line_id, at, kind)
 VALUES ('acme', 1, %d, CURRENT_TIMESTAMP, '%s')`, line, kind)).Error
	}
	require.NoError(t, event(5, "CONTRADICTING_EVIDENCE"))
	require.Error(t, event(5, "CONTRADICTING_EVIDENCE"))
	require.NoError(t, event(5, "VERDICT"))
	require.NoError(t, event(5, "VERDICT"))
	require.NoError(t, event(6, "CONTRADICTING_EVIDENCE"))

	// A contract version is appended once per operation.
	contract := func(op, version int) error {
		return db.Exec(fmt.Sprintf(`INSERT INTO fleet_operation_contracts (tenant_id, operation_id, version, digest, body,
 approval_kind, policy_version, proposed_by) VALUES ('acme', %d, %d, 'd', '{}', 'HUMAN_DIRECT', 1, 'u')`, op, version)).Error
	}
	require.NoError(t, contract(1, 1))
	require.Error(t, contract(1, 1))
	require.NoError(t, contract(1, 2))
	require.NoError(t, contract(2, 1))
}

func TestFleetOperationsHaveNoForeignKeysAndNoSoftDelete(t *testing.T) {
	// A journal must not block a device delete, and nothing here is ever soft-deleted: a
	// previous-release pod deleting a device has to succeed while an operation targeting it
	// is in flight.
	db := sqlitetest.OpenMemory(t, nil)
	require.NoError(t, NewFleetOperationsSchema().Migrate(db))
	for _, table := range fleetTables {
		var fks []struct{ Table string }
		require.NoError(t, db.Raw(fmt.Sprintf("PRAGMA foreign_key_list(%s)", table)).Scan(&fks).Error)
		require.Empty(t, fks, table)
		require.False(t, db.Migrator().HasColumn(table, "deleted_at"), table)
		require.True(t, db.Migrator().HasColumn(table, "tenant_id"), table)
	}
}

func TestFleetOperationsMigrationDoesNotRollBack(t *testing.T) {
	db := sqlitetest.OpenMemory(t, nil)
	require.Error(t, NewFleetOperationsSchema().Rollback(db))
}
