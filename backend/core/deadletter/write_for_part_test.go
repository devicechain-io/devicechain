// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deadletter

import (
	"encoding/json"
	"testing"
)

// A part that cannot tell one part from another is refused before any write and counted as
// a loss, exactly as WriteFor's own refusals are.
func TestWriteForPartRefusesAnEmptyPart(t *testing.T) {
	for _, part := range []string{"", "a b", "tab\there", "\n"} {
		p, reg := testProducer(t, "event-processing")
		w := &fakeWriter{}
		if err := p.NewSink(w).WriteForPart(tenantCtx(), consumed(), part, armEnvelope()); err == nil {
			t.Fatalf("part %q was accepted", part)
		}
		if w.calls != 0 {
			t.Fatalf("part %q reached the writer (%d calls)", part, w.calls)
		}
		if got := lostCount(t, reg, "event-processing"); got != 1 {
			t.Fatalf("part %q: dead_letter_lost_total = %v, want 1", part, got)
		}
	}
}

// Two parts of one message are two letters with distinct ids, each OriginID+".part."+part, and
// neither is the plain OriginID a whole-message letter (the recorder's, or an arm's) carries.
// Everything else is filled exactly as WriteFor fills it.
func TestWriteForPartIdsAreDistinctPerPart(t *testing.T) {
	p, _ := testProducer(t, "event-processing")
	w := &fakeWriter{}
	sink := p.NewSink(w)
	msg := consumed()
	for _, part := range []string{"tok-a", "tok-b"} {
		if err := sink.WriteForPart(tenantCtx(), msg, part, armEnvelope()); err != nil {
			t.Fatalf("WriteForPart(%q): %v", part, err)
		}
	}
	if err := sink.WriteFor(tenantCtx(), msg, armEnvelope()); err != nil {
		t.Fatalf("WriteFor: %v", err)
	}
	if len(w.got) != 3 {
		t.Fatalf("wrote %d letters, want 3", len(w.got))
	}
	origin := OriginID(msg)
	if origin == "" {
		t.Fatal("the fixture message has no origin id; nothing below would be measured")
	}
	want := []string{origin + ".part.tok-a", origin + ".part.tok-b", origin}
	for i, m := range w.got {
		if m.DedupID != want[i] {
			t.Errorf("letter %d dedup id = %q, want %q", i, m.DedupID, want[i])
		}
	}
	var e Envelope
	if err := json.Unmarshal(w.got[0].Value, &e); err != nil {
		t.Fatal(err)
	}
	if e.Kind != KindDetectionAction || e.Sequence != 42 || e.Attempts != 5 || e.Correlation != "corr-1" {
		t.Errorf("kind/sequence/attempts/correlation = %q/%d/%d/%q, want WriteFor's derivation",
			e.Kind, e.Sequence, e.Attempts, e.Correlation)
	}
}
