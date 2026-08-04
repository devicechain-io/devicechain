// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tenantpurge

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// col is a terse catalog-column builder for the synthetic catalogs below.
func col(schema, table, name, dataType string) column {
	return column{Schema: schema, Table: table, Name: name, Type: dataType}
}

// fk is a terse catalog-foreign-key builder (single-column).
func fk(schema, table, column, parentSchema, parentTable, parentColumn string) constraint {
	return constraint{
		Schema: schema, Table: table, Columns: column,
		ParentSchema: parentSchema, ParentTable: parentTable, ParentColumns: parentColumn,
	}
}

// classOf looks a table's class up in a plan.
func classOf(t *testing.T, plan *Plan, schema, name string) Entry {
	t.Helper()
	for _, e := range plan.Entries {
		if e.Table.Schema == schema && e.Table.Name == name {
			return e
		}
	}
	t.Fatalf("%s.%s is missing from the plan entirely — a table the purge never saw is the one "+
		"failure mode this package exists to prevent", schema, name)
	return Entry{}
}

// TestTheTwoRealTenantColumnSpellingsAreBothDirect pins that the classifier recognises
// both spellings that actually exist in the platform: the rdb.TenantScoped embed's
// tenant_id, and event-processing's plain `tenant` composite-key column. Recognising
// only the first would leave six event-processing projection tables unswept — which is
// exactly the shape ADR-077 warns about, since the tenant-scope callbacks miss them too.
func TestTheTwoRealTenantColumnSpellingsAreBothDirect(t *testing.T) {
	plan, err := classify([]column{
		col("device-management", "devices", "id", "bigint"),
		col("device-management", "devices", "tenant_id", "character varying"),
		col("event-processing", "device_rosters", "tenant", "character varying"),
		col("event-processing", "device_rosters", "device_token", "character varying"),
	}, nil, nil)
	require.NoError(t, err)

	embed := classOf(t, plan, "device-management", "devices")
	assert.Equal(t, ClassDirect, embed.Class)
	assert.Equal(t, "tenant_id", embed.Column)

	plain := classOf(t, plan, "event-processing", "device_rosters")
	assert.Equal(t, ClassDirect, plain.Class,
		"event-processing carries the tenant as a plain column, not the embed; a classifier that "+
			"only knows tenant_id silently skips its projections")
	assert.Equal(t, "tenant", plain.Column)
}

// TestANumericTenantIdIsNotATenantColumn pins the type check. A bigint column happening
// to be named tenant_id is a row-id foreign key, not a token; treating it as direct
// would build `WHERE tenant_id = 'acme'`, which fails at runtime — mid-purge. Leaving it
// unclassified fails up front instead, with the table named.
func TestANumericTenantIdIsNotATenantColumn(t *testing.T) {
	plan, err := classify([]column{
		col("some-area", "widgets", "tenant_id", "bigint"),
	}, nil, nil)
	require.NoError(t, err)

	e := classOf(t, plan, "some-area", "widgets")
	assert.Equal(t, ClassUnclassified, e.Class,
		"a numeric tenant_id must not be mistaken for a token column")
	var unclassified *ErrUnclassified
	assert.ErrorAs(t, checkClassified(plan), &unclassified, "and the plan must refuse to sweep")
}

// TestAJoinTableReachesTheTenantThroughItsForeignKey is the iam_membership_tenant_roles
// shape: a gorm many2many join with no tenant column of its own, whose rows are
// unambiguously the tenant's because they reference the tenant's membership. Nothing in
// the row says "acme"; only the foreign key does.
func TestAJoinTableReachesTheTenantThroughItsForeignKey(t *testing.T) {
	plan, err := classify([]column{
		col("user-management", "iam_memberships", "id", "bigint"),
		col("user-management", "iam_memberships", "tenant_id", "character varying"),
		col("user-management", "iam_roles", "id", "bigint"),
		col("user-management", "iam_membership_tenant_roles", "membership_id", "bigint"),
		col("user-management", "iam_membership_tenant_roles", "role_id", "bigint"),
	}, []constraint{
		fk("user-management", "iam_membership_tenant_roles", "membership_id",
			"user-management", "iam_memberships", "id"),
		fk("user-management", "iam_membership_tenant_roles", "role_id",
			"user-management", "iam_roles", "id"),
	}, nil)
	require.NoError(t, err)

	join := classOf(t, plan, "user-management", "iam_membership_tenant_roles")
	require.Equal(t, ClassTransitive, join.Class)
	require.Len(t, join.Links, 1,
		"only the edge into tenant-bearing data counts; the role edge points at an "+
			"instance-global catalog and must not widen the delete")
	assert.Equal(t, "iam_memberships", join.Links[0].Parent.Name)

	// iam_roles has no tenant column and no tenant-bearing parent: it is exactly the
	// case the exemption registry exists for, and it is registered.
	roles := classOf(t, plan, "user-management", "iam_roles")
	assert.Equal(t, ClassExempt, roles.Class)
	assert.NotEmpty(t, roles.Reason)
}

// TestTransitivityResolvesToAFixpointRegardlessOfMapOrder is the test that catches a
// single-pass implementation.
//
// The classifier walks a Go map, whose iteration order is randomised per run, so a
// single pass would classify a chain correctly or not depending on the order it happened
// to visit. One run therefore proves nothing. With a four-deep chain there are 4!
// orders and only one of them works for a single pass, so repeating the classification
// makes a single-pass bug fire with overwhelming probability — and the assertion is on
// the outcome, not on a count of passes.
func TestTransitivityResolvesToAFixpointRegardlessOfMapOrder(t *testing.T) {
	cols := []column{
		col("a", "root", "id", "bigint"),
		col("a", "root", "tenant_id", "character varying"),
		col("a", "mid", "id", "bigint"),
		col("a", "mid", "root_id", "bigint"),
		col("a", "leaf", "id", "bigint"),
		col("a", "leaf", "mid_id", "bigint"),
		col("a", "tip", "leaf_id", "bigint"),
	}
	fks := []constraint{
		fk("a", "mid", "root_id", "a", "root", "id"),
		fk("a", "leaf", "mid_id", "a", "mid", "id"),
		fk("a", "tip", "leaf_id", "a", "leaf", "id"),
	}

	for i := 0; i < 100; i++ {
		plan, err := classify(cols, fks, nil)
		require.NoError(t, err)
		for _, name := range []string{"mid", "leaf", "tip"} {
			assert.Equalf(t, ClassTransitive, classOf(t, plan, "a", name).Class,
				"run %d: %s is reachable from a tenant-bearing table and must classify as "+
					"transitive on every run, not on the runs whose map order happened to suit", i, name)
		}
	}
}

// TestADiamondCapturesEveryLinkOnEveryRun is the test that catches a transitive
// resolution which settles membership and links in one pass.
//
// Shape: T references A (direct) and B; B references A. T is tenant-bearing through
// BOTH edges. A single-phase walk that records T's links at the moment it first marks T
// captures only the parents that were bearing right then — and the walk is over a Go
// map, so if it reaches T before B, T keeps a link to A alone.
//
// 🔑 Note what has to be asserted. T's CLASS is transitive on every run either way; only
// len(Links) is wrong. The fixpoint test below asserts Class and passes with the bug
// present, which is exactly how it survived review of the first draft. A measured
// single-phase version failed here in 25 of 200 runs, so 200 iterations makes it certain.
func TestADiamondCapturesEveryLinkOnEveryRun(t *testing.T) {
	cols := []column{
		col("a", "root", "id", "bigint"),
		col("a", "root", "tenant_id", "character varying"),
		col("a", "middle", "id", "bigint"),
		col("a", "middle", "root_id", "bigint"),
		col("a", "joiner", "root_id", "bigint"),
		col("a", "joiner", "middle_id", "bigint"),
	}
	fks := []constraint{
		fk("a", "middle", "root_id", "a", "root", "id"),
		fk("a", "joiner", "root_id", "a", "root", "id"),
		fk("a", "joiner", "middle_id", "a", "middle", "id"),
	}

	for i := 0; i < 200; i++ {
		plan, err := classify(cols, fks, nil)
		require.NoError(t, err)
		joiner := classOf(t, plan, "a", "joiner")
		require.Equal(t, ClassTransitive, joiner.Class)
		require.Lenf(t, joiner.Links, 2,
			"run %d: joiner reaches the tenant through BOTH root and middle. With one link "+
				"missing, a joiner row whose other column is NULL survives the sweep and then "+
				"trips the foreign key when its parent is deleted, aborting the whole purge", i)
	}
}

// TestAMaterializedViewIsNeverSweptSilently pins the blind spot that made the
// fail-closed gate fail OPEN.
//
// A materialized view holds a physical copy of its query's rows and is absent from
// information_schema entirely — not from .tables and not from .columns, verified live on
// PostgreSQL 16. A classifier reading information_schema therefore cannot see one at
// all: it never reaches ClassUnclassified, the gate never fires, and a matview of tenant
// data is retained forever behind a green CI run.
//
// And a tenant column must not make it DIRECT either, since `DELETE FROM` a matview is
// a syntax error. The only correct outcome is that it demands an explicit answer.
func TestAMaterializedViewIsNeverSweptSilently(t *testing.T) {
	plan, err := classify([]column{
		{Schema: "event-management", Table: "tenant_rollup", Kind: "m",
			Name: "tenant_id", Type: "character varying"},
	}, nil, nil)
	require.NoError(t, err)

	e := classOf(t, plan, "event-management", "tenant_rollup")
	assert.Equal(t, ClassUnclassified, e.Class,
		"a matview carrying a tenant column cannot be swept with a DELETE, so it must demand "+
			"an explicit answer rather than be classified direct")
	require.Error(t, checkClassified(plan))
}

// TestAForeignTableIsNeverSweptSilently is the same rule for rows that are not in this
// database at all. Sweeping one would reach through a wrapper into another system.
func TestAForeignTableIsNeverSweptSilently(t *testing.T) {
	plan, err := classify([]column{
		{Schema: "some-area", Table: "remote_rows", Kind: "f",
			Name: "tenant_id", Type: "character varying"},
	}, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, ClassUnclassified, classOf(t, plan, "some-area", "remote_rows").Class)
}

// TestAPartitionedTableIsSweptNormally is the counterweight to the two tests above: the
// kind check must exclude what a DELETE cannot touch WITHOUT also excluding an ordinary
// partitioned table, which a DELETE handles perfectly well.
func TestAPartitionedTableIsSweptNormally(t *testing.T) {
	plan, err := classify([]column{
		{Schema: "event-management", Table: "events", Kind: "p",
			Name: "tenant_id", Type: "character varying"},
	}, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, ClassDirect, classOf(t, plan, "event-management", "events").Class)
}

// TestChildrenAreDeletedBeforeTheirParents pins the ordering the foreign keys require.
// None of the platform's foreign keys cascade, so deleting a parent while a child still
// references it is a constraint violation partway through the sweep transaction.
//
// 🔑 The table pair is chosen so that ALPHABETICAL order is the WRONG answer, and that
// is the whole reason this test is worth anything. Written with the obvious pair —
// device_credentials referencing devices — it passed with the topological sort removed
// entirely, because the child's name already sorted first and catalog order is
// alphabetical. `areas` referencing `area_types` is a real device-management foreign key
// whose correct delete order is the reverse of its alphabetical one, so an
// implementation that emits catalog order fails here instead of getting lucky.
func TestChildrenAreDeletedBeforeTheirParents(t *testing.T) {
	plan, err := classify([]column{
		col("device-management", "area_types", "id", "bigint"),
		col("device-management", "area_types", "tenant_id", "character varying"),
		col("device-management", "areas", "id", "bigint"),
		col("device-management", "areas", "tenant_id", "character varying"),
		col("device-management", "areas", "area_type_id", "bigint"),
	}, []constraint{
		fk("device-management", "areas", "area_type_id",
			"device-management", "area_types", "id"),
	}, nil)
	require.NoError(t, err)

	require.Less(t, indexOf(plan, "areas"), indexOf(plan, "area_types"),
		"areas references area_types, so areas must be emptied first — even though it sorts second")
}

// TestDeleteOrderIsAGraphWalkNotASort closes the remaining way an ordering test can pass
// for the wrong reason.
//
// The test above pins one orientation: a child that sorts AFTER its parent. Alone, it is
// satisfied by an implementation that merely reverses the alphabet — which is not a
// topological sort and breaks the moment a child sorts before its parent. This graph
// contains both orientations at once, so neither ascending nor descending name order can
// satisfy it and only a real dependency walk does.
//
//	alpha  -> zulu    (child sorts FIRST, parent last)
//	zebra  -> bravo   (child sorts LAST, parent first)
func TestDeleteOrderIsAGraphWalkNotASort(t *testing.T) {
	cols := []column{}
	for _, name := range []string{"alpha", "zulu", "zebra", "bravo"} {
		cols = append(cols,
			col("a", name, "id", "bigint"),
			col("a", name, "tenant_id", "character varying"),
			col("a", name, "ref", "bigint"))
	}
	plan, err := classify(cols, []constraint{
		fk("a", "alpha", "ref", "a", "zulu", "id"),
		fk("a", "zebra", "ref", "a", "bravo", "id"),
	}, nil)
	require.NoError(t, err)

	assert.Less(t, indexOf(plan, "alpha"), indexOf(plan, "zulu"),
		"alpha references zulu; ascending name order gets this one right by luck")
	assert.Less(t, indexOf(plan, "zebra"), indexOf(plan, "bravo"),
		"zebra references bravo; descending name order gets THIS one right by luck, and no "+
			"single sort direction can satisfy both")
}

// TestAForeignKeyCycleIsReportedRatherThanBroken pins that an unorderable schema stops
// the purge with a named cycle. Silently breaking one produces a sweep that violates a
// constraint partway through, which reads as a database fault rather than as the schema
// shape it is.
func TestAForeignKeyCycleIsReportedRatherThanBroken(t *testing.T) {
	_, err := classify([]column{
		col("a", "x", "id", "bigint"), col("a", "x", "y_id", "bigint"),
		col("a", "y", "id", "bigint"), col("a", "y", "x_id", "bigint"),
	}, []constraint{
		fk("a", "x", "y_id", "a", "y", "id"),
		fk("a", "y", "x_id", "a", "x", "id"),
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cycle")
	assert.Contains(t, err.Error(), "a.x", "the message must name the tables involved")
}

// TestTheCatalogOverridesAStaleExemption pins the precedence documented in classify():
// an exemption can only ever excuse a table the catalog could not explain. If a table
// named in the registry later gains a tenant column, it must be SWEPT — otherwise a
// stale entry written when the table was harmless quietly retains data forever.
func TestTheCatalogOverridesAStaleExemption(t *testing.T) {
	plan, err := classify([]column{
		// iam_roles is in the exemption registry. Give it a tenant column.
		col("user-management", "iam_roles", "id", "bigint"),
		col("user-management", "iam_roles", "tenant_id", "character varying"),
	}, nil, nil)
	require.NoError(t, err)

	e := classOf(t, plan, "user-management", "iam_roles")
	assert.Equal(t, ClassDirect, e.Class,
		"a registered exemption must not suppress a tenant column the catalog can see")
	assert.Empty(t, e.Reason)
}

// TestSystemSchemasAreNotClassified keeps PostgreSQL's and TimescaleDB's own catalogs
// out of the plan. _timescaledb_internal is the one that matters: it holds every
// hypertable's chunks, and classifying them individually would double-count deletes
// already made through the parent hypertable.
func TestSystemSchemasAreNotClassified(t *testing.T) {
	plan, err := classify([]column{
		col("pg_catalog", "pg_class", "tenant_id", "character varying"),
		col("information_schema", "tables", "tenant_id", "character varying"),
		col("_timescaledb_internal", "_hyper_1_1_chunk", "tenant_id", "character varying"),
		col("event-management", "events", "tenant_id", "character varying"),
	}, nil, nil)
	require.NoError(t, err)

	require.Len(t, plan.Entries, 1, "only the functional-area table belongs in the plan")
	assert.Equal(t, "event-management", plan.Entries[0].Table.Schema)
}

// TestEveryExemptionStatesAReason guards the registry's one rule. An entry with no
// reason is an unexamined skip wearing the costume of a decision — and the registry is
// consulted by code that is about to claim a tenant's data is gone.
func TestEveryExemptionStatesAReason(t *testing.T) {
	for _, e := range exemptions {
		assert.NotEmptyf(t, e.Reason, "%s.%s is exempt with no stated reason", e.Schema, e.Name)
		assert.Containsf(t, []Class{ClassExempt, ClassDeferred}, e.Class,
			"%s.%s: an exemption may only mark a table exempt or deferred", e.Schema, e.Name)
	}
}

// TestTheDetectSnapshotHoleIsRegisteredAsDeferredNotExempt pins the distinction the two
// classes exist for. detect_snapshots holds the purged tenant's open detection windows
// inside an opaque blob; recording it as ClassExempt would file a known hole among the
// "holds nothing of any tenant's" entries, where a reviewer reads it as reassurance.
// Deferred keeps it in every sweep result instead.
func TestTheDetectSnapshotHoleIsRegisteredAsDeferredNotExempt(t *testing.T) {
	e, ok := exemptionFor(Table{Schema: "event-processing", Name: "detect_snapshots"})
	require.True(t, ok)
	assert.Equal(t, ClassDeferred, e.Class,
		"a table holding un-erased tenant data must be deferred, not exempt")
}

// TestMigrationBookkeepingIsExemptInEveryArea pins the one derived exemption: each area
// has its own gormigrate ledger, named from the area with dashes turned to underscores.
func TestMigrationBookkeepingIsExemptInEveryArea(t *testing.T) {
	for _, area := range []string{"device-management", "user-management", "event-processing"} {
		table := Table{Schema: area, Name: sanitizedArea(area) + "_migrations"}
		e, ok := exemptionFor(table)
		require.Truef(t, ok, "%s has no exemption", table)
		assert.Equal(t, ClassExempt, e.Class)
	}
}

// TestTheMigrationExemptionCannotSwallowAFeatureTable is that rule's negative control.
// A suffix match on "*_migrations" — which is what this registry used to carry — would
// also exempt a future feature table ending in the word, under a reason about gormigrate
// that has nothing to do with it, and the coverage gate would go green over real tenant
// data. The name is derived from the schema instead, so it matches that one ledger.
func TestTheMigrationExemptionCannotSwallowAFeatureTable(t *testing.T) {
	for _, name := range []string{
		"channel_migrations", // a plausible firmware feature table
		"tenant_migrations",  // and a more alarming one
		"device_management_migrations_archive",
	} {
		_, ok := exemptionFor(Table{Schema: "device-management", Name: name})
		assert.Falsef(t, ok, "%s must not inherit the gormigrate exemption", name)
	}
	// Another area's ledger is not exempt in this schema either: the name has to be
	// derived from the schema it actually sits in.
	_, ok := exemptionFor(Table{Schema: "device-management", Name: "user_management_migrations"})
	assert.False(t, ok)
}

// sanitizedArea mirrors rdb.MigrationTableName's dash-to-underscore rule.
func sanitizedArea(area string) string {
	return strings.ReplaceAll(area, "-", "_")
}

// TestQuoteIdentSurvivesHyphenatedSchemas pins the one piece of SQL hygiene that is not
// optional here: functional-area schema names contain hyphens, so unquoted they parse as
// a subtraction rather than as a name.
func TestQuoteIdentSurvivesHyphenatedSchemas(t *testing.T) {
	assert.Equal(t, `"device-management"."devices"`,
		Table{Schema: "device-management", Name: "devices"}.quoted())
	// The escaping rule itself belongs to rdb.QuoteIdentifier and is pinned there;
	// what matters here is that a table renders SCHEMA-QUALIFIED and quoted on both
	// halves, which is what the generated SQL depends on.
}

func indexOf(plan *Plan, name string) int {
	for i, e := range plan.Entries {
		if e.Table.Name == name {
			return i
		}
	}
	return -1
}

// TestPlanReportsUnclassifiedTablesByName pins the error a maintainer will actually see
// when they add a table the purge cannot explain: it has to name the table, or it sends
// them looking through 87 of them.
func TestPlanReportsUnclassifiedTablesByName(t *testing.T) {
	plan, err := classify([]column{
		col("new-area", "mystery_rows", "id", "bigint"),
	}, nil, nil)
	require.NoError(t, err)

	err = checkClassified(plan)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "new-area.mystery_rows")
	assert.Contains(t, err.Error(), "exemption registry",
		"the message must say what to do about it, not just that something is wrong")
}
