// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tenantpurge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// journalPlan classifies a table shaped like the audit journal: a tenant column, and the
// two columns that name people. "main" is SQLite's own name for its default database,
// which is what makes the quoted "schema"."table" rendering resolvable here.
func journalPlan(t *testing.T) *Plan {
	t.Helper()
	return planOf(t, []column{
		col("main", "audit_events", "id", "bigint"),
		col("main", "audit_events", "tenant_id", "text"),
		col("main", "audit_events", "actor", "text"),
		col("main", "audit_events", "entity_label", "text"),
		col("main", "audit_events", "operation", "text"),
	}, nil)
}

func seedJournal(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`CREATE TABLE audit_events (
		id INTEGER PRIMARY KEY, tenant_id TEXT, actor TEXT, entity_label TEXT, operation TEXT)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO audit_events (id, tenant_id, actor, entity_label, operation)
		VALUES (1,'acme','jane@acme.example','jane-doe','create'),
		       (2,'acme','system','sensor-001','delete'),
		       (3,'globex','sam@globex.example','sam-smith','create')`).Error)
}

// 🔴 THE ROWS SURVIVE AND THE PEOPLE DO NOT. Both halves are the decision: sweeping the
// journal destroys the evidence of the erasure it is part of, and keeping it whole
// retains a tenant's people by name past the erasure that record certifies.
func TestAJournalRowIsKeptAndItsIdentifiersDestroyed(t *testing.T) {
	db := sqliteDB(t)
	seedJournal(t, db)
	plan := journalPlan(t)

	entry := plan.index()[Table{Schema: "main", Name: "audit_events"}]
	require.Equal(t, ClassRedacted, entry.Class,
		"a tenant column made it direct, so the sweep would have deleted the evidence")
	require.Equal(t, []string{"actor", "entity_label"}, entry.Redact)

	res, err := Sweep(context.Background(), db, plan, "acme", testEpoch, nil)
	require.NoError(t, err)
	assert.Zero(t, res.Rows, "a retained table must contribute nothing to the DELETED count")
	require.Len(t, res.Redacted, 1)
	assert.Equal(t, int64(2), res.Redacted[0].Rows)

	key := RetainedTenant("acme", testEpoch)
	var kept int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM audit_events WHERE tenant_id = ?`, key).
		Scan(&kept).Error)
	assert.Equal(t, int64(2), kept, "the journal was swept, so the erasure has no evidence")

	var underToken int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM audit_events WHERE tenant_id = 'acme'`).
		Scan(&underToken).Error)
	assert.Zero(t, underToken, "retained rows were left under the bare token, where a later tenant "+
		"created with it reads them as its own")

	var named int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM audit_events
		WHERE tenant_id = ? AND (actor <> '' OR entity_label <> '')`, key).Scan(&named).Error)
	assert.Zero(t, named, "a purged tenant's people are still named in the retained journal")

	// What survives is the shape of the activity, which is what the row is evidence of.
	var ops []string
	require.NoError(t, db.Raw(
		`SELECT operation FROM audit_events WHERE tenant_id = ? ORDER BY id`, key).Scan(&ops).Error)
	assert.Equal(t, []string{"create", "delete"}, ops)
}

// 🔴 A SECOND PASS MUST REPORT NOTHING REDACTED. The purge sweeps every minute for the
// whole token hold, so a redaction that kept matching rows it had already emptied would
// report a non-zero figure forever — and "identifiers destroyed on this pass" is what the
// deletion record cites as evidence.
func TestASecondPassRedactsNothing(t *testing.T) {
	db := sqliteDB(t)
	seedJournal(t, db)
	plan := journalPlan(t)

	first, err := Sweep(context.Background(), db, plan, "acme", testEpoch, nil)
	require.NoError(t, err)
	require.Len(t, first.Redacted, 1, "the first pass redacted nothing, so this proves nothing")
	require.Equal(t, int64(2), first.Redacted[0].Rows)

	second, err := Sweep(context.Background(), db, plan, "acme", testEpoch, nil)
	require.NoError(t, err)
	assert.Empty(t, second.Redacted,
		"the redaction rewrote rows it had already emptied, so every pass claims to have "+
			"destroyed identifiers that were destroyed on the first")

	// And the rows are still there, still blank — a second pass must not undo the first.
	var kept int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM audit_events WHERE tenant_id = ?`, RetainedTenant("acme", testEpoch)).
		Scan(&kept).Error)
	assert.Equal(t, int64(2), kept)
}

// 🔑 THE BYSTANDER IS THE CONTROL. A redaction with no tenant predicate, or one whose
// predicate matched everything, would satisfy every assertion above.
func TestARedactionLeavesEveryOtherTenantAlone(t *testing.T) {
	db := sqliteDB(t)
	seedJournal(t, db)

	_, err := Sweep(context.Background(), db, journalPlan(t), "acme", testEpoch, nil)
	require.NoError(t, err)

	var actor, label string
	require.NoError(t, db.Raw(
		`SELECT actor FROM audit_events WHERE id = 3`).Scan(&actor).Error)
	require.NoError(t, db.Raw(
		`SELECT entity_label FROM audit_events WHERE id = 3`).Scan(&label).Error)
	assert.Equal(t, "sam@globex.example", actor)
	assert.Equal(t, "sam-smith", label)
}

// 🔴 THE REDACTION IS THE ONE STEP THAT WOULD OTHERWISE GRADE ITSELF. The UPDATE reports
// a row count, and a row count is exactly what a WHERE clause selecting the wrong rows
// also produces. Residue asks the independent question.
func TestTheResidualScanSeesAnIdentifierTheRedactionMissed(t *testing.T) {
	db := sqliteDB(t)
	seedJournal(t, db)
	plan := journalPlan(t)

	_, err := Sweep(context.Background(), db, plan, "acme", testEpoch, nil)
	require.NoError(t, err)

	clean, err := Residue(context.Background(), db, plan, "acme", testEpoch)
	require.NoError(t, err)
	require.Zero(t, clean.Retained(), "the scan reports an identifier where none is left")

	// A straggler write lands after the sweep — the resurrection shape, in the one table
	// the purge keeps.
	require.NoError(t, db.Exec(`INSERT INTO audit_events (id, tenant_id, actor, entity_label, operation)
		VALUES (4,'acme','late@acme.example','','create')`).Error)

	dirty, err := Residue(context.Background(), db, plan, "acme", testEpoch)
	require.NoError(t, err)
	assert.Equal(t, int64(1), dirty.Retained(),
		"a retained row naming someone after the erasure was not seen by the re-verify")
	assert.Zero(t, dirty.Rows,
		"a retained row must not be reported as a failed DELETE — that sends an operator "+
			"looking for a service to stop")
}

// 🔴 A REDACTION RULE RUNS AHEAD OF THE CATALOG, so a relation carrying the registered
// name that an UPDATE cannot act on must fall through to UNCLASSIFIED and fail the purge
// in CI, where the coverage gate runs, rather than inside a real customer's sweep.
// Without the fall-through it would be classified redacted with nothing to select on,
// pass every gate, and then error on every pass forever.
func TestARelationNamedLikeTheJournalThatCannotBeWrittenIsRefused(t *testing.T) {
	matview := column{Schema: "device-management", Table: "audit_events", Name: "tenant_id",
		Type: "text", Kind: "m"}
	plan, err := classify([]column{matview}, nil, nil)
	require.NoError(t, err)

	e := plan.index()[Table{Schema: "device-management", Name: "audit_events"}]
	assert.Equal(t, ClassUnclassified, e.Class,
		"a materialized view named audit_events was classified redacted, so it passes the "+
			"coverage gate and fails inside every sweep transaction instead")
	require.Error(t, checkClassified(plan), "the purge would run over a relation it cannot write")

	// The counterweight: an ORDINARY table of that name is redacted, and carries the
	// column its predicate selects on. Without this, a rule that classified nothing at
	// all would pass the assertion above.
	ordinary, err := classify([]column{
		col("device-management", "audit_events", "tenant_id", "text"),
		col("device-management", "audit_events", "actor", "text"),
	}, nil, nil)
	require.NoError(t, err)
	ok := ordinary.index()[Table{Schema: "device-management", Name: "audit_events"}]
	require.Equal(t, ClassRedacted, ok.Class)
	require.Equal(t, "tenant_id", ok.Column,
		"a redacted table must carry the column its predicate selects on")
}

// The redaction registry covers the journal in EVERY schema, by a derived rule, because
// the table is core-owned and exists in all ten. A per-schema list would miss the area
// added next, and the coverage gate would stay green because the catalog can explain
// that table perfectly well.
func TestTheJournalIsRedactedInEverySchema(t *testing.T) {
	for _, schema := range []string{"user-management", "event-processing", "an-area-added-later"} {
		r, ok := redactionFor(Table{Schema: schema, Name: "audit_events"})
		require.Truef(t, ok, "the journal is not redacted in %q, so it is swept there", schema)
		assert.Equal(t, []string{"actor", "entity_label"}, r.Columns,
			"a redaction covering one of the two channels is worse than none")
		assert.NotEmpty(t, r.Reason)
	}
}

// 🔴 BOTH CHANNELS, OR NEITHER. A redaction naming only `actor` would leave every
// identity row's email and every customer-chosen token in `entity_label`, and would read
// as the question having been settled.
func TestARedactionMustNameEveryColumnThatCanIdentify(t *testing.T) {
	for _, r := range redactions {
		assert.NotEmptyf(t, r.Columns, "%s is registered redacted but names no columns", r.Name)
		assert.NotEmptyf(t, r.Reason, "%s states no reason", r.Name)
	}
	journal, ok := redactionFor(Table{Schema: "user-management", Name: "audit_events"})
	require.True(t, ok)
	assert.Contains(t, journal.Columns, "actor")
	assert.Contains(t, journal.Columns, "entity_label")
}

// 🔴 THE COLUMNS ARE WRITTEN EMPTY, NOT NULL, and the difference is not cosmetic. Every
// surface renders both the same way, so nothing downstream would notice — but the residual
// scan's own predicate has to cope with NULL for exactly that reason, and a registry entry
// that promises the empty string should be held to it.
func TestTheRedactionWritesTheEmptyStringRatherThanNull(t *testing.T) {
	db := sqliteDB(t)
	seedJournal(t, db)

	_, err := Sweep(context.Background(), db, journalPlan(t), "acme", testEpoch, nil)
	require.NoError(t, err)

	var nulls int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM audit_events
		WHERE tenant_id = ? AND (actor IS NULL OR entity_label IS NULL)`, RetainedTenant("acme", testEpoch)).Scan(&nulls).Error)
	assert.Zero(t, nulls, "the redaction wrote NULL; the registry entry promises the empty string")

	var blanks int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM audit_events
		WHERE tenant_id = ? AND actor = '' AND entity_label = ''`, RetainedTenant("acme", testEpoch)).Scan(&blanks).Error)
	assert.Equal(t, int64(2), blanks)
}

// 🔑 A NULL READS AS CLEAN, AND THAT IS CORRECT — a claim worth pinning because the
// tempting "defensive" fix is to wrap the comparison in coalesce, and a mutation showed
// that change was unobservable. `NULL <> ”` evaluates to NULL, so a NULL is not counted;
// a column holding no identifier is exactly what the scan is looking for the absence of.
// The purge writes the empty string (pinned above); a row that always carried NULL is
// equally clean and must not hold a purge open forever.
func TestANullIdentifierReadsAsClean(t *testing.T) {
	db := sqliteDB(t)
	seedJournal(t, db)
	plan := journalPlan(t)

	_, err := Sweep(context.Background(), db, plan, "acme", testEpoch, nil)
	require.NoError(t, err)
	nulled := db.Exec(
		`UPDATE audit_events SET actor = NULL WHERE tenant_id = ? AND id = 1`, RetainedTenant("acme", testEpoch))
	require.NoError(t, nulled.Error)
	require.Equal(t, int64(1), nulled.RowsAffected, "no retained row was set NULL, so this proves nothing")

	clean, err := Residue(context.Background(), db, plan, "acme", testEpoch)
	require.NoError(t, err)
	assert.Zero(t, clean.Retained(),
		"a NULL was counted as an identifier still present, which would hold the purge open "+
			"over a column that names nobody")
}

// 🔴 THE REDACTION SHARES THE SWEEP'S TRANSACTION. A redaction that committed while the
// deletes rolled back would leave a journal that could no longer say who did what, over
// data that is still there.
func TestARefusedSweepRedactsNothing(t *testing.T) {
	db := sqliteDB(t)
	seedJournal(t, db)
	refused := errors.New("that token names a LIVE tenant")

	_, err := Sweep(context.Background(), db, journalPlan(t), "acme", testEpoch,
		func(*gorm.DB) error { return refused })
	require.ErrorIs(t, err, refused)

	var named int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM audit_events
		WHERE tenant_id = 'acme' AND actor <> ''`).Scan(&named).Error)
	assert.Equal(t, int64(2), named,
		"the redaction committed over a sweep that was refused, so a live tenant's journal "+
			"lost its identifiers")
}

// A table with a redaction and no rows for this tenant contributes no line, so a reader
// of the result cannot mistake "nothing to do" for "something was done".
func TestARedactionWithNoMatchingRowsReportsNothing(t *testing.T) {
	db := sqliteDB(t)
	seedJournal(t, db)

	res, err := Sweep(context.Background(), db, journalPlan(t), "nobody", testEpoch, nil)
	require.NoError(t, err)
	assert.Empty(t, res.Redacted, "a redaction that matched no rows still reported a line")
}

// Nothing else is redacted by accident: the rule matches the journal's exact name.
func TestTheRedactionRuleDoesNotCoverLookalikeTables(t *testing.T) {
	for _, name := range []string{"audit_event", "audit_events_archive", "events", "x_audit_events"} {
		_, ok := redactionFor(Table{Schema: "user-management", Name: name})
		assert.Falsef(t, ok, "%q was redacted rather than swept", name)
	}
}

// 🔴 A TOKEN IS RELEASED WHEN ITS PURGE COMPLETES, and a new tenant may then be created with
// it. Every tenant-plane read of the journal selects on the tenant column, so the retained
// rows must not stay under the bare token: the later tenant would read the earlier one's
// activity as its own history. They are re-keyed, and stay findable under the key the
// deletion record names.
func TestALaterTenantAtTheSameTokenSeesNoneOfTheRetainedJournal(t *testing.T) {
	db := sqliteDB(t)
	seedJournal(t, db)
	plan := journalPlan(t)

	_, err := Sweep(context.Background(), db, plan, "acme", testEpoch, nil)
	require.NoError(t, err)
	// The later tenant's own activity.
	require.NoError(t, db.Exec(`INSERT INTO audit_events (id, tenant_id, actor, entity_label, operation)
		VALUES (4,'acme','new@acme.example','x','create')`).Error)

	var later []int
	require.NoError(t, db.Raw(`SELECT id FROM audit_events WHERE tenant_id = 'acme' ORDER BY id`).
		Scan(&later).Error)
	assert.Equal(t, []int{4}, later, "the later tenant at the same token sees the earlier tenant's journal")

	var retained []int
	require.NoError(t, db.Raw(`SELECT id FROM audit_events WHERE tenant_id = ? ORDER BY id`,
		RetainedTenant("acme", testEpoch)).Scan(&retained).Error)
	assert.Equal(t, []int{1, 2}, retained, "the retained rows are not under the key the deletion record names")
}

// Two purges of one token — the token was released, reused, and purged again — keep their
// retained rows apart, because the key carries the purge's epoch.
func TestTwoPurgesOfOneTokenKeepTheirRetainedRowsApart(t *testing.T) {
	db := sqliteDB(t)
	seedJournal(t, db)
	plan := journalPlan(t)

	_, err := Sweep(context.Background(), db, plan, "acme", testEpoch, nil)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`INSERT INTO audit_events (id, tenant_id, actor, entity_label, operation)
		VALUES (4,'acme','new@acme.example','x','create')`).Error)
	second := testEpoch.Add(30 * 24 * time.Hour)
	_, err = Sweep(context.Background(), db, plan, "acme", second, nil)
	require.NoError(t, err)

	ids := func(key string) []int {
		var out []int
		require.NoError(t, db.Raw(`SELECT id FROM audit_events WHERE tenant_id = ? ORDER BY id`, key).
			Scan(&out).Error)
		return out
	}
	assert.Equal(t, []int{1, 2}, ids(RetainedTenant("acme", testEpoch)))
	assert.Equal(t, []int{4}, ids(RetainedTenant("acme", second)))
	assert.Empty(t, ids("acme"))
}

// A row already re-keyed that still names someone — a pass whose emptying did not take — is
// unfinished: the re-verify sees it, and the next pass finishes it rather than skipping it
// because it is no longer under the token.
func TestARekeyedRowStillNamingSomeoneIsSeenAndFinished(t *testing.T) {
	db := sqliteDB(t)
	seedJournal(t, db)
	plan := journalPlan(t)
	key := RetainedTenant("acme", testEpoch)

	_, err := Sweep(context.Background(), db, plan, "acme", testEpoch, nil)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`UPDATE audit_events SET actor = 'jane@acme.example' WHERE id = 1`).Error)

	dirty, err := Residue(context.Background(), db, plan, "acme", testEpoch)
	require.NoError(t, err)
	assert.Equal(t, int64(1), dirty.Retained(), "a re-keyed row still naming someone was not seen")

	again, err := Sweep(context.Background(), db, plan, "acme", testEpoch, nil)
	require.NoError(t, err)
	require.Len(t, again.Redacted, 1)
	assert.Equal(t, int64(1), again.Redacted[0].Rows, "the next pass did not finish the row")

	clean, err := Residue(context.Background(), db, plan, "acme", testEpoch)
	require.NoError(t, err)
	assert.Zero(t, clean.Retained())
	var actor string
	require.NoError(t, db.Raw(`SELECT actor FROM audit_events WHERE id = 1 AND tenant_id = ?`, key).
		Scan(&actor).Error)
	assert.Empty(t, actor)
}

// A row under the bare token is unfinished whatever it holds: it is what a later tenant would
// read, so the re-verify counts it even when it names nobody.
func TestARowUnderTheTokenIsUnfinishedEvenWhenItNamesNobody(t *testing.T) {
	db := sqliteDB(t)
	seedJournal(t, db)
	plan := journalPlan(t)

	_, err := Sweep(context.Background(), db, plan, "acme", testEpoch, nil)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`INSERT INTO audit_events (id, tenant_id, actor, entity_label, operation)
		VALUES (5,'acme','','','delete')`).Error)

	dirty, err := Residue(context.Background(), db, plan, "acme", testEpoch)
	require.NoError(t, err)
	assert.Equal(t, int64(1), dirty.Retained(), "a nameless row under the bare token was read as finished")
}

// The key can never be a tenant: it fails the token grammar every tenant is created through,
// so no later tenant can be created whose reads match it. And it reads off the deletion
// record as "{token}~{epoch}", the epoch exactly as the record publishes it.
func TestARetainedKeyIsNeverATenantToken(t *testing.T) {
	key := RetainedTenant("acme", testEpoch)
	assert.Equal(t, "acme~2026-10-01T12:00:00.123456Z", key)
	require.NoError(t, core.ValidateToken("acme"), "control: the bare token is a valid token")
	assert.Error(t, core.ValidateToken(key), "a retained key is a valid tenant token")
	assert.Equal(t, key, RetainedTenant("acme", testEpoch.In(time.FixedZone("x", 5*3600))),
		"the key depends on the epoch's time zone")
}

// A purge with no epoch would file every purge's retained rows under one key, so both halves
// refuse it before touching the database.
func TestAZeroEpochIsRefused(t *testing.T) {
	db := sqliteDB(t)
	seedJournal(t, db)
	plan := journalPlan(t)

	_, err := Sweep(context.Background(), db, plan, "acme", time.Time{}, nil)
	require.ErrorContains(t, err, "no purge epoch")
	_, err = Residue(context.Background(), db, plan, "acme", time.Time{})
	require.ErrorContains(t, err, "no purge epoch")

	var named int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM audit_events WHERE tenant_id = 'acme' AND actor <> ''`).
		Scan(&named).Error)
	assert.Equal(t, int64(2), named, "a refused sweep touched the journal")
}
