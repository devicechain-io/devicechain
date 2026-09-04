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

// 🔴 THE WHOLE STATEMENT, NOT A HANDFUL OF PREFIXES OF IT. This assertion was a set
// of Contains calls, and two of them could not fail in the direction that mattered:
//
//   - `Contains(stmt, "timescaledb.compress")` is satisfied by the LONGER real
//     substring `timescaledb.compress_segmentby`, so a mutant deleting the
//     `timescaledb.compress,` flag itself — the one that actually turns compression on
//     — survived the whole unit suite;
//   - `Contains(stmt, "compress_segmentby = 'tenant_id'")` (as it read before the keys
//     were widened) is satisfied by `= 'tenant_id, device_token'`, so it could not tell
//     a tenant-only key from a widened one in either direction.
//
// The general shape: an expected substring that is a PREFIX of a longer value the code
// can legitimately produce is not an assertion about the value. The statement is a pure
// function of constants, so pinning it whole costs nothing and closes the class rather
// than the two instances.
func TestEnableCompressionStmt(t *testing.T) {
	assert.Equal(t,
		`ALTER TABLE "event-management"."measurement_events" SET (timescaledb.compress, `+
			`timescaledb.compress_segmentby = 'tenant_id, device_token', `+
			`timescaledb.compress_orderby = 'occurred_time DESC')`,
		enableCompressionStmt("measurement_events"))
}

// The per-hypertable segment keys, asserted whole and one table at a time. This is the
// statement-shape assertion the reconciler's behaviour rests on: it is the ONLY place
// the choice per table is checked, because applyOne just calls the builder.
func TestEnableCompressionStmtSegmentsPerHypertable(t *testing.T) {
	for table, want := range map[string]string{
		// Sampled streams: read by device, and dense enough per device per chunk that
		// the per-device segment still compresses (see the curve on compressSegmentBy).
		"events":             "tenant_id, device_token",
		"location_events":    "tenant_id, device_token",
		"measurement_events": "tenant_id, device_token",
		// Sparse by nature: also read by device, but below the compression break-even,
		// so the tenant-only key is the deliberate answer rather than the un-widened
		// leftover. TestSparseTablesAreNotSegmentedPerDevice carries the reason.
		"alert_events":        "tenant_id",
		"state_change_events": "tenant_id",
		// Anchor-keyed: read by the entity an event is anchored TO, not by device.
		"event_anchors": "tenant_id, anchor_type, anchor_token",
	} {
		assert.Contains(t, enableCompressionStmt(table),
			"timescaledb.compress_segmentby = '"+want+"'",
			"%s must be segmented by the columns it is actually read by", table)
	}
}

// 🔴 THE SPARSE TABLES MUST NOT BE SEGMENTED PER DEVICE, AND THIS TEST EXISTS SO THAT
// A LATER "MAKE THESE CONSISTENT" EDIT HAS TO ARGUE WITH A MEASUREMENT RATHER THAN
// WITH A STYLE. Segmenting per device is not merely a worse ratio below break-even —
// it INVERTS. Measured on TimescaleDB 2.28.3, 10k devices, one tenant, one 24h chunk:
// at 1 row per device per chunk, 'tenant_id' compresses 3.38× while
// 'tenant_id, device_token' compresses 0.33×, i.e. compression TRIPLES the stored
// size; at 4 rows it is still net expansion (0.93×); break-even is ~60–90 rows.
//
// alert_events and state_change_events are sparse BY NATURE, not by fleet: an alert
// and a presence transition are events that by definition do not occur continuously,
// so essentially every realistic deployment sits below that break-even for them. The
// compression policy fires unattended at 7 days on every chunk, so the operator does
// not get to opt out — a low-rate fleet would simply watch "compression" grow its
// disk, with the only lever (the chunk interval) instance-wide.
//
// Both tables ARE read by device, so this key costs read amplification. That is the
// trade taken, on the asymmetry stated over compressSegmentBy: a read that is slower
// is visible and tunable, and storage that silently grows under the name
// "compression" is not.
func TestSparseTablesAreNotSegmentedPerDevice(t *testing.T) {
	for _, table := range []string{"alert_events", "state_change_events"} {
		// Asserted through the built statement rather than through segmentByFor, so a
		// key reintroduced anywhere on the path — the map, the fallback, the builder —
		// fails here. The closing quote is load-bearing: without it the expected string
		// is a prefix of 'tenant_id, device_token' and the assertion cannot fail.
		assert.Containsf(t, enableCompressionStmt(table),
			"timescaledb.compress_segmentby = 'tenant_id'",
			"%s is sparse per device: at 1-4 rows per device per chunk a per-device "+
				"segment key measures 0.33x-0.93x, i.e. compression that EXPANDS. Read "+
				"the curve over compressSegmentBy before widening this", table)
		assert.NotContainsf(t, segmentByFor(table), "device_token",
			"%s must not be segmented per device", table)
	}
}

// A hypertable that joins the lifecycle set must have its segment key CHOSEN, not
// inherited. The fallback in segmentByFor is deliberately silent and always valid, so
// nothing at runtime would report a table that quietly kept the tenant-only setting;
// this is what reports it.
func TestEverySegmentByIsDeclared(t *testing.T) {
	for _, table := range LifecycleHypertables {
		assert.Containsf(t, compressSegmentBy, table,
			"hypertable %q is governed by the lifecycle policies but has no declared "+
				"compress_segmentby, so it silently falls back to tenant_id alone — decide "+
				"how it is read and how densely it is written, then add it to "+
				"compressSegmentBy. \"tenant_id\" is a perfectly good answer to declare; "+
				"what is not allowed is arriving at it by omission", table)
	}
	// The counterweight: an entry for a table nothing governs is dead configuration
	// that reads as coverage.
	for table := range compressSegmentBy {
		assert.Containsf(t, LifecycleHypertables, table,
			"compressSegmentBy declares %q, which is not a governed hypertable", table)
	}
}

// 🔴 Timescale REJECTS an ALTER whose compress_segmentby and compress_orderby share a
// column ("cannot use column X in both..."), and applyOne's exec is best-effort — so
// the failure would be one ERROR log line at startup and a hypertable that silently
// never compresses. Every segment key is checked against the shared orderby column
// here, where it costs nothing, rather than at 3am in a log.
func TestSegmentByNeverCollidesWithOrderBy(t *testing.T) {
	orderCol := strings.TrimSuffix(compressOrderBy, " DESC")
	require.NotEqual(t, compressOrderBy, orderCol, "compressOrderBy must carry a direction")
	for _, table := range LifecycleHypertables {
		for _, col := range strings.Split(segmentByFor(table), ",") {
			assert.NotEqualf(t, orderCol, strings.TrimSpace(col),
				"%s segments by %q, which is also its compress_orderby column — "+
					"Timescale rejects the ALTER", table, orderCol)
		}
	}
}

// tenant_id must be PRESENT in every segment key, and it must LEAD — two properties
// with two different reasons, which an earlier version of this comment ran together.
//
//   - PRESENCE is what the tenant-erasure cost measurement rests on (ADR-077,
//     tenant_purge_cost_test.go): a predicate over segmentby columns alone matches
//     whole compressed rows and never decompresses. It does not have to name all of
//     them, and it does not care where in the list they sit. Measured: with segmentby
//     'device_token, tenant_id' — tenant SECOND — a delete of 20k compressed rows
//     still reports `Batches scanned: 10000, Batches deleted: 10000` and decompresses
//     nothing, identically to tenant first. So dropping tenant_id OUT of a list is the
//     change that would invalidate the measurement; reordering is not.
//   - LEADING buys something narrower: Timescale auto-creates a btree on the
//     compressed chunk over (segmentby columns..., orderby metadata), and a btree
//     serves only a predicate over a PREFIX of its columns. Not every read here is
//     device-scoped but every read is tenant-scoped, so tenant_id first is what keeps
//     that index usable by the tenant-only reads.
//
// The assertion below pins the ORDER, so it is the index-prefix property it guards;
// presence follows from it. Both halves are stated because a future list that drops
// tenant_id would fail this test for the shallower of the two reasons, and the failure
// message should not send the reader to the wrong one.
func TestTenantIdLeadsEverySegmentBy(t *testing.T) {
	for _, table := range LifecycleHypertables {
		cols := strings.Split(segmentByFor(table), ",")
		require.NotEmpty(t, cols)
		assert.Equalf(t, "tenant_id", strings.TrimSpace(cols[0]),
			"%s must segment on tenant_id FIRST (the compressed chunk's auto btree is "+
				"prefixed by the segment key, and every read here is tenant-scoped) and "+
				"must not drop it at all (a tenant delete only avoids decompressing while "+
				"tenant_id is a segmentby column)", table)
	}
}

// The fallback is the pre-existing setting, so an undeclared table degrades to the old
// behaviour rather than to something invalid. Without this, TestEverySegmentByIsDeclared
// could be satisfied by a fallback that returned nonsense.
func TestSegmentByFallsBackToTenantOnly(t *testing.T) {
	assert.Equal(t, "tenant_id", segmentByFor("a_hypertable_nobody_declared"))
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
