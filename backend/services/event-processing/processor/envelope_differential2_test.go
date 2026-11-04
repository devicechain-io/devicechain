// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"testing"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
)

// More of the legacy-vs-enveloped differential (envelope_differential_test.go): the parts of
// the engine's input an envelope could get wrong that the first corpus does not reach. Each
// test delivers the same events as legacy messages and as envelopes and demands the same engine
// state and the same derived events; each was written against a mutant that survived without it.

// diffProcessor is a restored processor over reg publishing to w, on store.
func diffProcessor(t *testing.T, reg *runtime.RuleRegistry, w *derivedCapture, store *model.SnapshotStore) *ResolvedEventsProcessor {
	t.Helper()
	rp := &ResolvedEventsProcessor{
		Store: store,
		cfg: Config{
			PartitionId: "singleton", CheckpointEvents: 100000, CheckpointInterval: time.Hour,
			TickInterval: time.Hour, Clock: detectcore.RealClock{}, Output: serialOutput,
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

// mkMsgs turns events into stream messages: one per event (k = 0), or envelopes of k members.
// Sequences are the stream's: 1, 2, 3, ... whichever the shape. Every message is stored at
// appendAt (a single value, so the receipts of legacy and enveloped delivery are identical).
func mkMsgs(t *testing.T, evs []*resolvedevent.ResolvedEvent, k int, appendAt time.Time) []messaging.Message {
	t.Helper()
	var out []messaging.Message
	seq := uint64(0)
	if k == 0 {
		for _, ev := range evs {
			b, err := resolvedevent.MarshalResolvedEvent(ev)
			if err != nil {
				t.Fatal(err)
			}
			seq++
			m := messaging.NewConsumedMessage("dc.acme.resolved-events", b, 0, nil, &fakeAck{})
			m.StreamSeq, m.AppendTime = seq, appendAt
			out = append(out, m)
		}
		return out
	}
	for start := 0; start < len(evs); start += k {
		end := min(start+k, len(evs))
		var members []eventenvelope.Event
		for i := start; i < end; i++ {
			b, err := resolvedevent.MarshalResolvedEvent(evs[i])
			if err != nil {
				t.Fatal(err)
			}
			members = append(members, eventenvelope.Event{Index: uint32(i - start), Bytes: b})
		}
		h := eventenvelope.Header{Kind: eventenvelope.KindResolved, Tenant: "acme", SourceSeq: uint64(start)}
		if len(members) == 1 {
			h.Round = 1
		}
		body, err := eventenvelope.Encode(h, members)
		if err != nil {
			t.Fatal(err)
		}
		seq++
		m := messaging.NewConsumedMessage("dc.acme.resolved-events", body, 0,
			map[string]string{eventenvelope.HeaderName: eventenvelope.HeaderValue}, &fakeAck{})
		m.StreamSeq, m.AppendTime = seq, appendAt
		out = append(out, m)
	}
	return out
}

func snapshotOf(t *testing.T, rp *ResolvedEventsProcessor) string {
	t.Helper()
	b, err := rp.snapshotEngine()
	if err != nil {
		t.Fatal(err)
	}
	return string(lastSeqPattern.ReplaceAll(b, []byte(`"lastSeq":0`)))
}

func sameOutcome(t *testing.T, what string, want, got string, wantIDs, gotIDs []string) {
	t.Helper()
	if want != got {
		t.Errorf("%s: the engine snapshot differs from the legacy one", what)
	}
	if fmt.Sprint(wantIDs) != fmt.Sprint(gotIDs) {
		t.Errorf("%s: derived dedup ids differ:\n legacy    %v\n enveloped %v", what, wantIDs, gotIDs)
	}
}

func run(t *testing.T, reg *runtime.RuleRegistry, evs []*resolvedevent.ResolvedEvent, k int, appendAt time.Time) (string, []string) {
	t.Helper()
	w := &derivedCapture{}
	rp := diffProcessor(t, reg, w, newTestStore(t))
	for _, m := range mkMsgs(t, evs, k, appendAt) {
		rp.handle(m)
	}
	rp.checkpoint(context.Background())
	return snapshotOf(t, rp), w.ids
}

func measure(device string, at, processed time.Time, temp string) *resolvedevent.ResolvedEvent {
	return &resolvedevent.ResolvedEvent{
		Source: "mqtt", SourceDeviceToken: device, DeviceTypeToken: "thermostat", ProfileVersionToken: "p@1",
		OccurredTime: at, ProcessedTime: processed, EventType: eventtype.Measurement,
		Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
			OccurredTime: at, Entries: []resolvedevent.ResolvedMeasurementEntry{{Name: "temperature", Value: temp}},
		}}},
	}
}

// Each member of an envelope is judged by its OWN receipt: a reading that may be a redelivery
// (stored long after it was received) is refused behind the frontier, a prompt one is not, and
// one member's verdict must not leak into the next. The stale-receipt variant and the prompt one
// derive different alarms, so the comparison cannot pass by ignoring receipts.
func TestEnvelopeMembersAreJudgedByTheirOwnReceipts(t *testing.T) {
	stored := testBase.Add(130 * time.Second)
	at := func(s int) time.Time { return testBase.Add(time.Duration(s) * time.Second) }
	build := func(behindReceived int) []*resolvedevent.ResolvedEvent {
		return []*resolvedevent.ResolvedEvent{
			measure("d1", at(100), at(110), "20"),             // prompt, moves the frontier to +100 s
			measure("d1", at(60), at(behindReceived), "35"),   // matches, 40 s behind the frontier
			measure("d1", at(120), at(125), "20"),             // prompt again
			measure("d1", at(70), at(behindReceived+5), "36"), // matches, behind again
		}
	}
	derived := map[string]int{}
	for name, received := range map[string]int{"stale receipt": 60, "prompt receipt": 120} {
		evs := build(received)
		wantSnap, wantIDs := run(t, diffRegistry(t), evs, 0, stored)
		derived[name] = len(wantIDs)
		for _, k := range []int{2, 4} {
			gotSnap, gotIDs := run(t, diffRegistry(t), evs, k, stored)
			sameOutcome(t, fmt.Sprintf("%s k=%d", name, k), wantSnap, gotSnap, wantIDs, gotIDs)
		}
	}
	if derived["stale receipt"] == derived["prompt receipt"] {
		t.Fatalf("the two receipts derive %d and %d events: the corpus does not exercise receipts", derived["stale receipt"], derived["prompt receipt"])
	}
}

// A member that the device has left the scope of is descoped under the ENVELOPE's starting
// sequence, not the engine's current one: the first member of an envelope moves LastSeq to the
// envelope's sequence, and a descope gated on that would never run for the members after it.
func TestADescopeMemberInsideAnEnvelopeDescopesLikeALegacyMessage(t *testing.T) {
	thr := 30.0
	cr, err := rules0.Compile(rules0.Rule{ID: "acme/scoped-hot", Name: "scoped hot", Type: rules0.TypeThreshold,
		When: rules0.Condition{Metric: "temperature", Op: rules0.OpGt, Threshold: &thr}}, rules0.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	scoped := runtime.ScopedRule{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: cr, GroupToken: "arid", GroupVersion: 1}
	reg := func() *runtime.RuleRegistry {
		base := diffRegistry(t)
		var all []runtime.ScopedRule
		for _, sr := range base.RulesFor("acme", "p@1") {
			all = append(all, *sr)
		}
		return runtime.NewRuleRegistry(append(all, scoped))
	}
	at := func(s int) time.Time { return testBase.Add(time.Duration(s) * time.Second) }
	in := func(ev *resolvedevent.ResolvedEvent) *resolvedevent.ResolvedEvent {
		ev.ScopeMemberships = []resolvedevent.GroupRef{{GroupToken: "arid", Version: 1}}
		return ev
	}
	evs := []*resolvedevent.ResolvedEvent{
		in(measure("d1", at(1), at(1), "40")), // in scope, hot: the scoped rule raises
		in(measure("d2", at(2), at(2), "41")), // another device in scope
		measure("d1", at(3), at(3), "42"),     // d1 has left the group: descope resolves its alarm
		measure("d2", at(4), at(4), "20"),     // d2 has left too
		in(measure("d1", at(5), at(5), "43")), // and d1 is back
		measure("d3", at(6), at(6), "20"),
	}
	stored := at(10)
	wantSnap, wantIDs := run(t, reg(), evs, 0, stored)
	if len(wantIDs) < 4 {
		t.Fatalf("the corpus derives only %d events; it must raise and descope", len(wantIDs))
	}
	for _, k := range []int{2, 3, 6} {
		gotSnap, gotIDs := run(t, reg(), evs, k, stored)
		sameOutcome(t, fmt.Sprintf("descope k=%d", k), wantSnap, gotSnap, wantIDs, gotIDs)
	}
}

// A restart in the middle of a run of envelopes: checkpoint, restore a new processor from the
// snapshot and carry on. The snapshot is taken between envelopes (never inside one) and the
// restored engine carries on to the legacy outcome.
func TestEnvelopesRestoredFromASnapshotCarryOnLikeLegacy(t *testing.T) {
	corpus := diffCorpus(120)
	var evs []*resolvedevent.ResolvedEvent
	for _, e := range corpus {
		evs = append(evs, diffResolved(t, e))
	}
	stored := corpus[len(corpus)-1].occurred
	wantSnap, wantIDs := run(t, diffRegistry(t), evs, 0, stored)

	for _, k := range []int{4, 7, 64} {
		msgs := mkMsgs(t, evs, k, stored)
		cut := len(msgs) / 2
		store := newTestStore(t)
		w1 := &derivedCapture{}
		rp := diffProcessor(t, diffRegistry(t), w1, store)
		for _, m := range msgs[:cut] {
			rp.handle(m)
		}
		rp.checkpoint(context.Background())
		w2 := &derivedCapture{}
		rp2 := diffProcessor(t, diffRegistry(t), w2, store) // restores from the stored snapshot
		if rp2.engine.LastSeq() != uint64(cut) {
			t.Fatalf("k=%d: restored at %d, want the envelope %d", k, rp2.engine.LastSeq(), cut)
		}
		for _, m := range msgs[cut:] {
			rp2.handle(m)
		}
		rp2.checkpoint(context.Background())
		sameOutcome(t, fmt.Sprintf("restore k=%d", k), wantSnap, snapshotOf(t, rp2), wantIDs, append(append([]string{}, w1.ids...), w2.ids...))
	}
}

// A gap in the stream is filled with envelopes read by range, applied in order, and the live
// envelope that found the gap follows them: the same outcome as legacy delivery.
func TestEnvelopesReadToFillAGapApplyLikeLegacy(t *testing.T) {
	corpus := diffCorpus(96)
	var evs []*resolvedevent.ResolvedEvent
	for _, e := range corpus {
		evs = append(evs, diffResolved(t, e))
	}
	stored := corpus[len(corpus)-1].occurred
	reg := diffRegistry(t)
	wantSnap, _ := run(t, reg, evs, 0, stored)

	for _, k := range []int{4, 16} {
		msgs := mkMsgs(t, evs, k, stored)
		// The live consumer sees the first envelope and then the LAST three, skipping the middle:
		// the gap is read from the stream by sequence.
		opener := &fakeReplayOpener{msgs: msgs}
		g := newGapRigWith(t, opener, reg)
		g.rp.handle(msgs[0])
		for _, m := range msgs[len(msgs)-3:] {
			g.rp.handle(m)
		}
		// Anything the three did not cover is delivered live in order, as it would be.
		if got := g.rp.engine.LastSeq(); got != uint64(len(msgs)) {
			t.Fatalf("k=%d: LastSeq = %d after the fill, want %d", k, got, len(msgs))
		}
		g.rp.checkpoint(context.Background())
		if got := snapshotOf(t, g.rp); got != wantSnap {
			t.Errorf("gap fill k=%d: the engine snapshot differs from the legacy one", k)
		}
		if g.fills("filled") < 1 {
			t.Errorf("gap fill k=%d: no gap was filled; the test did not exercise it", k)
		}
	}
}
