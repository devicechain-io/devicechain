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
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// A threshold rule over a CALCULATED metric (ADR-082) fires in live detection exactly as one
// over a measured metric does: the value is in the resolved event, computed once by
// device-management's real resolver, and DETECT reads it as m.power_kw with no change here.
// The rule is on a key no device sends.
func TestAThresholdOnACalculatedMetricFires(t *testing.T) {
	profile := dmmodel.NewProfileResolution(dmmodel.ProfileScope{ProfileVersionToken: "p@1"},
		[]*dmmodel.MetricDefinition{
			{Model: gorm.Model{ID: 1}, MetricKey: "voltage", DataType: "DOUBLE"},
			{Model: gorm.Model{ID: 2}, MetricKey: "current", DataType: "DOUBLE"},
			{Model: gorm.Model{ID: 5}, MetricKey: "power_kw", DataType: "DOUBLE",
				Expression: sql.NullString{String: "voltage * current / 1000", Valid: true}},
		})
	rez := dmprocessor.NewEventResolver(1, nil, "", dmprocessor.EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	device := &dmmodel.Device{Model: gorm.Model{ID: 1}, TokenReference: rdb.TokenReference{Token: "meter-1"}}
	resolved := func(seq uint64, at time.Time, readings map[string]string, format eventtime.WireFormat) messaging.Message {
		t.Helper()
		unresolved := &esmodel.UnresolvedEvent{Source: "http1", Device: "meter-1", EventType: eventtype.Measurement,
			OccurredTime: at, ProcessedTime: at,
			Payload: &esmodel.UnresolvedMeasurementsPayload{Entries: []esmodel.UnresolvedMeasurementsEntry{{Measurements: readings}}}}
		payload, err := rez.ResolveMeasurementsEventPayload(context.Background(), device, profile, unresolved)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		b, err := resolvedevent.MarshalResolvedEventAs(&resolvedevent.ResolvedEvent{Source: "http1", SourceDeviceToken: "meter-1",
			ProfileVersionToken: "p@1", OccurredTime: at, ProcessedTime: at, EventType: eventtype.Measurement,
			Payload: payload}, format)
		if err != nil {
			t.Fatalf("marshal resolved event: %v", err)
		}
		m := messaging.NewConsumedMessage("dc.acme.resolved-events", b, 0, nil, &fakeAck{})
		m.StreamSeq = seq
		m.AppendTime = at
		return m
	}

	thr := 3.0
	cr, err := rules0.Compile(rules0.Rule{ID: "acme/overload", Name: "overload", Type: rules0.TypeThreshold,
		When: rules0.Condition{Metric: "power_kw", Op: rules0.OpGt, Threshold: &thr}}, rules0.Limits{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	reg := runtime.NewRuleRegistry([]runtime.ScopedRule{{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: cr}})
	w := &derivedCapture{}
	rp := stampingProcessor(t, reg, w)

	// 2.823625 kW: below. 3.45 kW: above, a RAISE. A reading with no current computes no
	// power at all, so it neither raises nor resolves. 2 kW: below again, a RESOLVE.
	rp.handle(resolved(1, testBase.Add(1*time.Second), map[string]string{"voltage": "230.5", "current": "12.25"}, eventtime.WireLegacy))
	rp.handle(resolved(2, testBase.Add(2*time.Second), map[string]string{"voltage": "230", "current": "15"}, eventtime.WireCompact))
	rp.handle(resolved(3, testBase.Add(3*time.Second), map[string]string{"voltage": "230"}, eventtime.WireCompact))
	rp.handle(resolved(4, testBase.Add(4*time.Second), map[string]string{"voltage": "200", "current": "10"}, eventtime.WireLegacy))
	rp.checkpoint(context.Background())

	if len(w.events) != 2 {
		t.Fatalf("published %d derived events, want a raise and a resolve: %+v", len(w.events), w.events)
	}
	raise, resolve := w.events[0], w.events[1]
	if raise.RuleID != "acme/overload" || raise.Series != "meter-1" || raise.Edge != runtime.EdgeRaised ||
		!raise.OccurredTime.Equal(testBase.Add(2*time.Second)) || raise.Value == nil || *raise.Value != 3.45 {
		t.Fatalf("raise = %+v (value %v), want acme/overload raised for meter-1 at +2s with power 3.45", raise, raise.Value)
	}
	if resolve.Edge != runtime.EdgeResolved || !resolve.OccurredTime.Equal(testBase.Add(4*time.Second)) {
		t.Fatalf("resolve = %+v, want a resolve at +4s", resolve)
	}
}
