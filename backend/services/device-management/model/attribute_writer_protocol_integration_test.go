// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/attrprotocol"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// These tests run against the migration chain on a real server, with the enforcing trigger the
// chain installs. They are the proof that the database, not a convention, refuses an unmarked
// write of a device's SHARED attribute row.

const protocolInsert = `INSERT INTO "device-management".entity_attributes
 (created_at, updated_at, tenant_id, entity_type, entity_id, scope, attr_key, value_type, value, last_updated)
 VALUES (now(), now(), ?, ?, ?, ?, ?, 'STRING', 'v', now())`

// protocolTx runs fn in a transaction, optionally marked as a protocol writer.
func protocolTx(t *testing.T, db *gorm.DB, marked bool, fn func(tx *gorm.DB) error) error {
	t.Helper()
	return db.Transaction(func(tx *gorm.DB) error {
		if marked {
			if err := tx.Exec(attrprotocol.MarkerSQL).Error; err != nil {
				return err
			}
		}
		return fn(tx)
	})
}

// markAsProtocolWriter makes a hand-driven transaction behave as the v1.9-and-later writer it
// stands in for. The database now refuses a device SHARED attribute write without the marker, so a
// test that plays a pod of the supported previous release must mark exactly as that pod does.
func markAsProtocolWriter(t *testing.T, tx *gorm.DB) {
	t.Helper()
	if err := tx.Exec(attrprotocol.MarkerSQL).Error; err != nil {
		t.Fatal(err)
	}
}

func requireDCAP1(t *testing.T, err error, what string) {
	t.Helper()
	require.Error(t, err, what)
	require.Equal(t, "DCAP1", attributeSQLState(err), what)
}

func TestTheDatabaseRefusesAnUnmarkedDeviceSharedAttributeWrite(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	db := api.RDB.Database
	tenant := fmt.Sprintf("dcap%d", time.Now().UnixNano())
	var seq uint = 900000000
	next := func() uint { seq++; return seq }
	t.Cleanup(func() {
		require.NoError(t, protocolTx(t, db, true, func(tx *gorm.DB) error {
			return tx.Exec(`DELETE FROM "device-management".entity_attributes WHERE tenant_id = ?`, tenant).Error
		}))
	})

	insert := func(tx *gorm.DB, entityType string, id uint, scope, key string) error {
		return tx.Exec(protocolInsert, tenant, entityType, id, scope, key).Error
	}

	// The negative control: a marked writer is not in the way, for every operation.
	id := next()
	require.NoError(t, protocolTx(t, db, true, func(tx *gorm.DB) error { return insert(tx, "device", id, "SHARED", "k") }))
	require.NoError(t, protocolTx(t, db, true, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE "device-management".entity_attributes SET value = 'w' WHERE tenant_id = ? AND entity_id = ?`, tenant, id).Error
	}))

	// An unmarked writer is refused on INSERT, UPDATE and DELETE.
	requireDCAP1(t, protocolTx(t, db, false, func(tx *gorm.DB) error { return insert(tx, "device", next(), "SHARED", "k") }), "insert")
	requireDCAP1(t, protocolTx(t, db, false, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE "device-management".entity_attributes SET value = 'x' WHERE tenant_id = ? AND entity_id = ?`, tenant, id).Error
	}), "update")
	requireDCAP1(t, protocolTx(t, db, false, func(tx *gorm.DB) error {
		return tx.Exec(`DELETE FROM "device-management".entity_attributes WHERE tenant_id = ? AND entity_id = ?`, tenant, id).Error
	}), "delete")
	// A soft delete is an UPDATE of deleted_at.
	requireDCAP1(t, protocolTx(t, db, false, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE "device-management".entity_attributes SET deleted_at = now() WHERE tenant_id = ? AND entity_id = ?`, tenant, id).Error
	}), "soft delete")
	// The marker must be a '1': another value is not the protocol.
	requireDCAP1(t, db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Exec(`SELECT set_config('devicechain.attr_protocol', '0', true)`).Error)
		return insert(tx, "device", next(), "SHARED", "k")
	}), "marker 0")
	// The marker is transaction-local: it does not leak to the next transaction on the connection.
	require.NoError(t, protocolTx(t, db, true, func(tx *gorm.DB) error { return insert(tx, "device", next(), "SHARED", "k2") }))
	requireDCAP1(t, protocolTx(t, db, false, func(tx *gorm.DB) error { return insert(tx, "device", next(), "SHARED", "k3") }), "after a marked transaction")

	// Either image counts on UPDATE: moving a row INTO or OUT OF device SHARED is a write to one.
	other := next()
	require.NoError(t, protocolTx(t, db, true, func(tx *gorm.DB) error { return insert(tx, "device", other, "SERVER", "s") }))
	requireDCAP1(t, protocolTx(t, db, false, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE "device-management".entity_attributes SET scope = 'SHARED' WHERE tenant_id = ? AND entity_id = ?`, tenant, other).Error
	}), "into shared")
	require.NoError(t, protocolTx(t, db, true, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE "device-management".entity_attributes SET scope = 'SHARED' WHERE tenant_id = ? AND entity_id = ?`, tenant, other).Error
	}))
	requireDCAP1(t, protocolTx(t, db, false, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE "device-management".entity_attributes SET scope = 'SERVER' WHERE tenant_id = ? AND entity_id = ?`, tenant, other).Error
	}), "out of shared")

	// Everything that is not a device's SHARED row is untouched: other scopes and other owners
	// keep writing without the marker, so no ordinary writer is slowed or refused.
	for i, row := range []struct{ entityType, scope string }{
		{"device", "SERVER"}, {"device", "CLIENT"}, {"asset", "SHARED"}, {"customer", "SHARED"}, {"area", "SHARED"},
	} {
		eid := next()
		require.NoError(t, protocolTx(t, db, false, func(tx *gorm.DB) error { return insert(tx, row.entityType, eid, row.scope, fmt.Sprintf("u%d", i)) }), row)
		require.NoError(t, protocolTx(t, db, false, func(tx *gorm.DB) error {
			return tx.Exec(`DELETE FROM "device-management".entity_attributes WHERE tenant_id = ? AND entity_id = ?`, tenant, eid).Error
		}), row)
	}
}

// The tenant purge marks its own transaction; the purge executor's contract is covered by its own
// tests. What this proves is the readiness and activation answers over the shipped trigger.
func TestTheShippedGuardReadsAsEnforcedAndActivationHoldsItInPlace(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	db := api.RDB.Database
	ctx := core.WithTenant(context.Background(), "dcapgate")
	require.Equal(t, "ENFORCED", api.FleetOperationEnforcement(ctx))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return CheckFleetOperationActivation(tx) }))

	// Outside a transaction the check refuses to answer: a lock outside one lasts a statement.
	require.Error(t, CheckFleetOperationActivation(db))

	// Once a transaction holds the activation check, the guard cannot be disabled or dropped
	// until it ends: that is why the check locks BEFORE it reads the catalog.
	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- db.Transaction(func(tx *gorm.DB) error {
			if err := CheckFleetOperationActivation(tx); err != nil {
				close(held)
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	for _, ddl := range []string{
		`ALTER TABLE "device-management".entity_attributes DISABLE TRIGGER dc_enforce_attr_protocol`,
		`DROP TRIGGER dc_enforce_attr_protocol ON "device-management".entity_attributes`,
	} {
		err := db.Transaction(func(tx *gorm.DB) error {
			require.NoError(t, tx.Exec(`SET LOCAL lock_timeout = '300ms'`).Error)
			return tx.Exec(ddl).Error
		})
		require.Error(t, err, ddl)
		require.Equal(t, "55P03", attributeSQLState(err), ddl)
	}
	close(release)
	require.NoError(t, <-done)
	require.Equal(t, "ENFORCED", api.FleetOperationEnforcement(ctx))
}

// Everything below changes the shared trigger for the length of one subtest, inside a transaction
// that is rolled back, so no other test sees it. DDL is transactional in PostgreSQL.
func TestTheActivationCheckFailsClosedUnlessTheGuardIsExactlyTheShippedOne(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	db := api.RDB.Database
	const table = `"device-management".entity_attributes`
	const trigger = "dc_enforce_attr_protocol"
	const fn = `"device-management".dc_require_attr_protocol`
	errRollback := errors.New("rollback")
	for _, tc := range []struct {
		name string
		ddl  []string
	}{
		{"disabled", []string{`ALTER TABLE ` + table + ` DISABLE TRIGGER ` + trigger}},
		{"enabled but not ALWAYS", []string{`ALTER TABLE ` + table + ` ENABLE TRIGGER ` + trigger}},
		{"enabled for replicas only", []string{`ALTER TABLE ` + table + ` ENABLE REPLICA TRIGGER ` + trigger}},
		{"dropped", []string{`DROP TRIGGER ` + trigger + ` ON ` + table}},
		{"missing UPDATE", []string{
			`DROP TRIGGER ` + trigger + ` ON ` + table,
			`CREATE TRIGGER ` + trigger + ` BEFORE INSERT OR DELETE ON ` + table + ` FOR EACH ROW EXECUTE FUNCTION ` + fn + `()`,
			`ALTER TABLE ` + table + ` ENABLE ALWAYS TRIGGER ` + trigger}},
		{"conditional", []string{
			`DROP TRIGGER ` + trigger + ` ON ` + table,
			`CREATE TRIGGER ` + trigger + ` BEFORE INSERT OR UPDATE OR DELETE ON ` + table + ` FOR EACH ROW WHEN (false) EXECUTE FUNCTION ` + fn + `()`,
			`ALTER TABLE ` + table + ` ENABLE ALWAYS TRIGGER ` + trigger}},
		{"statement level", []string{
			`DROP TRIGGER ` + trigger + ` ON ` + table,
			`CREATE TRIGGER ` + trigger + ` BEFORE INSERT OR UPDATE OR DELETE ON ` + table + ` FOR EACH STATEMENT EXECUTE FUNCTION ` + fn + `()`,
			`ALTER TABLE ` + table + ` ENABLE ALWAYS TRIGGER ` + trigger}},
		{"right name, silenced body", []string{
			`CREATE OR REPLACE FUNCTION ` + fn + `() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF TG_OP = 'DELETE' THEN RETURN OLD; END IF; RETURN NEW; END; $$`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := db.Transaction(func(tx *gorm.DB) error {
				for _, stmt := range tc.ddl {
					require.NoError(t, tx.Exec(stmt).Error, stmt)
				}
				require.ErrorIs(t, CheckFleetOperationActivation(tx), ErrFleetOperationNotActivated)
				return errRollback
			})
			require.Equal(t, errRollback, err)
		})
	}
	// The negative control: the shipped guard, untouched, passes the same check.
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return CheckFleetOperationActivation(tx) }))
}

// The migration is individually re-runnable and repairs what a failed or tampered earlier run left.
func TestTheWriterProtocolMigrationReplaysAndRepairs(t *testing.T) {
	api := NewApi(newPostgresRdbManager(t))
	db := api.RDB.Database
	migration := schema.NewAttributeWriterProtocolSchema()
	const table = `"device-management".entity_attributes`
	const trigger = "dc_enforce_attr_protocol"
	ctx := core.WithTenant(context.Background(), "dcapreplay")
	errRollback := errors.New("rollback")
	for name, ddl := range map[string][]string{
		"as shipped":            nil,
		"trigger disabled":      {`ALTER TABLE ` + table + ` DISABLE TRIGGER ` + trigger},
		"trigger dropped":       {`DROP TRIGGER ` + trigger + ` ON ` + table},
		"function body changed": {`CREATE OR REPLACE FUNCTION "device-management".dc_require_attr_protocol() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$`},
	} {
		t.Run(name, func(t *testing.T) {
			err := db.Transaction(func(tx *gorm.DB) error {
				for _, stmt := range ddl {
					require.NoError(t, tx.Exec(stmt).Error, stmt)
				}
				require.NoError(t, migration.Migrate(tx))
				require.NoError(t, migration.Migrate(tx), "a second replay changes nothing")
				require.Equal(t, "ENFORCED", fleetOperationEnforcement(func() ([]string, error) { return fleetOperationEnforcementStates(tx) }))
				return errRollback
			})
			require.Equal(t, errRollback, err)
		})
	}
	require.Equal(t, "ENFORCED", api.FleetOperationEnforcement(ctx))
}
