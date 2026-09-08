// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// THE LOSING SIDE OF A RENAME RACE GETS THE SAME SENTENCE AS THE UNCONTENDED REFUSAL.
//
// 🔴 THE LOOKUP INSIDE RenameConnector'S TRANSACTION IS NOT THE WHOLE ANSWER, AND THAT IS
// WHAT THESE TESTS EXIST FOR. At READ COMMITTED a SELECT cannot lock a row that does not
// exist, so two renames onto one token — or a rename racing a create — both see zero rows,
// and the second UPDATE discovers the collision at the partial unique index instead.
// Without a translation the loser is handed `duplicate key value violates unique constraint
// "uix_connectors_tenant_token" (SQLSTATE 23505)`, which is not something a client can write
// a handler against and is not what the served API reference promises.
//
// The uncontended refusal is covered by TestRenameConnector_RefusesATokenAlreadyInUse. What
// is here is the contended one, driven three ways because no single one is enough alone:
//
//  1. against a REAL unique index, through the REAL RenameConnector, with the colliding row
//     appearing in exactly the window the race opens — the end-to-end claim;
//  2. against the real Postgres message text, spelled out, so the branch production takes is
//     exercised even though these tests run on SQLite;
//  3. against the index NAME the migration builds, so the constant this hangs on cannot
//     drift away from the schema in silence.

// insertOnFirstConnectorUpdate reproduces the race window deterministically: a one-shot
// callback that runs just before the UPDATE and inserts the colliding row through the SAME
// connection, which is the state a concurrent committer leaves behind — the rename's lookup
// has already run and seen nothing, and the row exists by the time the write executes.
//
// 🔴 A REAL TWO-GOROUTINE RACE WAS REJECTED FOR THIS. It would be timing-dependent, and on
// SQLite the loser is as likely to get "database is locked" as the constraint error, so the
// test would be measuring the fixture's locking rather than the translation. What is being
// asserted is the OUTCOME of losing, not that losing is reachable — the index guarantees
// that.
func insertOnFirstConnectorUpdate(t *testing.T, api *Api, ctx context.Context, tenant, token string) {
	t.Helper()
	db := api.RDB.Database
	fired := false
	name := "test:insert_before_connector_update"
	err := db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if fired || tx.Statement.Table != "connectors" {
			return
		}
		fired = true
		if _, err := tx.Statement.ConnPool.ExecContext(ctx,
			"INSERT INTO connectors (created_at, updated_at, tenant_id, token, type, config) "+
				"VALUES (CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, ?, ?, ?, ?)",
			tenant, token, string(ConnectorTypeMQTT), mqttConfig); err != nil {
			t.Errorf("could not plant the racing row, so the collision this test needs never "+
				"happened and a pass would mean nothing: %v", err)
		}
	})
	if err != nil {
		t.Fatalf("register the racing callback: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(name) })
}

// THE END-TO-END CLAIM. A rename whose target appears between the lookup and the write is
// refused by name, not by SQLSTATE.
func TestRenameConnector_ARacedTokenIsRefusedByTheSameName(t *testing.T) {
	api := newTestApi(t) // newTestApi builds the real per-tenant token index
	ctx := core.WithTenant(context.Background(), "acme")
	if _, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "conn-a", Name: strp("Original"), Type: string(ConnectorTypeMQTT), Config: mqttConfig,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	insertOnFirstConnectorUpdate(t, api, ctx, "acme", "conn-b")

	_, err := api.RenameConnector(ctx, "conn-a", "conn-b")
	if err == nil {
		t.Fatal("the raced rename succeeded, which means two connectors now hold one token or " +
			"the collision was never planted")
	}

	// 🔴 THE ASSERTION IS ON BOTH HALVES: the sentence the API promises IS there, and the
	// driver text it replaces is NOT. Checking only the first would pass on an error that
	// concatenated the two, which is what a naive wrap produces.
	want := ErrConnectorTokenTaken("conn-a", "conn-b").Error()
	if err.Error() != want {
		t.Fatalf("the losing racer got:\n  %v\nwant exactly the uncontended refusal:\n  %s", err, want)
	}
	for _, leak := range []string{"SQLSTATE", "23505", "constraint", connectorTokenIndexName} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the refusal still carries driver detail (%q): %v", leak, err)
		}
	}

	// And the rename did not half-apply: the transaction rolled back, so the connector is
	// still findable by its own token and still holds its name.
	rows, ferr := api.ConnectorsByToken(ctx, []string{"conn-a"})
	if ferr != nil || len(rows) != 1 {
		t.Fatalf("the connector is no longer findable by its own token: err=%v rows=%d", ferr, len(rows))
	}
	if rows[0].Name.String != "Original" {
		t.Fatalf("the refused rename still wrote the row: %+v", rows[0])
	}
}

// 🔴 THE NEGATIVE CONTROL. Without the translation the loser gets the raw driver text — this
// is what the test above is worth, stated by showing the untranslated outcome rather than
// asserting it is impossible. It drives the SAME planted collision through a bare write, so
// a reader can see the two messages side by side.
func TestRenameConnector_WithoutTheTranslationTheRacerGetsDriverText(t *testing.T) {
	api := newTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	if _, err := api.CreateConnector(ctx, &ConnectorCreateRequest{
		Token: "conn-a", Type: string(ConnectorTypeMQTT), Config: mqttConfig,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	rows, _ := api.ConnectorsByToken(ctx, []string{"conn-a"})
	insertOnFirstConnectorUpdate(t, api, ctx, "acme", "conn-b")

	raw := api.RDB.DB(ctx).Model(rows[0]).Update("token", "conn-b").Error
	if raw == nil {
		t.Fatal("the untranslated write succeeded, so this control proves nothing about what " +
			"the translation is protecting the caller from")
	}
	if !rdb.IsUniqueViolation(raw, connectorTokenIndexName, "connectors.token") {
		t.Fatalf("the raw failure is not the collision this suite is about: %v", raw)
	}
	if raw.Error() == ErrConnectorTokenTaken("conn-a", "conn-b").Error() {
		t.Fatal("the raw driver error already reads as the API's sentence, so the end-to-end " +
			"test above would pass with the translation removed")
	}
}

// THE COUNTERWEIGHT. The translation must not swallow an unrelated write failure into "that
// token is taken", which would be a worse lie than the driver text: it names a cause the
// caller can act on, and acting on it would not help.
func TestRenameConnector_AnUnrelatedWriteFailureIsNotReportedAsACollision(t *testing.T) {
	for name, err := range map[string]error{
		"connection lost": fmt.Errorf("driver: bad connection"),
		"another table":   fmt.Errorf(`UNIQUE constraint failed: connector_versions.connector_id, connector_versions.version`),
		"a different index": fmt.Errorf(`duplicate key value violates unique constraint ` +
			`"uix_connector_versions_connector_version" (SQLSTATE 23505)`),
		"no error": nil,
	} {
		t.Run(name, func(t *testing.T) {
			if rdb.IsUniqueViolation(err, connectorTokenIndexName, "connectors.token") {
				t.Fatalf("%v was classified as a connector-token collision", err)
			}
		})
	}
}

// THE PRODUCTION BRANCH, which the SQLite fixture above cannot reach.
//
// 🔴 THE MESSAGE IS SPELLED OUT IN FULL RATHER THAN BUILT FROM connectorTokenIndexName, AND
// THAT IS THE WHOLE VALUE OF THIS TEST. A fixture assembled from the constant matches the
// constant whatever the constant says, so it would pass just as happily after a typo moved
// the name away from the index the migration actually creates. Written out, a drifting
// constant fails here.
func TestRenameConnector_ThePostgresUniqueViolationIsRecognised(t *testing.T) {
	pgError := fmt.Errorf(`ERROR: duplicate key value violates unique constraint ` +
		`"uix_connectors_tenant_token" (SQLSTATE 23505)`)
	if !rdb.IsUniqueViolation(pgError, connectorTokenIndexName, "connectors.token") {
		t.Fatalf("the Postgres unique violation was not recognised, so production — which runs "+
			"on Postgres, not on this test's SQLite — would hand the loser raw driver text:\n  %v",
			pgError)
	}
	sqliteError := fmt.Errorf("UNIQUE constraint failed: connectors.tenant_id, connectors.token")
	if !rdb.IsUniqueViolation(sqliteError, connectorTokenIndexName, "connectors.token") {
		t.Fatalf("the SQLite unique violation was not recognised, so the end-to-end test above "+
			"would be passing for a reason other than the translation:\n  %v", sqliteError)
	}
}

// 🔴 THE CONSTANT MUST NAME THE INDEX THE MIGRATION ACTUALLY BUILDS, and the two are spelled
// in different packages because schema/baseline.go's createTenantTokenIndex is unexported.
// This re-derives the name by that helper's own rule — "uix_" + the bare table + "_tenant_token"
// — from GORM's table name for the live model, which is the same table the migration parses.
// A rename of the table moves both together and this still passes, correctly; a constant
// edited by hand away from the rule does not.
func TestConnectorTokenIndexNameMatchesTheMigration(t *testing.T) {
	api := newTestApi(t)
	stmt := &gorm.Statement{DB: api.RDB.Database}
	if err := stmt.Parse(&Connector{}); err != nil {
		t.Fatalf("parse the connector model: %v", err)
	}
	// The migration strips any schema qualifier before building the name; mirrored here so
	// the two agree under production's TablePrefix as well as under the bare fixture.
	bare := stmt.Table
	if i := strings.LastIndex(bare, "."); i >= 0 {
		bare = bare[i+1:]
	}
	want := "uix_" + bare + "_tenant_token"
	if connectorTokenIndexName != want {
		t.Fatalf("connectorTokenIndexName is %q but the migration's naming rule builds %q for "+
			"table %q — a losing racer would then get raw driver text, and nothing else in this "+
			"suite would notice", connectorTokenIndexName, want, bare)
	}
}
