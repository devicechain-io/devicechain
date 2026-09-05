// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// THE LOSING SIDE OF A RENAME RACE GETS THE SAME SENTENCE AS THE UNCONTENDED REFUSAL.
//
// 🔴 THE LOOKUP INSIDE RenameAIProvider'S TRANSACTION IS NOT THE WHOLE ANSWER, AND THAT IS
// WHAT THESE TESTS EXIST FOR. At READ COMMITTED a SELECT cannot lock a row that does not
// exist, so two renames onto one token — or a rename racing a create — both see zero rows,
// and the second UPDATE discovers the collision at the unique index instead. Without a
// translation the loser is handed `duplicate key value violates unique constraint
// "uix_ai_providers_token" (SQLSTATE 23505)`, which is not something a client can write a
// handler against and is not what the served API reference promises.
//
// The uncontended refusal is covered by TestRenameAIProvider_RefusesATokenAlreadyInUse.
// What is here is the contended one. The provider list is INSTANCE-global, so unlike the
// tenant-scoped tables the index it collides with has no tenant column — a rename can lose
// to a provider in no tenant at all, which is the only sense in which this differs.

// insertOnFirstProviderUpdate reproduces the race window deterministically: a one-shot
// callback that runs just before the UPDATE and inserts the colliding row through the SAME
// connection, which is the state a concurrent committer leaves behind.
//
// 🔴 A REAL TWO-GOROUTINE RACE WAS REJECTED FOR THIS. It would be timing-dependent, and on
// SQLite the loser is as likely to get "database is locked" as the constraint error, so the
// test would be measuring the fixture's locking rather than the translation. What is being
// asserted is the OUTCOME of losing, not that losing is reachable — the index guarantees
// that.
func insertOnFirstProviderUpdate(t *testing.T, api *Api, ctx context.Context, token string) {
	t.Helper()
	db := api.RDB.Database
	fired := false
	name := "test:insert_before_provider_update"
	err := db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if fired || tx.Statement.Table != "ai_providers" {
			return
		}
		fired = true
		if _, err := tx.Statement.ConnPool.ExecContext(ctx,
			"INSERT INTO ai_providers (created_at, updated_at, token, kind, model, enabled) "+
				"VALUES (CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, ?, ?, ?, ?)",
			token, string(AIProviderKindAnthropic), "claude-opus-4-8", true); err != nil {
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
func TestRenameAIProvider_ARacedTokenIsRefusedByTheSameName(t *testing.T) {
	api, _ := auditedTestApi(t) // the real migrations, so the real unique index exists
	ctx := context.Background()
	if _, err := api.CreateAIProvider(ctx, claudeReq("prov-a", nil)); err != nil {
		t.Fatalf("create: %v", err)
	}
	insertOnFirstProviderUpdate(t, api, ctx, "prov-b")

	_, err := api.RenameAIProvider(ctx, "prov-a", "prov-b")
	if err == nil {
		t.Fatal("the raced rename succeeded, which means two providers now hold one token or " +
			"the collision was never planted")
	}

	// 🔴 THE ASSERTION IS ON BOTH HALVES: the sentence the API promises IS there, and the
	// driver text it replaces is NOT. Checking only the first would pass on an error that
	// concatenated the two, which is what a naive wrap produces.
	want := ErrAIProviderTokenTaken("prov-a", "prov-b").Error()
	if err.Error() != want {
		t.Fatalf("the losing racer got:\n  %v\nwant exactly the uncontended refusal:\n  %s", err, want)
	}
	for _, leak := range []string{"SQLSTATE", "23505", "constraint", aiProviderTokenIndexName} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("the refusal still carries driver detail (%q): %v", leak, err)
		}
	}

	// And the rename did not half-apply: the transaction rolled back, so the provider is
	// still findable by its own token.
	rows, ferr := api.AIProvidersByToken(ctx, []string{"prov-a"})
	if ferr != nil || len(rows) != 1 {
		t.Fatalf("the provider is no longer findable by its own token: err=%v rows=%d", ferr, len(rows))
	}
}

// 🔴 THE NEGATIVE CONTROL. Without the translation the loser gets the raw driver text — this
// is what the test above is worth, stated by showing the untranslated outcome rather than
// asserting it is impossible.
func TestRenameAIProvider_WithoutTheTranslationTheRacerGetsDriverText(t *testing.T) {
	api, _ := auditedTestApi(t)
	ctx := context.Background()
	if _, err := api.CreateAIProvider(ctx, claudeReq("prov-a", nil)); err != nil {
		t.Fatalf("create: %v", err)
	}
	rows, _ := api.AIProvidersByToken(ctx, []string{"prov-a"})
	insertOnFirstProviderUpdate(t, api, ctx, "prov-b")

	raw := api.sys(ctx).Model(rows[0]).Update("token", "prov-b").Error
	if raw == nil {
		t.Fatal("the untranslated write succeeded, so this control proves nothing about what " +
			"the translation is protecting the caller from")
	}
	if !rdb.IsUniqueViolation(raw, aiProviderTokenIndexName, "ai_providers.token") {
		t.Fatalf("the raw failure is not the collision this suite is about: %v", raw)
	}
	if raw.Error() == ErrAIProviderTokenTaken("prov-a", "prov-b").Error() {
		t.Fatal("the raw driver error already reads as the API's sentence, so the end-to-end " +
			"test above would pass with the translation removed")
	}
}

// THE COUNTERWEIGHT. The translation must not swallow an unrelated write failure into "that
// token is taken", which would be a worse lie than the driver text: it names a cause the
// caller can act on, and acting on it would not help.
func TestRenameAIProvider_AnUnrelatedWriteFailureIsNotReportedAsACollision(t *testing.T) {
	for name, err := range map[string]error{
		"connection lost": fmt.Errorf("driver: bad connection"),
		"another table":   fmt.Errorf(`UNIQUE constraint failed: ai_function_assignments.tenant_id, ai_function_assignments.function`),
		"a different index": fmt.Errorf(`duplicate key value violates unique constraint ` +
			`"uix_ai_tier_grant_default" (SQLSTATE 23505)`),
		"no error": nil,
	} {
		t.Run(name, func(t *testing.T) {
			if rdb.IsUniqueViolation(err, aiProviderTokenIndexName, "ai_providers.token") {
				t.Fatalf("%v was classified as a provider-token collision", err)
			}
		})
	}
}

// THE PRODUCTION BRANCH, which the SQLite fixture above cannot reach.
//
// 🔴 THE MESSAGE IS SPELLED OUT IN FULL RATHER THAN BUILT FROM aiProviderTokenIndexName,
// AND THAT IS THE WHOLE VALUE OF THIS TEST. A fixture assembled from the constant matches
// the constant whatever the constant says, so it would pass just as happily after a typo
// moved the name away from the index the migration actually creates.
func TestRenameAIProvider_ThePostgresUniqueViolationIsRecognised(t *testing.T) {
	pgError := fmt.Errorf(`ERROR: duplicate key value violates unique constraint ` +
		`"uix_ai_providers_token" (SQLSTATE 23505)`)
	if !rdb.IsUniqueViolation(pgError, aiProviderTokenIndexName, "ai_providers.token") {
		t.Fatalf("the Postgres unique violation was not recognised, so production — which runs "+
			"on Postgres, not on this test's SQLite — would hand the loser raw driver text:\n  %v",
			pgError)
	}
	sqliteError := fmt.Errorf("UNIQUE constraint failed: ai_providers.token")
	if !rdb.IsUniqueViolation(sqliteError, aiProviderTokenIndexName, "ai_providers.token") {
		t.Fatalf("the SQLite unique violation was not recognised, so the end-to-end test above "+
			"would be passing for a reason other than the translation:\n  %v", sqliteError)
	}
}

// 🔴 THE CONSTANT MUST NAME THE INDEX THE MIGRATION ACTUALLY BUILDS.
//
// Unlike every tenant-scoped table, this index's name is a LITERAL in schema/baseline.go
// rather than the output of a naming rule, so there is no rule to re-derive it from — the
// two literals are compared by reading the one the migration actually created out of the
// database it built. That is stronger than comparing two constants: it fails if the
// migration's literal moves, if the constant moves, or if the index stops being created at
// all.
func TestAIProviderTokenIndexNameMatchesTheMigration(t *testing.T) {
	api, db := auditedTestApi(t)
	_ = api
	var names []string
	if err := db.Raw(
		"SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'ai_providers'").
		Scan(&names).Error; err != nil {
		t.Fatalf("read the migration's indexes: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("the migration created no index on ai_providers at all, so the losing racer " +
			"would not collide and this whole suite would be asserting nothing")
	}
	for _, name := range names {
		if name == aiProviderTokenIndexName {
			return
		}
	}
	t.Fatalf("aiProviderTokenIndexName is %q but the migration built %v — a losing racer would "+
		"then get raw driver text, and nothing else in this suite would notice",
		aiProviderTokenIndexName, names)
}
