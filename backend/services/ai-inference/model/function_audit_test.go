// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"fmt"
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

// tierGrantAudit returns the journal entries for the tier-grant table, oldest first. The
// table is instance-global (no TenantId), but the journal's own rows are tenant-scoped, so
// the read takes the same sanctioned system-context bypass as assignmentAudit.
func tierGrantAudit(t *testing.T, db *gorm.DB) []rdb.AuditEvent {
	t.Helper()
	var events []rdb.AuditEvent
	require.NoError(t, db.WithContext(core.WithSystemContext(context.Background())).
		Where("table_name LIKE ?", "%ai_provider_tier_grants%").
		Order("id asc").Find(&events).Error)
	return events
}

// TestEveryTierDefaultChangeIsAttributableInTheJournal. Marking a tier's default decides
// which model every non-choosing tenant at that tier uses — and therefore where their
// spend goes — so it is exactly the packaging act ADR-065 decision 7 makes accountable.
//
// It asserts the CONTENT, not the existence, of the journal, because the failure mode here
// is silent: the audit callback builds its label from the struct handed to gorm, so
// mutating through a zero-value model (`tx.Model(&AIProviderTierGrant{}).Where(...)`)
// journals "→ provider#0" — a row that never existed. That is the trap SetFunctionModel
// documents, and the demote arm is the easiest place to fall into it, since the row being
// demoted is not the one the caller named.
func TestEveryTierDefaultChangeIsAttributableInTheJournal(t *testing.T) {
	api, db := auditedTestApi(t)
	p1 := mustProvider(t, api, "haiku")
	p2 := mustProvider(t, api, "opus")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "haiku"))
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "opus"))

	require.NoError(t, api.SetTierDefault(adminCtx(), "gold", "haiku"))
	// Re-points the tier: demotes haiku, promotes opus. BOTH rows must be attributable.
	require.NoError(t, api.SetTierDefault(adminCtx(), "gold", "opus"))
	require.NoError(t, api.ClearTierDefault(adminCtx(), "gold"))

	events := tierGrantAudit(t, db)
	// 2 creates (the grants) then 4 updates: promote haiku, demote haiku, promote opus,
	// demote opus.
	updates := make([]rdb.AuditEvent, 0, 4)
	for _, e := range events {
		if e.Operation == "update" {
			updates = append(updates, e)
		}
	}
	require.Len(t, updates, 4, "every promote and demote is journalled")

	haiku := AIProviderTierGrant{TierToken: "gold", ProviderID: p1.ID}.AuditLabel()
	opus := AIProviderTierGrant{TierToken: "gold", ProviderID: p2.ID}.AuditLabel()
	assert.Equal(t, []string{haiku, haiku, opus, opus},
		[]string{updates[0].EntityLabel, updates[1].EntityLabel, updates[2].EntityLabel, updates[3].EntityLabel},
		"each entry must name the tier and the REAL provider — including the DEMOTED row, "+
			"which the caller never named")
	for i, e := range updates {
		assert.NotContains(t, e.EntityLabel, "provider#0",
			"entry %d: provider#0 is a row that never existed", i)
	}
}

// TestRevokingAGrantIsAttributableInTheJournal is the arm the promote/demote/clear test
// stopped one step short of — the same "assert the thing NEXT TO the risky thing" shape
// that hid two of this arc's five bugs, recurring in the test written to prevent it.
//
// Revoking is not a lesser act here. RevokeProviderFromTier's own contract makes revoking
// the marked grant THE way a tier loses its default (the mark is a column on the row being
// deleted), which drops every non-choosing tenant on that tier to NONE. That is the
// packaging change ADR-065 decision 7 makes auditable, and a predicate delete through a
// zero-value model journals " → provider#0" — no tier, and a provider that never existed.
func TestRevokingAGrantIsAttributableInTheJournal(t *testing.T) {
	api, db := auditedTestApi(t)
	haiku := mustProvider(t, api, "haiku")
	fable := mustProvider(t, api, "fable")

	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "haiku"))
	require.NoError(t, api.SetTierDefault(adminCtx(), "gold", "haiku"))
	require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "fable"))

	// Revoke the tier's DEFAULT — the act that takes gold's default away.
	removed, err := api.RevokeProviderFromTier(adminCtx(), "gold", "haiku")
	require.NoError(t, err)
	require.True(t, removed)
	// ...and withdraw decision 7's audited exception.
	removed, err = api.RevokeProviderFromTenant(adminCtx(), "acme", "fable")
	require.NoError(t, err)
	require.True(t, removed)

	var deletes []rdb.AuditEvent
	require.NoError(t, db.WithContext(core.WithSystemContext(context.Background())).
		Where("operation = ?", "delete").Order("id asc").Find(&deletes).Error)
	require.Len(t, deletes, 2, "both revokes are journalled")

	assert.Equal(t, AIProviderTierGrant{TierToken: "gold", ProviderID: haiku.ID}.AuditLabel(),
		deletes[0].EntityLabel, "the tier revoke must name the tier and the REAL provider")
	wantTenant := AIProviderTenantGrant{ProviderID: fable.ID}
	wantTenant.TenantId = "acme"
	assert.Equal(t, wantTenant.AuditLabel(), deletes[1].EntityLabel,
		"the tenant revoke must name the tenant and the REAL provider")
	for _, e := range deletes {
		assert.NotContains(t, e.EntityLabel, "provider#0", "provider#0 is a row that never existed")
	}
}

// 🔴 A RENAME IS THE MOST IDENTITY-RELEVANT MUTATION THERE IS, AND IT MUST BE
// ATTRIBUTABLE.
//
// The journal has no per-mutation vocabulary — it records "update" on the provider table
// by construction, from the GORM callbacks — so what makes a rename readable afterwards
// is the ENTITY LABEL, which is the provider's token. The label recorded is the NEW
// token, because the row is what it now is rather than what it was; the old one is
// recoverable from the entry before it, and the pair is what makes the rename legible.
//
// 🔴 WHAT THIS TEST PINS IS THE OUTCOME, NOT THE WRITE STYLE, AND THE DIFFERENCE WAS
// MEASURED RATHER THAN ASSUMED. The assignment test above documents a real trap —
// mutating through a zero-value model journals an entry naming nothing, because the
// label is read off the struct handed to gorm and that struct's fields are still zero.
// The obvious inference is that a one-column update would break this too. It does not:
// a mutant replacing the write with `Model(&AIProvider{}).Update("token", …)` SURVIVES,
// because gorm assigns the new value back onto the model it was given and `token` is
// the only field this label reads. The trap needs a label built from fields the write
// does NOT name, which a rename's never is.
//
// So this asserts what a reader of the journal gets, and nothing about how the row was
// written. The write is narrow for an unrelated reason — see RenameAIProvider.
func TestRenamingAProviderIsAttributableInTheJournal(t *testing.T) {
	api, db := auditedTestApi(t)
	created := mustProvider(t, api, "haiku")

	before := providerAuditCount(t, db)
	_, err := api.RenameAIProvider(adminCtx(), "haiku", "haiku-eu")
	require.NoError(t, err)

	var events []rdb.AuditEvent
	require.NoError(t, db.WithContext(core.WithSystemContext(context.Background())).
		Where("table_name LIKE ? AND operation = ?", "%ai_providers%", "update").
		Order("id asc").Find(&events).Error)
	require.Len(t, events, 1, "the rename is journalled exactly once")
	assert.Equal(t, "haiku-eu", events[0].EntityLabel,
		"the rename journalled an unusable label, which is the same as no entry at all")

	// 🔴 THE PRIMARY KEY IS THE ONLY THING LINKING THE TWO LABELS, and it is the assertion
	// this test did not make the first time. The journal records the NEW token here and the
	// OLD one on the create before it; without a PK there is nothing to say they are the
	// same row, and core/rdb/audit.go defines an empty EntityPK as "a bulk/condition
	// update" — so a rename would be journalled as a bulk operation over an unnamed set.
	//
	// It is empty whenever the write passes a ZERO struct as its model
	// (`Model(&AIProvider{}).Where("id = ?", …)`), which is exactly the shape a one-column
	// update reaches for. Passing the loaded row keeps the narrow write AND the key.
	assert.Equal(t, fmt.Sprint(created.ID), events[0].EntityPK,
		"the rename journalled an EMPTY primary key, which the journal reads as a bulk update "+
			"— and with the label being the token, nothing then ties `haiku` to `haiku-eu`")

	// The counterweight: the create before it recorded the same key, so the two entries
	// really do join. An assertion on the rename alone would be satisfied by a journal that
	// stamped some constant.
	var creates []rdb.AuditEvent
	require.NoError(t, db.WithContext(core.WithSystemContext(context.Background())).
		Where("table_name LIKE ? AND operation = ?", "%ai_providers%", "create").
		Order("id asc").Find(&creates).Error)
	require.Len(t, creates, 1)
	assert.Equal(t, creates[0].EntityPK, events[0].EntityPK,
		"the create and the rename name different rows, so the journal cannot be read as a history")
	assert.Equal(t, "haiku", creates[0].EntityLabel,
		"the create's label is not the old token, so the pair proves nothing about the rename")

	assert.Greater(t, providerAuditCount(t, db), before,
		"nothing at all was journalled for the rename")
}

// providerAuditCount is the anti-vacuity counterpart to the assertion above: a filter
// that matched nothing would report an empty list as agreement.
func providerAuditCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.WithContext(core.WithSystemContext(context.Background())).
		Model(&rdb.AuditEvent{}).Where("table_name LIKE ?", "%ai_providers%").Count(&n).Error)
	return n
}
