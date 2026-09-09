// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
)

// TestADeliverNewDurableStillReceivesTrafficPublishedWhileItIsDown states the half of
// ReaderWithDeliverNew's contract that its own doc comment asserts and nothing measured:
// "Downtime-safety is unaffected: once the durable exists its ack cursor persists, so a
// restart still resumes from the last ack, not the tail."
//
// 🔴 THAT SENTENCE IS LOAD-BEARING FOR A HUMAN BEING GETTING PAGED. notification-management
// is the one reader that opts into DeliverNew, and it is the last hop of the alarm flow. If
// the policy applied on every bind rather than only on first creation, every alarm that
// transitioned while that service was restarting would be skipped at the tail — never
// delivered, never retried, and invisible, because a delivery policy leaves no trace of what
// it stepped over. The claim that this does not happen was written as prose beside the code
// that would have to be wrong for it to happen.
//
// The test states BOTH halves of the policy, because they pull in opposite directions and
// only asserting the safe one would let the unsafe one grow:
//
//   - a message retained BEFORE the durable was ever created is skipped. That is the point
//     of the option — DeliverAll here would page humans about every alarm still inside the
//     stream's retention the first time the service is enabled on a running fleet — and it
//     is also the option's one real cost, so it is written down as a measured fact rather
//     than left to be rediscovered.
//   - a message published while an EXISTING durable's reader is down is delivered when it
//     comes back. The consumer is created out of band and attached with nats.Bind, so it
//     outlives every pod that reads it and its cursor is the only thing that decides where
//     the next read starts.
//
// TestTrafficPublishedWhileTheConsumerIsDownSurvives proves the second half for the DEFAULT
// (DeliverAll) durable. This is the same property for the durable whose creation-time policy
// is the one that could plausibly break it.
func TestADeliverNewDurableStillReceivesTrafficPublishedWhileItIsDown(t *testing.T) {
	const (
		suffix = streams.AlarmEvents // the stream the one DeliverNew reader in the tree reads
		tenant = "acme"
	)
	srv := startEmbeddedServer(t)
	ctx := core.WithTenant(context.Background(), tenant)

	// A producer that stays up throughout, standing in for device-management going on
	// publishing alarm transitions while the notification service is replaced.
	var writer MessageWriter
	producer := NewNatsManager(testMicroservice(t, srv, uniqueArea("delivernew-producer")),
		core.NewNoOpLifecycleCallbacks(), func(n *NatsManager) error {
			w, err := n.NewWriter(suffix)
			writer = w
			return err
		})
	if err := producer.Initialize(ctx); err != nil {
		t.Fatalf("producer initialize: %v", err)
	}
	if err := producer.Start(ctx); err != nil {
		t.Fatalf("producer start: %v", err)
	}
	t.Cleanup(func() { _ = producer.Stop(ctx) })

	write := func(body string) {
		t.Helper()
		if err := writer.WriteMessages(ctx, Message{Value: []byte(body)}); err != nil {
			t.Fatalf("write %q: %v", body, err)
		}
	}

	// Retained before any durable exists. This is the backlog DeliverNew exists to skip.
	write("before-the-durable-existed")

	var reader MessageReader
	// Taken once and reused: the replacement must land on the SAME durable, and
	// DurableName is built from InstanceId + FunctionalArea + suffix.
	consumerArea := uniqueArea("delivernew-consumer")
	consumer := NewNatsManager(testMicroservice(t, srv, consumerArea),
		core.NewNoOpLifecycleCallbacks(), func(n *NatsManager) error {
			r, err := n.NewReader(suffix, ReaderWithDeliverNew())
			reader = r
			return err
		})
	if err := consumer.Initialize(ctx); err != nil {
		t.Fatalf("consumer initialize: %v", err)
	}
	if err := consumer.Start(ctx); err != nil {
		t.Fatalf("consumer start: %v", err)
	}

	streamName := StreamName("test", suffix)
	durableName := DurableName("test", consumerArea, suffix)

	read := func(who string) (string, uint64) {
		t.Helper()
		rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		m, err := reader.ReadMessage(rctx)
		if err != nil {
			t.Fatalf("%s read: %v", who, err)
		}
		if err := m.Ack(); err != nil {
			t.Fatalf("%s ack: %v", who, err)
		}
		return string(m.Value), m.StreamSeq
	}
	// The ack has to be ON THE BROKER before the consumer goes down, or the assertion
	// that the gap traffic — and not the already-read message — comes back first is a
	// race rather than a property.
	waitAckFloor := func(seq uint64) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			info, err := consumer.js.ConsumerInfo(streamName, durableName)
			if err != nil {
				t.Fatalf("consumer info: %v", err)
			}
			if info.AckFloor.Stream >= seq {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("ack floor did not reach seq %d (stuck at %d)", seq, info.AckFloor.Stream)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	// Half one: the durable was created at the tail, so the retained message is skipped
	// and the first thing read is the first thing published AFTER the bind.
	write("after-the-durable-existed")
	got, seq := read("first consumer")
	if got != "after-the-durable-existed" {
		t.Fatalf("first read was %q, want %q: a DeliverNew durable must start at the tail, "+
			"not replay what the stream was already holding", got, "after-the-durable-existed")
	}
	waitAckFloor(seq)

	// The reader goes down — the rollout, crash or restart window.
	if err := consumer.Stop(ctx); err != nil {
		t.Fatalf("consumer stop: %v", err)
	}

	// Alarm transitions the broker accepts while nothing is reading them.
	const gapCount = 3
	for i := 0; i < gapCount; i++ {
		write(fmt.Sprintf("during-%d", i))
	}

	// The replacement binds the SAME durable, with the SAME option a restarted pod would
	// pass, the way a replacement pod does.
	replacement := &NatsManager{Microservice: testMicroservice(t, srv, consumerArea)}
	if err := replacement.ExecuteInitialize(ctx); err != nil {
		t.Fatalf("replacement initialize: %v", err)
	}
	t.Cleanup(func() {
		if replacement.nc != nil {
			replacement.nc.Close()
		}
	})
	r, err := replacement.NewReader(suffix, ReaderWithDeliverNew())
	if err != nil {
		t.Fatalf("replacement reader: %v", err)
	}
	reader = r

	// Half two: everything published during the gap is delivered, in order. A durable that
	// had been deleted and recreated — or one whose delivery policy were applied on every
	// bind rather than at creation — would start at the tail here and hand back nothing but
	// what arrives next, which is the silent loss this asserts against.
	for i := 0; i < gapCount; i++ {
		want := fmt.Sprintf("during-%d", i)
		if got, _ := read("replacement"); got != want {
			t.Fatalf("replacement read %q, want %q: an alarm event published while the "+
				"consumer was down was skipped, so nobody would have been paged about it "+
				"and nothing would record that it happened", got, want)
		}
	}

	// And the already-acked message does not come back, so the test would not also pass on
	// a durable that simply replays everything — which loses nothing but duplicates every
	// page.
	write("after")
	if got, _ := read("replacement"); got != "after" {
		t.Fatalf("replacement read %q, want %q: an already-acked message came back, so the "+
			"durable's position did not survive the restart", got, "after")
	}
}
