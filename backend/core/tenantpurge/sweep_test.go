// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tenantpurge

import (
	"context"
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// planOf classifies a synthetic catalog and fails the test if it will not sweep.
func planOf(t *testing.T, cols []column, fks []constraint) *Plan {
	t.Helper()
	plan, err := classify(cols, fks, nil)
	require.NoError(t, err)
	require.NoError(t, checkClassified(plan))
	return plan
}

// TestADirectTableIsSweptByItsOwnColumn pins the simple case's SQL, including that the
// token arrives as a BIND ARGUMENT rather than interpolated into the statement.
func TestADirectTableIsSweptByItsOwnColumn(t *testing.T) {
	plan := planOf(t, []column{
		col("device-management", "devices", "tenant_id", "character varying"),
	}, nil)

	where, args, err := tenantPredicate(plan.index(),
		Table{Schema: "device-management", Name: "devices"}, "acme", 0)
	require.NoError(t, err)
	assert.Equal(t, `"tenant_id" = ?`, where)
	assert.Equal(t, []any{"acme"}, args)
}

// TestATransitiveTableIsSweptThroughItsForeignKey pins the subquery shape, and that the
// parent is reached by ITS tenant column rather than by anything on the child.
func TestATransitiveTableIsSweptThroughItsForeignKey(t *testing.T) {
	plan := planOf(t, []column{
		col("user-management", "iam_memberships", "id", "bigint"),
		col("user-management", "iam_memberships", "tenant_id", "character varying"),
		col("user-management", "iam_membership_tenant_roles", "membership_id", "bigint"),
	}, []constraint{
		fk("user-management", "iam_membership_tenant_roles", "membership_id",
			"user-management", "iam_memberships", "id"),
	})

	where, args, err := tenantPredicate(plan.index(),
		Table{Schema: "user-management", Name: "iam_membership_tenant_roles"}, "acme", 0)
	require.NoError(t, err)
	assert.Equal(t,
		`("membership_id") IN (SELECT "id" FROM "user-management"."iam_memberships" WHERE "tenant_id" = ?)`,
		where)
	assert.Equal(t, []any{"acme"}, args)
}

// TestTwoLinksAreOredNotAnded pins the choice argued in tenantPredicate. A join row
// referencing the purged tenant through EITHER foreign key is that tenant's data;
// requiring both would leave exactly the rows joining the purged tenant to something
// shared, which is most of them.
func TestTwoLinksAreOredNotAnded(t *testing.T) {
	plan := planOf(t, []column{
		col("a", "left_side", "id", "bigint"),
		col("a", "left_side", "tenant_id", "character varying"),
		col("a", "right_side", "id", "bigint"),
		col("a", "right_side", "tenant_id", "character varying"),
		col("a", "joiner", "left_id", "bigint"),
		col("a", "joiner", "right_id", "bigint"),
	}, []constraint{
		fk("a", "joiner", "left_id", "a", "left_side", "id"),
		fk("a", "joiner", "right_id", "a", "right_side", "id"),
	})

	where, args, err := tenantPredicate(plan.index(), Table{Schema: "a", Name: "joiner"}, "acme", 0)
	require.NoError(t, err)
	assert.Contains(t, where, " OR ")
	assert.NotContains(t, where, " AND ")
	assert.Equal(t, []any{"acme", "acme"}, args,
		"one bind argument per link, in clause order")
}

// TestANestedTransitiveParentRecurses pins the two-deep case. It does not arise in
// today's schema, which is exactly why it is worth a test: the depth is a property of
// whatever migrations exist at the time, so the code resolves it rather than assuming
// one level and breaking silently on the day a second appears.
func TestANestedTransitiveParentRecurses(t *testing.T) {
	plan := planOf(t, []column{
		col("a", "root", "id", "bigint"),
		col("a", "root", "tenant_id", "character varying"),
		col("a", "mid", "id", "bigint"),
		col("a", "mid", "root_id", "bigint"),
		col("a", "leaf", "mid_id", "bigint"),
	}, []constraint{
		fk("a", "mid", "root_id", "a", "root", "id"),
		fk("a", "leaf", "mid_id", "a", "mid", "id"),
	})

	where, args, err := tenantPredicate(plan.index(), Table{Schema: "a", Name: "leaf"}, "acme", 0)
	require.NoError(t, err)
	assert.Equal(t,
		`("mid_id") IN (SELECT "id" FROM "a"."mid" WHERE ("root_id") IN `+
			`(SELECT "id" FROM "a"."root" WHERE "tenant_id" = ?))`,
		where)
	assert.Equal(t, []any{"acme"}, args)
}

// TestACompositeForeignKeyPairsColumnsByPosition pins that a multi-column key keeps its
// pairing. Mispairing one would build a syntactically valid subquery that matches the
// wrong rows — a silent partial erasure, which is the worst outcome available here.
func TestACompositeForeignKeyPairsColumnsByPosition(t *testing.T) {
	plan := planOf(t, []column{
		col("a", "parent", "tenant", "character varying"),
		col("a", "parent", "token", "character varying"),
		col("a", "child", "p_tenant", "character varying"),
		col("a", "child", "p_token", "character varying"),
	}, []constraint{
		{Schema: "a", Table: "child", Columns: "p_tenant,p_token",
			ParentSchema: "a", ParentTable: "parent", ParentColumns: "tenant,token"},
	})

	where, _, err := tenantPredicate(plan.index(), Table{Schema: "a", Name: "child"}, "acme", 0)
	require.NoError(t, err)
	assert.Equal(t,
		`("p_tenant", "p_token") IN (SELECT "tenant", "token" FROM "a"."parent" WHERE "tenant" = ?)`,
		where)
}

// TestSweepRefusesAnEmptyTokenBeforeTouchingTheDatabase is a destructive-input guard.
// An empty token matches every row whose tenant column was never set, in every area.
//
// The nil *gorm.DB is the assertion, not an accident: if Sweep reached the database at
// all this test would panic rather than fail. Nothing may execute before the check.
func TestSweepRefusesAnEmptyTokenBeforeTouchingTheDatabase(t *testing.T) {
	plan := planOf(t, []column{
		col("a", "rows", "tenant_id", "character varying"),
	}, nil)

	_, err := Sweep(context.Background(), nil, plan, "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty tenant token")
}

// TestSweepRefusesAnUnclassifiedPlanBeforeTouchingTheDatabase is the fail-closed hinge.
// Sweeping what we understand and skipping the rest is the outcome ADR-077 exists to
// prevent: a purge reporting success while an unexplained table keeps the data.
//
// Again the nil db proves the ordering — the refusal has to come before any statement.
func TestSweepRefusesAnUnclassifiedPlanBeforeTouchingTheDatabase(t *testing.T) {
	plan, err := classify([]column{
		col("a", "known", "tenant_id", "character varying"),
		col("a", "mystery", "id", "bigint"),
	}, nil, nil)
	require.NoError(t, err)

	_, err = Sweep(context.Background(), nil, plan, "acme", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a.mystery")
}

// TestADeferredTableIsCarriedOnEveryResult pins that a known hole travels with the
// answer, on BOTH the refusal path and the success path.
//
// 🔑 The success path is the one that matters and the one an earlier draft left untested.
// A caller about to write a deletion record reads Complete(); if the successful return
// dropped Deferred, Complete() would report true with detect_snapshots outstanding and
// certify a total erasure that did not happen — which is the exact certification the
// exempt/deferred distinction exists to prevent. Testing only the refusal path leaves
// that mutation alive.
func TestADeferredTableIsCarriedOnEveryResult(t *testing.T) {
	plan := planOf(t, []column{
		col("event-processing", "detect_rules", "tenant", "character varying"),
		col("event-processing", "detect_snapshots", "partition_id", "character varying"),
	}, nil)

	t.Run("on the refusal path", func(t *testing.T) {
		res, err := Sweep(context.Background(), nil, plan, "", nil)
		require.Error(t, err)
		require.Len(t, res.Deferred, 1)
		assert.Equal(t, "detect_snapshots", res.Deferred[0].Table.Name)
		assert.False(t, res.Complete())
	})

	t.Run("on the success path", func(t *testing.T) {
		db := sqliteDB(t)
		require.NoError(t, db.Exec(`CREATE TABLE detect_rules (tenant TEXT)`).Error)
		require.NoError(t, db.Exec(`CREATE TABLE detect_snapshots (partition_id TEXT)`).Error)
		require.NoError(t, db.Exec(`INSERT INTO detect_rules (tenant) VALUES ('acme')`).Error)

		// The registry keys the deferral on event-processing's schema, and SQLite's is
		// "main", so the verdict is mirrored onto this plan rather than re-derived.
		// classify (not planOf) because the table is unclassified until it is.
		live, err := classify([]column{
			col("main", "detect_rules", "tenant", "text"),
			col("main", "detect_snapshots", "partition_id", "text"),
		}, nil, nil)
		require.NoError(t, err)
		for i := range live.Entries {
			if live.Entries[i].Table.Name == "detect_snapshots" {
				live.Entries[i].Class = ClassDeferred
				live.Entries[i].Reason = "stand-in for the registered hole"
			}
		}
		require.NoError(t, checkClassified(live))

		res, err := Sweep(context.Background(), db, live, "acme", nil) //nolint
		require.NoError(t, err)
		require.Equal(t, int64(1), res.Rows, "the sweep really ran")
		require.Len(t, res.Deferred, 1,
			"a successful sweep must still carry what it did not erase")
		assert.False(t, res.Complete(),
			"a successful sweep leaving a deferred table behind must not report a complete erasure")
	})
}

// --- execution against a real database -------------------------------------------
//
// SQLite stands in for Postgres here on purpose: it exercises the generated SQL, the
// delete ORDER and the transaction, which is where this package's bugs would live,
// without needing a container. What it cannot exercise is the catalog reading — SQLite
// has no information_schema — so Classify itself is covered by the integration test
// and by the coverage gate that runs against every area's real migrated schema.
//
// The schema is "main" because that is what SQLite calls its default database, which
// makes the same quoted `"schema"."table"` rendering the real path uses resolvable here.

// sqliteDB opens a fresh in-memory database with foreign keys enforced.
//
// The pragma is in the DSN rather than issued as a statement on purpose. gorm hands out
// pooled connections, so `db.Exec("PRAGMA foreign_keys = ON")` configures whichever
// connection happened to serve it and a later transaction can land on a different one
// with the pragma off — leaving the ordering tests passing against a database that was
// never enforcing the constraint they exist to exercise.
func sqliteDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(
		"file:"+t.Name()+"?mode=memory&cache=shared&_pragma=foreign_keys(1)"), &gorm.Config{})
	require.NoError(t, err)
	return db
}

func TestSweepDeletesOnlyTheNamedTenantsRows(t *testing.T) {
	db := sqliteDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE devices (id INTEGER PRIMARY KEY, tenant_id TEXT)`).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO devices (id, tenant_id) VALUES (1,'acme'),(2,'acme'),(3,'globex')`).Error)

	plan := planOf(t, []column{
		col("main", "devices", "id", "bigint"),
		col("main", "devices", "tenant_id", "text"),
	}, nil)

	res, err := Sweep(context.Background(), db, plan, "acme", nil)
	require.NoError(t, err)
	assert.Equal(t, int64(2), res.Rows)

	var survivors []string
	require.NoError(t, db.Raw(`SELECT tenant_id FROM devices`).Scan(&survivors).Error)
	assert.Equal(t, []string{"globex"}, survivors,
		"the other tenant's row is the control: a sweep that took it would be indistinguishable "+
			"from a correct one by row count alone")
}

func TestSweepDeletesAJoinRowThroughItsParent(t *testing.T) {
	db := sqliteDB(t)
	require.NoError(t, db.Exec(
		`CREATE TABLE iam_memberships (id INTEGER PRIMARY KEY, tenant_id TEXT)`).Error)
	require.NoError(t, db.Exec(
		`CREATE TABLE iam_membership_tenant_roles (membership_id INTEGER, role_id INTEGER)`).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO iam_memberships (id, tenant_id) VALUES (1,'acme'),(2,'globex')`).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO iam_membership_tenant_roles (membership_id, role_id) VALUES (1,10),(1,11),(2,10)`).Error)

	plan := planOf(t, []column{
		col("main", "iam_memberships", "id", "bigint"),
		col("main", "iam_memberships", "tenant_id", "text"),
		col("main", "iam_membership_tenant_roles", "membership_id", "bigint"),
		col("main", "iam_membership_tenant_roles", "role_id", "bigint"),
	}, []constraint{
		fk("main", "iam_membership_tenant_roles", "membership_id", "main", "iam_memberships", "id"),
	})

	res, err := Sweep(context.Background(), db, plan, "acme", nil)
	require.NoError(t, err)
	assert.Equal(t, int64(3), res.Rows, "two join rows plus the membership itself")

	var joins int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM iam_membership_tenant_roles`).Scan(&joins).Error)
	assert.Equal(t, int64(1), joins,
		"only globex's join row survives — the tenant's join rows carry nothing that names it, "+
			"so a sweep that missed the foreign key would leave them and report success")
}

// TestSweepDeletesTheChildBeforeTheParent drives the ordering against a database that
// actually enforces the foreign key. Without the topological order the parent delete
// runs first and the constraint rejects it, so this fails loudly rather than subtly.
//
// 🔑 Same choice as the unit-level ordering test, for the same reason: the pair is
// `areas` referencing `area_types`, whose correct delete order is the reverse of its
// alphabetical one. With the obvious device_credentials/devices pair this test passed
// with the topological sort deleted, because the child already sorted first.
func TestSweepDeletesTheChildBeforeTheParent(t *testing.T) {
	db := sqliteDB(t)
	require.NoError(t, db.Exec(`PRAGMA foreign_keys = ON`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE area_types (id INTEGER PRIMARY KEY, tenant_id TEXT)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE areas (
		id INTEGER PRIMARY KEY, tenant_id TEXT, area_type_id INTEGER REFERENCES area_types(id))`).Error)
	require.NoError(t, db.Exec(`INSERT INTO area_types (id, tenant_id) VALUES (1,'acme')`).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO areas (id, tenant_id, area_type_id) VALUES (1,'acme',1)`).Error)

	plan := planOf(t, []column{
		col("main", "area_types", "id", "bigint"),
		col("main", "area_types", "tenant_id", "text"),
		col("main", "areas", "id", "bigint"),
		col("main", "areas", "tenant_id", "text"),
		col("main", "areas", "area_type_id", "bigint"),
	}, []constraint{
		fk("main", "areas", "area_type_id", "main", "area_types", "id"),
	})

	// The guard that makes the assertion meaningful: a database not enforcing the
	// constraint would accept a parent-first sweep and report this test green forever.
	require.Error(t, db.Exec(`DELETE FROM area_types WHERE id = 1`).Error,
		"foreign keys are not being enforced, so this test cannot observe a wrong delete order")

	res, err := Sweep(context.Background(), db, plan, "acme", nil)
	require.NoError(t, err, "a parent-first order would be rejected by the foreign key")
	assert.Equal(t, int64(2), res.Rows)
}

// TestResidueReportsWhatSurvivedTheSweep drives the re-verify half against rows that
// come back after the delete — the resurrection case, simulated by re-inserting.
func TestResidueReportsWhatSurvivedTheSweep(t *testing.T) {
	db := sqliteDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE devices (id INTEGER PRIMARY KEY, tenant_id TEXT)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO devices (id, tenant_id) VALUES (1,'acme')`).Error)

	plan := planOf(t, []column{
		col("main", "devices", "id", "bigint"),
		col("main", "devices", "tenant_id", "text"),
	}, nil)
	ctx := context.Background()

	_, err := Sweep(ctx, db, plan, "acme", nil)
	require.NoError(t, err)

	clean, err := Residue(ctx, db, plan, "acme")
	require.NoError(t, err)
	assert.Zero(t, clean.Rows)
	assert.Empty(t, clean.Tables, "a clean scan reports no tables at all")

	// A service that was still running re-registers the device.
	require.NoError(t, db.Exec(`INSERT INTO devices (id, tenant_id) VALUES (2,'acme')`).Error)

	dirty, err := Residue(ctx, db, plan, "acme")
	require.NoError(t, err)
	assert.Equal(t, int64(1), dirty.Rows)
	require.Len(t, dirty.Tables, 1)
	assert.Equal(t, "devices", dirty.Tables[0].Table.Name)
}

// TestSweepIsAllOrNothing pins the single-transaction claim. A sweep that fails partway
// leaves a database whose erasure claim is false and which nothing outside can inspect
// to find out which half ran.
func TestSweepIsAllOrNothing(t *testing.T) {
	db := sqliteDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE first (tenant_id TEXT)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO first (tenant_id) VALUES ('acme')`).Error)

	// The second table is in the plan but absent from the database, so its delete
	// fails and must take the first table's delete down with it.
	plan := planOf(t, []column{
		col("main", "first", "tenant_id", "text"),
		col("main", "second", "tenant_id", "text"),
	}, nil)

	_, err := Sweep(context.Background(), db, plan, "acme", nil)
	require.Error(t, err)

	var remaining int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM first`).Scan(&remaining).Error)
	assert.Equal(t, int64(1), remaining,
		"the first table's delete must have rolled back with the failure")
}

// TestAFailingPreconditionAbortsTheSweepWithNothingDeleted pins what Precondition is for.
//
// The check runs INSIDE the transaction, so a refusal has to leave the database exactly as
// it was — not merely stop the remaining deletes. That distinction is the whole reason the
// hook is here rather than in the caller: a caller checking beforehand proves the token was
// safe at some earlier moment, and the sweep commits every schema at once, so "earlier" is
// not a claim anyone can act on.
func TestAFailingPreconditionAbortsTheSweepWithNothingDeleted(t *testing.T) {
	db := sqliteDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE devices (id INTEGER PRIMARY KEY, tenant_id TEXT)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO devices (id, tenant_id) VALUES (1,'acme')`).Error)

	plan := planOf(t, []column{
		col("main", "devices", "id", "bigint"),
		col("main", "devices", "tenant_id", "text"),
	}, nil)

	refuse := func(*gorm.DB) error { return errors.New("that token names a live tenant") }
	_, err := Sweep(context.Background(), db, plan, "acme", refuse)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "that token names a live tenant")

	var n int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM devices WHERE tenant_id = 'acme'`).Scan(&n).Error)
	assert.Equal(t, int64(1), n, "a refused precondition must roll back, not merely stop")
}

// TestAPassingPreconditionIsTheCounterweight. Refusing is only useful while a correct
// precondition still lets the sweep through untouched.
func TestAPassingPreconditionIsTheCounterweight(t *testing.T) {
	db := sqliteDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE devices (id INTEGER PRIMARY KEY, tenant_id TEXT)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO devices (id, tenant_id) VALUES (1,'acme')`).Error)

	plan := planOf(t, []column{
		col("main", "devices", "id", "bigint"),
		col("main", "devices", "tenant_id", "text"),
	}, nil)

	called := 0
	allow := func(tx *gorm.DB) error {
		called++
		require.NotNil(t, tx, "the precondition receives the sweep's own transaction")
		return nil
	}
	res, err := Sweep(context.Background(), db, plan, "acme", allow)
	require.NoError(t, err)
	assert.Equal(t, 1, called)
	assert.Equal(t, int64(1), res.Rows)
}

// TestAnEmptyClassificationIsRefusedRatherThanReportedClean is the vacuity guard, and it
// is the one failure in this package that arrives wearing a success.
//
// Everything else here fails LOUDLY when it is wrong: an unclassified table stops the
// purge, a residual row is an error, a cycle is reported. A plan with no tables in it
// fails at nothing — checkClassified finds no unclassified table, the sweep deletes from
// no table, Residue reads no table — and the caller is handed "erased, nothing left" for
// a database it never looked inside. That answer settles, completes the purge, and
// releases the tenant's token for reuse.
func TestAnEmptyClassificationIsRefusedRatherThanReportedClean(t *testing.T) {
	empty, err := classify(nil, nil, nil)
	require.NoError(t, err)
	require.Empty(t, empty.Entries, "the premise: an empty catalog yields an empty plan")

	// It passes every other gate, which is exactly why it needs its own.
	require.NoError(t, checkClassified(empty),
		"an empty plan has no unclassified table, so the fail-closed gate says nothing")

	err = checkSweepable(empty, "victim")
	require.Error(t, err, "a sweep of nothing must not be reported as an erasure")
	assert.Contains(t, err.Error(), "examined nothing")

	// Control: one table is enough to make the classification real, so the guard cannot
	// be blocking anything else.
	populated, err := classify([]column{
		col("device-management", "devices", "tenant_id", "character varying"),
	}, nil, nil)
	require.NoError(t, err)
	assert.NoError(t, checkSweepable(populated, "victim"))
}
