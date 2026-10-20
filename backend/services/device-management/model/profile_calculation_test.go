// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"encoding/json"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"testing"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingObserver struct {
	computed []string
	skipped  []string
}

func (o *recordingObserver) Computed(key string) { o.computed = append(o.computed, key) }
func (o *recordingObserver) Skipped(key string, reason calcfield.SkipReason, _ error) {
	o.skipped = append(o.skipped, key+":"+string(reason))
}

func calcTestDefs() []*MetricDefinition {
	v := &MetricDefinition{MetricKey: "voltage", DataType: "DOUBLE"}
	v.ID = 1
	c := &MetricDefinition{MetricKey: "current", DataType: "DOUBLE"}
	c.ID = 2
	p := &MetricDefinition{MetricKey: "power", DataType: "DOUBLE", Unit: sql.NullString{String: "W", Valid: true},
		Expression: sql.NullString{String: "voltage * current", Valid: true}}
	p.ID = 3
	return []*MetricDefinition{v, c, p}
}

// evaluate runs a resolution's formulas against one sample, as the resolver does.
func evaluate(r ProfileResolution, values map[string]string) ([]resolvedevent.ResolvedMeasurementEntry, *recordingObserver) {
	obs := &recordingObserver{}
	calc := r.BeginCalculation(nil)
	defer calc.Release()
	for k, v := range values {
		if m, ok := r.Metric(k); ok && !m.Calculated() {
			calc.Set(&m, v)
		}
	}
	return calc.Append(nil, obs), obs
}

// The cache stores a calculated metric's formula as text and a decoded entry compiles its
// own: the formula survives the cache, and nothing but source text is ever encoded.
func TestACalculatedMetricSurvivesTheCache(t *testing.T) {
	built := NewProfileResolution(ProfileScope{ProfileVersionToken: "meter@2"}, calcTestDefs())
	require.Equal(t, 1, built.CalculatedCount())
	wire, err := json.Marshal(built)
	require.NoError(t, err)
	assert.Equal(t, `{"Scope":{"DeviceTypeToken":"","ProfileVersionToken":"meter@2","FenceSetVersion":0},"Metrics":[`+
		`{"id":1,"key":"voltage","type":"DOUBLE"},{"id":2,"key":"current","type":"DOUBLE"},`+
		`{"id":3,"key":"power","type":"DOUBLE","unit":"W","expr":"voltage * current"}]}`, string(wire))

	decoded, err := decodeProfileResolution(wire)
	require.NoError(t, err)
	require.Equal(t, 1, decoded.CalculatedCount())
	power, _ := decoded.Metric("power")
	assert.True(t, power.Calculated())
	voltage, _ := decoded.Metric("voltage")
	assert.False(t, voltage.Calculated())

	for name, r := range map[string]ProfileResolution{"built": built, "decoded": *decoded} {
		got, obs := evaluate(r, map[string]string{"voltage": "2", "current": "3.5"})
		require.Lenf(t, got, 1, name)
		assert.Equal(t, "7", got[0].Value, name)
		assert.True(t, got[0].Derived, name)
		assert.EqualValues(t, 3, *got[0].Classifier, name)
		assert.Equal(t, "W", *got[0].Unit, name)
		assert.Equal(t, []string{"power"}, obs.computed, name)
	}
}

// A resolution without formulas takes no sample and appends nothing, and validation of a
// calculated key passes whatever it holds (the resolver drops it).
func TestAResolutionWithoutFormulasCalculatesNothing(t *testing.T) {
	defs := calcTestDefs()[:2]
	r := NewProfileResolution(ProfileScope{}, defs)
	assert.Zero(t, r.CalculatedCount())
	got, obs := evaluate(r, map[string]string{"voltage": "2", "current": "3"})
	assert.Empty(t, got)
	assert.Empty(t, obs.computed)
	assert.Empty(t, obs.skipped)

	calc := NewProfileResolution(ProfileScope{}, calcTestDefs())
	assert.NoError(t, calc.ValidateMeasurement("power", "not a number"), "a calculated key is dropped, not validated")
	assert.Error(t, calc.ValidateMeasurement("voltage", "not a number"), "a measured key is still validated")
}

// Only the inputs a formula reads are parsed: a measured metric no formula names is not an
// input slot, so a reading's other values cost the calculation nothing.
func TestOnlyTheInputsAFormulaReadsAreSlotted(t *testing.T) {
	temp := &MetricDefinition{MetricKey: "temp", DataType: "DOUBLE"}
	r := NewProfileResolution(ProfileScope{}, append(calcTestDefs(), temp))
	for key, want := range map[string]bool{"voltage": true, "current": true, "temp": false, "power": false} {
		m, ok := r.Metric(key)
		require.True(t, ok)
		assert.Equalf(t, want, m.inputSlot > 0, "%s slotted", key)
	}
	// And an unparseable value of an input is the formula's skip, never a zero.
	got, obs := evaluate(r, map[string]string{"voltage": "abc", "current": "3"})
	assert.Empty(t, got)
	assert.Equal(t, []string{"power:invalid_input"}, obs.skipped)
}
