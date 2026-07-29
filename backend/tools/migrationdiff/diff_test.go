// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

// twoTables is the shape every test below needs and the shape the old tests did not
// have: MULTI-LINE CREATE TABLEs, more than one of them, sharing column definitions.
// The previous tests all used single-line `CREATE TABLE "area".a (id integer);`, which is
// why none of them could catch the defect this file exists for — a real pg_dump puts one
// column per line, and that is the entire mechanism.
const twoTables = `CREATE TABLE "area".alpha (
 id bigint NOT NULL,
 tenant_id character varying(128) NOT NULL,
 description character varying(1024),
 deleted_at timestamp with time zone
);
CREATE TABLE "area".beta (
 id bigint NOT NULL,
 tenant_id character varying(128) NOT NULL,
 description character varying(1024),
 deleted_at timestamp with time zone
);`

func diffOf(t *testing.T, want, got string) string {
	t.Helper()
	return statementDiff(splitNormalized(want), splitNormalized(got))
}

// TestDiffCatchesColumnMissingFromOneOfTwoTables is the regression test for the defect
// that made this harness unable to fail. `description character varying(1024),` is
// present in both tables, so removing it from ONE leaves the text present in the schema
// and a set-difference-over-lines comparison reports nothing at all.
//
// This was measured on the real thing before it was fixed: dropping the column from
// user-management's baseline snapshot produced a schema that a plain `diff` showed to be
// exactly one line short of the committed golden, and `hack/migration-diff.sh verify`
// printed "ok ... matches golden" and exited 0.
func TestDiffCatchesColumnMissingFromOneOfTwoTables(t *testing.T) {
	got := strings.Replace(twoTables, " description character varying(1024),\n", "", 1)
	if strings.Count(got, "description") != 1 {
		t.Fatalf("test setup did not remove exactly one description column (%d left)", strings.Count(got, "description"))
	}

	diff := diffOf(t, twoTables, got)
	if diff == "" {
		t.Fatal("a column removed from one of two tables that share its definition MUST be reported; " +
			"reporting nothing here is the defect this test exists for")
	}
	if !strings.Contains(diff, "alpha") {
		t.Errorf("the diff must name the table that lost the column; got:\n%s", diff)
	}
	if !strings.Contains(diff, "description") {
		t.Errorf("the diff must name the missing column; got:\n%s", diff)
	}
	// The other table kept its copy, so it has no business in the report.
	if strings.Contains(diff, "beta") {
		t.Errorf("the diff must not implicate the unchanged table; got:\n%s", diff)
	}
}

// TestDiffCatchesColumnAddedToOneOfTwoTables is the same defect in the other direction:
// an extra column whose text already exists elsewhere. A flatten produces this by
// applying an ALTER to the wrong snapshot type.
func TestDiffCatchesColumnAddedToOneOfTwoTables(t *testing.T) {
	got := strings.Replace(twoTables,
		"CREATE TABLE \"area\".alpha (\n id bigint NOT NULL,",
		"CREATE TABLE \"area\".alpha (\n id bigint NOT NULL,\n metadata jsonb,", 1)
	extra := strings.Replace(twoTables,
		"CREATE TABLE \"area\".beta (\n id bigint NOT NULL,",
		"CREATE TABLE \"area\".beta (\n id bigint NOT NULL,\n metadata jsonb,", 1)
	// Both tables now legitimately have the column somewhere in the SCHEMA, so a
	// set difference over lines sees nothing wrong with either arrangement.
	if diff := diffOf(t, extra, got); diff == "" {
		t.Fatal("the same column on the WRONG table must be reported")
	}
	if diff := diffOf(t, twoTables, got); diff == "" {
		t.Fatal("an added column must be reported")
	}
}

// TestDiffIsCountAware covers the multiset half. Two objects with identical text cannot
// normally coexist, but if the comparison is a plain set then losing one of a duplicated
// pair is invisible, which is the same absorption bug one level up.
func TestDiffIsCountAware(t *testing.T) {
	one := "CREATE INDEX x ON \"area\".a (id);"
	two := one + "\n" + one
	if diff := statementDiff(splitNormalized(two), splitNormalized(one)); diff == "" {
		t.Fatal("losing one of two identical statements must be reported")
	}
	if diff := statementDiff(splitNormalized(one), splitNormalized(two)); diff == "" {
		t.Fatal("gaining a duplicate statement must be reported")
	}
}

// TestScrubDoesNotSwallowStructuralNumbers guards the scrub block, which is the one place
// in this tool where a one-line edit turns detection off silently — and the place a future
// "the harness is too noisy" change will land.
//
// It earns its place by mutation: replacing the whitespace collapse in scrub() with a
// digit-stripping regex left the ENTIRE suite passing, because the only type-difference
// test used `integer` vs `bigint`, which contain no digits. Under that mutant
// `varying(128)` and `varying(256)` compare equal — and the goldens are saturated with
// varying(128)/(256)/(1024)/(4096) and numeric(10,8)/(11,8)/(12,4)/(20,8). Same absorption
// class as the line-diff defect, one layer down.
func TestScrubDoesNotSwallowStructuralNumbers(t *testing.T) {
	for _, pair := range [][2]string{
		{"CREATE TABLE \"area\".a (\n token character varying(128)\n);", "CREATE TABLE \"area\".a (\n token character varying(256)\n);"},
		{"CREATE TABLE \"area\".a (\n lat numeric(10,8)\n);", "CREATE TABLE \"area\".a (\n lat numeric(20,8)\n);"},
		{"CREATE TABLE \"area\".a (\n v integer\n);", "CREATE TABLE \"area\".a (\n v bigint\n);"},
	} {
		if normalizeDump(pair[0]) == normalizeDump(pair[1]) {
			t.Errorf("a column-width/precision difference must survive scrubbing:\n%s\nvs\n%s", pair[0], pair[1])
		}
	}
}

// TestScrubStillNormalizesTimescaleInternalIDs is the counterweight to the test above: the
// scrub must keep absorbing the ids it exists for, or every event-management run diffs on
// a number that is not schema.
func TestScrubStillNormalizesTimescaleInternalIDs(t *testing.T) {
	a := `TIMESCALE POLICY policy_refresh_continuous_aggregate ON measurement_rollups SCHEDULE 00:01:00 CONFIG {"mat_hypertable_id": 6};`
	b := strings.Replace(a, `"mat_hypertable_id": 6`, `"mat_hypertable_id": 11`, 1)
	if normalizeDump(a) != normalizeDump(b) {
		t.Errorf("the materialization hypertable id is a sequential internal id, not schema:\n%s\nvs\n%s",
			normalizeDump(a), normalizeDump(b))
	}
}

// TestRenderPrintsEveryStatementInACollidingGroup pins render's 1/1 narrowing guard.
// Header collisions are real in the committed goldens — `ALTER TABLE ONLY
// "user-management".iam_identity_system_roles` carries three distinct statements, and
// fourteen more headers carry two — so a 2-versus-1 group is what a flatten that rewrites
// a table's constraints produces. Loosening the guard to `>= 1` left every other test
// passing while one dropped constraint vanished from the report.
func TestRenderPrintsEveryStatementInACollidingGroup(t *testing.T) {
	header := "ALTER TABLE ONLY \"area\".t"
	want := header + "\n ADD CONSTRAINT c1 PRIMARY KEY (id);\n" + header + "\n ADD CONSTRAINT c2 UNIQUE (token);"
	got := header + "\n ADD CONSTRAINT c1 PRIMARY KEY (id);"

	diff := diffOf(t, want, got)
	if !strings.Contains(diff, "c2") {
		t.Errorf("the dropped constraint must appear in the report; got:\n%s", diff)
	}
	if strings.Contains(diff, "~ ") {
		t.Errorf("a 2-vs-1 group must not be narrowed as if it were one changed object; got:\n%s", diff)
	}
}

// TestSplitNormalizedKeepsAnUnterminatedTail covers the documented safety property in
// splitNormalized: a body that does not end on ';' keeps its tail. All ten goldens do end
// on ';' so this is unreachable today, which is exactly why it needs a test — an untested
// safety net is removed by the next person who reads it as dead code, and silently
// dropping the tail of a schema is worse than a confusing diff.
func TestSplitNormalizedKeepsAnUnterminatedTail(t *testing.T) {
	stmts := splitNormalized("CREATE TABLE \"area\".a (id integer);\nCREATE TABLE \"area\".b (\n id integer")
	if len(stmts) != 2 {
		t.Fatalf("expected the tail to be kept as its own statement, got %d: %q", len(stmts), stmts)
	}
	if !strings.Contains(stmts[1], "\"area\".b") {
		t.Errorf("the unterminated tail was dropped or merged: %q", stmts)
	}
}

// TestDiffCatchesRenamedIndex is the TableName trap from the baseline snapshots: gorm's
// table prefix is applied only when it DERIVES a table name, so adding a TableName
// method to a snapshot type keeps every index but renames all of them. The index is
// present either way and only its name moves.
//
// Note this one is a legacy-strength assertion, not a regression test for the statement
// comparison: both sides are single distinct lines, so the old line-set implementation
// passed it too. It stays because the trap is real and worth pinning, but it does not
// count toward coverage of what changed.
func TestDiffCatchesRenamedIndex(t *testing.T) {
	want := `CREATE INDEX "idx_user-management_signing_keys_active" ON "user-management".signing_keys USING btree (active);`
	got := `CREATE INDEX idx_signing_keys_active ON "user-management".signing_keys USING btree (active);`
	if diff := diffOf(t, want, got); diff == "" {
		t.Fatal("a renamed index must be reported — an index's name is part of the schema")
	}
}

// TestDiffCatchesReorderedColumns pins that physical column order is treated as schema.
// Statement sorting deliberately ignores the order objects were CREATED in; it must not
// also ignore the order of columns inside one of them.
func TestDiffCatchesReorderedColumns(t *testing.T) {
	want := "CREATE TABLE \"area\".a (\n id bigint,\n name text\n);"
	got := "CREATE TABLE \"area\".a (\n name text,\n id bigint\n);"
	diff := diffOf(t, want, got)
	if diff == "" {
		t.Fatal("reordered columns must be reported — physical column order is part of the schema")
	}
	// Swapping two columns also moves the trailing comma, so the lines differ as a
	// multiset and the report is an ordinary line diff naming both columns.
	for _, want := range []string{"id bigint", "name text"} {
		if !strings.Contains(diff, want) {
			t.Errorf("the diff should name %q; got:\n%s", want, diff)
		}
	}
}

// TestDiffReportsPureReorderingAsOrdering covers the one shape where two statements
// differ ONLY in line order, with no punctuation moving to give it away: a CREATE
// SEQUENCE's clauses carry no trailing commas. A line diff of that pair is empty, so
// without a dedicated message the report would print an object header with nothing
// underneath and read as a spurious failure.
func TestDiffReportsPureReorderingAsOrdering(t *testing.T) {
	want := "CREATE SEQUENCE \"area\".s\n START WITH 1\n NO MINVALUE\n NO MAXVALUE\n CACHE 1;"
	got := "CREATE SEQUENCE \"area\".s\n START WITH 1\n NO MAXVALUE\n NO MINVALUE\n CACHE 1;"
	diff := diffOf(t, want, got)
	if diff == "" {
		t.Fatal("a reordered statement body must be reported")
	}
	if !strings.Contains(diff, "order") {
		t.Errorf("a pure reordering must say so rather than printing an empty body; got:\n%s", diff)
	}
}

// TestDiffAcceptsIdenticalAndReorderedObjects is the counterweight, and it is not
// optional: a comparison made stricter is only an improvement while everything that
// SHOULD pass still passes. Without this, "report a difference" could be satisfied by a
// function that reports one unconditionally.
func TestDiffAcceptsIdenticalAndReorderedObjects(t *testing.T) {
	if diff := diffOf(t, twoTables, twoTables); diff != "" {
		t.Fatalf("identical schemas must compare equal; got:\n%s", diff)
	}

	// The same two tables created in the opposite order. normalizeDump sorts
	// statements, so this must still compare equal — object creation order is not
	// schema, and that property predates this change and must survive it.
	a := "CREATE TABLE \"area\".beta (\n id bigint\n);\nCREATE TABLE \"area\".alpha (\n id bigint\n);"
	b := "CREATE TABLE \"area\".alpha (\n id bigint\n);\nCREATE TABLE \"area\".beta (\n id bigint\n);"
	if diff := statementDiff(splitNormalized(normalizeDump(a)), splitNormalized(normalizeDump(b))); diff != "" {
		t.Fatalf("object creation order is not schema; got:\n%s", diff)
	}
}

// TestSplitNormalizedRoundTrips pins the invariant the symmetry argument rests on: the
// joined form a golden file holds parses back into exactly the statements it was built
// from. If this drifts, the golden on disk stops meaning what the comparison thinks it
// means.
func TestSplitNormalizedRoundTrips(t *testing.T) {
	stmts := splitNormalized(normalizeDump(twoTables))
	if len(stmts) != 2 {
		t.Fatalf("expected 2 statements, got %d: %q", len(stmts), stmts)
	}
	for _, s := range stmts {
		if !strings.HasSuffix(strings.TrimSpace(s), ";") {
			t.Errorf("statement does not end at its terminator: %q", s)
		}
		if strings.Count(s, "CREATE TABLE") != 1 {
			t.Errorf("statements ran together: %q", s)
		}
	}
	if rejoined := strings.Join(stmts, "\n"); rejoined != normalizeDump(twoTables) {
		t.Errorf("split is not the inverse of the join:\n--want--\n%s\n--got--\n%s", normalizeDump(twoTables), rejoined)
	}
}
