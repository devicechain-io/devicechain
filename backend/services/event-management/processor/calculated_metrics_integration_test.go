// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Calculated metrics (ADR-082) against a REAL TimescaleDB: the history rows, the
// measurement_rollups continuous aggregate and the decimal(20,8) column, none of which sqlite
// can answer for. Each reading is resolved by device-management's real resolver and crosses the
// real wire (calculated_metrics_persist_test.go's resolveOverTheWire).
package processor

import (
	"context"
	"fmt"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A calculated metric is stored as history and rolled up like any measurement; a device-level
// duplicate straddling a formula change stores it once; a value the column cannot hold is
// skipped at resolve rather than failing its reading; and the stored row is the value
// rounded to the column's eight decimals while the event carries it in full.
func TestCalculatedMetricsOnPostgres(t *testing.T) {
	mgr, _ := newFenceBenchManager(t, "calc3it", true)
	api := model.NewApi(mgr)
	ep := &EventPersistenceWorker{WorkerId: 1, Api: api}
	tenant := fmt.Sprintf("calc%d", time.Now().UnixNano())
	ctx := core.WithTenant(context.Background(), tenant)
	v1 := calcProfile("meter@1", "voltage * current / 1000")
	v2 := calcProfile("meter@2", "voltage * current")

	// Four readings a minute apart in one hour; the second is delivered again, as a device
	// resend, after a publish changed the formula.
	hour := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	at := func(i int) time.Time { return hour.Add(time.Duration(i) * time.Minute) }
	type reading struct {
		at       time.Time
		profile  dmmodel.ProfileResolution
		readings map[string]string
	}
	deliveries := []reading{
		{at(0), v1, map[string]string{"voltage": "230.5", "current": "12.25"}},      // 2.823625
		{at(1), v1, map[string]string{"voltage": "230", "current": "15.123456789"}}, // 3.47839506147
		{at(1), v2, map[string]string{"voltage": "230", "current": "15.123456789"}}, // the resend, new formula
		{at(2), v1, map[string]string{"voltage": "1e9", "current": "1e4"}},          // 1e10 kW: storable
		{at(3), v2, map[string]string{"voltage": "1e9", "current": "1e4"}},          // 1e13: unstorable, skipped
	}
	var full string
	for i, d := range deliveries {
		ev := resolveAt(t, d.profile, d.readings, d.at)
		if i == 1 {
			full = derivedValue(t, ev, "power_kw")
		}
		_, err := ep.PersistEvent(ctx, ev)
		require.NoErrorf(t, err, "delivery %d", i)
	}
	assert.Equal(t, "3.47839506147", full, "the event carries the shortest round-trip value in full")

	device := "meter-1"
	got, err := api.MeasurementEvents(ctx, model.EventSearchCriteria{DeviceToken: &device,
		Pagination: rdb.Pagination{PageSize: 100}})
	require.NoError(t, err)
	power := map[time.Time]float64{}
	raw := 0
	for _, row := range got.Results {
		if row.Name == "power_kw" {
			_, dup := power[row.OccurredTime.UTC()]
			require.False(t, dup, "two calculated rows for the sample at %v", row.OccurredTime)
			power[row.OccurredTime.UTC()] = row.Value.Float64
			require.NotNil(t, row.Classifier)
			assert.EqualValues(t, 5, *row.Classifier)
		} else {
			raw++
		}
	}
	assert.Equal(t, 8, raw, "every raw value of the four readings is stored once, the unstorable one's included")
	require.Len(t, power, 3, "three calculated values: the resend added none and the unstorable one was skipped")
	assert.Equal(t, 2.823625, power[at(0)])
	assert.Equal(t, 3.47839506, power[at(1)], "the first copy's value, rounded to the column's 8 decimals")
	assert.Equal(t, 1e10, power[at(2)])

	// The hourly rollup groups it by name like any measurement.
	require.NoError(t, mgr.DB(core.WithSystemContext(context.Background())).
		Exec(`CALL refresh_continuous_aggregate('"event-management".measurement_rollups', NULL, NULL)`).Error)
	name := "power_kw"
	start, end := at(0).Truncate(time.Hour), at(0).Truncate(time.Hour).Add(time.Hour)
	buckets, err := api.BucketedMeasurements(ctx, model.MeasurementAggregationCriteria{DeviceToken: &device,
		Name: &name, StartTime: &start, EndTime: &end, IntervalSeconds: 3600,
		EventTypes: []eventtype.EventType{eventtype.Measurement}})
	require.NoError(t, err)
	require.Len(t, buckets, 1)
	assert.EqualValues(t, 3, buckets[0].Count)
	assert.InDelta(t, 2.823625+3.47839506+1e10, buckets[0].Sum.Float64, 1e-6)
	assert.Equal(t, 1e10, buckets[0].Max.Float64)

	// The negative control for the skip: the same 1e13 written as a calculated value the
	// evaluator did NOT skip is refused by the column, and takes its reading's raw values
	// with it. That is what the unstorable skip prevents.
	forged := resolveAt(t, v1, map[string]string{"voltage": "1", "current": "2"}, at(4))
	entries := forged.Payload.(*resolvedevent.ResolvedMeasurementsPayload).Entries[0].Entries
	for i := range entries {
		if entries[i].Derived {
			entries[i].Value = "1e+13"
		}
	}
	_, err = ep.PersistEvent(ctx, forged)
	require.Error(t, err, "decimal(20,8) accepted 1e13: the unstorable skip would be protecting nothing")
}

// resolveAt is resolveOverTheWire for a reading at a chosen instant.
func resolveAt(t *testing.T, profile dmmodel.ProfileResolution, readings map[string]string, at time.Time) resolvedevent.ResolvedEvent {
	t.Helper()
	ev := resolveOverTheWire(t, profile, readings, eventtime.WireCompact)
	ev.OccurredTime, ev.ProcessedTime = at, at
	for i := range ev.Payload.(*resolvedevent.ResolvedMeasurementsPayload).Entries {
		ev.Payload.(*resolvedevent.ResolvedMeasurementsPayload).Entries[i].OccurredTime = at
	}
	return ev
}
