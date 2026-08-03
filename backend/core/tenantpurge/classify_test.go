// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tenantpurge

import (
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
	}, nil)
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
	}, nil)
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
	})
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
		plan, err := classify(cols, fks)
		require.NoError(t, err)
		for _, name := range []string{"mid", "leaf", "tip"} {
			assert.Equalf(t, ClassTransitive, classOf(t, plan, "a", name).Class,
				"run %d: %s is reachable from a tenant-bearing table and must classify as "+
					"transitive on every run, not on the runs whose map order happened to suit", i, name)
		}
	}
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
	})
	require.NoError(t, err)

	require.Less(t, indexOf(plan, "areas"), indexOf(plan, "area_types"),
		"areas references area_types, so areas must be emptied first — even though it sorts second")
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
	})
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
	}, nil)
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
	}, nil)
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

// TestMigrationBookkeepingIsExemptInEveryArea pins the one wildcard in the registry:
// each of the ten areas has its own gormigrate table, and none of them is worth a
// hand-written entry.
func TestMigrationBookkeepingIsExemptInEveryArea(t *testing.T) {
	for _, area := range []string{"device-management", "user-management", "event-processing"} {
		table := Table{Schema: area, Name: sanitizedArea(area) + "_migrations"}
		e, ok := exemptionFor(table)
		require.Truef(t, ok, "%s has no exemption", table)
		assert.Equal(t, ClassExempt, e.Class)
	}
}

// sanitizedArea mirrors rdb.MigrationTableName's dash-to-underscore rule.
func sanitizedArea(area string) string {
	out := []rune(area)
	for i, r := range out {
		if r == '-' {
			out[i] = '_'
		}
	}
	return string(out)
}

// TestMatchNameIsDeliberatelyNarrow pins that the registry's patterns cannot be widened
// by accident into something that swallows a table nobody meant to exempt.
func TestMatchNameIsDeliberatelyNarrow(t *testing.T) {
	assert.True(t, matchName("iam_roles", "iam_roles"))
	assert.True(t, matchName("*_migrations", "device_management_migrations"))
	assert.True(t, matchName("ai_*", "ai_providers"))

	assert.False(t, matchName("iam_roles", "iam_roles_extra"))
	assert.False(t, matchName("*_migrations", "migrations_pending"))
	assert.False(t, matchName("ai_*", "not_ai_providers"))
	assert.False(t, matchName("*", "anything"),
		"a bare * is not a supported pattern; an exemption must name what it excuses")
}

// TestQuoteIdentSurvivesHyphenatedSchemas pins the one piece of SQL hygiene that is not
// optional here: functional-area schema names contain hyphens, so unquoted they parse as
// a subtraction rather than as a name.
func TestQuoteIdentSurvivesHyphenatedSchemas(t *testing.T) {
	assert.Equal(t, `"device-management"."devices"`,
		Table{Schema: "device-management", Name: "devices"}.quoted())
	assert.Equal(t, `"we""ird"`, quoteIdent(`we"ird`))
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
	}, nil)
	require.NoError(t, err)

	err = checkClassified(plan)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "new-area.mystery_rows")
	assert.Contains(t, err.Error(), "exemption registry",
		"the message must say what to do about it, not just that something is wrong")
}
