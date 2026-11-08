// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"math/rand"
	"regexp"
	"testing"
	"time"

	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
)

// The replay-correctness gate for event envelopes: the same events delivered as legacy
// messages and as envelopes (k = 1, 4, 64, and a size that leaves a remainder) leave the
// detection engine in a byte-identical state and derive the same alarms under the same dedup
// ids. Only the stream sequences differ, and the final one is made equal so the snapshot,
// which records it, can be compared whole.
//
// The corpus is built to be hard on exactly what an envelope could get wrong: four devices,
// irregular gaps (some long enough for an Absence timer to fire inside an envelope, driven by
// ANOTHER device's later member), readings that raise and resolve a threshold, a tumbling
// aggregate whose panes close mid-envelope, and a few events slightly out of order.

type diffEvent struct {
	device   string
	value    string
	occurred time.Time
}

// diffStored is the broker append time every message of a corpus is delivered with: after the newest
// event in it. The shared frontier is clamped to a message's append time, so a message stored
// BEFORE its events occurred would be testing the clamp, not the batching these files hold equal
// to single messages; the clamp has its own tests. One instant for every message and both shapes
// keeps what a message's receipt reads as the same however the events are grouped.
func diffStored(corpus []diffEvent) time.Time {
	var at time.Time
	for _, e := range corpus {
		if e.occurred.After(at) {
			at = e.occurred
		}
	}
	return at
}

func diffCorpus(n int) []diffEvent {
	rng := rand.New(rand.NewSource(20261103))
	devices := []string{"d1", "d2", "d3", "d4"}
	at := testBase
	out := make([]diffEvent, 0, n)
	for i := 0; i < n; i++ {
		step := 100*time.Millisecond + time.Duration(rng.Int63n(int64(3*time.Second)))
		// A long silence now and then, so Absence timers come due while others report.
		if i%23 == 22 {
			step += 20 * time.Second
		}
		at = at.Add(step)
		dev := devices[rng.Intn(len(devices))]
		// d4 goes quiet for a stretch: only the others' later events can fire its timer.
		if dev == "d4" && i > 30 && i < 70 {
			dev = "d1"
		}
		occurred := at
		if i%17 == 16 {
			occurred = at.Add(-2 * time.Second) // slightly out of order
		}
		out = append(out, diffEvent{device: dev, value: fmt.Sprintf("%.1f", 18+rng.Float64()*20), occurred: occurred})
	}
	return out
}

func diffRegistry(t *testing.T) *runtime.RuleRegistry {
	t.Helper()
	thr, agg := 30.0, 26.0
	compile := func(r rules0.Rule) runtime.ScopedRule {
		cr, err := rules0.Compile(r, rules0.Limits{})
		if err != nil {
			t.Fatalf("compile %s: %v", r.ID, err)
		}
		return runtime.ScopedRule{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: cr}
	}
	return runtime.NewRuleRegistry([]runtime.ScopedRule{
		compile(rules0.Rule{ID: "acme/hot", Name: "hot", Type: rules0.TypeThreshold,
			When: rules0.Condition{Metric: "temperature", Op: rules0.OpGt, Threshold: &thr}}),
		compile(rules0.Rule{ID: "acme/silent", Name: "silent", Type: rules0.TypeAbsence, Ttl: rules0.Duration(15 * time.Second)}),
		compile(rules0.Rule{ID: "acme/warm-window", Name: "warm window", Type: rules0.TypeAggregate,
			Metric: "temperature", Agg: rules0.AggAvg, Mode: rules0.ModeTumbling,
			Window: rules0.Duration(10 * time.Second), Op: rules0.OpGt, Threshold: &agg}),
	})
}

func diffResolved(t *testing.T, e diffEvent) *resolvedevent.ResolvedEvent {
	t.Helper()
	return &resolvedevent.ResolvedEvent{
		Source: "mqtt", SourceDeviceToken: e.device, DeviceTypeToken: "thermostat", ProfileVersionToken: "p@1",
		OccurredTime: e.occurred, ProcessedTime: e.occurred, EventType: eventtype.Measurement,
		Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
			OccurredTime: e.occurred,
			Entries:      []resolvedevent.ResolvedMeasurementEntry{{Name: "temperature", Value: e.value}},
		}}},
	}
}

// deliver feeds the corpus to a fresh processor as groups of k members (k = 0: legacy messages)
// and returns the engine's snapshot and the derived events with their dedup ids.
func deliver(t *testing.T, corpus []diffEvent, k int) ([]byte, *derivedCapture) {
	t.Helper()
	w := &derivedCapture{}
	rp := stampingProcessor(t, diffRegistry(t), w)
	ack := &fakeAck{}
	stored := diffStored(corpus)
	if k == 0 {
		for i, e := range corpus {
			b, err := resolvedevent.MarshalResolvedEvent(diffResolved(t, e))
			if err != nil {
				t.Fatal(err)
			}
			m := messaging.NewConsumedMessage("dc.acme.resolved-events", b, 0, nil, ack)
			m.StreamSeq = uint64(i + 1)
			m.AppendTime = stored
			rp.handle(m)
		}
	} else {
		for start := 0; start < len(corpus); start += k {
			end := min(start+k, len(corpus))
			var events []eventenvelope.Event
			for i := start; i < end; i++ {
				b, err := resolvedevent.MarshalResolvedEvent(diffResolved(t, corpus[i]))
				if err != nil {
					t.Fatal(err)
				}
				events = append(events, eventenvelope.Event{Index: uint32(i - start), Bytes: b})
			}
			// k = 1 cannot be an envelope in the first round; a later round may be, which
			// still exercises the whole envelope path for a one-member message.
			h := eventenvelope.Header{Kind: eventenvelope.KindResolved, Tenant: "acme", SourceSeq: uint64(start)}
			if len(events) == 1 {
				h.Round = 1
			}
			msg, err := eventenvelope.Encode(h, events)
			if err != nil {
				t.Fatal(err)
			}
			m := messaging.NewConsumedMessage("dc.acme.resolved-events", msg, 0,
				map[string]string{eventenvelope.HeaderName: eventenvelope.HeaderValue}, ack)
			// An envelope is ONE message of the stream: the sequences are 1, 2, 3, ... as ever.
			m.StreamSeq = uint64(start/k + 1)
			m.AppendTime = stored
			rp.handle(m)
		}
	}
	rp.checkpoint(context.Background()) // derived events are published at the checkpoint
	snap, err := rp.snapshotEngine()
	if err != nil {
		t.Fatal(err)
	}
	// The stream position is the one thing allowed to differ: legacy ends at the Nth message,
	// an enveloped delivery at the Mth envelope.
	return lastSeqPattern.ReplaceAll(snap, []byte(`"lastSeq":0`)), w
}

var lastSeqPattern = regexp.MustCompile(`"lastSeq":\d+`)

func TestEnvelopedDetectionIsByteIdenticalToLegacy(t *testing.T) {
	corpus := diffCorpus(150)
	wantSnap, wantDerived := deliver(t, corpus, 0)
	if len(wantDerived.ids) < 8 {
		t.Fatalf("the corpus derived only %d events; it must exercise raises, resolves and timers", len(wantDerived.ids))
	}
	kinds := map[string]bool{}
	for _, e := range wantDerived.events {
		kinds[e.RuleID] = true
	}
	if len(kinds) != 3 {
		t.Fatalf("the corpus fired rules %v; all three kinds must fire for the comparison to mean anything", kinds)
	}
	for _, k := range []int{1, 4, 7, 64} {
		gotSnap, gotDerived := deliver(t, corpus, k)
		if string(gotSnap) != string(wantSnap) {
			t.Errorf("k=%d: the engine snapshot differs from the legacy one (%d vs %d bytes)", k, len(gotSnap), len(wantSnap))
		}
		if len(gotDerived.ids) != len(wantDerived.ids) {
			t.Errorf("k=%d: %d derived events, legacy derived %d", k, len(gotDerived.ids), len(wantDerived.ids))
			continue
		}
		for i := range wantDerived.ids {
			if gotDerived.ids[i] != wantDerived.ids[i] {
				t.Errorf("k=%d: derived event %d has dedup id %q, legacy %q", k, i, gotDerived.ids[i], wantDerived.ids[i])
				break
			}
			g, w := gotDerived.events[i], wantDerived.events[i]
			if g.RuleID != w.RuleID || g.Series != w.Series || g.Edge != w.Edge || !g.OccurredTime.Equal(w.OccurredTime) {
				t.Errorf("k=%d: derived event %d is %+v, legacy %+v", k, i, g, w)
				break
			}
		}
	}
}

// Gate (b) for DETECT: a member that cannot be decoded is skipped alone, by its place in the
// envelope, and the others apply. The envelope is handled (LastSeq reaches its sequence) and
// nothing is redelivered for it; an envelope that cannot be read at all is skipped whole, by its
// sequence, as a poison message is.
func TestAPoisonMemberIsSkippedAloneAndTheEnvelopeIsHandled(t *testing.T) {
	corpus := diffCorpus(10)
	build := func(devices ...string) messaging.Message {
		var events []eventenvelope.Event
		for i, d := range devices {
			e := corpus[i]
			e.device = d
			b, err := resolvedevent.MarshalResolvedEvent(diffResolved(t, e))
			if err != nil {
				t.Fatal(err)
			}
			events = append(events, eventenvelope.Event{Index: uint32(i), Bytes: b})
		}
		msg, err := eventenvelope.Encode(eventenvelope.Header{Kind: eventenvelope.KindResolved, Tenant: "acme"}, events)
		if err != nil {
			t.Fatal(err)
		}
		m := messaging.NewConsumedMessage("dc.acme.resolved-events", msg, 0,
			map[string]string{eventenvelope.HeaderName: eventenvelope.HeaderValue}, &fakeAck{})
		m.StreamSeq, m.AppendTime = 1, diffStored(corpus)
		return m
	}
	rp := stampingProcessor(t, diffRegistry(t), &derivedCapture{})
	rp.decodeResolved = func(b []byte) (*resolvedevent.ResolvedEvent, error) {
		ev, err := resolvedevent.UnmarshalResolvedEvent(b)
		if err == nil && ev.SourceDeviceToken == "poison" {
			return nil, fmt.Errorf("injected: undecodable member")
		}
		return ev, err
	}
	before := rp.engine.Watermark()
	rp.handle(build("d1", "poison", "d3"))
	if rp.engine.LastSeq() != 1 {
		t.Fatalf("LastSeq = %d; the envelope is handled whatever its members did", rp.engine.LastSeq())
	}
	if rp.poisonSkipped != 1 {
		t.Fatalf("poisonSkipped = %d, want the one member", rp.poisonSkipped)
	}
	if !rp.engine.Watermark().After(before) {
		t.Fatal("the healthy members did not apply")
	}

	// An envelope nobody can read is skipped whole, by its sequence.
	bad := build("d1", "d2")
	bad.Value = append([]byte(nil), bad.Value...)
	bad.Value[len(bad.Value)/2] ^= 0xff
	bad.StreamSeq = 2
	rp.handle(bad)
	if rp.engine.LastSeq() != 2 || rp.poisonSkipped != 2 {
		t.Fatalf("LastSeq = %d, poisonSkipped = %d; an unreadable envelope is skipped whole", rp.engine.LastSeq(), rp.poisonSkipped)
	}
}

// An envelope redelivered or replayed after it was applied is dropped whole, by its sequence,
// as a message is: its members do not apply a second time.
func TestAnEnvelopeAppliedTwiceIsAppliedOnce(t *testing.T) {
	corpus := diffCorpus(40)
	var events []eventenvelope.Event
	for i := 0; i < 8; i++ {
		b, err := resolvedevent.MarshalResolvedEvent(diffResolved(t, corpus[i]))
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, eventenvelope.Event{Index: uint32(i), Bytes: b})
	}
	msg, err := eventenvelope.Encode(eventenvelope.Header{Kind: eventenvelope.KindResolved, Tenant: "acme"}, events)
	if err != nil {
		t.Fatal(err)
	}
	mk := func() messaging.Message {
		m := messaging.NewConsumedMessage("dc.acme.resolved-events", msg, 0,
			map[string]string{eventenvelope.HeaderName: eventenvelope.HeaderValue}, &fakeAck{})
		m.StreamSeq, m.AppendTime = 1, diffStored(corpus)
		return m
	}
	w := &derivedCapture{}
	rp := stampingProcessor(t, diffRegistry(t), w)
	rp.handle(mk())
	once, err := rp.snapshotEngine()
	if err != nil {
		t.Fatal(err)
	}
	rp.handle(mk())
	twice, err := rp.snapshotEngine()
	if err != nil {
		t.Fatal(err)
	}
	if string(once) != string(twice) {
		t.Fatal("a redelivered envelope changed the engine's state")
	}
	if rp.engine.LastSeq() != 1 {
		t.Fatalf("LastSeq = %d, want the envelope's 1", rp.engine.LastSeq())
	}
}
