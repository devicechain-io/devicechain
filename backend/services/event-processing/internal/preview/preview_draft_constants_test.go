// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package preview

import (
	"context"
	"database/sql"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/messaging"
	"gorm.io/gorm"
)

// Per-device constants in the preview (ADR-082, S6b). A stored event carries a calculated value
// but not the constants it was computed with, and event-processing holds no device records, so
// the caller hands the replay each device's CURRENT constants and the draft recalculates with
// the constants of the device that sent each reading.

func deviceMsg(t *testing.T, seq uint64, device, temp string, occurred time.Time) messaging.Message {
	t.Helper()
	ev := &dmmodel.ResolvedEvent{
		Source: "mqtt", SourceDeviceToken: device, ProfileVersionToken: "p@1",
		OccurredTime: occurred, ProcessedTime: occurred, EventType: esmodel.Measurement,
		Payload: &dmmodel.ResolvedMeasurementsPayload{Entries: []dmmodel.ResolvedMeasurementsEntry{{
			OccurredTime: occurred, Entries: []dmmodel.ResolvedMeasurementEntry{{Name: "temp", Value: temp}},
		}}},
	}
	b, err := dmproto.MarshalResolvedEvent(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	m := messaging.NewConsumedMessage("dc.acme.resolved-events", b, 0, nil, nil)
	m.StreamSeq = seq
	return m
}

// calibratedDraft is a draft whose heat is the reading times a per-device calibration.
func calibratedDraft(t *testing.T) *dmmodel.ProfileResolution {
	t.Helper()
	defs := []*dmmodel.MetricDefinition{
		{Model: gorm.Model{ID: 1}, MetricKey: "temp", DataType: "DOUBLE"},
		{Model: gorm.Model{ID: 2}, MetricKey: "heat", DataType: "DOUBLE",
			Expression: sql.NullString{String: "temp * device.calibration", Valid: true}},
	}
	if problems := dmmodel.CheckDraftCalculatedMetrics(defs); len(problems) > 0 {
		t.Fatalf("the draft does not pass the gate: %v", problems)
	}
	r := dmmodel.NewProfileResolution(dmmodel.ProfileScope{}, defs)
	return &r
}

// Both devices read 60; calibration 2 puts d1 over heat > 100 and 1 leaves d2 under it. A device
// with no constants, and one whose constant is not a number, compute nothing and are counted.
func TestADraftConstantIsPreviewedPerDevice(t *testing.T) {
	msgs := []messaging.Message{
		deviceMsg(t, 1, "d1", "60", base),
		deviceMsg(t, 2, "d2", "60", base.Add(time.Second)),
		deviceMsg(t, 3, "d3", "60", base.Add(2*time.Second)),
		deviceMsg(t, 4, "d4", "60", base.Add(3*time.Second)),
	}
	constants := map[string][]calcfield.Constant{
		"d1": {calcfield.NewConstant("calibration", 2)},
		"d2": {calcfield.NewConstant("calibration", 1)},
		// d3 is not listed: it holds none. d4 holds the name, but not as a number.
		"d4": {calcfield.InvalidConstant("calibration")},
	}
	res, err := Run(context.Background(), &fakeOpener{msgs: msgs}, "resolved-events", heatReg(t, "acme"),
		"acme", "p@1", window(), 0, 0, 0, nil, calibratedDraft(t), WithDeviceConstants(constants))
	if err != nil {
		t.Fatal(err)
	}
	if raises(res) != 1 || res.Firings[0].Series == "" || res.Firings[0].Value != 120 {
		t.Fatalf("only d1 (60 x 2 = 120) must raise: %+v", res.Firings)
	}
	if res.Stats.RecalculatedSamples != 4 || res.Stats.CalculatedSkips != 2 {
		t.Fatalf("four samples recalculated, d3 (absent) and d4 (not a number) skipped: %+v", res.Stats)
	}
}

// Without constants handed in, a formula that reads one computes nothing for anybody.
func TestADraftConstantWithoutConstantsSkipsEverywhere(t *testing.T) {
	msgs := []messaging.Message{deviceMsg(t, 1, "d1", "60", base), deviceMsg(t, 2, "d2", "60", base.Add(time.Second))}
	res, err := Run(context.Background(), &fakeOpener{msgs: msgs}, "resolved-events", heatReg(t, "acme"),
		"acme", "p@1", window(), 0, 0, 0, nil, calibratedDraft(t))
	if err != nil {
		t.Fatal(err)
	}
	if raises(res) != 0 || res.Stats.CalculatedSkips != 2 {
		t.Fatalf("a formula whose constant is unavailable must skip, never default: %+v", res)
	}
}

// The constants are read, never written: the replay leaves the caller's map as it found it.
func TestThePreviewDoesNotWriteTheConstants(t *testing.T) {
	cs := []calcfield.Constant{calcfield.NewConstant("calibration", 2)}
	constants := map[string][]calcfield.Constant{"d1": cs}
	if _, err := Run(context.Background(), &fakeOpener{msgs: []messaging.Message{deviceMsg(t, 1, "d1", "60", base)}},
		"resolved-events", heatReg(t, "acme"), "acme", "p@1", window(), 0, 0, 0, nil, calibratedDraft(t),
		WithDeviceConstants(constants)); err != nil {
		t.Fatal(err)
	}
	if len(constants) != 1 || len(constants["d1"]) != 1 || constants["d1"][0].Key() != "calibration" {
		t.Fatalf("the constants changed: %+v", constants)
	}
}
