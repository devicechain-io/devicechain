// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Calculated metrics at resolve (ADR-082): what ResolveMeasurementsEventPayload appends, drops
// and counts.

func measured(id uint, key string, t model.MetricDataType, unit string) *model.MetricDefinition {
	return &model.MetricDefinition{Model: gorm.Model{ID: id}, MetricKey: key, DataType: string(t),
		Unit: sql.NullString{String: unit, Valid: unit != ""}}
}

func calculatedDef(id uint, key string, t model.MetricDataType, unit, expr string) *model.MetricDefinition {
	m := measured(id, key, t, unit)
	m.Expression = sql.NullString{String: expr, Valid: true}
	return m
}

// testCalcMetrics are CalcMetrics built from bare collectors, so a test can read them.
func testCalcMetrics() *CalcMetrics {
	m := &CalcMetrics{
		evaluations:      prometheus.NewCounter(prometheus.CounterOpts{Name: "e"}),
		deviceKeyDropped: prometheus.NewCounter(prometheus.CounterOpts{Name: "d"}),
		skipped:          map[calcfield.SkipReason]prometheus.Counter{},
		warned:           newLruSet[struct{}](calcWarnCapacity),
	}
	for _, r := range calcfield.SkipReasons {
		m.skipped[r] = prometheus.NewCounter(prometheus.CounterOpts{Name: "s_" + string(r)})
	}
	return m
}

func (m *CalcMetrics) skips(r calcfield.SkipReason) float64 { return testutil.ToFloat64(m.skipped[r]) }

// resolveSamples resolves one measurement event of the given sample entries against
// profile, with metrics counting.
func resolveSamples(t *testing.T, profile model.ProfileResolution, metrics *CalcMetrics,
	samples ...map[string]string) *resolvedevent.ResolvedMeasurementsPayload {
	t.Helper()
	device := &model.Device{Model: gorm.Model{ID: 1}, TokenReference: rdb.TokenReference{Token: "meter-1"}}
	return resolveSamplesFor(t, device, profile, metrics, samples...)
}

// deviceByTokenApi answers the by-token lookup with one device: the re-read a device with no
// metadata in hand triggers when its profile's formulas read device constants.
type deviceByTokenApi struct {
	model.DeviceManagementApi
	device *model.Device
}

func (a deviceByTokenApi) DevicesByToken(context.Context, []string) ([]*model.Device, error) {
	return []*model.Device{a.device}, nil
}

// resolveSamplesFor is resolveSamples for a given device record, whose metadata carries the
// per-device constants.
func resolveSamplesFor(t *testing.T, device *model.Device, profile model.ProfileResolution, metrics *CalcMetrics,
	samples ...map[string]string) *resolvedevent.ResolvedMeasurementsPayload {
	t.Helper()
	rez := NewEventResolver(1, nil, "", EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	rez.calcObs.metrics = metrics
	rez.Api = deviceByTokenApi{device: device}
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	entries := make([]esmodel.UnresolvedMeasurementsEntry, len(samples))
	for i, s := range samples {
		when := at.Add(time.Duration(i) * time.Second)
		entries[i] = esmodel.UnresolvedMeasurementsEntry{Measurements: s, OccurredTime: &when}
	}
	event := &esmodel.UnresolvedEvent{Source: "test", Device: "meter-1", EventType: eventtype.Measurement,
		OccurredTime: at, ProcessedTime: at.Add(time.Hour),
		Payload: &esmodel.UnresolvedMeasurementsPayload{Entries: entries}}
	out, err := rez.ResolveMeasurementsEventPayload(core.WithTenant(context.Background(), "acme"), device, profile, event)
	require.NoError(t, err)
	return out.(*resolvedevent.ResolvedMeasurementsPayload)
}

func meterResolution(extra ...*model.MetricDefinition) model.ProfileResolution {
	defs := []*model.MetricDefinition{
		measured(1, "voltage", model.MetricDouble, "V"),
		measured(2, "current", model.MetricDouble, "A"),
		measured(3, "temp", model.MetricDouble, "Cel"),
		measured(4, "fan", model.MetricInt, ""),
	}
	return model.NewProfileResolution(model.ProfileScope{ProfileVersionToken: "meter@1"}, append(defs, extra...))
}

func names(entries []resolvedevent.ResolvedMeasurementEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name
		if e.Derived {
			out[i] += "*"
		}
	}
	return out
}

// A calculated value is appended to its sample as a Derived entry, stamped with its own
// definition's classifier, unit and data type, and takes its place in the name order.
func TestTheResolverAppendsCalculatedValues(t *testing.T) {
	metrics := testCalcMetrics()
	profile := meterResolution(
		calculatedDef(5, "power_kw", model.MetricDouble, "kW", "voltage * current / 1000"),
		calculatedDef(6, "over_temp", model.MetricBoolean, "", "temp > 80.0 && (has(m.fan) ? fan == 0 : true)"),
		calculatedDef(7, "fan_x2", model.MetricInt, "", "fan * 2"),
	)
	require.Equal(t, 3, profile.CalculatedCount())
	got := resolveSamples(t, profile, metrics,
		map[string]string{"voltage": "230.5", "current": "12.25", "temp": "81.25", "fan": "0", "extra": "7"})

	entries := got.Entries[0].Entries
	assert.Equal(t, []string{"current", "extra", "fan", "fan_x2*", "over_temp*", "power_kw*", "temp", "voltage"}, names(entries))
	byName := map[string]resolvedevent.ResolvedMeasurementEntry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	power := byName["power_kw"]
	assert.Equal(t, "2.823625", power.Value)
	assert.EqualValues(t, 5, *power.Classifier)
	assert.Equal(t, "kW", *power.Unit)
	assert.Equal(t, "DOUBLE", *power.DataType)
	assert.Equal(t, "1", byName["over_temp"].Value, "a BOOLEAN is stored as 1/0, as a device-sent one is")
	assert.Nil(t, byName["over_temp"].Unit)
	assert.Equal(t, "0", byName["fan_x2"].Value)
	assert.Equal(t, "INT", *byName["fan_x2"].DataType)
	assert.False(t, byName["voltage"].Derived, "a raw entry is never flagged")
	assert.Nil(t, byName["extra"].Classifier, "an undeclared key still resolves unclassified")
	assert.EqualValues(t, 3, testutil.ToFloat64(metrics.evaluations))

	// The stamp is the entry's own: changing it changes no later resolution.
	*power.Unit = "W"
	again := resolveSamples(t, profile, metrics, map[string]string{"voltage": "230.5", "current": "12.25"})
	for _, e := range again.Entries[0].Entries {
		if e.Name == "power_kw" {
			assert.Equal(t, "kW", *e.Unit, "a derived entry pointed into the shared resolution")
		}
	}
}

// A profile with no formula resolves exactly as it did before calculated metrics: no Derived
// entry, nothing counted.
func TestAFormulaFreeProfileResolvesAsBefore(t *testing.T) {
	metrics := testCalcMetrics()
	profile := meterResolution()
	require.Zero(t, profile.CalculatedCount())
	got := resolveSamples(t, profile, metrics, map[string]string{"voltage": "230.5", "power_kw": "9"})
	assert.Equal(t, []string{"power_kw", "voltage"}, names(got.Entries[0].Entries))
	assert.Zero(t, testutil.ToFloat64(metrics.evaluations))
	assert.Zero(t, testutil.ToFloat64(metrics.deviceKeyDropped))
}

// Each sample entry is computed from its OWN values: a formula never reads an input from a
// sibling sample of the same message, even one a second earlier.
func TestEachSampleIsComputedFromItsOwnValues(t *testing.T) {
	metrics := testCalcMetrics()
	profile := meterResolution(calculatedDef(5, "power_kw", model.MetricDouble, "kW", "voltage * current / 1000"))
	got := resolveSamples(t, profile, metrics,
		map[string]string{"voltage": "230.5", "current": "12.25"},
		map[string]string{"voltage": "231"},
		map[string]string{"voltage": "200", "current": "10"})
	assert.Equal(t, []string{"current", "power_kw*", "voltage"}, names(got.Entries[0].Entries))
	assert.Equal(t, []string{"voltage"}, names(got.Entries[1].Entries), "current is in the other samples, not this one")
	assert.Equal(t, "2", got.Entries[2].Entries[1].Value)
	assert.EqualValues(t, 1, metrics.skips(calcfield.SkipMissingInput))
	assert.EqualValues(t, 2, testutil.ToFloat64(metrics.evaluations))
}

// A value a device sends under a calculated key is dropped and counted — whatever it holds,
// even a value its definition would refuse — and the computed value is stored in its place.
// Validation does not see it either, so a device still sending the key after it became
// calculated loses that one value, never the reading.
func TestADeviceSentCalculatedKeyIsDropped(t *testing.T) {
	metrics := testCalcMetrics()
	bounded := calculatedDef(5, "power_kw", model.MetricDouble, "kW", "voltage * current / 1000")
	bounded.MaxValue = sql.NullFloat64{Float64: 100, Valid: true}
	profile := meterResolution(bounded)

	for _, forged := range []string{"999", "not a number", "1e300"} {
		event := &esmodel.UnresolvedEvent{EventType: eventtype.Measurement,
			Payload: &esmodel.UnresolvedMeasurementsPayload{Entries: []esmodel.UnresolvedMeasurementsEntry{
				{Measurements: map[string]string{"voltage": "230.5", "current": "12.25", "power_kw": forged}}}}}
		reason, err := validateMeasurements(profile, event)
		assert.NoErrorf(t, err, "a device-sent %q under a calculated key must not fail the reading", forged)
		assert.Zero(t, reason)

		got := resolveSamples(t, profile, metrics, map[string]string{"voltage": "230.5", "current": "12.25", "power_kw": forged})
		entries := got.Entries[0].Entries
		require.Equal(t, []string{"current", "power_kw*", "voltage"}, names(entries))
		assert.Equal(t, "2.823625", entries[1].Value, "the computed value, not the device's %q", forged)
	}
	assert.EqualValues(t, 3, testutil.ToFloat64(metrics.deviceKeyDropped))

	// The counterweight: a MEASURED metric's bounds are still enforced by validation.
	event := &esmodel.UnresolvedEvent{EventType: eventtype.Measurement,
		Payload: &esmodel.UnresolvedMeasurementsPayload{Entries: []esmodel.UnresolvedMeasurementsEntry{
			{Measurements: map[string]string{"voltage": "not a number"}}}}}
	_, err := validateMeasurements(profile, event)
	assert.Error(t, err, "skipping calculated keys must not skip measured ones")
}

// Every way a formula can produce nothing skips that one metric for that one sample, with
// its reason counted, and keeps the reading's other values.
func TestSkipsAreCountedAndTheReadingIsKept(t *testing.T) {
	cases := []struct {
		name    string
		def     *model.MetricDefinition
		reading map[string]string
		reason  calcfield.SkipReason
	}{
		{"an input is absent", calculatedDef(5, "power_kw", model.MetricDouble, "", "voltage * current"),
			map[string]string{"voltage": "230.5"}, calcfield.SkipMissingInput},
		// The measurement validator admits NaN and Inf for a DOUBLE (ParseFloat does).
		{"an input is NaN", calculatedDef(5, "power_kw", model.MetricDouble, "", "voltage * current"),
			map[string]string{"voltage": "NaN", "current": "1"}, calcfield.SkipNonFinite},
		{"an input is Inf", calculatedDef(5, "hot", model.MetricBoolean, "", "voltage > 80.0"),
			map[string]string{"voltage": "+Inf"}, calcfield.SkipNonFinite},
		{"a double division by zero", calculatedDef(5, "ratio", model.MetricDouble, "", "voltage / current"),
			map[string]string{"voltage": "1", "current": "0"}, calcfield.SkipNonFinite},
		{"an int division by zero", calculatedDef(5, "per", model.MetricInt, "", "100 / fan"),
			map[string]string{"fan": "0"}, calcfield.SkipDivZero},
		{"outside the declared range", func() *model.MetricDefinition {
			d := calculatedDef(5, "power_kw", model.MetricDouble, "", "voltage * current")
			d.MaxValue = sql.NullFloat64{Float64: 10, Valid: true}
			return d
		}(), map[string]string{"voltage": "230.5", "current": "12.25"}, calcfield.SkipRange},
		// 1e9 * 1e4 = 1e13: FormatDouble renders it, decimal(20,8) refuses it.
		{"beyond what storage holds", calculatedDef(5, "power_kw", model.MetricDouble, "", "voltage * current"),
			map[string]string{"voltage": "1e9", "current": "1e4"}, calcfield.SkipUnstorable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metrics := testCalcMetrics()
			got := resolveSamples(t, meterResolution(tc.def), metrics, tc.reading)
			for _, e := range got.Entries[0].Entries {
				assert.False(t, e.Derived, "%s produced %q", e.Name, e.Value)
			}
			assert.Len(t, got.Entries[0].Entries, len(tc.reading), "the reading's own values are all kept")
			for _, r := range calcfield.SkipReasons {
				want := 0.0
				if r == tc.reason {
					want = 1
				}
				assert.Equalf(t, want, metrics.skips(r), "skipped{reason=%s}", r)
			}
			assert.Zero(t, testutil.ToFloat64(metrics.evaluations))
		})
	}
}

// A published formula that does not compile in this build — here one reading another
// calculated metric, which the publish gate refuses but an older snapshot could carry —
// produces no value on any reading, counted as eval_error, and still drops the device's own
// value for its key. It is never computed from anything else.
func TestAFormulaThatDoesNotCompileHereSkipsLoudly(t *testing.T) {
	metrics := testCalcMetrics()
	profile := meterResolution(
		calculatedDef(5, "power_kw", model.MetricDouble, "", "voltage * current"),
		calculatedDef(6, "power_x2", model.MetricDouble, "", "voltage * undeclared_input"),
	)
	got := resolveSamples(t, profile, metrics, map[string]string{"voltage": "2", "current": "3", "power_x2": "1"})
	assert.Equal(t, []string{"current", "power_kw*", "voltage"}, names(got.Entries[0].Entries))
	assert.EqualValues(t, 1, metrics.skips(calcfield.SkipEvalError))
	assert.EqualValues(t, 1, testutil.ToFloat64(metrics.deviceKeyDropped))
}

// A version whose measured metrics are more than a formula environment declares cannot
// compute any formula: every calculated metric skips as eval_error rather than reading a
// subset of inputs. (The publish gate refuses such a profile, so this is the backstop.)
func TestTooManyMeasuredMetricsSkipEveryFormula(t *testing.T) {
	defs := make([]*model.MetricDefinition, 0, calcfield.MaxInputs+2)
	for i := 0; i <= calcfield.MaxInputs; i++ {
		defs = append(defs, measured(uint(i+1), fmt.Sprintf("m%03d", i), model.MetricDouble, ""))
	}
	defs = append(defs, calculatedDef(9999, "sum", model.MetricDouble, "", "m000 + m001"))
	profile := model.NewProfileResolution(model.ProfileScope{ProfileVersionToken: "wide@1"}, defs)
	metrics := testCalcMetrics()
	got := resolveSamples(t, profile, metrics, map[string]string{"m000": "1", "m001": "2"})
	assert.Equal(t, []string{"m000", "m001"}, names(got.Entries[0].Entries))
	assert.EqualValues(t, 1, metrics.skips(calcfield.SkipEvalError))
}

// A skip is logged once per (tenant, profile version, metric, reason), not once a reading:
// the rate limiter claims each combination once.
func TestASkipIsReportedOncePerVersionMetricAndReason(t *testing.T) {
	metrics := testCalcMetrics()
	obs := &calcObserver{metrics: metrics, ctx: core.WithTenant(context.Background(), "acme"), version: "meter@1"}
	assert.True(t, obs.claim("power_kw", "missing_input"))
	assert.False(t, obs.claim("power_kw", "missing_input"))
	assert.True(t, obs.claim("power_kw", "range"), "another reason is reported")
	assert.True(t, obs.claim("temp_f", "missing_input"), "another metric is reported")
	obs.version = "meter@2"
	assert.True(t, obs.claim("power_kw", "missing_input"), "another version is reported")
	obs.ctx = core.WithTenant(context.Background(), "other")
	assert.True(t, obs.claim("power_kw", "missing_input"), "another tenant is reported")
}

// With no metrics wired (a resolver built in a test, or before initialization) every outcome
// is silently uncounted rather than a panic.
func TestCalculatedMetricsWithNoMetricsWired(t *testing.T) {
	profile := meterResolution(calculatedDef(5, "power_kw", model.MetricDouble, "", "voltage * current"))
	got := resolveSamples(t, profile, nil, map[string]string{"voltage": "2", "power_kw": "1"})
	assert.Equal(t, []string{"voltage"}, names(got.Entries[0].Entries))
}
