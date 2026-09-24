// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deadletter

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
)

// consumed is a message as a durable reader on raise-alarm hands it out: attributable, on
// its fifth delivery, carrying a correlation id.
func consumed() messaging.Message {
	return messaging.NewConsumedMessage("inst.acme.raise-alarm", []byte(`{"x":1}`), 5, nil, nil).
		WithCorrelationID("corr-1").
		WithOrigin(messaging.Origin{Suffix: streams.RaiseAlarm, Stream: "inst_raise-alarm",
			Consumer: "inst_device-management_raise-alarm", Seq: 42})
}

func armEnvelope() Envelope {
	return Envelope{Reason: ReasonExhausted, Summary: "an edge could not be applied",
		Reference: "overheat", OccurredAt: time.Now().UTC(), Payload: []byte(`{"x":1}`)}
}

func tenantCtx() context.Context { return core.WithTenant(context.Background(), "acme") }

// A letter about a consumed message must come through WriteFor, where its dedup id is made.
// Write refuses one — and counts it — rather than storing a letter that would sit beside the
// max-delivery recorder's letter about the same delivery instead of deduping against it.
func TestWriteRefusesAMessageBoundEnvelope(t *testing.T) {
	p, reg := testProducer(t, "device-management")
	w := &fakeWriter{}
	e := good()
	e.Sequence = 7
	if err := p.NewSink(w).Write(tenantCtx(), e); err == nil {
		t.Fatal("Write accepted an envelope carrying a stream sequence")
	}
	if w.calls != 0 {
		t.Fatalf("the refused letter reached the writer (%d calls)", w.calls)
	}
	if got := lostCount(t, reg, "device-management"); got != 1 {
		t.Fatalf("dead_letter_lost_total = %v, want 1: a refused letter is a lost letter", got)
	}
	// The counterweight: without a sequence, Write still writes.
	if err := p.NewSink(w).Write(tenantCtx(), good()); err != nil || len(w.got) != 1 {
		t.Fatalf("an unbound letter was refused: %v", err)
	}
	if w.got[0].DedupID != "" {
		t.Fatalf("an unbound letter carried dedup id %q", w.got[0].DedupID)
	}
}

// WriteFor fills everything the message knows, and the kind comes from the stream's
// declaration — raise-alarm is declared detection-action.
func TestWriteForDerivesKindFromTheStream(t *testing.T) {
	p, _ := testProducer(t, "device-management")
	w := &fakeWriter{}
	if err := p.NewSink(w).WriteFor(tenantCtx(), consumed(), armEnvelope()); err != nil {
		t.Fatalf("WriteFor: %v", err)
	}
	if len(w.got) != 1 {
		t.Fatalf("wrote %d messages, want 1", len(w.got))
	}
	var e Envelope
	if err := json.Unmarshal(w.got[0].Value, &e); err != nil {
		t.Fatal(err)
	}
	if e.Kind != KindDetectionAction {
		t.Errorf("kind = %q, want %q (raise-alarm's declaration)", e.Kind, KindDetectionAction)
	}
	if e.Subject != "inst.acme.raise-alarm" || e.Sequence != 42 || e.Attempts != 5 || e.Correlation != "corr-1" {
		t.Errorf("subject/sequence/attempts/correlation = %q/%d/%d/%q", e.Subject, e.Sequence, e.Attempts, e.Correlation)
	}
	if e.Source != "device-management" {
		t.Errorf("source = %q", e.Source)
	}
	if got, want := w.got[0].DedupID, "mdl.inst_raise-alarm.inst_device-management_raise-alarm.42"; got != want {
		t.Errorf("dedup id = %q, want %q — the id the max-delivery recorder derives for the same delivery", got, want)
	}
}

// A preset kind is a second place deciding the kind, and the defect is that it can disagree
// with the recorder's. It is refused and counted.
func TestWriteForRefusesAPresetKind(t *testing.T) {
	p, reg := testProducer(t, "device-management")
	w := &fakeWriter{}
	e := armEnvelope()
	e.Kind = KindDetectionAction // even the RIGHT kind: the point is who decides
	if err := p.NewSink(w).WriteFor(tenantCtx(), consumed(), e); err == nil {
		t.Fatal("WriteFor accepted a preset kind")
	}
	if w.calls != 0 || lostCount(t, reg, "device-management") != 1 {
		t.Fatalf("calls=%d lost=%v, want 0 and 1", w.calls, lostCount(t, reg, "device-management"))
	}
}

// No origin, no letter: a message not from a durable reader has nothing to derive a kind or
// an id from. And a message from a NotLettered stream is refused too.
func TestWriteForRefusesAnUnattributableOrUnletteredOrigin(t *testing.T) {
	p, reg := testProducer(t, "user-management")
	w := &fakeWriter{}
	bare := messaging.NewConsumedMessage("inst.acme.raise-alarm", nil, 5, nil, nil)
	if err := p.NewSink(w).WriteFor(tenantCtx(), bare, armEnvelope()); err == nil {
		t.Error("WriteFor accepted a message with no origin")
	}
	sink := bare.WithOrigin(messaging.Origin{Suffix: streams.DeadLetters, Stream: "s", Consumer: "c", Seq: 1})
	if err := p.NewSink(w).WriteFor(tenantCtx(), sink, armEnvelope()); err == nil {
		t.Error("WriteFor accepted a message from the dead-letter stream")
	}
	if w.calls != 0 || lostCount(t, reg, "user-management") != 2 {
		t.Fatalf("calls=%d lost=%v, want 0 and 2", w.calls, lostCount(t, reg, "user-management"))
	}
}

// An id built from a partial origin would collide across unrelated messages and suppress one
// of them, so a message whose sequence is unknown is lettered with NO dedup id at all.
func TestWriteForSetsNoDedupIDWithoutOrigin(t *testing.T) {
	p, _ := testProducer(t, "device-management")
	w := &fakeWriter{}
	msg := consumed().WithOrigin(messaging.Origin{Suffix: streams.RaiseAlarm, Stream: "inst_raise-alarm",
		Consumer: "inst_device-management_raise-alarm", Seq: 0})
	if err := p.NewSink(w).WriteFor(tenantCtx(), msg, armEnvelope()); err != nil {
		t.Fatalf("WriteFor: %v", err)
	}
	if len(w.got) != 1 || w.got[0].DedupID != "" {
		t.Fatalf("wrote %d, dedup id %q; want one letter with no id", len(w.got), w.got[0].DedupID)
	}
	for _, o := range []messaging.Origin{
		{Suffix: streams.RaiseAlarm, Stream: "", Consumer: "c", Seq: 1},
		{Suffix: streams.RaiseAlarm, Stream: "s", Consumer: "", Seq: 1},
	} {
		if id := OriginID(consumed().WithOrigin(o)); id != "" {
			t.Errorf("OriginID(%+v) = %q, want empty", o, id)
		}
	}
}

// Every lettered stream's declared kind is a member of the vocabulary. core/streams cannot
// import this package, so this is where its string values are checked.
func TestEveryStreamKindIsInTheVocabulary(t *testing.T) {
	lettered := 0
	for _, s := range streams.All {
		if s.DeadLetterKind == streams.NotLettered {
			continue
		}
		lettered++
		if !Kind(s.DeadLetterKind).Valid() {
			t.Errorf("stream %q declares dead-letter kind %q, which is not in the vocabulary", s.Suffix, s.DeadLetterKind)
		}
	}
	if lettered == 0 {
		t.Fatal("no stream is lettered, so this test checks nothing")
	}
}
