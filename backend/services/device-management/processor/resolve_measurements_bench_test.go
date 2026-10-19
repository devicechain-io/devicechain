// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/eventtype"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
)

// The calculated-metrics watcher: the in-process cost of ResolveMeasurementsEventPayload for
// one meter reading, on a profile with 0, 1 and 4 calculated metrics. It is committed BEFORE
// the feature so the feature is measured against it: Calc0 must stay within noise of its
// baseline through every slice — a profile with no formulas pays one length check and
// nothing else — while Calc1/Calc4 measure what evaluating formulas adds.
//
// The reading is the same in all three: four declared, measured metrics (three DOUBLE, one
// INT), one sample entry. Only the profile changes. Calc1/Calc4 add, in this order, the
// formulas core/calcfield's own benchmark measures in isolation (calcfield/bench_test.go,
// benchFormulas), so the two read against each other:
//
//	power_kw  = voltage * current / 1000                               (DOUBLE)
//	temp_f    = temp * 1.8 + 32                                        (DOUBLE)
//	apparent  = math.sqrt(voltage * voltage + current * current)       (DOUBLE)
//	over_temp = temp > 80 && (has(m.fan) ? fan == 0 : true)            (BOOLEAN)

// calcBenchMeasured are the reading's declared, measured metrics.
var calcBenchMeasured = []*model.MetricDefinition{
	{Model: gorm.Model{ID: 1}, MetricKey: "voltage", DataType: string(model.MetricDouble), Unit: sql.NullString{String: "V", Valid: true}},
	{Model: gorm.Model{ID: 2}, MetricKey: "current", DataType: string(model.MetricDouble), Unit: sql.NullString{String: "A", Valid: true}},
	{Model: gorm.Model{ID: 3}, MetricKey: "temp", DataType: string(model.MetricDouble), Unit: sql.NullString{String: "Cel", Valid: true}},
	{Model: gorm.Model{ID: 4}, MetricKey: "fan", DataType: string(model.MetricInt)},
}

// calcBenchFormulas are the calculated metrics Calc1 and Calc4 add, in order.
var calcBenchFormulas = []*model.MetricDefinition{
	calcBenchMetric(5, "power_kw", model.MetricDouble, "kW", "voltage * current / 1000"),
	calcBenchMetric(6, "temp_f", model.MetricDouble, "[degF]", "temp * 1.8 + 32"),
	calcBenchMetric(7, "apparent", model.MetricDouble, "", "math.sqrt(voltage * voltage + current * current)"),
	calcBenchMetric(8, "over_temp", model.MetricBoolean, "", "temp > 80.0 && (has(m.fan) ? fan == 0 : true)"),
}

func calcBenchMetric(id uint, key string, t model.MetricDataType, unit, expr string) *model.MetricDefinition {
	return &model.MetricDefinition{Model: gorm.Model{ID: id}, MetricKey: key, DataType: string(t),
		Unit: sql.NullString{String: unit, Valid: unit != ""}, Expression: sql.NullString{String: expr, Valid: true}}
}

// calcBenchProfile is the meter profile with the first n calculated metrics.
func calcBenchProfile(n int) model.ProfileResolution {
	defs := append(append([]*model.MetricDefinition{}, calcBenchMeasured...), calcBenchFormulas[:n]...)
	return model.NewProfileResolution(model.ProfileScope{ProfileVersionToken: "meter@1"}, defs)
}

// calcBenchEvent is one meter reading carrying the four measured values.
func calcBenchEvent() *esmodel.UnresolvedEvent {
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	return &esmodel.UnresolvedEvent{
		Source: "bench", Device: "METER-1", EventType: eventtype.Measurement,
		OccurredTime: at, ProcessedTime: at,
		Payload: &esmodel.UnresolvedMeasurementsPayload{Entries: []esmodel.UnresolvedMeasurementsEntry{{
			Measurements: map[string]string{"voltage": "230.5", "current": "12.25", "temp": "81.25", "fan": "0"},
		}}},
	}
}

// benchResolveMeasurements resolves the reading against profile, b.N times.
func benchResolveMeasurements(b *testing.B, profile model.ProfileResolution) {
	want := len(calcBenchMeasured) + profile.CalculatedCount()
	rez := NewEventResolver(1, nil, "", EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	device := &model.Device{Model: gorm.Model{ID: 1}, TokenReference: rdb.TokenReference{Token: "METER-1"}}
	event := calcBenchEvent()
	ctx := context.Background()
	// One resolution outside the timer, checked, so a benchmark that silently resolved
	// nothing (a dropped entry, an error) cannot report a flattering number.
	out, err := rez.ResolveMeasurementsEventPayload(ctx, device, profile, event)
	if err != nil {
		b.Fatal(err)
	}
	if got := len(out.(*resolvedevent.ResolvedMeasurementsPayload).Entries[0].Entries); got != want {
		b.Fatalf("resolved %d entries, want %d (a calculated metric that skipped measures nothing)", got, want)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := rez.ResolveMeasurementsEventPayload(ctx, device, profile, event); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkResolveMeasurements_Calc0 is today's resolver on a profile with no calculated
// metrics: the baseline every later slice is held to.
func BenchmarkResolveMeasurements_Calc0(b *testing.B) {
	benchResolveMeasurements(b, model.NewProfileResolution(model.ProfileScope{}, calcBenchMeasured))
}

// BenchmarkResolveMeasurements_Calc1 is the same reading with one calculated metric
// (power_kw).
func BenchmarkResolveMeasurements_Calc1(b *testing.B) {
	benchResolveMeasurements(b, calcBenchProfile(1))
}

// BenchmarkResolveMeasurements_Calc4 is the same reading with all four calculated metrics.
func BenchmarkResolveMeasurements_Calc4(b *testing.B) {
	benchResolveMeasurements(b, calcBenchProfile(4))
}

// calcChainFormulas are CalcChain4's calculated metrics: a chain of depth four (the most a
// profile may publish), each link a single multiplication on the one before it, so the
// difference to Calc4 is what evaluation order, writing a value back to the sample and
// reading it again cost, not a heavier formula.
var calcChainFormulas = []*model.MetricDefinition{
	calcBenchMetric(5, "power_kw", model.MetricDouble, "kW", "voltage * current / 1000"),
	calcBenchMetric(6, "power_x2", model.MetricDouble, "kW", "power_kw * 2.0"),
	calcBenchMetric(7, "power_x4", model.MetricDouble, "kW", "power_x2 * 2.0"),
	calcBenchMetric(8, "power_x8", model.MetricDouble, "kW", "power_x4 * 2.0"),
}

// BenchmarkResolveMeasurements_CalcChain4 is the same reading with a depth-4 chain of
// calculated metrics (calcChainFormulas): four formulas like Calc4, each after the one it reads.
func BenchmarkResolveMeasurements_CalcChain4(b *testing.B) {
	defs := append(append([]*model.MetricDefinition{}, calcBenchMeasured...), calcChainFormulas...)
	benchResolveMeasurements(b, model.NewProfileResolution(model.ProfileScope{ProfileVersionToken: "meter@1"}, defs))
}

// calcConstMetric is the formula CalcConst adds: power_kw calibrated per device.
var calcConstMetric = calcBenchMetric(5, "power_cal", model.MetricDouble, "kW",
	"voltage * current / 1000 * device.calibration")

// BenchmarkResolveMeasurements_CalcConst is Calc1 with the formula reading a per-device
// constant: the device record carries realistic metadata (a numeric calibration among other
// keys), parsed once per event. The difference to Calc1 is the whole cost of a constant.
func BenchmarkResolveMeasurements_CalcConst(b *testing.B) {
	defs := append(append([]*model.MetricDefinition{}, calcBenchMeasured...), calcConstMetric)
	profile := model.NewProfileResolution(model.ProfileScope{ProfileVersionToken: "meter@1"}, defs)
	meta := datatypes.JSON(`{"site":"north","tags":["a","b"],"calibration":1.02,"installedBy":"crew-7"}`)
	rez := NewEventResolver(1, nil, "", EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	device := &model.Device{Model: gorm.Model{ID: 1}, TokenReference: rdb.TokenReference{Token: "METER-1"},
		MetadataEntity: rdb.MetadataEntity{Metadata: &meta}}
	event := calcBenchEvent()
	ctx := context.Background()
	out, err := rez.ResolveMeasurementsEventPayload(ctx, device, profile, event)
	if err != nil {
		b.Fatal(err)
	}
	if got := len(out.(*resolvedevent.ResolvedMeasurementsPayload).Entries[0].Entries); got != len(calcBenchMeasured)+1 {
		b.Fatalf("resolved %d entries, want %d (a constant that was not read skips the formula)", got, len(calcBenchMeasured)+1)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := rez.ResolveMeasurementsEventPayload(ctx, device, profile, event); err != nil {
			b.Fatal(err)
		}
	}
}
