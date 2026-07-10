// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"math"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
)

// BuildInput extracts the device, anchors (first token per type), occurred time, and numeric
// measurements; non-numeric and non-finite readings are skipped (ADR-016 numeric-only).
func TestBuildInputMeasurements(t *testing.T) {
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	ev := &dmmodel.ResolvedEvent{
		SourceDeviceToken: "d1",
		OccurredTime:      base,
		Anchors: []dmmodel.ResolvedAnchor{
			{AnchorType: "area", AnchorToken: "zone-a"},
			{AnchorType: "area", AnchorToken: "zone-b"}, // first wins
			{AnchorType: "gateway", AnchorToken: "gw-1"},
		},
		Payload: &dmmodel.ResolvedMeasurementsPayload{Entries: []dmmodel.ResolvedMeasurementsEntry{{
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
	ev := &dmmodel.ResolvedEvent{
		SourceDeviceToken: "d1",
		OccurredTime:      base,
		Payload: &dmmodel.ResolvedMeasurementsPayload{Entries: []dmmodel.ResolvedMeasurementsEntry{
			{Entries: []dmmodel.ResolvedMeasurementEntry{{Name: "temp", Value: "120"}}},
			{Entries: []dmmodel.ResolvedMeasurementEntry{{Name: "temp", Value: "80"}}},
		}},
	}
	inputs := BuildInputs(ev, base)
	if len(inputs) != 2 {
		t.Fatalf("two-sample batch should yield two inputs; got %d", len(inputs))
	}
	if inputs[0].M["temp"] != 120 || inputs[1].M["temp"] != 80 {
		t.Fatalf("both readings must be preserved; got %v and %v", inputs[0].M, inputs[1].M)
	}
	for _, in := range inputs {
		if !in.Occurred.Equal(base) {
			t.Fatalf("every sample stamped at the message time; got %v", in.Occurred)
		}
	}
}

// EffectiveEventTime clamps a far-future device time against processed+skew, leaves a normal
// (past/near) time alone, and disables when processed is unset or skew is non-positive.
func TestEffectiveEventTimeClamp(t *testing.T) {
	proc := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	skew := 5 * time.Minute
	// Far-future device time is clamped to processed+skew.
	future := proc.Add(48 * time.Hour)
	if got := EffectiveEventTime(future, proc, skew); !got.Equal(proc.Add(skew)) {
		t.Fatalf("future time should clamp to processed+skew; got %v", got)
	}
	// A normal (slightly-ahead, within skew) time is untouched.
	near := proc.Add(time.Minute)
	if got := EffectiveEventTime(near, proc, skew); !got.Equal(near) {
		t.Fatalf("within-skew time must not clamp; got %v", got)
	}
	// A past time is untouched (handled by watermark lateness, not this clamp).
	past := proc.Add(-time.Hour)
	if got := EffectiveEventTime(past, proc, skew); !got.Equal(past) {
		t.Fatalf("past time must not clamp; got %v", got)
	}
	// Disabled when processed is unset or skew non-positive.
	if got := EffectiveEventTime(future, time.Time{}, skew); !got.Equal(future) {
		t.Fatalf("unset processed disables clamp; got %v", got)
	}
	if got := EffectiveEventTime(future, proc, 0); !got.Equal(future) {
		t.Fatalf("non-positive skew disables clamp; got %v", got)
	}
}
