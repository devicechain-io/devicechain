// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// An event envelope is projected member by member through the same batch path a message is,
// and settles as one message.

// envelopeFrom packs the events of msgs (messages of one tenant) into one envelope message
// delivered the given number of times, acknowledged through ack.
func envelopeFrom(t *testing.T, delivered int, ack messaging.Acknowledger, msgs ...messaging.Message) messaging.Message {
	t.Helper()
	events := make([]eventenvelope.Event, len(msgs))
	for i, m := range msgs {
		events[i] = eventenvelope.Event{Index: uint32(i), Bytes: m.Value}
	}
	tenant, ok := messaging.ParseTenantFromSubject(msgs[0].Subject)
	if !ok {
		t.Fatalf("no tenant in %q", msgs[0].Subject)
	}
	body, err := eventenvelope.Encode(eventenvelope.Header{Kind: eventenvelope.KindResolved, Tenant: tenant, SourceSeq: 11}, events)
	if err != nil {
		t.Fatal(err)
	}
	return messaging.NewConsumedMessage(msgs[0].Subject, body, delivered,
		map[string]string{eventenvelope.HeaderName: eventenvelope.HeaderValue}, ack)
}

// Gate (b): a member that cannot be merged costs its neighbours nothing. They are projected;
// the envelope is left unacknowledged while the member has deliveries left, and acknowledged
// once, with the member dropped alone, on its last.
func TestAPoisonMemberDoesNotBlockItsEnvelopeMates(t *testing.T) {
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		delivered  int
		wantAcked  int32
		wantResult string
	}{
		{"with deliveries left, the envelope is redelivered whole", 1, 0, core.ResultRetry},
		{"on the last delivery, the member is dropped and the envelope acknowledged", messaging.MaxDeliver, 1, core.ResultDropped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			built, _, _ := newFencedStateProcessor(t)
			api := &poisonApi{Api: built.Api.(*model.Api)}
			built.Api = api
			reg := stateRegistry(built)

			ack := &recordingAck{}
			env := envelopeFrom(t, tc.delivered, ack,
				threeMetrics(t, "ea", t0), threeMetrics(t, "poison", t0), threeMetrics(t, "eb", t0))
			ch := make(chan messaging.Message, 1)
			ch <- env
			close(ch)
			built.messages = ch
			built.processMessages(context.Background())
			built.Api = api.Api

			if got := ackCounts([]*recordingAck{ack})[0]; got != tc.wantAcked {
				t.Errorf("the envelope was acknowledged %d times; want %d", got, tc.wantAcked)
			}
			assertProjectedAt(t, built, "ea", t0)
			assertProjectedAt(t, built, "eb", t0)
			// The RED counters count MEMBERS, as device-management's do: the poison member under
			// its own result (so a drop is never hidden behind a neighbour's result) and each
			// healthy member as ok.
			if v, _, _, _ := gathered(t, reg, "state_messages_total", tc.wantResult); v != 1 {
				t.Errorf("state_messages_total{result=%s} = %v; want 1 (the poison member)", tc.wantResult, v)
			}
			if v, _, _, _ := gathered(t, reg, "state_messages_total", core.ResultOK); v != 2 {
				t.Errorf("state_messages_total{result=ok} = %v; want 2 (the members around it)", v)
			}
		})
	}
}

// Gate (c): the same envelope delivered again - a lost ack, a restart - projects the same
// state, and each delivery is acknowledged once it has been merged.
func TestAnEnvelopeDeliveredTwiceProjectsTheSameState(t *testing.T) {
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	built, _, _ := newFencedStateProcessor(t)
	acks := []*recordingAck{{}, {}}
	for i, ack := range acks {
		env := envelopeFrom(t, i+1, ack, threeMetrics(t, "ra", t0), threeMetrics(t, "rb", t0.Add(time.Second)))
		ch := make(chan messaging.Message, 1)
		ch <- env
		close(ch)
		built.messages = ch
		built.processMessages(context.Background())
		assertProjectedAt(t, built, "ra", t0)
		assertProjectedAt(t, built, "rb", t0.Add(time.Second))
	}
	for i, n := range ackCounts(acks) {
		if n != 1 {
			t.Errorf("delivery %d acknowledged %d times; want 1", i+1, n)
		}
	}
}

// An envelope that cannot be read is poison and is dropped; the legacy message beside it is
// merged.
func TestAnUnreadableEnvelopeIsDroppedAndItsQueueMateMerged(t *testing.T) {
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	sp, _, _ := newFencedStateProcessor(t)
	good := threeMetrics(t, "ok-a", t0)
	acks := []*recordingAck{{}, {}}
	env := envelopeFrom(t, 1, acks[0], threeMetrics(t, "x", t0), threeMetrics(t, "y", t0))
	env.Value[len(env.Value)/2] ^= 0xff
	ch := make(chan messaging.Message, 2)
	ch <- env
	ch <- consumed(good, acks[1])
	close(ch)
	sp.messages = ch
	sp.processMessages(context.Background())
	for i, n := range ackCounts(acks) {
		if n != 1 {
			t.Errorf("message %d acknowledged %d times; want 1", i, n)
		}
	}
	assertProjectedAt(t, sp, "ok-a", t0)
}
