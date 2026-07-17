// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// auditedTestApi is newTestApi plus the ADR-019 audit journal, which the default harness
// leaves out. It exists because the journal is the ONLY thing that makes re-pointing a
// tenant's model an accountable act, and its content was claimed by a comment and pinned
// by nothing — so the claim was false on two of three arms and the suite was silent.
func auditedTestApi(t *testing.T) (*Api, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?_pragma=foreign_keys(1)"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, db.AutoMigrate(&rdb.AuditEvent{}))
	require.NoError(t, rdb.RegisterAuditJournal(db))
	require.NoError(t, secrets.NewSecretStoreSchema().Migrate(db))
	require.NoError(t, schemaMigrateAll(t, db))
	kek, err := secrets.NewInstanceKeyProvider(testRootKey)
	require.NoError(t, err)
	return NewApi(&rdb.RdbManager{Database: db}, secrets.NewStore(db, kek)), db
}

// assignmentAudit returns the journal entries for the assignment table, oldest first.
// AuditEvent carries a TenantId, so the tenant-scope callback fails the read closed
// without a tenant — reading the journal ACROSS tenants is exactly what the sanctioned
// system-context bypass is for, and an operator surface would do the same.
func assignmentAudit(t *testing.T, db *gorm.DB) []rdb.AuditEvent {
	t.Helper()
	var events []rdb.AuditEvent
	require.NoError(t, db.WithContext(core.WithSystemContext(context.Background())).
		Where("table_name LIKE ?", "%ai_function_assignments%").
		Order("id asc").Find(&events).Error)
	return events
}

// TestEveryAssignmentChangeIsAttributableInTheJournal. ADR-065 decision 7 makes changing
// what a tenant is entitled to an AUDITED act, and re-pointing which model a tenant's
// function uses is exactly that: it moves the tenant's prompts, and its spend, to a
// different model.
//
// The journal labels an entry from the struct handed to gorm, so mutating through a
// zero-value model (`tx.Model(&AIFunctionAssignment{}).Where("id = ?")`) journals an
// EMPTY row: no tenant, no function, and a delete that names "provider#0" — a row that
// never existed. Create was fine; update and delete, the two arms that change a live
// answer, recorded nothing usable. An unattributable entry is the same as no entry, so
// this asserts the CONTENT rather than the existence of the journal.
func TestEveryAssignmentChangeIsAttributableInTheJournal(t *testing.T) {
	api, db := auditedTestApi(t)
	p1 := mustProvider(t, api, "haiku")
	p2 := mustProvider(t, api, "opus")

	require.NoError(t, api.SetFunctionModel(adminCtx(), "acme", FunctionRuleDrafting, "haiku"))
	require.NoError(t, api.SetFunctionModel(adminCtx(), "acme", FunctionRuleDrafting, "opus"))
	removed, err := api.ClearFunctionModel(adminCtx(), "acme", FunctionRuleDrafting)
	require.NoError(t, err)
	require.True(t, removed)

	events := assignmentAudit(t, db)
	require.Len(t, events, 3, "create, update and delete are each journalled")

	want := []struct{ op, label string }{
		{"create", AIFunctionAssignment{Function: FunctionRuleDrafting, ProviderID: p1.ID, TenantScoped: rdb.TenantScoped{TenantId: "acme"}}.AuditLabel()},
		{"update", AIFunctionAssignment{Function: FunctionRuleDrafting, ProviderID: p2.ID, TenantScoped: rdb.TenantScoped{TenantId: "acme"}}.AuditLabel()},
		{"delete", AIFunctionAssignment{Function: FunctionRuleDrafting, ProviderID: p2.ID, TenantScoped: rdb.TenantScoped{TenantId: "acme"}}.AuditLabel()},
	}
	for i, w := range want {
		assert.Equal(t, w.op, events[i].Operation)
		assert.Equal(t, w.label, events[i].EntityLabel,
			"entry %d must name the tenant, the function and the REAL provider", i)
		assert.Contains(t, events[i].EntityLabel, "acme", "an entry that cannot name the tenant cannot hold anyone to account")
		assert.NotContains(t, events[i].EntityLabel, "provider#0", "provider#0 is a row that never existed")
	}
}

// TestSettingTheBaselineToAVanishedProviderLeavesTheOldOneStanding. SetPlatformBaseline
// resolves the token OUTSIDE its transaction, so the provider can legally be deleted in
// between — legally because it is not the baseline yet, so DeleteAIProvider's in-use
// refusal lets it through.
//
// The designation has neither an FK nor a uniqueness violation to catch that: it is a
// column on the provider's own row, so nothing objects on our behalf. Without a
// RowsAffected check the demote commits, the promote updates nothing, and the operator
// is told "set" while the instance is left with NO baseline — silently dropping every
// tenant that never chose a model to NONE. Fail the call instead, and leave the previous
// baseline where it was.
func TestSettingTheBaselineToAVanishedProviderLeavesTheOldOneStanding(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "haiku")
	mustProvider(t, api, "doomed")
	require.NoError(t, api.SetPlatformBaseline(adminCtx(), "haiku"))

	// Stand in for the race by deleting the target first: the token no longer resolves
	// to a live row, which is the state SetPlatformBaseline would find mid-flight.
	removed, err := api.DeleteAIProvider(adminCtx(), "doomed")
	require.NoError(t, err)
	require.True(t, removed)

	err = api.SetPlatformBaseline(adminCtx(), "doomed")
	require.Error(t, err, "designating a provider that is not there must fail, not half-apply")

	base, err := api.PlatformBaseline(adminCtx())
	require.NoError(t, err)
	require.NotNil(t, base, "the previous baseline must survive a failed re-designation")
	assert.Equal(t, "haiku", base.Token)
}

// TestThePlatformBaselineCannotBeDeleted. Deleting the baseline drops every tenant that
// never chose a model to NONE, so it is refused like any other in-use provider. The
// refusal is enforced on the DELETE itself, not only by the check before it: grants and
// assignments have FKs to fall back on, but the designation is a column on the row being
// deleted, so nothing would object if the check and the write disagreed.
func TestThePlatformBaselineCannotBeDeleted(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "haiku")
	require.NoError(t, api.SetPlatformBaseline(adminCtx(), "haiku"))

	_, err := api.DeleteAIProvider(adminCtx(), "haiku")
	require.ErrorIs(t, err, ErrProviderInUse)

	// Undesignate it and the delete goes through: the refusal is about the ROLE, not the row.
	require.NoError(t, api.ClearPlatformBaseline(adminCtx()))
	removed, err := api.DeleteAIProvider(adminCtx(), "haiku")
	require.NoError(t, err)
	assert.True(t, removed)
}
