// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"fmt"
	"math/rand"
	"sort"
	"testing"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

// The whole-plan evaluator against the LIVE resolver. The same profile version's metrics are
// loaded into a device-management ProfileResolution (what the resolver runs, through
// BeginCalculation, Set and Append) and into a calcfield.Plan evaluated by PlanEvaluator (what
// a backfill runs over stored rows). The same readings go through both, and every derived value
// and every skip must agree. Readings carry at most eight decimals, so the stored form (a
// decimal(20,8)) is the device's own and the two must match exactly: that is the equality a
// backfill promises for a reading that has them.

type parityMetric struct {
	key, dataType, expr string
	min, max            *float64
}

func parityProfile() []parityMetric {
	hi, lo := 100.0, -100.0
	return []parityMetric{
		{key: "voltage", dataType: "DOUBLE"},
		{key: "current", dataType: "DOUBLE"},
		{key: "pf_in", dataType: "DOUBLE"},
		{key: "fan", dataType: "INT"},
		{key: "door_open", dataType: "BOOLEAN"},
		// kva -> kw -> pf, and pf reads kva too (a diamond over kva); depth 3.
		{key: "kva", dataType: "DOUBLE", expr: "voltage * current / 1000.0"},
		{key: "kw", dataType: "DOUBLE", expr: "kva * pf_in"},
		{key: "pf", dataType: "DOUBLE", expr: "kw / kva", min: &lo, max: &hi},
		// a guarded read of a chained metric: runs even when kw produced nothing.
		{key: "kw_or_zero", dataType: "DOUBLE", expr: "has(m.kw) ? kw : 0.0"},
		{key: "fan_next", dataType: "INT", expr: "fan + 1"},
		{key: "fan_ratio", dataType: "DOUBLE", expr: "double(fan_next) / double(fan)"},
		{key: "alarmish", dataType: "BOOLEAN", expr: "door_open && voltage > 200.0"},
		{key: "calibrated", dataType: "DOUBLE", expr: "voltage * device.calibration"},
	}
}

type parityRig struct {
	live dmmodel.ProfileResolution
	plan *calcfield.Plan
	keys []string
}

func newParityRig(t *testing.T) *parityRig {
	t.Helper()
	r := &parityRig{}
	var defs []*dmmodel.MetricDefinition
	var measured []calcfield.Input
	var calcDefs []calcfield.Definition
	for i, m := range parityProfile() {
		d := &dmmodel.MetricDefinition{MetricKey: m.key, DataType: m.dataType}
		d.ID = uint(i + 1)
		if m.expr != "" {
			d.Expression = sql.NullString{String: m.expr, Valid: true}
		}
		if m.min != nil {
			d.MinValue = sql.NullFloat64{Float64: *m.min, Valid: true}
		}
		if m.max != nil {
			d.MaxValue = sql.NullFloat64{Float64: *m.max, Valid: true}
		}
		defs = append(defs, d)
		ty, ok := calcfield.ParseType(m.dataType)
		require.True(t, ok)
		if m.expr == "" {
			measured = append(measured, calcfield.Input{Key: m.key, Type: ty})
			continue
		}
		calcDefs = append(calcDefs, calcfield.Definition{Key: m.key, Expression: m.expr, Type: ty, Min: m.min, Max: m.max})
		r.keys = append(r.keys, m.key)
	}
	r.live = dmmodel.NewProfileResolution(dmmodel.ProfileScope{ProfileVersionToken: "p@1"}, defs)
	var err error
	r.plan, err = calcfield.NewPlan(measured, calcDefs)
	require.NoError(t, err)
	require.Empty(t, r.plan.Problems())
	return r
}

type recObserver struct {
	skipped map[string]calcfield.SkipReason
}

func (o *recObserver) Computed(string) {}
func (o *recObserver) Skipped(k string, r calcfield.SkipReason, _ error) {
	o.skipped[k] = r
}

// runLive is the resolver's evaluation of one sample: raw strings, as the device sent them.
func (r *parityRig) runLive(raw map[string]string, md *datatypes.JSON) (map[string]string, map[string]calcfield.SkipReason) {
	obs := &recObserver{skipped: map[string]calcfield.SkipReason{}}
	calc := r.live.BeginCalculation(r.live.DeviceConstants(md))
	defer calc.Release()
	for k, v := range raw {
		if m, ok := r.live.Metric(k); ok && !m.Calculated() {
			calc.Set(&m, v)
		}
	}
	vals := map[string]string{}
	for _, e := range calc.Append(nil, obs) {
		vals[e.Name] = e.Value
	}
	return vals, obs.skipped
}

// runStored is the backfill's: the stored numbers, through the PlanEvaluator.
func runStored(ev *PlanEvaluator, stored map[string]float64) (map[string]string, map[string]calcfield.SkipReason) {
	ev.Begin()
	names := make([]string, 0, len(stored))
	for k := range stored {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		ev.Add(k, sql.NullFloat64{Float64: stored[k], Valid: true})
	}
	vals := map[string]string{}
	skipped := map[string]calcfield.SkipReason{}
	for _, o := range ev.Eval() {
		if o.OK {
			vals[o.Key] = o.Value
		} else {
			skipped[o.Key] = o.Skip
		}
	}
	return vals, skipped
}

func TestPlanEvaluatorMatchesTheLiveResolver(t *testing.T) {
	rig := newParityRig(t)
	md := datatypes.JSON(`{"calibration": 1.5}`)
	ev, err := NewPlanEvaluator(rig.plan, rig.keys, []calcfield.Constant{calcfield.ConstantFromString("calibration", "1.5")})
	require.NoError(t, err)
	defer ev.Release()

	rnd := rand.New(rand.NewSource(7))
	pick := func(vals ...float64) float64 { return vals[rnd.Intn(len(vals))] }
	computed, skipped := 0, 0
	for n := 0; n < 600; n++ {
		raw := map[string]string{}
		stored := map[string]float64{}
		set := func(key, text string, num float64) { raw[key], stored[key] = text, num }
		// Each measured input is present about 80% of the time; values include zero (division
		// by zero), negatives, and a magnitude that overflows the range check.
		if rnd.Intn(5) > 0 {
			v := pick(0, 1, 230, -12.5, 0.00000001, 1e6, 240.12345678)
			set("voltage", fmt.Sprintf("%v", v), v)
		}
		if rnd.Intn(5) > 0 {
			v := pick(0, 5, 10.25, -3, 0.5)
			set("current", fmt.Sprintf("%v", v), v)
		}
		if rnd.Intn(5) > 0 {
			v := pick(0, 0.9, 1, 0.75)
			set("pf_in", fmt.Sprintf("%v", v), v)
		}
		if rnd.Intn(5) > 0 {
			v := pick(0, 1, 2, 3, -1)
			set("fan", fmt.Sprintf("%d", int(v)), v)
		}
		if rnd.Intn(5) > 0 {
			if rnd.Intn(2) == 0 {
				set("door_open", "true", 1)
			} else {
				set("door_open", "false", 0)
			}
		}
		liveVals, liveSkips := rig.runLive(raw, &md)
		gotVals, gotSkips := runStored(ev, stored)
		require.Equal(t, liveVals, gotVals, "sample %d %v", n, raw)
		require.Equal(t, liveSkips, gotSkips, "sample %d %v", n, raw)
		computed += len(gotVals)
		skipped += len(gotSkips)
	}
	// Both outcomes were exercised, or the equality above proves little.
	assert.Greater(t, computed, 500)
	assert.Greater(t, skipped, 500)
}

// The skip REASONS agree too, and a guarded read of a chained metric runs when its dependency
// produced nothing.
func TestPlanEvaluatorSkipReasonsMatchLive(t *testing.T) {
	rig := newParityRig(t)
	ev, err := NewPlanEvaluator(rig.plan, rig.keys, nil)
	require.NoError(t, err)
	defer ev.Release()
	liveVals, liveSkips := rig.runLive(map[string]string{"voltage": "1", "current": "0", "pf_in": "1"}, nil)
	gotVals, gotSkips := runStored(ev, map[string]float64{"voltage": 1, "current": 0, "pf_in": 1})
	assert.Equal(t, liveSkips, gotSkips)
	assert.Equal(t, liveVals, gotVals)
	assert.Equal(t, calcfield.SkipNonFinite, gotSkips["pf"], "kw / kva with kva 0")
	assert.Equal(t, calcfield.SkipMissingInput, gotSkips["calibrated"], "no constants were given")
	assert.Equal(t, "0", gotVals["kw_or_zero"], "kw is 0 so the guard reads it")
}

// Evaluating a subset: the closure of the requested keys runs (a dependency feeds the key that
// reads it), nothing else does, and the values equal the whole plan's.
func TestPlanEvaluatorEvaluatesOnlyTheClosure(t *testing.T) {
	rig := newParityRig(t)
	all, err := NewPlanEvaluator(rig.plan, rig.keys, nil)
	require.NoError(t, err)
	defer all.Release()
	sub, err := NewPlanEvaluator(rig.plan, []string{"pf"}, nil)
	require.NoError(t, err)
	defer sub.Release()

	stored := map[string]float64{"voltage": 230, "current": 10, "pf_in": 0.9, "fan": 2}
	want, _ := runStored(all, stored)
	got, _ := runStored(sub, stored)
	assert.Equal(t, map[string]string{"kva": want["kva"], "kw": want["kw"], "pf": want["pf"]}, got)
	assert.NotEmpty(t, want["pf"])

	_, err = NewPlanEvaluator(rig.plan, []string{"voltage"}, nil)
	assert.Error(t, err, "a measured metric is not a calculated one")
}

// A key a sample holds twice keeps the first row, and a row without a value is absent.
func TestPlanEvaluatorFirstRowWinsAndNullIsAbsent(t *testing.T) {
	rig := newParityRig(t)
	ev, err := NewPlanEvaluator(rig.plan, []string{"fan_next"}, nil)
	require.NoError(t, err)
	defer ev.Release()
	ev.Begin()
	v, ok := ev.Add("fan", sql.NullFloat64{Float64: 1, Valid: true})
	assert.True(t, ok)
	assert.Equal(t, "1", v)
	_, ok = ev.Add("fan", sql.NullFloat64{Float64: 9, Valid: true})
	assert.False(t, ok)
	_, ok = ev.Add("unknown", sql.NullFloat64{Float64: 9, Valid: true})
	assert.False(t, ok, "not an input of the plan")
	assert.Equal(t, "2", ev.Eval()[0].Value)

	ev.Begin()
	_, ok = ev.Add("fan", sql.NullFloat64{})
	assert.False(t, ok)
	out := ev.Eval()[0]
	assert.False(t, out.OK)
	assert.Equal(t, calcfield.SkipMissingInput, out.Skip)
}
