// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/nats-io/nats.go/jetstream"
)

// These tests record WHY the durables that several replicas (and several reader loops) share
// are acknowledged one message at a time, and keep them that way.
//
// An AckAll ack names a stream sequence and the BROKER settles everything at or below it on
// the whole consumer: not "what this client received", but every entry in the consumer's
// pending set, including the ones the broker handed to a different pull. A reader that
// acknowledges a floor is therefore only correct when it is the durable's ONLY reader
// (event-processing's DETECT tap, which is term-gated to one leader). device-management's
// inbound durable, event-management's and device-state's resolved-events durables are read by
// every replica of the area, by several loops in each, and by worker pools that finish out of
// order; an AckAll ack from any one of them would silently settle a message another was still
// resolving, persisting or merging, which is data loss, not redelivery.

// TestAckAllOnASharedDurableSettlesAnotherPullsHeldMessage: replica B has been handed seq 1
// and is still working on it (or has failed it and is leaving it unacked for redelivery);
// replica A is handed seq 2 and acknowledges it. Under AckAll, seq 1 is settled by A's ack and
// never comes back. Control: the same two pulls under explicit acknowledgement leave seq 1
// pending and the broker redelivers it after AckWait.
func TestAckAllOnASharedDurableSettlesAnotherPullsHeldMessage(t *testing.T) {
	const ackWait = time.Second

	run := func(t *testing.T, policy jetstream.AckPolicy) (pendingAfterAck int, redelivered []uint64) {
		b, ctx := newAckBroker(t, 2)
		// One durable, two pulls: the shape of two replicas of one area.
		c := b.consumer(ctx, t, jetstream.ConsumerConfig{AckPolicy: policy, AckWait: ackWait})
		held := fetchAll(t, c, 1, 5*time.Second) // replica B: seq 1, still in flight
		if got := seqs(t, held); !slices.Equal(got, []uint64{1}) {
			t.Fatalf("replica B first delivery = %v, want [1]", got)
		}
		later := fetchAll(t, c, 1, 5*time.Second) // replica A: seq 2
		if got := seqs(t, later); !slices.Equal(got, []uint64{2}) {
			t.Fatalf("replica A delivery = %v, want [2]", got)
		}
		if err := later[0].Ack(); err != nil {
			t.Fatalf("ack seq 2: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
		info, err := c.Info(ctx)
		if err != nil {
			t.Fatalf("info: %v", err)
		}
		time.Sleep(2 * ackWait) // B never settles seq 1
		again, err := c.Fetch(5, jetstream.FetchMaxWait(time.Second))
		if err != nil {
			t.Fatalf("refetch: %v", err)
		}
		var got []jetstream.Msg
		for m := range again.Messages() {
			got = append(got, m)
		}
		return info.NumAckPending, seqs(t, got)
	}

	t.Run("AckAll settles the held message and it is never redelivered", func(t *testing.T) {
		pending, redelivered := run(t, jetstream.AckAllPolicy)
		if pending != 0 || len(redelivered) != 0 {
			t.Fatalf("AckAll: pending=%d redelivered=%v, want 0 and none (seq 1 settled by seq 2's ack)", pending, redelivered)
		}
	})
	t.Run("control: explicit ack leaves it pending and it is redelivered", func(t *testing.T) {
		pending, redelivered := run(t, jetstream.AckExplicitPolicy)
		if pending != 1 || !slices.Equal(redelivered, []uint64{1}) {
			t.Fatalf("explicit: pending=%d redelivered=%v, want 1 and [1]", pending, redelivered)
		}
	})
}

// sharedDurableReaders are the readers whose durable more than one process (or loop) pulls
// from, as the stream catalogue declares them.
var sharedDurableReaders = []struct{ area, suffix string }{
	{"device-management", streams.InboundEvents},
	{"event-management", streams.ResolvedEvents},
	{"device-state", streams.ResolvedEvents},
}

// consumingModes lists the consuming modes area is declared with on suffix.
func consumingModes(area, suffix string) []streams.ReadMode {
	var modes []streams.ReadMode
	for _, r := range streams.ReadersOf(suffix) {
		if r.Area == area && r.Mode.Consumes() {
			modes = append(modes, r.Mode)
		}
	}
	return modes
}

// TestSharedDurablesAreDeclaredAndBuiltAckExplicit: the catalogue declares these three as
// plain durable readers (not ReadAckFloor), and the consumer a plain reader creates on the
// broker has the explicit policy. Flipping either to AckAll trips this and sends the author to
// the comment above. Control: event-processing's resolved-events reader IS the ack-floor mode,
// so the assertion can tell the two apart.
func TestSharedDurablesAreDeclaredAndBuiltAckExplicit(t *testing.T) {
	if got := consumingModes("event-processing", streams.ResolvedEvents); !slices.Equal(got, []streams.ReadMode{streams.ReadAckFloor}) {
		t.Fatalf("control: event-processing's resolved-events mode = %v, want [ack-floor]", got)
	}
	for _, sd := range sharedDurableReaders {
		t.Run(sd.area+"/"+sd.suffix, func(t *testing.T) {
			if got := consumingModes(sd.area, sd.suffix); !slices.Equal(got, []streams.ReadMode{streams.ReadDurable}) {
				t.Fatalf("%s on %s declares consuming modes %v, want exactly [durable]", sd.area, sd.suffix, got)
			}
			srv := startEmbeddedServer(t)
			ctx := core.WithTenant(context.Background(), "acme")
			nmgr := NewNatsManager(testMicroservice(t, srv, sd.area), core.NewNoOpLifecycleCallbacks(),
				func(n *NatsManager) error {
					_, err := n.NewReader(sd.suffix)
					return err
				})
			nmgr.RecordMaxDeliveries(recordNothing)
			if err := nmgr.Initialize(ctx); err != nil {
				t.Fatalf("initialize: %v", err)
			}
			if err := nmgr.Start(ctx); err != nil {
				t.Fatalf("start: %v", err)
			}
			t.Cleanup(func() { _ = nmgr.Stop(ctx) })
			info, err := nmgr.js.ConsumerInfo(StreamName("test", sd.suffix), DurableName("test", sd.area, sd.suffix))
			if err != nil {
				t.Fatalf("consumer info: %v", err)
			}
			if got := info.Config.AckPolicy.String(); got != "AckExplicit" {
				t.Fatalf("%s's durable on %s has ack policy %q, want AckExplicit", sd.area, sd.suffix, got)
			}
		})
	}
}
