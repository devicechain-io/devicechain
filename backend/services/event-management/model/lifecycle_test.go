// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Every event hypertable is governed by the lifecycle policies: the base events
// table (a bare create_hypertable on events with no policies was the original
// ADR-026 gap), the three payload hypertables, and event_anchors (whose rows must
// age out alongside the events they index, else retention orphans them).
func TestLifecycleHypertablesCoverAll(t *testing.T) {
	assert.ElementsMatch(t,
		[]string{"events", "location_events", "measurement_events", "alert_events", "event_anchors"},
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
