// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package tenantpurge

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The catalog rows a TimescaleDB continuous aggregate actually produces, which is what
// makes these tests worth anything. Three relations exist for one aggregate:
//
//   - the raw hypertable, an ordinary 'r' in the area's own schema;
//   - the aggregate itself, relkind 'v' in the area's schema — loadColumns never returns
//     it, so no synthetic row here represents it either;
//   - the materialization hypertable, an ordinary 'r' in _timescaledb_internal, holding
//     the aggregate's rows physically.
//
// The names are the real ones from the shipped schema (event-management's
// measurement_rollups), so a reader can check these against a live database.
const (
	matSchema = "_timescaledb_internal"
	matName   = "_materialized_hypertable_7"
)

func rollupAggregate() aggregate {
	return aggregate{
		ViewSchema: "event-management", ViewName: "measurement_rollups",
		MatSchema: matSchema, MatName: matName,
	}
}

// rawAndMaterialization is the catalog for one hypertable and the materialization of one
// aggregate over it.
func rawAndMaterialization(matTenantColumn, matTenantType string) []column {
	cols := []column{
		col("event-management", "measurement_events", "tenant_id", "character varying"),
		col("event-management", "measurement_events", "occurred_time", "timestamp with time zone"),
		col(matSchema, matName, "bucket", "timestamp with time zone"),
		col(matSchema, matName, "device_token", "character varying"),
	}
	if matTenantColumn != "" {
		cols = append(cols, col(matSchema, matName, matTenantColumn, matTenantType))
	}
	return cols
}

// TestAContinuousAggregatesMaterializationIsSwept is the whole point of aggregate.go.
//
// 🔑 THE CONTROL IS THE FIRST ASSERTION, NOT THE SECOND. Every other table in the
// platform reaches the plan by being in a non-system schema; this one reaches it only
// because loadContinuousAggregates named it. So the test first proves the SAME catalog
// with no aggregate declared leaves the materialization out entirely — otherwise
// "it is in the plan" would be satisfied by a classifier that had simply stopped
// excluding _timescaledb_internal, which would drag every chunk of every hypertable in
// with it.
func TestAContinuousAggregatesMaterializationIsSwept(t *testing.T) {
	cols := rawAndMaterialization("tenant_id", "character varying")

	// Control: without the aggregate catalog saying so, the materialization is invisible.
	unnamed, err := classify(cols, nil, nil)
	require.NoError(t, err)
	for _, e := range unnamed.Entries {
		require.NotEqual(t, matSchema, e.Table.Schema,
			"nothing in a system schema may reach the plan except a relation named individually "+
				"by another catalog; a schema-level escape hatch would admit every chunk")
	}

	// And with it, the materialization is classified and swept like any other table.
	plan, err := classify(cols, nil, []aggregate{rollupAggregate()})
	require.NoError(t, err)

	mat := classOf(t, plan, matSchema, matName)
	assert.Equal(t, ClassDirect, mat.Class,
		"the aggregate keeps a PHYSICAL copy of its rows; a delete against the raw hypertable "+
			"does not reach it and the refresh policy's trailing window never revisits an old bucket")
	assert.Equal(t, "tenant_id", mat.Column)

	// It has to be in Actionable, not merely present: an entry the sweep skips retains
	// the rows just as completely as one that was never classified.
	var swept bool
	for _, e := range plan.Actionable() {
		if e.Table.Schema == matSchema && e.Table.Name == matName {
			swept = true
		}
	}
	assert.True(t, swept, "the materialization is classified but the sweep would not touch it")
}

// TestAMaterializationCarriesItsAggregatesNameIntoEveryMessage covers the part a
// maintainer sees rather than the part the sweep runs.
//
// "_timescaledb_internal._materialized_hypertable_7" is a name TimescaleDB chose and
// nobody wrote; whoever trips the fail-closed gate on one has to be told which aggregate
// it belongs to or they cannot act on it at all.
func TestAMaterializationCarriesItsAggregatesNameIntoEveryMessage(t *testing.T) {
	plan, err := classify(rawAndMaterialization("tenant_id", "character varying"),
		nil, []aggregate{rollupAggregate()})
	require.NoError(t, err)

	mat := classOf(t, plan, matSchema, matName)
	assert.Contains(t, mat.Describe(), "event-management.measurement_rollups",
		"the entry must name the aggregate it belongs to")
	assert.Contains(t, mat.Describe(), matName, "and still name the relation itself")

	// An ordinary table gains nothing, so the provenance stays signal rather than noise.
	raw := classOf(t, plan, "event-management", "measurement_events")
	assert.Equal(t, "event-management.measurement_events", raw.Describe())
}

// TestAMaterializationWithNoTenantColumnFailsTheSweepByName is the fail-closed half, and
// the reason this belongs in the plan rather than in a step the telemetry store runs.
//
// A step only ever covers the aggregates its author knew about. An aggregate whose GROUP
// BY drops the tenant column produces a materialization nothing can erase per-tenant —
// and here that stops the purge with the aggregate named, instead of being skipped in
// silence by a step that never heard of it.
func TestAMaterializationWithNoTenantColumnFailsTheSweepByName(t *testing.T) {
	plan, err := classify(rawAndMaterialization("", ""), nil, []aggregate{rollupAggregate()})
	require.NoError(t, err)

	mat := classOf(t, plan, matSchema, matName)
	require.Equal(t, ClassUnclassified, mat.Class)

	err = checkClassified(plan)
	var unclassified *ErrUnclassified
	require.ErrorAs(t, err, &unclassified)
	assert.Contains(t, err.Error(), "measurement_rollups",
		"the refusal must name the AGGREGATE — the generated relation name is not something a "+
			"maintainer can look up or act on")
}

// TestAnAggregateWhoseMaterializationIsMissingFailsClosed pins the check on the seam
// between the two catalogs.
//
// Admitting a relation by name is only as good as the name matching. If
// timescaledb_information ever reports a materialization pg_class does not have — a
// release that names them differently, a view whose materialization was dropped — the
// aggregate would simply drop out of the plan, and every check this package has would
// report clean over retained rows. Two catalogs disagreeing is worth failing on.
func TestAnAggregateWhoseMaterializationIsMissingFailsClosed(t *testing.T) {
	// The raw hypertable is present; the materialization is not in the catalog at all.
	cols := []column{
		col("event-management", "measurement_events", "tenant_id", "character varying"),
	}
	_, err := classify(cols, nil, []aggregate{rollupAggregate()})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "measurement_rollups")
	assert.Contains(t, err.Error(), matName)
}

// TestADatabaseWithNoAggregatesIsUnchanged is the other half of the control above: ten of
// the eleven functional areas share a plain Postgres cluster with no TimescaleDB at all,
// and this classification must be exactly what it was before aggregates existed.
func TestADatabaseWithNoAggregatesIsUnchanged(t *testing.T) {
	cols := []column{
		col("device-management", "devices", "tenant_id", "character varying"),
		col("pg_catalog", "pg_class", "tenant_id", "character varying"),
		col("_timescaledb_internal", "_hyper_1_2_chunk", "tenant_id", "character varying"),
	}
	plan, err := classify(cols, nil, nil)
	require.NoError(t, err)

	require.Len(t, plan.Entries, 1, "only the area's own table may be classified")
	assert.Equal(t, "device-management.devices", plan.Entries[0].Table.String())
	assert.Empty(t, plan.Entries[0].Origin)
}

// TestAChunkIsStillExcludedWhenAnAggregateIsPresent is the assertion that keeps the
// re-admission narrow. _timescaledb_internal holds the chunks of every hypertable
// alongside the materializations; a mature telemetry database has thousands. Sweeping
// them individually is at best redundant work (the parent's delete already reached them)
// and drags the plan from tens of tables to thousands.
func TestAChunkIsStillExcludedWhenAnAggregateIsPresent(t *testing.T) {
	cols := append(rawAndMaterialization("tenant_id", "character varying"),
		col(matSchema, "_hyper_3_41_chunk", "tenant_id", "character varying"),
		col(matSchema, "compress_hyper_4_42_chunk", "tenant_id", "character varying"),
	)
	plan, err := classify(cols, nil, []aggregate{rollupAggregate()})
	require.NoError(t, err)

	for _, e := range plan.Entries {
		if e.Table.Schema != matSchema {
			continue
		}
		assert.Equal(t, matName, e.Table.Name,
			"only the named materialization may be admitted from the internal schema")
	}
}
