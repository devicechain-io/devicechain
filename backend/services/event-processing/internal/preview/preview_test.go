// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package preview

import (
	"context"
	"io"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/messaging"
)

var base = time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)

// fakeReplayReader delivers a fixed message slice in order, then io.EOF; it records Close so a test
// can assert the ephemeral consumer was released.
type fakeReplayReader struct {
	msgs   []messaging.Message
	idx    int
	closed bool
}

func (r *fakeReplayReader) Read(context.Context) (messaging.Message, error) {
	if r.idx >= len(r.msgs) {
		return messaging.Message{}, io.EOF
	}
	m := r.msgs[r.idx]
	r.idx++
	return m, nil
}
func (r *fakeReplayReader) Close() error { r.closed = true; return nil }

// fakeOpener is a ReplayOpener returning a canned reader + first-retained time, recording the start
// time it was asked for.
type fakeOpener struct {
	msgs      []messaging.Message
	firstTime time.Time
	reader    *fakeReplayReader
	gotStart  time.Time
}

func (o *fakeOpener) NewReplayReaderFromTime(_ string, start time.Time) (messaging.ReplayReader, time.Time, error) {
	o.gotStart = start
	o.reader = &fakeReplayReader{msgs: o.msgs}
	return o.reader, o.firstTime, nil
}

// msg builds a consumed resolved measurement message for tenant/device under a profile version,
// carrying one metric reading, occurred at the given time, at stream sequence seq.
func msg(t *testing.T, seq uint64, tenant, device, profileVersion, metric, value string, occurred time.Time) messaging.Message {
	t.Helper()
	ev := &dmmodel.ResolvedEvent{
		Source:              "http1",
		SourceDeviceToken:   device,
		ProfileVersionToken: profileVersion,
		OccurredTime:        occurred,
		ProcessedTime:       occurred,
		EventType:           esmodel.Measurement,
		Payload: &dmmodel.ResolvedMeasurementsPayload{Entries: []dmmodel.ResolvedMeasurementsEntry{{
			OccurredTime: occurred,
			Entries:      []dmmodel.ResolvedMeasurementEntry{{Name: metric, Value: value}},
		}}},
	}
	b, err := dmproto.MarshalResolvedEvent(ev)
	if err != nil {
		t.Fatalf("marshal resolved event: %v", err)
	}
	m := messaging.NewConsumedMessage("dc."+tenant+".resolved-events", b, 0, nil, nil)
	m.StreamSeq = seq
	return m
}

// thresholdReg wires a registry with a single threshold rule (temperature > 80) for tenant acme /
// profile p@1 — the scope the preview harness filters and fans out against.
func thresholdReg(t *testing.T) *runtime.RuleRegistry {
	t.Helper()
	thr := 80.0
	cr, err := rules0.Compile(rules0.Rule{
		ID: "acme/p@1/hot", Name: "hot", Type: rules0.TypeThreshold, Severity: rules0.SeverityCritical,
		When: rules0.Condition{Metric: "temperature", Op: rules0.OpGt, Threshold: &thr},
	}, rules0.Limits{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return runtime.NewRuleRegistry([]runtime.ScopedRule{{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: cr}})
}

func window() TimeRange { return TimeRange{Start: base.Add(-time.Hour), End: base.Add(time.Hour)} }

// TestPreviewThresholdRaiseResolve: a device crossing above then below the threshold produces a
// RAISE then a RESOLVE (ADR-057 edge lifecycle), collected off the ephemeral engine — nothing published.
func TestPreviewThresholdRaiseResolve(t *testing.T) {
	op := &fakeOpener{msgs: []messaging.Message{
		msg(t, 1, "acme", "d1", "p@1", "temperature", "90", base),                  // rising → RAISE
		msg(t, 2, "acme", "d1", "p@1", "temperature", "70", base.Add(time.Minute)), // falling → RESOLVE
	}}
	res, err := Run(context.Background(), op, "resolved-events", thresholdReg(t), "acme", "p@1", window(), 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Firings) != 2 {
		t.Fatalf("want 2 firings (raise, resolve), got %d: %+v", len(res.Firings), res.Firings)
	}
	if !res.Firings[0].Raise || res.Firings[0].Series != "d1" {
		t.Fatalf("firing[0] = %+v, want a RAISE for d1", res.Firings[0])
	}
	if res.Firings[1].Raise {
		t.Fatalf("firing[1] = %+v, want a RESOLVE", res.Firings[1])
	}
	if res.Stats.EventsScanned != 2 || res.Stats.FiringCount != 2 {
		t.Fatalf("stats = %+v, want scanned=2 firings=2", res.Stats)
	}
	if res.Degraded != "" {
		t.Fatalf("unexpected degraded: %q", res.Degraded)
	}
	// Isolation: the ephemeral reader was released; the harness took no store/writer so it cannot have
	// published or snapshotted (structural).
	if !op.reader.closed {
		t.Fatal("the replay reader was not closed (ephemeral consumer leaked)")
	}
	if !op.gotStart.Equal(window().Start) {
		t.Fatalf("opener asked for start %v, want %v", op.gotStart, window().Start)
	}
}

// TestPreviewFiltersTenantAndProfile: events from a different tenant or a different profile version
// are ignored (not scanned, no firings).
func TestPreviewFiltersTenantAndProfile(t *testing.T) {
	op := &fakeOpener{msgs: []messaging.Message{
		msg(t, 1, "other", "d1", "p@1", "temperature", "90", base), // wrong tenant
		msg(t, 2, "acme", "d1", "p@2", "temperature", "90", base),  // wrong profile version
		msg(t, 3, "acme", "d1", "p@1", "temperature", "90", base),  // the only in-scope one → RAISE
	}}
	res, err := Run(context.Background(), op, "resolved-events", thresholdReg(t), "acme", "p@1", window(), 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stats.EventsScanned != 1 {
		t.Fatalf("scanned = %d, want 1 (tenant/profile filtered)", res.Stats.EventsScanned)
	}
	if len(res.Firings) != 1 || !res.Firings[0].Raise {
		t.Fatalf("want one RAISE, got %+v", res.Firings)
	}
}

// TestPreviewWindowFilter: events occurring outside [start, end] are skipped even if delivered.
func TestPreviewWindowFilter(t *testing.T) {
	tr := window()
	op := &fakeOpener{msgs: []messaging.Message{
		msg(t, 1, "acme", "d1", "p@1", "temperature", "90", tr.Start.Add(-time.Minute)), // before start
		msg(t, 2, "acme", "d1", "p@1", "temperature", "90", tr.End.Add(time.Minute)),    // after end
		msg(t, 3, "acme", "d2", "p@1", "temperature", "90", base),                       // in window → RAISE
	}}
	res, err := Run(context.Background(), op, "resolved-events", thresholdReg(t), "acme", "p@1", tr, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stats.EventsScanned != 1 || len(res.Firings) != 1 || res.Firings[0].Series != "d2" {
		t.Fatalf("only the in-window event should count; scanned=%d firings=%+v", res.Stats.EventsScanned, res.Firings)
	}
}

// TestPreviewAgedOut: a window starting before the earliest retained event degrades (but still runs).
func TestPreviewAgedOut(t *testing.T) {
	tr := window()
	op := &fakeOpener{
		firstTime: tr.Start.Add(30 * time.Minute), // earliest retained is AFTER the requested start
		msgs:      []messaging.Message{msg(t, 1, "acme", "d1", "p@1", "temperature", "90", base)},
	}
	res, err := Run(context.Background(), op, "resolved-events", thresholdReg(t), "acme", "p@1", tr, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Degraded == "" {
		t.Fatal("expected a degraded reason for an aged-out window start")
	}
	if len(res.Firings) != 1 {
		t.Fatalf("an aged-out preview still runs from the earliest retained event; got %+v", res.Firings)
	}
}

// TestPreviewReadCap: a flood of OUT-OF-SCOPE messages (wrong tenant) is bounded by the total-read
// cap even though none is in-scope — the in-scope scan cap alone would never trip (H1).
func TestPreviewReadCap(t *testing.T) {
	var msgs []messaging.Message
	for i := uint64(1); i <= 5; i++ {
		msgs = append(msgs, msg(t, i, "other", "d", "p@1", "temperature", "90", base)) // all wrong tenant
	}
	op := &fakeOpener{msgs: msgs}
	// maxRead=2: the loop must stop after reading 2 messages, not drain all 5, despite zero in-scope.
	res, err := Run(context.Background(), op, "resolved-events", thresholdReg(t), "acme", "p@1", window(), 0, 0, 2, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Degraded == "" {
		t.Fatal("expected a degraded reason when the total-read cap is hit")
	}
	if res.Stats.EventsScanned != 0 {
		t.Fatalf("no event is in scope; scanned should be 0, got %d", res.Stats.EventsScanned)
	}
	if op.reader.idx > 3 {
		t.Fatalf("the read loop should have stopped near the read cap; read %d messages", op.reader.idx)
	}
}

// TestPreviewScanCap: hitting the scan cap truncates and degrades rather than running unbounded.
func TestPreviewScanCap(t *testing.T) {
	op := &fakeOpener{msgs: []messaging.Message{
		msg(t, 1, "acme", "a", "p@1", "temperature", "90", base),
		msg(t, 2, "acme", "b", "p@1", "temperature", "90", base.Add(time.Second)),
		msg(t, 3, "acme", "c", "p@1", "temperature", "90", base.Add(2*time.Second)),
	}}
	res, err := Run(context.Background(), op, "resolved-events", thresholdReg(t), "acme", "p@1", window(), 0, 1, 0, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Degraded == "" {
		t.Fatal("expected a degraded reason when the scan cap is hit")
	}
	if res.Stats.EventsScanned > 2 {
		t.Fatalf("scan should have stopped near the cap; scanned=%d", res.Stats.EventsScanned)
	}
}

// batchMsg builds a STORE-AND-FORWARD message: one envelope carrying a sample taken earlier.
//
// 🔑 This is the shape msg() cannot make. msg() stamps the envelope and the entry at the SAME
// instant, so every test built on it is blind to which of the two the code under test reads —
// which is exactly how the preview came to filter on the envelope while claiming to filter on the
// span, with a green suite.
func batchMsg(t *testing.T, seq uint64, tenant, device, profileVersion, metric, value string,
	uploadedAt, sampledAt time.Time) messaging.Message {
	t.Helper()
	ev := &dmmodel.ResolvedEvent{
		Source:              "http1",
		SourceDeviceToken:   device,
		ProfileVersionToken: profileVersion,
		OccurredTime:        uploadedAt,
		ProcessedTime:       uploadedAt,
		EventType:           esmodel.Measurement,
		Payload: &dmmodel.ResolvedMeasurementsPayload{Entries: []dmmodel.ResolvedMeasurementsEntry{{
			OccurredTime: sampledAt,
			Entries:      []dmmodel.ResolvedMeasurementEntry{{Name: metric, Value: value}},
		}}},
	}
	b, err := dmproto.MarshalResolvedEvent(ev)
	if err != nil {
		t.Fatalf("marshal resolved event: %v", err)
	}
	m := messaging.NewConsumedMessage("dc."+tenant+".resolved-events", b, 0, nil, nil)
	m.StreamSeq = seq
	return m
}

// A store-and-forward batch whose ENVELOPE falls outside the previewed window is still
// evaluated, because its samples fall inside it.
//
// 🔴 REPLAY-CORRECTNESS IS THE WHOLE FEATURE, so this is not an edge case. A preview that
// tested only the envelope skipped the message WHOLE and reported "this rule would not have
// fired" about samples it never looked at, with no degraded flag saying so — a confident wrong
// answer, which is worse than a truncation notice.
//
// 🔑 THE ASSERTION IS THAT THE EVENT WAS ADMITTED AND COUNTED, not a claim about what the live
// engine does with a late batch. The engine advances a watermark from the instant it is handed
// and can still drop a batch's entries as late — the same in production as here, and a separate
// concern needing per-series watermarks. This test holds the narrower guarantee: an event
// overlapping the window reaches the engine, so a preview's silence is the engine's answer and
// not an artefact of the filter in front of it. The RAISE below is the evidence it got that far.
//
// 🔴🔴 THIS TEST GOES THROUGH Run, AND THAT IS THE POINT OF IT. Its predecessor called
// occurredSpan directly and then hand-wrote the comparison it believed Run made, under a comment
// saying "which is exactly the test the filter now makes". It was not: Run filtered on the
// envelope, and occurredSpan had no non-test caller at all. The assertion restated the intent
// instead of measuring the subject, so it passed whether or not the filter was ever wired in —
// and the feature shipped inert behind it. Assert on Run's OUTPUT (EventsScanned, Firings), never
// on a helper the production path might not call.
func TestPreviewEvaluatesABatchWhoseEnvelopeIsOutsideTheWindow(t *testing.T) {
	// The window closes at base+45m; the upload happened at base+65m carrying a reading taken at
	// base+30m. On the envelope alone the message is out; on its span it is in.
	tr := TimeRange{Start: base.Add(-15 * time.Minute), End: base.Add(45 * time.Minute)}
	op := &fakeOpener{msgs: []messaging.Message{
		batchMsg(t, 1, "acme", "d1", "p@1", "temperature", "150",
			base.Add(65*time.Minute), base.Add(30*time.Minute)),
	}}

	res, err := Run(context.Background(), op, "resolved-events", thresholdReg(t), "acme", "p@1", tr, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stats.EventsScanned != 1 {
		t.Fatalf("scanned=%d, want 1: a batch sampled inside the window was dropped whole by its "+
			"envelope, so the preview never saw readings live detection did fire on", res.Stats.EventsScanned)
	}
	if len(res.Firings) != 1 || !res.Firings[0].Raise {
		t.Fatalf("want one RAISE for the in-window sample, got %+v", res.Firings)
	}
	if res.Degraded != "" {
		t.Fatalf("nothing here is degraded, got %q", res.Degraded)
	}
}

// An event genuinely outside the window is still excluded. The counterweight: widening the
// filter to a span is only safe while it still excludes — a filter that admitted everything
// would satisfy the test above and silently preview events the window never asked for.
func TestPreviewSpanStillExcludesAnEventOutsideTheWindow(t *testing.T) {
	tr := TimeRange{Start: base, End: base.Add(30 * time.Minute)}
	after := base.Add(2 * time.Hour)
	op := &fakeOpener{msgs: []messaging.Message{
		// Envelope AND sample both past the window: nothing about this event overlaps it.
		batchMsg(t, 1, "acme", "d1", "p@1", "temperature", "150", after, after),
	}}

	res, err := Run(context.Background(), op, "resolved-events", thresholdReg(t), "acme", "p@1", tr, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stats.EventsScanned != 0 {
		t.Fatalf("scanned=%d, want 0: an event entirely outside the window must not be evaluated",
			res.Stats.EventsScanned)
	}
	if len(res.Firings) != 0 {
		t.Fatalf("an out-of-window event produced firings: %+v", res.Firings)
	}
}
