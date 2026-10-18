// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Preview parity (ADR-082): the rule preview re-derives a replayed event's calculated values
// under a DRAFT with model.ProfileResolution.Recalculate. These tests hold it to the resolver:
// recalculating, under the draft, an event the PUBLISHED version resolved must give exactly
// what the resolver gives when it resolves the device's reading under the draft itself.

// countingObserver records every outcome, by key and reason.
type countingObserver struct {
	computed map[string]int
	skipped  map[string]int
}

func newCountingObserver() *countingObserver {
	return &countingObserver{computed: map[string]int{}, skipped: map[string]int{}}
}

func (o *countingObserver) Computed(key string) { o.computed[key]++ }
func (o *countingObserver) Skipped(key string, reason calcfield.SkipReason, _ error) {
	o.skipped[key+":"+string(reason)]++
}

// parityMeasured is the measured vocabulary both versions declare.
func parityMeasured() []*model.MetricDefinition {
	return []*model.MetricDefinition{
		measured(1, "voltage", model.MetricDouble, "V"),
		measured(2, "current", model.MetricDouble, "A"),
		measured(3, "temp", model.MetricDouble, "Cel"),
		measured(4, "fan", model.MetricInt, ""),
		measured(5, "door", model.MetricBoolean, ""),
	}
}

// publishedVersion is the version history was resolved under.
func publishedVersion() model.ProfileResolution {
	return model.NewProfileResolution(model.ProfileScope{ProfileVersionToken: "meter@1"}, append(parityMeasured(),
		calculatedDef(10, "power_kw", model.MetricDouble, "kW", "voltage * current / 1000"),
		calculatedDef(11, "retired", model.MetricDouble, "", "temp * 2.0"),
	))
}

// draftVersion edits power_kw, deletes retired, and adds three formulas, one of them over a
// boolean input and one with a range.
func draftVersion() model.ProfileResolution {
	power := calculatedDef(20, "power_kw", model.MetricDouble, "W", "voltage * current")
	bounded := calculatedDef(23, "temp_f", model.MetricDouble, "[degF]", "temp * 1.8 + 32.0")
	bounded.MaxValue.Float64, bounded.MaxValue.Valid = 200, true
	return model.NewProfileResolution(model.ProfileScope{ProfileVersionToken: "meter@draft"}, append(parityMeasured(),
		power,
		calculatedDef(21, "open_and_hot", model.MetricBoolean, "", "door && temp > 30.0"),
		calculatedDef(22, "fan_pct", model.MetricInt, "%", "has(m.fan) ? fan * 100 / 3 : 0"),
		bounded,
	))
}

// parityCorpus is the readings both resolutions see: full-precision decimals (more than the
// eight places event storage keeps — the replayed event carries the device's string, not the
// stored row), every boolean spelling the validator admits, integers, absent inputs, an
// invalid and a non-finite input, a division and a range skip.
func parityCorpus() []map[string]string {
	return []map[string]string{
		{"voltage": "230.5", "current": "12.25", "temp": "81.25", "fan": "2", "door": "true"},
		{"voltage": "230.123456789012", "current": "0.000000015", "temp": "21.123456789", "door": "FALSE"},
		{"voltage": "1e3", "current": "-0.5", "temp": "-40", "fan": "0", "door": "1"},
		{"voltage": "0.1", "current": "0.2", "temp": "120", "fan": "3", "door": "t"},
		{"temp": "30.000000001", "door": "0"},
		{"voltage": "NaN", "current": "1", "temp": "Inf", "fan": "1", "door": "F"},
		{"voltage": "230", "fan": "9223372036854775807", "extra": "7.5"},
		{"current": "5", "door": "true", "power_kw": "999"},
		{},
	}
}

// TestRecalculateEqualsResolvingUnderTheDraft is the parity contract: for every reading,
// Recalculate(draft, resolve(published, reading)) == resolve(draft, reading), entry for entry
// — raw entries, derived values, stamps and order.
func TestRecalculateEqualsResolvingUnderTheDraft(t *testing.T) {
	published, draft := publishedVersion(), draftVersion()
	for i, reading := range parityCorpus() {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			stored := resolveSamples(t, published, testCalcMetrics(), reading).Entries[0].Entries
			want := resolveSamples(t, draft, testCalcMetrics(), reading).Entries[0].Entries
			before := mustJSON(t, stored)

			got := draft.Recalculate(stored, nil, newCountingObserver())

			assert.Equal(t, want, got, "the draft's recalculation differs from resolving under the draft")
			assert.Equal(t, before, mustJSON(t, stored), "Recalculate wrote to the stored entries")
		})
	}
}

// The corpus is not vacuous: it exercises every formula both ways, and the published
// version's values really are different from the draft's, so a Recalculate that kept them
// would fail the test above. This is that test's negative control, run against the other
// obvious design — add a draft value only where the stored event has none — which keeps
// the published power_kw and the deleted retired.
func TestKeepingStoredCalculatedValuesFailsParity(t *testing.T) {
	published, draft := publishedVersion(), draftVersion()
	obs := newCountingObserver()
	diverged := 0
	for _, reading := range parityCorpus() {
		stored := resolveSamples(t, published, testCalcMetrics(), reading).Entries[0].Entries
		want := resolveSamples(t, draft, testCalcMetrics(), reading).Entries[0].Entries
		if !assert.ObjectsAreEqual(want, addOnlyWhereAbsent(draft, stored, obs)) {
			diverged++
		}
		draft.Recalculate(stored, nil, obs)
	}
	assert.Greater(t, diverged, 0, "keeping stored values matched the draft on every reading: the corpus cannot tell the designs apart")
	for _, key := range []string{"power_kw", "open_and_hot", "fan_pct", "temp_f"} {
		assert.Greater(t, obs.computed[key], 0, "%s never produced a value over the corpus", key)
	}
	assert.Greater(t, obs.skipped["temp_f:range"], 0, "the corpus never reaches the range skip")
	assert.Greater(t, obs.skipped["power_kw:missing_input"], 0, "the corpus never reaches a missing input")
	assert.Greater(t, obs.skipped["power_kw:non_finite"], 0, "the corpus never reaches a non-finite input")
}

// addOnlyWhereAbsent is the rejected design: keep the stored derived entries and append a
// draft value only for a key with none.
func addOnlyWhereAbsent(draft model.ProfileResolution, stored []model.ResolvedMeasurementEntry,
	obs model.CalculationObserver) []model.ResolvedMeasurementEntry {
	have := map[string]bool{}
	for _, e := range stored {
		if e.Derived {
			have[e.Name] = true
		}
	}
	out := slices.Clone(stored)
	for _, e := range draft.Recalculate(stored, nil, obs) {
		if e.Derived && !have[e.Name] {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b model.ResolvedMeasurementEntry) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// The one documented difference: a value the published version DROPPED is not in the
// stored event, so a draft that would have kept it cannot recover it. Here the device sends
// "retired", which the published version calculates (so the device's copy is dropped at
// resolve) and the draft no longer declares (so resolving under the draft keeps it as an
// undeclared reading).
func TestAValueThePublishedVersionDroppedIsNotRecovered(t *testing.T) {
	published, draft := publishedVersion(), draftVersion()
	reading := map[string]string{"temp": "20", "retired": "1"}
	stored := resolveSamples(t, published, testCalcMetrics(), reading).Entries[0].Entries
	want := resolveSamples(t, draft, testCalcMetrics(), reading).Entries[0].Entries
	got := draft.Recalculate(stored, nil, newCountingObserver())
	assert.Equal(t, []string{"fan_pct*", "retired", "temp", "temp_f*"}, names(want))
	assert.Equal(t, []string{"fan_pct*", "temp", "temp_f*"}, names(got))
}

// A raw value stored under a key the DRAFT calculates is dropped, as the resolver drops a
// device-sent value under a calculated key: the draft's formula is the only value the key
// has. Here "extra" is undeclared by the published version, so its device-sent value is
// stored as a raw entry, and the draft turns it into a calculated metric. Keeping the raw
// entry would hand the rule two values under one key.
func TestARawValueUnderAKeyTheDraftCalculatesIsDropped(t *testing.T) {
	published := publishedVersion()
	draft := model.NewProfileResolution(model.ProfileScope{ProfileVersionToken: "meter@draft"}, append(parityMeasured(),
		calculatedDef(30, "extra", model.MetricDouble, "", "temp * 1.0")))
	reading := map[string]string{"temp": "20", "extra": "7.5"}
	stored := resolveSamples(t, published, testCalcMetrics(), reading).Entries[0].Entries
	require.Contains(t, names(stored), "extra", "the fixture must store the device's extra as a raw entry")
	want := resolveSamples(t, draft, testCalcMetrics(), reading).Entries[0].Entries
	got := draft.Recalculate(stored, nil, newCountingObserver())
	assert.Equal(t, []string{"extra*", "temp"}, names(got))
	assert.Equal(t, want, got, "the draft's recalculation differs from resolving under the draft")
}

// A thousand recalculations of one stored reading are byte-identical: the preview of a draft
// formula does not wobble between runs, replicas or goroutine schedules.
func TestRecalculateIsDeterministic(t *testing.T) {
	published, draft := publishedVersion(), draftVersion()
	stored := resolveSamples(t, published, testCalcMetrics(), parityCorpus()[1]).Entries[0].Entries
	first := mustJSON(t, draft.Recalculate(stored, nil, newCountingObserver()))
	for i := 0; i < 1000; i++ {
		require.Equal(t, first, mustJSON(t, draft.Recalculate(stored, nil, newCountingObserver())), "run %d", i)
	}
}

// A draft with no formulas strips every stored calculated value and the device-sent copies
// of nothing: the raw entries come back exactly as stored.
func TestADraftWithoutFormulasStripsStoredCalculatedValues(t *testing.T) {
	stored := resolveSamples(t, publishedVersion(), testCalcMetrics(), parityCorpus()[0]).Entries[0].Entries
	require.Contains(t, names(stored), "power_kw*")
	none := model.NewProfileResolution(model.ProfileScope{}, parityMeasured())
	got := none.Recalculate(stored, nil, newCountingObserver())
	assert.Equal(t, []string{"current", "door", "fan", "temp", "voltage"}, names(got))
}

func TestCheckDraftCalculatedMetricsIsThePublishGate(t *testing.T) {
	ok := append(parityMeasured(), calculatedDef(9, "p", model.MetricDouble, "", "voltage * current"))
	assert.Empty(t, model.CheckDraftCalculatedMetrics(ok))

	chained := append(parityMeasured(),
		calculatedDef(9, "p", model.MetricDouble, "", "voltage * current"),
		calculatedDef(10, "q", model.MetricDouble, "", "p * 2.0"))
	assert.Empty(t, model.CheckDraftCalculatedMetrics(chained), "a calculated metric may read another")

	cycle := append(parityMeasured(),
		calculatedDef(9, "p", model.MetricDouble, "", "q * 2.0 + voltage"),
		calculatedDef(10, "q", model.MetricDouble, "", "p * 2.0"))
	problems := model.CheckDraftCalculatedMetrics(cycle)
	require.Len(t, problems, 1, "a ring is one problem")
	assert.Contains(t, problems[0], "p -> q -> p")

	wrongType := append(parityMeasured(), calculatedDef(9, "p", model.MetricDouble, "", "fan * 2"))
	assert.Len(t, model.CheckDraftCalculatedMetrics(wrongType), 1)

	many := parityMeasured()
	for i := 0; i < 33; i++ {
		many = append(many, calculatedDef(uint(100+i), fmt.Sprintf("c%d", i), model.MetricDouble, "", "temp * 1.0"))
	}
	problems = model.CheckDraftCalculatedMetrics(many)
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0], "above the maximum of 32")
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
