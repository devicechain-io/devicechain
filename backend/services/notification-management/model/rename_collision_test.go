// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// THE LOSING SIDE OF A RENAME RACE GETS THE SAME SENTENCE AS THE UNCONTENDED REFUSAL.
//
// 🔴 THE LOOKUP INSIDE RenameNotificationChannel'S TRANSACTION IS NOT THE WHOLE ANSWER, AND
// THAT IS WHAT THESE TESTS EXIST FOR. At READ COMMITTED a SELECT cannot lock a row that
// does not exist, so two renames onto one token — or a rename racing a create — both see
// zero rows, and the second UPDATE discovers the collision at the partial unique index
// instead. Without a translation the loser is handed
// `duplicate key value violates unique constraint "uix_notification_channels_tenant_token"
// (SQLSTATE 23505)`, which is not something a client can write a handler against and is not
// what the served API reference promises.
//
// The uncontended refusal is covered by TestRenameChannel_ATakenTokenIsRefusedByName in
// api_token_argument_test.go. What is here is the contended one, and it is driven three
// ways, because no single one of them is enough on its own:
//
//  1. against a REAL unique index, through the REAL RenameNotificationChannel, with the
//     colliding row appearing in exactly the window the race opens — the end-to-end claim;
//  2. against the real Postgres message text, spelled out, so the branch production takes
//     is exercised even though these tests run on SQLite;
//  3. against the index NAME the migration builds, so the constant this all hangs on cannot
//     drift away from the schema in silence.

// newCollisionApi is newTestApi plus the per-tenant partial unique index the real migration
// creates. The ordinary fixture deliberately omits it (see newTestApi), which is why the
// uncontended check is the only thing those tests can observe — here the index is the point.
func newCollisionApi(t *testing.T) *Api {
	t.Helper()
	api := newTestApi(t)
	if err := api.RDB.Database.Exec(
		"CREATE UNIQUE INDEX " + channelTokenIndexName +
			" ON notification_channels (tenant_id, token) WHERE deleted_at IS NULL").Error; err != nil {
		t.Fatalf("create the tenant/token index: %v", err)
	}
	return api
}

// insertOnFirstUpdate reproduces the race window deterministically: it registers a one-shot
// callback that runs just before the UPDATE statement and inserts the colliding row through
// the SAME connection, which is the state a concurrent committer leaves behind — the
// rename's lookup has already run and seen nothing, and the row exists by the time the write
// executes.
//
// 🔴 A REAL TWO-GOROUTINE RACE WAS REJECTED FOR THIS. It would be timing-dependent, and on
// SQLite the loser is as likely to get "database is locked" as the constraint error, so the
// test would be measuring the fixture's locking rather than the translation. What is being
// asserted is the OUTCOME of losing, not that losing is reachable — the index guarantees
// that, and the comment on ErrChannelTokenTaken says why.
func insertOnFirstUpdate(t *testing.T, api *Api, ctx context.Context, tenant, token string) {
	t.Helper()
	db := api.RDB.Database
	fired := false
	name := "test:insert_before_update"
	err := db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if fired || tx.Statement.Table != "notification_channels" {
			return
		}
		fired = true
		if _, err := tx.Statement.ConnPool.ExecContext(ctx,
			"INSERT INTO notification_channels (created_at, updated_at, tenant_id, token, channel_type, enabled) "+
				"VALUES (CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, ?, ?, ?, ?)",
			tenant, token, ChannelTypeSMTP, true); err != nil {
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
func TestRenameChannel_ARacedTokenIsRefusedByTheSameName(t *testing.T) {
	api := newCollisionApi(t)
	ctx := tenantCtx("A")
	if _, err := api.CreateNotificationChannel(ctx, &NotificationChannelCreateRequest{
		Token: "chan-a", Name: strPtr("Original"), ChannelType: ChannelTypeWebhook, Enabled: true,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	insertOnFirstUpdate(t, api, ctx, "A", "chan-b")

	_, err := api.RenameNotificationChannel(ctx, "chan-a", "chan-b")
	if err == nil {
		t.Fatal("the raced rename succeeded, which means two channels now hold one token or " +
			"the collision was never planted")
	}

	// 🔴 THE ASSERTION IS ON BOTH HALVES: the sentence the API promises IS there, and the
	// driver text it replaces is NOT. Checking only the first would pass on an error that
	// concatenated the two, which is what a naive wrap produces.
	want := ErrChannelTokenTaken("chan-a", "chan-b").Error()
	if err.Error() != want {
		t.Fatalf("the losing racer got:\n  %v\nwant exactly the uncontended refusal:\n  %s", err, want)
	}
	for _, leak := range []string{"SQLSTATE", "23505", "constraint", channelTokenIndexName} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the refusal still carries driver detail (%q): %v", leak, err)
		}
	}

	// And the rename did not half-apply: the transaction rolled back, so the channel is
	// still findable by its own token and still holds its name.
	rows, ferr := api.NotificationChannelsByToken(ctx, []string{"chan-a"})
	if ferr != nil || len(rows) != 1 {
		t.Fatalf("the channel is no longer findable by its own token: err=%v rows=%d", ferr, len(rows))
	}
	if rows[0].Name.String != "Original" {
		t.Fatalf("the refused rename still wrote the row: %+v", rows[0])
	}
}

// THE COUNTERWEIGHT. The translation must not swallow an unrelated write failure into "that
// token is taken", which would be a worse lie than the driver text: it names a cause the
// caller can act on, and acting on it would not help.
func TestRenameChannel_AnUnrelatedWriteFailureIsNotReportedAsACollision(t *testing.T) {
	for name, err := range map[string]error{
		"connection lost":   fmt.Errorf("driver: bad connection"),
		"another table":     fmt.Errorf(`UNIQUE constraint failed: notification_policies.tenant_id, notification_policies.token`),
		"a different index": fmt.Errorf(`duplicate key value violates unique constraint "uix_notification_policies_tenant_token" (SQLSTATE 23505)`),
		"no error":          nil,
	} {
		t.Run(name, func(t *testing.T) {
			if isChannelTokenCollision(err) {
				t.Fatalf("%v was classified as a channel-token collision", err)
			}
		})
	}
}

// THE PRODUCTION BRANCH, which the SQLite fixture above cannot reach.
//
// 🔴 THE MESSAGE IS SPELLED OUT IN FULL RATHER THAN BUILT FROM channelTokenIndexName, AND
// THAT IS THE WHOLE VALUE OF THIS TEST. A fixture assembled from the constant matches the
// constant whatever the constant says, so it would pass just as happily after a typo moved
// the name away from the index the migration actually creates. Written out, a drifting
// constant fails here.
func TestRenameChannel_ThePostgresUniqueViolationIsRecognised(t *testing.T) {
	pgError := fmt.Errorf(`ERROR: duplicate key value violates unique constraint ` +
		`"uix_notification_channels_tenant_token" (SQLSTATE 23505)`)
	if !isChannelTokenCollision(pgError) {
		t.Fatalf("the Postgres unique violation was not recognised, so production — which runs "+
			"on Postgres, not on this test's SQLite — would hand the loser raw driver text:\n  %v",
			pgError)
	}
	sqliteError := fmt.Errorf("UNIQUE constraint failed: notification_channels.tenant_id, " +
		"notification_channels.token")
	if !isChannelTokenCollision(sqliteError) {
		t.Fatalf("the SQLite unique violation was not recognised, so the end-to-end test above "+
			"would be passing for a reason other than the translation:\n  %v", sqliteError)
	}
}

// 🔴 THE CONSTANT MUST NAME THE INDEX THE MIGRATION ACTUALLY BUILDS, and the two are spelled
// in different packages because schema/baseline.go's createTenantTokenIndex is unexported.
// This re-derives the name by that helper's own rule — "uix_" + table + "_tenant_token" —
// from GORM's table name for the live model, which is the same table the migration parses.
// A rename of the table moves both together and this still passes, correctly; a constant
// edited by hand away from the rule does not.
func TestRenameCollisionIndexNameMatchesTheTable(t *testing.T) {
	api := newTestApi(t)
	stmt := &gorm.Statement{DB: api.RDB.Database}
	if err := stmt.Parse(&NotificationChannel{}); err != nil {
		t.Fatalf("parse the channel model: %v", err)
	}
	// The migration strips any schema qualifier before building the name; mirrored here so
	// the two agree under production's TablePrefix as well as under the bare fixture.
	bare := stmt.Table
	if i := strings.LastIndex(bare, "."); i >= 0 {
		bare = bare[i+1:]
	}
	want := "uix_" + bare + "_tenant_token"
	if channelTokenIndexName != want {
		t.Fatalf("channelTokenIndexName is %q but the migration's naming rule builds %q for "+
			"table %q — a losing racer would then get raw driver text, and nothing else in this "+
			"suite would notice", channelTokenIndexName, want, bare)
	}
}
