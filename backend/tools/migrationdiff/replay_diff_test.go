// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// snapshot.diff is the whole verdict of the replay gate, and until now nothing tested it.
//
// 🔴 That mattered more than it looks, because every seed in every chain happens to be
// idempotent today (user-management's uses FirstOrCreate). So the rows comparison has
// never once fired, and a live green is exactly what a comparison that could not fire
// would also print. The concrete failure: rename the SQL alias `n`, or the struct field
// it maps to, and gorm scans nothing — every count becomes 0, every diff is empty, and
// the gate is green forever with a dimension that no longer exists.
//
// These are pure and need no database, which is the point: the check that proves the
// comparison can fail should not depend on the container that makes it hard to run.

func snap(normalized, probe string, rows map[string]int64) snapshot {
	return snapshot{normalized: normalized, rawProbe: probe, rows: rows}
}

func TestIdenticalSnapshotsDiffClean(t *testing.T) {
	s := snap("CREATE TABLE a;", "TIMESCALE HYPERTABLE x;", map[string]int64{"a": 3})
	assert.NoError(t, s.diff(s))
}

func TestASchemaChangeIsReported(t *testing.T) {
	before := snap("CREATE TABLE a;", "", nil)
	after := snap("CREATE TABLE a;\nCREATE INDEX a_idx1 ON a;", "", nil)

	err := before.diff(after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CHANGED THE SCHEMA")
	assert.Contains(t, err.Error(), "a_idx1", "the diff must name what appeared")
}

// The Timescale comparison is the one normalize actively defeats: it scrubs the
// materialization-hypertable number, which is the digit that moves when a continuous
// aggregate is dropped and recreated — and DROP + CREATE is the obvious way to make a cagg
// step re-runnable, which is why this dimension compares the RAW probe. The baseline used
// to do exactly that, and this probe is what caught it: the migration ran twice without
// erroring while discarding the materialization. It creates only when absent now; the probe
// stays because the next author will reach for DROP + CREATE too. This test is what proves
// the raw dimension is actually being used.
func TestARecreatedContinuousAggregateIsReported(t *testing.T) {
	const normalized = "CREATE VIEW rollups;" // identical either way once scrubbed
	before := snap(normalized, "TIMESCALE CONTINUOUS AGGREGATE rollups MATERIALIZATION _materialized_hypertable_7;", nil)
	after := snap(normalized, "TIMESCALE CONTINUOUS AGGREGATE rollups MATERIALIZATION _materialized_hypertable_9;", nil)

	err := before.diff(after)
	require.Error(t, err, "a recreated materialization must not pass as clean")
	assert.Contains(t, err.Error(), "CHANGED A TIMESCALE OBJECT")
	assert.Contains(t, err.Error(), "_materialized_hypertable_9")
}

func TestARowCountChangeIsReported(t *testing.T) {
	before := snap("", "", map[string]int64{"seeds": 3})
	after := snap("", "", map[string]int64{"seeds": 6})

	err := before.diff(after)
	require.Error(t, err, "a seed with no ON CONFLICT clause duplicates silently; this is what sees it")
	assert.Contains(t, err.Error(), "CHANGED ROWS")
	assert.Contains(t, err.Error(), "seeds: 3 rows -> 6 rows")
}

func TestATableAppearingOrDisappearingIsReported(t *testing.T) {
	err := snap("", "", map[string]int64{"a": 1}).diff(snap("", "", map[string]int64{"a": 1, "b": 0}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "b: table appeared")

	err = snap("", "", map[string]int64{"a": 1, "b": 2}).diff(snap("", "", map[string]int64{"a": 1}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "b: table disappeared")
}

// A zero count must be a real observation, not an absent one. This is the shape the
// alias-rename failure takes: every table reads 0, which is indistinguishable from
// "every table was empty" unless zero and missing are different things.
func TestAZeroCountIsNotTheSameAsAMissingTable(t *testing.T) {
	err := snap("", "", map[string]int64{"a": 0}).diff(snap("", "", map[string]int64{}))
	require.Error(t, err, "a table that vanished must not read as a table with no rows")
	assert.Contains(t, err.Error(), "table disappeared")
}

// The verdict must not swallow a harness failure as evidence about a migration. Without
// this, a container reset mid-dump on the one registered known-bad migration would print
// "the defect moved — update the entry's symptom", which is an instruction to write down
// a symptom describing an infrastructure outage.
func TestAHarnessFailureIsNotReadAsEvidenceAboutTheDefect(t *testing.T) {
	ex := replayExemption{area: "a", id: "1", symptom: "cannot alter type", reason: "a registered defect"}

	msg := exemptionVerdict(ex, &harnessError{errors.New("dumping schema: docker exec: connection reset")})
	require.NotEmpty(t, msg)
	assert.Contains(t, msg, "harness itself failed")
	assert.NotContains(t, msg, "defect moved",
		"a harness outage must not be reported as the defect having changed")
	assert.NotContains(t, msg, "Delete its entry",
		"nor as the defect having been fixed")
}

// And a harness failure that happens to contain the registered symptom must still not be
// confirmed as the known defect — the type decides, not the text.
func TestAHarnessFailureIsNotConfirmedEvenIfItContainsTheSymptom(t *testing.T) {
	ex := replayExemption{area: "a", id: "1", symptom: "cannot alter type", reason: "a registered defect"}
	msg := exemptionVerdict(ex, &harnessError{errors.New("the FIRST run failed: cannot alter type")})
	assert.Contains(t, msg, "harness itself failed")
}
