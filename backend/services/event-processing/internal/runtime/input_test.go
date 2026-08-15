// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"math"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
)

// BuildInput extracts the device, anchors (lowest token per type), occurred time, and numeric
// measurements; non-numeric and non-finite readings are skipped (ADR-016 numeric-only).
func TestBuildInputMeasurements(t *testing.T) {
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	ev := &dmmodel.ResolvedEvent{
		SourceDeviceToken: "d1",
		OccurredTime:      base,
		Anchors: []dmmodel.ResolvedAnchor{
			// Deliberately ANTI-SORTED: two anchors of one type, the higher token first. The
			// collapse takes the lowest token per type, so this must yield zone-a. Listing
			// zone-a first would make first-wins and lowest-wins agree, and the assertion
			// below would hold no matter which rule the code implemented.
			{AnchorType: "area", AnchorToken: "zone-b"},
			{AnchorType: "area", AnchorToken: "zone-a"},
			{AnchorType: "gateway", AnchorToken: "gw-1"},
		},
		Payload: &dmmodel.ResolvedMeasurementsPayload{Entries: []dmmodel.ResolvedMeasurementsEntry{{
			OccurredTime: base,
			Entries: []dmmodel.ResolvedMeasurementEntry{
				{Name: "temperature", Value: "90.5"},
				{Name: "label", Value: "hot"}, // non-numeric: skipped
				{Name: "bad", Value: "NaN"},   // non-finite: skipped
				{Name: "inf", Value: "+Inf"},  // non-finite: skipped
				{Name: "battery", Value: "50"},
			},
		}}},
	}
	inputs := BuildInputs(ev, base)
	if len(inputs) != 1 {
		t.Fatalf("single-sample event should yield one input; got %d", len(inputs))
	}
	in := inputs[0]
	if in.Device != "d1" {
		t.Fatalf("device: got %q", in.Device)
	}
	if !in.Occurred.Equal(base) {
		t.Fatalf("occurred: got %v", in.Occurred)
	}
	if in.Anchors["area"] != "zone-a" || in.Anchors["gateway"] != "gw-1" {
		t.Fatalf("anchors: got %+v", in.Anchors)
	}
	if in.M["temperature"] != 90.5 || in.M["battery"] != 50 {
		t.Fatalf("numeric metrics: got %+v", in.M)
	}
	if _, ok := in.M["label"]; ok {
		t.Fatalf("non-numeric metric must be skipped")
	}
	for _, k := range []string{"bad", "inf"} {
		if v, ok := in.M[k]; ok {
			t.Fatalf("non-finite metric %q must be skipped; got %v", k, v)
		}
	}
	for _, v := range in.M {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Fatalf("no non-finite value may survive; got %v", v)
		}
	}
}

// A non-measurement resolved event carries no metrics — one heartbeat input with nil M
// (still a heartbeat for an absence rule, but matches no metric-gated rule).
func TestBuildInputsNonMeasurement(t *testing.T) {
	ev := &dmmodel.ResolvedEvent{SourceDeviceToken: "d1", Payload: nil}
	inputs := BuildInputs(ev, time.Time{})
	if len(inputs) != 1 || inputs[0].M != nil {
		t.Fatalf("non-measurement event should have one nil-M input; got %+v", inputs)
	}
}

// A batched measurement event yields one input PER sample entry — every reading is preserved
// (not collapsed last-value-wins), so a rule evaluates against each sample.
func TestBuildInputsBatchedSamples(t *testing.T) {
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	// A store-and-forward upload: the two readings were taken a minute apart and the
	// message carries the later one's time. The instants are DISTINCT and neither equals
	// the envelope's alone, so an implementation that flattened the batch onto the message
	// time — which is what this did before — fails rather than passing by coincidence.
	first := base.Add(-time.Minute)
	ev := &dmmodel.ResolvedEvent{
		SourceDeviceToken: "d1",
		OccurredTime:      base,
		Payload: &dmmodel.ResolvedMeasurementsPayload{Entries: []dmmodel.ResolvedMeasurementsEntry{
			{OccurredTime: first, Entries: []dmmodel.ResolvedMeasurementEntry{{Name: "temp", Value: "120"}}},
			{OccurredTime: base, Entries: []dmmodel.ResolvedMeasurementEntry{{Name: "temp", Value: "80"}}},
		}},
	}
	inputs := BuildInputs(ev, base)
	if len(inputs) != 2 {
		t.Fatalf("two-sample batch should yield two inputs; got %d", len(inputs))
	}
	if inputs[0].M["temp"] != 120 || inputs[1].M["temp"] != 80 {
		t.Fatalf("both readings must be preserved; got %v and %v", inputs[0].M, inputs[1].M)
	}
	if !inputs[0].Occurred.Equal(first) {
		t.Fatalf("the buffered sample must keep its own instant; got %v want %v", inputs[0].Occurred, first)
	}
	if !inputs[1].Occurred.Equal(base) {
		t.Fatalf("the live sample must keep its own instant; got %v want %v", inputs[1].Occurred, base)
	}
}

// A batched LOCATION event stamps each fix at the instant it was taken.
//
// 🔴 THIS IS NOT A COPY OF THE MEASUREMENT TEST ABOVE — it is the case that had no test at
// all. Every location fixture in this module (fanout_geofence_test.go, the preview suite)
// gives every fix the message's own time, so removing the per-fix stamp left the entire
// module green. And a comment in fanout_geofence_test.go claimed this file already covered
// it, which was how the gap stayed invisible.
//
// It is also the case with the most behaviour riding on it. A tracker that buffers a run
// of fixes and uploads them as one message is the normal shape, and a geofence rule reads
// each fix's time: on the message's time the whole run is simultaneous, so a device that
// entered a fence and left it during the run can neither open nor close a duration hold.
func TestBuildInputsBatchedLocationFixes(t *testing.T) {
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	first := base.Add(-2 * time.Minute)
	lat, lon := "33.749", "-84.388"
	ev := &dmmodel.ResolvedEvent{
		SourceDeviceToken: "d1",
		OccurredTime:      base,
		EventType:         esmodel.Location,
		Payload: &dmmodel.ResolvedLocationsPayload{Entries: []dmmodel.ResolvedLocationEntry{
			{Latitude: &lat, Longitude: &lon, OccurredTime: first},
			{Latitude: &lat, Longitude: &lon, OccurredTime: base},
		}},
	}
	inputs := BuildInputs(ev, base)
	if len(inputs) != 2 {
		t.Fatalf("a two-fix batch should yield two inputs; got %d", len(inputs))
	}
	if !inputs[0].Occurred.Equal(first) {
		t.Fatalf("the buffered fix must keep its own instant; got %v want %v", inputs[0].Occurred, first)
	}
	if !inputs[1].Occurred.Equal(base) {
		t.Fatalf("the live fix must keep its own instant; got %v want %v", inputs[1].Occurred, base)
	}
	// Both still carry a position and no measurements — the per-fix stamp must not have
	// disturbed what the fan-out was already doing.
	for i, in := range inputs {
		if in.Position == nil {
			t.Fatalf("fix %d lost its position", i)
		}
		if in.M != nil {
			t.Fatalf("fix %d gained measurements: %v", i, in.M)
		}
	}
}
