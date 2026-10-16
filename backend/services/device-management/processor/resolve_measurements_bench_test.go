// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
)

// The calculated-metrics watcher: the in-process cost of ResolveMeasurementsEventPayload for
// one meter reading, on a profile with 0, 1 and 4 calculated metrics. It is committed BEFORE
// the feature so the feature is measured against it: Calc0 must stay within noise of its
// baseline through every slice — a profile with no formulas pays one length check and
// nothing else — and Calc1/Calc4 become runnable when the resolver evaluates formulas.
//
// The reading is the same in all three: four declared, measured metrics (three DOUBLE, one
// INT), one sample entry. Only the profile changes. When Calc1/Calc4 are wired they add, in
// this order, the formulas core/calcfield's own benchmark measures in isolation
// (calcfield/bench_test.go, benchFormulas), so the two read against each other:
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

// calcBenchEvent is one meter reading carrying the four measured values.
func calcBenchEvent() *esmodel.UnresolvedEvent {
	at := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	return &esmodel.UnresolvedEvent{
		Source: "bench", Device: "METER-1", EventType: esmodel.Measurement,
		OccurredTime: at, ProcessedTime: at,
		Payload: &esmodel.UnresolvedMeasurementsPayload{Entries: []esmodel.UnresolvedMeasurementsEntry{{
			Measurements: map[string]string{"voltage": "230.5", "current": "12.25", "temp": "81.25", "fan": "0"},
		}}},
	}
}

// benchResolveMeasurements resolves the reading against profile, b.N times.
func benchResolveMeasurements(b *testing.B, profile model.ProfileResolution) {
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
	if got := len(out.(*model.ResolvedMeasurementsPayload).Entries[0].Entries); got != len(calcBenchMeasured) {
		b.Fatalf("resolved %d entries, want %d", got, len(calcBenchMeasured))
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
// (power_kw). A metric definition cannot carry a formula until device-management authoring
// lands, and the resolver does not evaluate one until it is wired, so there is nothing yet to
// measure; a skip says so rather than reporting Calc0's number under this name.
func BenchmarkResolveMeasurements_Calc1(b *testing.B) {
	b.Skip("calculated fields not wired until S3")
}

// BenchmarkResolveMeasurements_Calc4 is the same reading with all four calculated metrics.
// See Calc1 for why it skips today.
func BenchmarkResolveMeasurements_Calc4(b *testing.B) {
	b.Skip("calculated fields not wired until S3")
}
