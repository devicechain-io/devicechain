// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmprocessor "github.com/devicechain-io/dc-device-management/processor"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// A calculated metric (ADR-082) reaches the device's latest state on the path every
// measurement takes: the reading is resolved by device-management's real resolver under a
// profile version with a formula, published on the real wire, and merged — no change here.
func TestACalculatedMetricIsTheDevicesLatestValue(t *testing.T) {
	profile := dmmodel.NewProfileResolution(dmmodel.ProfileScope{ProfileVersionToken: "meter@1"},
		[]*dmmodel.MetricDefinition{
			{Model: gorm.Model{ID: 1}, MetricKey: "voltage", DataType: "DOUBLE"},
			{Model: gorm.Model{ID: 2}, MetricKey: "current", DataType: "DOUBLE"},
			{Model: gorm.Model{ID: 5}, MetricKey: "power_kw", DataType: "DOUBLE", Unit: sql.NullString{String: "kW", Valid: true},
				Expression: sql.NullString{String: "voltage * current / 1000", Valid: true}},
		})
	rez := dmprocessor.NewEventResolver(1, nil, "", dmprocessor.EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	device := &dmmodel.Device{Model: gorm.Model{ID: 1}, TokenReference: rdb.TokenReference{Token: "meter-ds"}}
	sp := newLocationProcessor(t)
	ctx := core.WithTenant(context.Background(), "tenant1")
	base := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	publish := func(at time.Time, readings map[string]string, format eventtime.WireFormat) {
		t.Helper()
		unresolved := &esmodel.UnresolvedEvent{Source: mqttTestSource, Device: "meter-ds", EventType: eventtype.Measurement,
			OccurredTime: at, ProcessedTime: at,
			Payload: &esmodel.UnresolvedMeasurementsPayload{Entries: []esmodel.UnresolvedMeasurementsEntry{{Measurements: readings}}}}
		payload, err := rez.ResolveMeasurementsEventPayload(ctx, device, profile, unresolved)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		encoded, err := resolvedevent.MarshalResolvedEventAs(&resolvedevent.ResolvedEvent{Source: mqttTestSource,
			SourceDeviceToken: "meter-ds", EventType: eventtype.Measurement, OccurredTime: at, ProcessedTime: at,
			ProfileVersionToken: "meter@1", Payload: payload}, format)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		sp.mergeOne(context.Background(), messaging.Message{Subject: locationTestSubject, Value: encoded})
	}

	publish(base, map[string]string{"voltage": "230.5", "current": "12.25"}, eventtime.WireLegacy)
	got := loadProjectedMeasurement(t, sp, ctx, "meter-ds", "power_kw")
	if !got.Value.Valid || got.Value.Float64 != 2.823625 || !got.OccurredTime.Equal(base) {
		t.Fatalf("latest power_kw = %+v at %v, want 2.823625 at %v", got.Value, got.OccurredTime, base)
	}
	if got.Unit == nil || *got.Unit != "kW" {
		t.Fatalf("latest power_kw unit = %v, want kW", got.Unit)
	}

	// A newer reading moves it; a reading with no current computes nothing, so the latest
	// calculated value stays the last one that was computed rather than turning into a
	// plausible zero.
	publish(base.Add(time.Minute), map[string]string{"voltage": "200", "current": "10"}, eventtime.WireCompact)
	publish(base.Add(2*time.Minute), map[string]string{"voltage": "210"}, eventtime.WireCompact)
	got = loadProjectedMeasurement(t, sp, ctx, "meter-ds", "power_kw")
	if !got.Value.Valid || got.Value.Float64 != 2 || !got.OccurredTime.Equal(base.Add(time.Minute)) {
		t.Fatalf("latest power_kw = %+v at %v, want 2 at %v", got.Value, got.OccurredTime, base.Add(time.Minute))
	}
	if v := loadProjectedMeasurement(t, sp, ctx, "meter-ds", "voltage"); v.Value.Float64 != 210 {
		t.Fatalf("latest voltage = %+v, want 210: the reading itself was merged", v.Value)
	}
}
