// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
)

// A durable created with ReaderWithDeliverLookback starts INSIDE the window: what was published
// just before the durable existed is read (the rollout-window traffic a tail-only durable
// skips), what is older than the window is not, and a restarted reader keeps the position the
// durable was created with instead of recomputing a start time the server would refuse.
func TestDeliverLookbackStartsInsideTheWindowAndSurvivesARestart(t *testing.T) {
	const suffix = streams.AlarmEvents
	srv := startEmbeddedServer(t)
	ctx := core.WithTenant(context.Background(), "acme")

	var writer MessageWriter
	producer := NewNatsManager(testMicroservice(t, srv, uniqueArea("lookback-producer")),
		core.NewNoOpLifecycleCallbacks(), func(n *NatsManager) error {
			w, err := n.NewWriter(suffix)
			writer = w
			return err
		})
	if err := producer.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := producer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = producer.Stop(ctx) })
	write := func(body string) {
		t.Helper()
		if err := writer.WriteMessages(ctx, Message{Value: []byte(body)}); err != nil {
			t.Fatal(err)
		}
	}

	write("older-than-the-window")
	time.Sleep(2500 * time.Millisecond)
	write("inside-the-window")

	area := uniqueArea("lookback-consumer")
	var reader MessageReader
	consumer := NewNatsManager(testMicroservice(t, srv, area), core.NewNoOpLifecycleCallbacks(),
		func(n *NatsManager) error {
			r, err := n.NewReader(suffix, ReaderWithDeliverLookback(time.Second))
			reader = r
			return err
		})
	consumer.RecordMaxDeliveries(recordNothing)
	if err := consumer.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := consumer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	read := func() (string, uint64) {
		t.Helper()
		rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		m, err := reader.ReadMessage(rctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if err := m.Ack(); err != nil {
			t.Fatal(err)
		}
		return string(m.Value), m.StreamSeq
	}
	got, seq := read()
	if got != "inside-the-window" {
		t.Fatalf("first read %q, want %q: the lookback must include what was published just before the durable existed and nothing older", got, "inside-the-window")
	}
	streamName, durable := StreamName("test", suffix), DurableName("test", area, suffix)
	deadline := time.Now().Add(10 * time.Second)
	for {
		info, err := consumer.js.ConsumerInfo(streamName, durable)
		if err != nil {
			t.Fatal(err)
		}
		if info.AckFloor.Stream >= seq {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ack did not reach the broker")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := consumer.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	write("during-the-restart")

	// The replacement computes a later "now". Were that applied to the existing durable the
	// server would refuse the config and the reader would fail to bind at all.
	time.Sleep(1200 * time.Millisecond)
	replacement := &NatsManager{Microservice: testMicroservice(t, srv, area)}
	if err := replacement.ExecuteInitialize(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if replacement.nc != nil {
			replacement.nc.Close()
		}
	})
	r, err := replacement.NewReader(suffix, ReaderWithDeliverLookback(time.Second))
	if err != nil {
		t.Fatalf("a restarted reader could not bind its existing durable: %v", err)
	}
	reader = r
	if got, _ := read(); got != "during-the-restart" {
		t.Fatalf("replacement read %q, want %q", got, "during-the-restart")
	}
}
