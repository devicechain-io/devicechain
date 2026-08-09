// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Every event hypertable is governed by the lifecycle policies: the base events
// table (a bare create_hypertable on events with no policies was the original
// ADR-026 gap), the three payload hypertables, and event_anchors (whose rows must
// age out alongside the events they index, else retention orphans them).
func TestLifecycleHypertablesCoverAll(t *testing.T) {
	assert.ElementsMatch(t,
		[]string{"events", "location_events", "measurement_events", "alert_events", "event_anchors", "state_change_events"},
		LifecycleHypertables)
}

// The hypertable identifier is always schema-qualified and double-quoted so the
// hyphen in the schema name is a valid SQL identifier, not a subtraction.
func TestQualifiedHypertable(t *testing.T) {
	assert.Equal(t, `"event-management"."measurement_events"`, qualifiedHypertable("measurement_events"))
}

func TestSetChunkIntervalStmt(t *testing.T) {
	stmt := setChunkIntervalStmt("events")
	assert.Contains(t, stmt, "set_chunk_time_interval")
	// Identifier embedded as a regclass string literal; interval magnitude is a bound param.
	assert.Contains(t, stmt, `'"event-management"."events"'::regclass`)
	assert.Contains(t, stmt, "make_interval(hours => ?)")
}

func TestEnableCompressionStmt(t *testing.T) {
	stmt := enableCompressionStmt("measurement_events")
	// Segment by tenant (every read is tenant-scoped), order by time DESC (range scans).
	assert.Contains(t, stmt, `ALTER TABLE "event-management"."measurement_events"`)
	assert.Contains(t, stmt, "timescaledb.compress")
	assert.Contains(t, stmt, "compress_segmentby = 'tenant_id'")
	assert.Contains(t, stmt, "compress_orderby = 'occurred_time DESC'")
}

func TestCompressionPolicyStmts(t *testing.T) {
	add := addCompressionPolicyStmt("alert_events")
	assert.Contains(t, add, "add_compression_policy")
	assert.Contains(t, add, `'"event-management"."alert_events"'::regclass`)
	assert.Contains(t, add, "make_interval(days => ?)")

	remove := removeCompressionPolicyStmt("alert_events")
	assert.Contains(t, remove, "remove_compression_policy")
	// if_exists makes the remove a no-op when no policy is present.
	assert.Contains(t, remove, "if_exists => true")
}

func TestRetentionPolicyStmts(t *testing.T) {
	add := addRetentionPolicyStmt("location_events")
	assert.Contains(t, add, "add_retention_policy")
	assert.Contains(t, add, `'"event-management"."location_events"'::regclass`)
	assert.Contains(t, add, "make_interval(days => ?)")

	remove := removeRetentionPolicyStmt("location_events")
	assert.Contains(t, remove, "remove_retention_policy")
	assert.Contains(t, remove, "if_exists => true")
}

// Every statement targets exactly one hypertable and never a wildcard/all-tables
// form — a guard against an accidental broad drop.
func TestStatementsAreSingleTable(t *testing.T) {
	for _, table := range LifecycleHypertables {
		for _, stmt := range []string{
			setChunkIntervalStmt(table),
			enableCompressionStmt(table),
			addCompressionPolicyStmt(table),
			removeCompressionPolicyStmt(table),
			addRetentionPolicyStmt(table),
			removeRetentionPolicyStmt(table),
		} {
			assert.Equal(t, 1, strings.Count(stmt, qualifiedHypertable(table)),
				"statement should reference its hypertable exactly once: %q", stmt)
		}
	}
}

// ---- per-table retention override (location) ----
//
// Location retention is independently configurable, defaulting to the uniform
// window. Both directions have to be pinned: an override mechanism that always
// overrides is not an override, and one that never does is not a mechanism.

func intPtr(n int) *int { return &n }

// capturedExec is one statement the reconciler would have sent, with its bound
// parameters — the interval magnitude rides as a `?` param, so the VALUE lives here
// and not in the SQL text.
type capturedExec struct {
	sql  string
	vars []interface{}
}

// dryRunDB returns a gorm handle that BUILDS every statement and executes none,
// recording each one. This is what lets the assertions below be about what the
// reconciler would apply rather than about a helper that merely resembles it:
// applyOne itself is driven, on the real statement builders, with the real policy.
// A live TimescaleDB is unavailable here (sqlite has neither the catalog functions
// nor chunks), and dry-run is the honest substitute — it captures intent exactly and
// claims nothing about the engine's response.
func dryRunDB(t *testing.T, sink *[]capturedExec) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{DryRun: true})
	require.NoError(t, err, "open sqlite")
	require.NoError(t, db.Callback().Raw().After("gorm:raw").
		Register("test:capture-lifecycle", func(tx *gorm.DB) {
			*sink = append(*sink, capturedExec{sql: tx.Statement.SQL.String(), vars: tx.Statement.Vars})
		}), "register capture callback")
	return db
}

// retentionAppliedPerTable drives applyOne over every governed hypertable and
// returns, per table, the retention window the reconciler would install. A table
// absent from the map had NO add_retention_policy issued (retention off for it).
func retentionAppliedPerTable(t *testing.T, policy DataLifecyclePolicy) map[string]int {
	t.Helper()
	var captured []capturedExec
	db := dryRunDB(t, &captured)

	applied := map[string]int{}
	for _, table := range LifecycleHypertables {
		captured = captured[:0]
		require.NoError(t, applyOne(db, table, policy), "applyOne(%s)", table)

		// The remove always runs, on every table, in both directions — it is what makes
		// a changed window replace the old one and a disabled window converge to none.
		sawRemove := false
		for _, c := range captured {
			if strings.Contains(c.sql, "remove_retention_policy") {
				sawRemove = true
			}
			if strings.Contains(c.sql, "add_retention_policy") {
				require.Contains(t, c.sql, qualifiedHypertable(table),
					"an add must target its own hypertable")
				require.Len(t, c.vars, 1, "the retention window rides as one bound parameter")
				days, ok := c.vars[0].(int)
				require.True(t, ok, "retention window should be an int, got %T", c.vars[0])
				applied[table] = days
			}
		}
		require.True(t, sawRemove, "%s: the retention-policy remove must always run", table)
	}
	return applied
}

// With NO override set, location ages out on exactly the uniform window — nothing
// changes for an operator who never sets the knob. This is the direction that fails
// if the override is wired to apply unconditionally.
func TestRetentionUniformWhenNoLocationOverride(t *testing.T) {
	applied := retentionAppliedPerTable(t, DataLifecyclePolicy{
		ChunkIntervalHours: 24, CompressAfterDays: 7, RetentionDays: 365,
	})

	for _, table := range LifecycleHypertables {
		assert.Equal(t, 365, applied[table],
			"%s must age out on the uniform window when no override is set", table)
	}
}

// With an override set, ONLY location_events moves. This is the direction that fails
// if the override is ignored — and the "every other table is untouched" half is what
// fails if it leaks onto the rest.
func TestRetentionLocationOverrideAppliesToLocationOnly(t *testing.T) {
	applied := retentionAppliedPerTable(t, DataLifecyclePolicy{
		ChunkIntervalHours: 24, CompressAfterDays: 7, RetentionDays: 365,
		LocationRetentionDays: intPtr(30),
	})

	assert.Equal(t, 30, applied[LocationHypertable],
		"a set location override must be the window location_events is given")
	for _, table := range LifecycleHypertables {
		if table == LocationHypertable {
			continue
		}
		assert.Equal(t, 365, applied[table],
			"%s must keep the uniform window; the override is location-only", table)
	}
}

// An override LONGER than the uniform window is honored too. It is an override, not
// a floor or a ceiling — a `min(uniform, override)` implementation would pass the
// test above and fail here.
func TestRetentionLocationOverrideMayExceedUniform(t *testing.T) {
	applied := retentionAppliedPerTable(t, DataLifecyclePolicy{
		ChunkIntervalHours: 24, CompressAfterDays: 7, RetentionDays: 30,
		LocationRetentionDays: intPtr(365),
	})

	assert.Equal(t, 365, applied[LocationHypertable], "the override wins in both directions")
	assert.Equal(t, 30, applied["measurement_events"], "the uniform window is unchanged")
}

// An explicit 0 turns retention OFF for location alone — distinct from unset, which
// inherits. This is why the field is a pointer, and the assertion is the ABSENCE of
// an add_retention_policy for that one table while the others still get one.
func TestRetentionLocationOverrideZeroDisablesLocationOnly(t *testing.T) {
	applied := retentionAppliedPerTable(t, DataLifecyclePolicy{
		ChunkIntervalHours: 24, CompressAfterDays: 7, RetentionDays: 365,
		LocationRetentionDays: intPtr(0),
	})

	_, present := applied[LocationHypertable]
	assert.False(t, present, "an explicit 0 must add NO retention policy for location")
	assert.Equal(t, 365, applied["measurement_events"],
		"disabling location retention must not disable it elsewhere")
}

// The mirror image: retention off globally, on for location. Nothing else gets a
// policy, so a uniform-window read of the override would leave location unprotected.
func TestRetentionLocationOverrideWithUniformOff(t *testing.T) {
	applied := retentionAppliedPerTable(t, DataLifecyclePolicy{
		ChunkIntervalHours: 24, CompressAfterDays: 7, RetentionDays: 0,
		LocationRetentionDays: intPtr(30),
	})

	assert.Equal(t, 30, applied[LocationHypertable],
		"location retention may be on while the uniform window is off")
	assert.Len(t, applied, 1, "no other hypertable may receive a retention policy")
}

// The resolution itself, stated directly: only location_events consults the
// override, and only when one is set.
func TestRetentionDaysForResolution(t *testing.T) {
	uniform := DataLifecyclePolicy{RetentionDays: 90}
	assert.Equal(t, 90, uniform.retentionDaysFor(LocationHypertable),
		"unset override inherits the uniform window")

	overridden := DataLifecyclePolicy{RetentionDays: 90, LocationRetentionDays: intPtr(7)}
	assert.Equal(t, 7, overridden.retentionDaysFor(LocationHypertable))
	assert.Equal(t, 90, overridden.retentionDaysFor("events"),
		"the base events table is not location and must not see the override")
	assert.Equal(t, 90, overridden.retentionDaysFor("measurement_events"))
}

// LocationHypertable must name a hypertable that is actually governed; a typo would
// silently make the override apply to nothing at all.
func TestLocationHypertableIsGoverned(t *testing.T) {
	assert.Contains(t, LifecycleHypertables, LocationHypertable)
}
