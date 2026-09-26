// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/conflict"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// THE LOSING SIDE OF A RENAME RACE GETS THE SAME SENTENCE AS THE UNCONTENDED REFUSAL.
//
// 🔴 THE LOOKUP INSIDE RenameDeviceProfile'S TRANSACTION IS NOT THE WHOLE ANSWER, AND THAT
// IS WHAT THESE TESTS EXIST FOR. At READ COMMITTED a SELECT cannot lock a row that does not
// exist, so two renames onto one token — or a rename racing a create — both see zero rows,
// and the second UPDATE discovers the collision at the partial unique index instead.
// Without a translation the loser would get the GraphQL boundary's
// NEUTRAL conflict sentence (code CONFLICT) instead of this rename's own sentence, which
// is not what the served API reference promises.
//
// The uncontended refusal is covered by TestRenameDeviceProfile_RefusesATokenAlreadyInUse.
// What is here is the contended one.

// newProfileCollisionApi is the ordinary profile fixture plus the per-tenant partial unique
// index the real migration creates.
//
// 🔴 THE ORDINARY FIXTURE DELIBERATELY OMITS THE INDEX, which is why every other test in
// this package can only observe the uncontended check — here the index IS the subject, so
// building it is the first thing that has to be true. The predicate mirrors the migration's:
// unique on (tenant_id, token) among rows that are not soft-deleted.
func newProfileCollisionApi(t *testing.T) *Api {
	t.Helper()
	api := newPartialUpdateApi(t, deviceProfileTables...)
	if err := rdb.CreateTenantTokenIndex(api.RDB.Database, &DeviceProfile{}); err != nil {
		t.Fatalf("create the tenant/token index: %v", err)
	}
	return api
}

// insertOnFirstProfileUpdate reproduces the race window deterministically: a one-shot
// callback that runs just before the UPDATE and inserts the colliding row through the SAME
// connection, which is the state a concurrent committer leaves behind — the rename's lookup
// has already run and seen nothing, and the row exists by the time the write executes.
//
// 🔴 A REAL TWO-GOROUTINE RACE WAS REJECTED FOR THIS. It would be timing-dependent, and on
// SQLite the loser is as likely to get "database is locked" as the constraint error, so the
// test would be measuring the fixture's locking rather than the translation. What is being
// asserted is the OUTCOME of losing, not that losing is reachable — the index guarantees
// that.
func insertOnFirstProfileUpdate(t *testing.T, api *Api, ctx context.Context, tenant, token string) {
	t.Helper()
	db := api.RDB.Database
	fired := false
	name := "test:insert_before_profile_update"
	err := db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if fired || tx.Statement.Table != "device_profiles" {
			return
		}
		fired = true
		if _, err := tx.Statement.ConnPool.ExecContext(ctx,
			"INSERT INTO device_profiles (created_at, updated_at, tenant_id, token) "+
				"VALUES (CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, ?, ?)",
			tenant, token); err != nil {
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
func TestRenameDeviceProfile_ARacedTokenIsRefusedByTheSameName(t *testing.T) {
	api := newProfileCollisionApi(t)
	ctx := partialUpdateCtx()
	seedDeviceProfile(t, api, ctx, "prof")
	insertOnFirstProfileUpdate(t, api, ctx, partialUpdateTenant, "prof2")

	_, err := api.RenameDeviceProfile(ctx, "prof", "prof2")
	if err == nil {
		t.Fatal("the raced rename succeeded, which means two profiles now hold one token or " +
			"the collision was never planted")
	}

	// 🔴 THE ASSERTION IS ON BOTH HALVES: the sentence the API promises IS there, and the
	// driver text it replaces is NOT. Checking only the first would pass on an error that
	// concatenated the two, which is what a naive wrap produces.
	want := ErrDeviceProfileTokenTaken("prof", "prof2").Error()
	if err.Error() != want {
		t.Fatalf("the losing racer got:\n  %v\nwant exactly the uncontended refusal:\n  %s", err, want)
	}
	if !conflict.Is(err) {
		t.Fatalf("the losing racer's refusal is not a conflict, so it would reach the caller "+
			"without extensions.code CONFLICT: %v", err)
	}
	for _, leak := range []string{"SQLSTATE", "23505", "UNIQUE constraint", "constraint", "uix_"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the refusal still carries driver detail (%q): %v", leak, err)
		}
	}

	// And the rename did not half-apply: the transaction rolled back, so the profile is
	// still findable by its own token.
	rows, ferr := api.DeviceProfilesByToken(ctx, []string{"prof"})
	if ferr != nil || len(rows) != 1 {
		t.Fatalf("the profile is no longer findable by its own token: err=%v rows=%d", ferr, len(rows))
	}
}

// 🔴 THE NEGATIVE CONTROL. The raw racer IS a conflict — the GraphQL boundary would give it
// the code and the neutral sentence on its own — but it is NOT this rename's sentence, which
// is what the test above is worth: remove the translation and the caller gets the boundary's
// generic wording instead of the one the API promises. It drives the SAME planted collision
// through a bare write, so a reader can see the two side by side.
func TestRenameDeviceProfile_TheRawRacerIsAConflictButNotTheSentence(t *testing.T) {
	api := newProfileCollisionApi(t)
	ctx := partialUpdateCtx()
	seedDeviceProfile(t, api, ctx, "prof")
	rows, _ := api.DeviceProfilesByToken(ctx, []string{"prof"})
	insertOnFirstProfileUpdate(t, api, ctx, partialUpdateTenant, "prof2")

	raw := api.RDB.DB(ctx).Model(rows[0]).Update("token", "prof2").Error
	if raw == nil {
		t.Fatal("the untranslated write succeeded, so this control proves nothing about what " +
			"the translation is protecting the caller from")
	}
	if !conflict.Is(raw) {
		t.Fatalf("the raw failure is not the collision this suite is about: %v", raw)
	}
	if raw.Error() == ErrDeviceProfileTokenTaken("prof", "prof2").Error() {
		t.Fatal("the raw driver error already reads as the API's sentence, so the end-to-end " +
			"test above would pass with the translation removed")
	}
}

// THE COUNTERWEIGHT, driven through the REAL rename. The translation must not swallow an
// unrelated write failure into "that token is taken", which would be a worse lie than the
// driver text: it names a cause the caller can act on, and acting on it would not help.
func TestRenameDeviceProfile_AnUnrelatedWriteFailureIsNotReportedAsACollision(t *testing.T) {
	api := newProfileCollisionApi(t)
	db := api.RDB.Database
	ctx := partialUpdateCtx()
	seedDeviceProfile(t, api, ctx, "prof")
	failOnFirstUpdate(t, db, "device_profiles")

	_, err := api.RenameDeviceProfile(ctx, "prof", "prof2")
	if err == nil {
		t.Fatal("the rename succeeded, so the injected failure never reached it")
	}
	if err.Error() == ErrDeviceProfileTokenTaken("prof", "prof2").Error() {
		t.Fatalf("an unrelated write failure was reported as a token collision: %v", err)
	}
	if conflict.Is(err) {
		t.Fatalf("an unrelated write failure was classified as a conflict: %v", err)
	}
	if !strings.Contains(err.Error(), "driver: bad connection") {
		t.Fatalf("the rename did not return the write's own failure: %v", err)
	}
}

// failOnFirstUpdate makes the next UPDATE on table fail with an error that is not a
// uniqueness violation.
func failOnFirstUpdate(t *testing.T, db *gorm.DB, table string) {
	t.Helper()
	fired := false
	name := "test:fail_first_update"
	if err := db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if fired || tx.Statement.Table != table {
			return
		}
		fired = true
		_ = tx.AddError(errors.New("driver: bad connection"))
	}); err != nil {
		t.Fatalf("register the failing callback: %v", err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(name) })
}
