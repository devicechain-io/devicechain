// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package preview

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// A window of history that holds event envelopes replays as the events inside them, in order:
// the same firings as the same events delivered one to a message.
func TestPreviewReplaysEnvelopesAsTheirMembers(t *testing.T) {
	temps := []string{"90", "91", "70", "92", "60", "95"}
	var legacy []messaging.Message
	var members []eventenvelope.Event
	for i, v := range temps {
		m := msg(t, uint64(i+1), "acme", "d1", "p@1", "temperature", v, base.Add(time.Duration(i)*time.Minute))
		legacy = append(legacy, m)
		members = append(members, eventenvelope.Event{Index: uint32(i), Bytes: m.Value})
	}
	// Two envelopes (3 + 3), sequences 1 and 2, then a legacy message at 3 carrying one more.
	envelope := func(seq uint64, evs []eventenvelope.Event) messaging.Message {
		body, err := eventenvelope.Encode(eventenvelope.Header{Kind: eventenvelope.KindResolved, Tenant: "acme"}, evs)
		if err != nil {
			t.Fatal(err)
		}
		m := messaging.NewConsumedMessage("dc.acme.resolved-events", body, 0,
			map[string]string{eventenvelope.HeaderName: eventenvelope.HeaderValue}, nil)
		m.StreamSeq = seq
		return m
	}
	enveloped := []messaging.Message{envelope(1, members[:3]), envelope(2, members[3:])}

	run := func(msgs []messaging.Message) Result {
		res, err := Run(context.Background(), &fakeOpener{msgs: msgs}, "resolved-events", thresholdReg(t), "acme", "p@1", window(), 0, 0, 0, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	want, got := run(legacy), run(enveloped)
	if len(want.Firings) < 4 {
		t.Fatalf("the corpus fires only %d times; it must raise and resolve repeatedly", len(want.Firings))
	}
	if got.Stats.EventsScanned != want.Stats.EventsScanned {
		t.Fatalf("scanned %d events from envelopes, %d from messages", got.Stats.EventsScanned, want.Stats.EventsScanned)
	}
	if len(got.Firings) != len(want.Firings) {
		t.Fatalf("%d firings from envelopes, %d from messages", len(got.Firings), len(want.Firings))
	}
	for i := range want.Firings {
		if got.Firings[i] != want.Firings[i] {
			t.Fatalf("firing %d: %+v from envelopes, %+v from messages", i, got.Firings[i], want.Firings[i])
		}
	}
}
