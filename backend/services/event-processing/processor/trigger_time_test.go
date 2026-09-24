// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// pending wraps detections as buffered, unstamped pending detections, for the tests that seed
// the buffer directly.
func pending(dets []detectcore.Detection) []pendingDetection {
	out := make([]pendingDetection, 0, len(dets))
	for _, d := range dets {
		out = append(out, pendingDetection{Detection: d})
	}
	return out
}

// derivedCapture records every derived event published, decoded, with the dedup id it carried.
type derivedCapture struct {
	events []runtime.DerivedEvent
	ids    []string
}

func (w *derivedCapture) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	for _, m := range msgs {
		var de runtime.DerivedEvent
		if err := json.Unmarshal(m.Value, &de); err != nil {
			return err
		}
		w.events = append(w.events, de)
		w.ids = append(w.ids, m.DedupID)
	}
	return nil
}

func (w *derivedCapture) WriteToDevice(ctx context.Context, _ string, msgs ...messaging.Message) error {
	return w.WriteMessages(ctx, msgs...)
}
func (w *derivedCapture) HandleResponse(error) {}

// stampedMsg is a resolved measurement for device at seq whose device time, ingest processing
// time and broker append time are each chosen by the test.
func stampedMsg(t *testing.T, seq uint64, device, value string, occurred, processed, appended time.Time) messaging.Message {
	t.Helper()
	ev := &dmmodel.ResolvedEvent{
		Source:              "http1",
		SourceDeviceToken:   device,
		ProfileVersionToken: "p@1",
		OccurredTime:        occurred,
		ProcessedTime:       processed,
		EventType:           esmodel.Measurement,
		Payload: &dmmodel.ResolvedMeasurementsPayload{Entries: []dmmodel.ResolvedMeasurementsEntry{{
			OccurredTime: occurred,
			Entries:      []dmmodel.ResolvedMeasurementEntry{{Name: "temperature", Value: value}},
		}}},
	}
	b, err := dmproto.MarshalResolvedEvent(ev)
	if err != nil {
		t.Fatalf("marshal resolved event: %v", err)
	}
	m := messaging.NewConsumedMessage("dc.acme.resolved-events", b, 0, nil, &fakeAck{})
	m.StreamSeq = seq
	m.AppendTime = appended
	return m
}

// stampingProcessor is a restored processor over reg publishing to w.
func stampingProcessor(t *testing.T, reg *runtime.RuleRegistry, w *derivedCapture) *ResolvedEventsProcessor {
	t.Helper()
	rp := &ResolvedEventsProcessor{
		Store: newTestStore(t),
		cfg: Config{
			PartitionId:        "singleton",
			CheckpointEvents:   1000,
			CheckpointInterval: time.Hour,
			TickInterval:       time.Hour,
			Clock:              detectcore.RealClock{},
		},
		registry:  reg,
		publisher: runtime.NewPublisher(w, reg, (*DetectMetrics)(nil)),
		clock:     detectcore.RealClock{},
		procCtx:   context.Background(),
	}
	if err := rp.restore(context.Background()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	return rp
}

// A detection is stamped with the earlier of the two platform clocks that saw its input — the
// ingest processing time and the broker's append time — and never with the device's own time,
// however far ahead of both the device claims to be.
func TestDerivedEventCarriesThePlatformTriggerTime(t *testing.T) {
	ctx := context.Background()
	w := &derivedCapture{}
	rp := stampingProcessor(t, thresholdReg(t), w)

	early, late := testBase.Add(10*time.Second), testBase.Add(20*time.Second)
	aYearAhead := testBase.AddDate(1, 0, 0)
	// ProcessedTime before AppendTime: the processing time.
	rp.handle(stampedMsg(t, 1, "d1", "90", testBase.Add(time.Second), early, late))
	// ProcessedTime after AppendTime: the append time.
	rp.handle(stampedMsg(t, 2, "d2", "90", testBase.Add(2*time.Second), late.Add(time.Minute), late))
	// A device claiming a time a year ahead changes nothing: only the platform clocks count.
	rp.handle(stampedMsg(t, 3, "d3", "90", aYearAhead, early, late))
	rp.checkpoint(ctx)

	if len(w.events) != 3 {
		t.Fatalf("published %d derived events, want 3", len(w.events))
	}
	for i, want := range []time.Time{early, late, early} {
		if got := w.events[i].TriggeredAt; !got.Equal(want) {
			t.Errorf("event %d (%s) triggeredAt = %v, want %v", i, w.events[i].Series, got, want)
		}
	}
	if !w.events[2].OccurredTime.Equal(aYearAhead) {
		t.Fatalf("the fixture did not carry the device's future time (%v): nothing was tested", w.events[2].OccurredTime)
	}
	for i, de := range w.events {
		if w.ids[i] == "" || w.ids[i] != de.DedupID() {
			t.Errorf("event %d was published with dedup id %q, want its DedupID %q", i, w.ids[i], de.DedupID())
		}
	}
}

// An absence is fired by the watermark, which another series' event moves. That event is the
// input that caused the emission, so the absence carries ITS message's time — not the silent
// series' last report, and not the detection's own (device-time) At.
func TestAWatermarkFiredAbsenceCarriesTheMovingMessagesTime(t *testing.T) {
	ctx := context.Background()
	w := &derivedCapture{}
	rp := stampingProcessor(t, absenceReg(t), w)

	xAt := testBase.Add(5 * time.Second)
	yAt := testBase.Add(90 * time.Second)
	// X reports once (arming a 30s dead-man), then goes silent.
	rp.handle(stampedMsg(t, 1, "x", "20", testBase.Add(time.Second), xAt, xAt.Add(time.Millisecond)))
	// Y's report, a minute later in device time, moves the watermark past X's deadline.
	rp.handle(stampedMsg(t, 2, "y", "20", testBase.Add(60*time.Second), yAt, yAt.Add(time.Millisecond)))
	rp.checkpoint(ctx)

	var found bool
	for _, de := range w.events {
		if de.Series != "x" {
			continue
		}
		found = true
		if !de.TriggeredAt.Equal(yAt) {
			t.Errorf("x's absence triggeredAt = %v, want the moving message's %v (x's own was %v, fired at %v)",
				de.TriggeredAt, yAt, xAt, de.OccurredTime)
		}
	}
	if !found {
		t.Fatalf("no absence fired for x; published %+v", w.events)
	}
}

// A detection fired by an idle advance has no triggering message: it is stamped with the
// advance's own time, which is non-zero.
func TestIdleAdvanceDetectionsCarryTheAdvanceTime(t *testing.T) {
	ctx := context.Background()
	clock := detectcore.NewManualClock(testBase)
	cw := &captureWriter{}
	rp := absenceProcessor(t, clock, cw, 5*time.Second)
	w := &derivedCapture{}
	rp.publisher = runtime.NewPublisher(w, rp.registry, (*DetectMetrics)(nil))

	rp.handle(measuredMsg(t, 1, "acme", "d1", "p@1", "temperature", "20", &fakeAck{}))
	now := testBase.Add(40 * time.Second)
	openGates(rp, now)
	clock.Set(now)
	rp.idleAdvance(ctx, now)

	if len(w.events) != 1 {
		t.Fatalf("idle advance published %d derived events, want 1", len(w.events))
	}
	if got := w.events[0].TriggeredAt; got.IsZero() || !got.Equal(now) {
		t.Fatalf("idle-advance detection triggeredAt = %v, want the advance time %v", got, now)
	}
}

// A replay re-derives event-driven detections from the same messages, so it stamps them with the
// same times — the stamp comes from the message, never from the wall clock at re-derivation.
func TestReplayStampsIdentically(t *testing.T) {
	ctx := context.Background()
	msgs := func() []messaging.Message {
		var out []messaging.Message
		for i := uint64(1); i <= 5; i++ {
			at := testBase.Add(time.Duration(i) * time.Second)
			out = append(out, stampedMsg(t, i, "d1", "90", at, at.Add(time.Millisecond), at.Add(2*time.Millisecond)))
		}
		return out
	}
	stamps := func() []time.Time {
		w := &derivedCapture{}
		rp := stampingProcessor(t, thresholdReg(t), w)
		for _, m := range msgs() {
			rp.handle(m)
		}
		rp.checkpoint(ctx)
		var out []time.Time
		for _, de := range w.events {
			out = append(out, de.TriggeredAt)
		}
		return out
	}
	first := stamps()
	time.Sleep(10 * time.Millisecond) // a wall-clock stamp would now differ
	second := stamps()
	if len(first) == 0 || len(first) != len(second) {
		t.Fatalf("detections: first %d, replay %d", len(first), len(second))
	}
	for i := range first {
		if !first[i].Equal(second[i]) {
			t.Errorf("detection %d: first stamped %v, replay stamped %v", i, first[i], second[i])
		}
	}
}
