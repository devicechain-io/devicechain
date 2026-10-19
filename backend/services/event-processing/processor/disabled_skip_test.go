// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"testing"
	"time"
)

// presenceAt is a resolved presence transition at stream sequence seq, marked as a disabled
// device's when disabled is set.
func presenceAt(t *testing.T, seq uint64, ack *fakeAck, occurred time.Time, disabled bool) messaging.Message {
	t.Helper()
	b, err := resolvedevent.MarshalResolvedEvent(&resolvedevent.ResolvedEvent{
		Source: "mqtt", SourceDeviceToken: "dev-1", OccurredTime: occurred, ProcessedTime: occurred,
		EventType: esmodel.StateChange, SourceDeviceDisabled: disabled,
		Payload: &resolvedevent.ResolvedStateChangePayload{State: "DISCONNECTED", SessionId: 9},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	m := messaging.NewConsumedMessage(testSubject, b, 0, nil, ack)
	m.StreamSeq = seq
	registerPending(t, ack, seq)
	return m
}

// 🔴 A DISABLED DEVICE'S TRANSITION MOVES NOTHING IN DETECTION, THE WATERMARK INCLUDED. Before
// these transitions were let through resolution they never reached the stream; an empty plan
// still advanced the shared frontier to the event's time, firing other devices' timers at
// it. The sequence is recorded as handled, so the checkpoint still moves past it. The
// unmarked transition is the control: it does move the watermark.
func TestADisabledDevicesTransitionDoesNotMoveTheWatermark(t *testing.T) {
	for _, tc := range []struct {
		disabled bool
		moves    bool
	}{{false, true}, {true, false}} {
		ctx := context.Background()
		store := newTestStore(t)
		rp := newTestProcessor(store, nil, 100)
		if err := rp.restore(ctx); err != nil {
			t.Fatalf("restore: %v", err)
		}
		rp.handle(msgAt(t, 1, &fakeAck{}))
		before := rp.engine.Watermark()
		later := testBase.Add(time.Hour)
		rp.handle(presenceAt(t, 2, &fakeAck{}, later, tc.disabled))
		if got := rp.engine.LastSeq(); got != 2 {
			t.Fatalf("disabled=%v: lastSeq = %d, want 2 (the sequence is handled either way)", tc.disabled, got)
		}
		if moved := !rp.engine.Watermark().Equal(before); moved != tc.moves {
			t.Fatalf("disabled=%v: watermark moved=%v (now %v), want moved=%v", tc.disabled, moved, rp.engine.Watermark(), tc.moves)
		}
		rp.checkpoint(ctx)
		snap, ok, err := store.Load(ctx, "singleton")
		if err != nil || !ok || snap.StreamSeq != 2 {
			t.Fatalf("disabled=%v: checkpoint ok=%v err=%v seq=%d, want 2", tc.disabled, ok, err, snap.StreamSeq)
		}
	}
}
