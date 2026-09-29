// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"math"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/rs/zerolog"
)

// What a stream's warnings are based on, measured against a real embedded JetStream server.
//
// Every stream is Limits retention with a week of history and discards its oldest message
// at its ceiling, so a busy stream sits near that ceiling full of messages its consumers
// have ALREADY read. Its fill therefore says nothing about loss. What precedes a loss is a
// consumer whose UNREAD backlog nears the ceiling, and these tests pin the series that
// carries it (jetstream_consumer_unread_ratio), who exports it, and the declaration that
// decides which streams' fill still means something (jetstream_stream_sink).

// The defect, as a value: a stream 90% full, of which 50 points are history the durable has
// read and acknowledged, is a durable 40% of the way to losing anything. A fill-based
// number reads 0.9 here.
func TestConsumerUnreadRatioIgnoresHistoryTheDurableHasRead(t *testing.T) {
	g := newUnreadRig(t, 100) // alarm-events, MaxMsgs 100: the message limit binds
	g.publish("t1", 50)
	g.sample()
	if got := g.mustSeries(unreadRatioSeries); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("unread ratio = %v with 50 of 100 unread; want 0.5", got)
	}
	g.read(50) // read and acked: history now
	g.publish("t1", 40)
	g.sample()
	info := g.streamInfo()
	if info.State.Msgs != 90 {
		t.Fatalf("stream holds %d; want 90 (50 read + 40 unread), nothing evicted", info.State.Msgs)
	}
	if fill := streamFillRatio(info); math.Abs(fill-0.9) > 1e-9 {
		t.Fatalf("stream fill = %v; want 0.9, or this is not the case of a stream full of read history", fill)
	}
	if got := g.mustSeries(unreadRatioSeries); math.Abs(got-0.4) > 1e-9 {
		t.Errorf("unread ratio = %v with the stream 90%% full of which 50 are read; want 0.4 — read history must not count", got)
	}
}

// Messages handed out but not yet acknowledged are unread too: a consumer that fetched them
// and then stalled has not processed them, and DiscardOld would take them just the same.
func TestConsumerUnreadRatioCountsHandedOutUnacked(t *testing.T) {
	g := newUnreadRig(t, 100)
	g.publish("t1", 10)
	g.readNoAck(4)
	g.sample()
	if got := g.mustSeries(unreadRatioSeries); math.Abs(got-0.1) > 1e-9 {
		t.Errorf("unread ratio = %v with 10 of 100 not yet acknowledged (4 of them handed out); want 0.1", got)
	}
}

// A durable whose backlog makes its stream refuse writers (event-management's on
// resolved-events) is measured by the WRITERS, as jetstream_backpressure_unread_ratio, which
// is what the refusal is decided on and what JetStreamUnreadBacklogNearFull reads. Its reader
// leaves the reader-side ratio out, so the two alerts can never both fire for one durable.
// A non-gating reader of the SAME stream (device-state) still exports its own.
func TestAGatingDurableLeavesItsUnreadRatioToTheWriters(t *testing.T) {
	srv := startEmbeddedServer(t)
	b := bpBounds{maxMsgs: 20}
	em := newBPService(t, srv, "event-management", b)
	em.reader(t, streams.ResolvedEvents)
	ds := newBPService(t, srv, "device-state", b)
	ds.reader(t, streams.ResolvedEvents)
	dm := newBPService(t, srv, "device-management", b)
	w := dm.writer(t, streams.ResolvedEvents)
	if refused, _ := fill(t, dm, w, streams.ResolvedEvents, 5, []byte("m")); refused != 0 {
		t.Fatalf("%d of 5 publishes refused on a stream 25%% full; want none", refused)
	}
	em.nmgr.sampleNow(context.Background())
	ds.nmgr.sampleNow(context.Background())
	stream := StreamName("test", streams.ResolvedEvents)

	// The gating durable IS sampled: its absence below is the exclusion, not a missed sample.
	if v, ok := em.metric(t, "jetstream_consumer_pending_messages", stream); !ok || v != 5 {
		t.Fatalf("event-management pending = %v (present %v); want 5", v, ok)
	}
	if v, ok := em.metric(t, "jetstream_consumer_unread_ratio", stream); ok {
		t.Errorf("the gating durable exports a reader-side unread ratio (%v); the writers export it", v)
	}
	if v, ok := dm.metric(t, "jetstream_backpressure_unread_ratio", stream); !ok || math.Abs(v-0.25) > 1e-9 {
		t.Errorf("the writer's backpressure ratio for the gating durable = %v (present %v); want 0.25", v, ok)
	}
	if v, ok := ds.metric(t, "jetstream_consumer_unread_ratio", stream); !ok || math.Abs(v-0.25) > 1e-9 {
		t.Errorf("device-state unread ratio = %v (present %v); want 0.25", v, ok)
	}
}

// The sink marker is read from the stream's declaration: 1 for a stream that holds records
// for an operator (failed-events), 0 for one that services read to process (alarm-events),
// on the same service's scrape. The fill alert reads a stream's fill only where it is 1.
func TestTheSinkMarkerFollowsTheDeclaration(t *testing.T) {
	srv := startEmbeddedServer(t)
	svc := newBPService(t, srv, "device-management", bpBounds{})
	svc.writer(t, streams.FailedEvents)
	svc.writer(t, streams.AlarmEvents)
	svc.nmgr.sampleNow(context.Background())
	for suffix, want := range map[string]float64{streams.FailedEvents: 1, streams.AlarmEvents: 0} {
		if v, ok := svc.metric(t, "jetstream_stream_sink", StreamName("test", suffix)); !ok || v != want {
			t.Errorf("jetstream_stream_sink{stream=%s} = %v (present %v); want %v", suffix, v, ok, want)
		}
	}
	// Every sink the platform declares, and nothing else, is one.
	var sinks []string
	for _, s := range streams.All {
		if isSinkStream(s.Suffix) {
			sinks = append(sinks, s.Suffix)
		}
	}
	want := []string{streams.FailedDecode, streams.FailedEvents, streams.ConnectorDispatchDead,
		streams.DeadLetters, streams.MaxDeliveries}
	if len(sinks) != len(want) {
		t.Fatalf("declared sinks = %v; want %v", sinks, want)
	}
	for i := range want {
		if sinks[i] != want[i] {
			t.Errorf("declared sinks = %v; want %v", sinks, want)
			break
		}
	}
}

// The sampler's near-full line: a WARNING for a sink, whose records nobody processes, and
// an INFO for a stream services read, whose fill is normally history they have read.
func TestTheNearFullLineWarnsOnlyForASink(t *testing.T) {
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.TraceLevel)
	t.Cleanup(func() { zerolog.SetGlobalLevel(prev) })
	logs := captureLogs(t)

	srv := startEmbeddedServer(t)
	svc := newBPService(t, srv, "device-management", bpBounds{maxMsgs: 10})
	ctx := core.WithTenant(context.Background(), "acme")
	for _, suffix := range []string{streams.FailedEvents, streams.AlarmEvents} {
		w := svc.writer(t, suffix)
		for i := 0; i < 9; i++ {
			if err := w.WriteMessages(ctx, Message{Value: []byte("m")}); err != nil {
				t.Fatalf("publish to %s: %v", suffix, err)
			}
		}
	}
	svc.nmgr.sampleNow(context.Background())

	if got := levelOf(t, logs.String(), nearFullSinkMsg); got != "warn" {
		t.Errorf("a sink stream near its ceiling logged at %q; want warn", got)
	}
	if got := levelOf(t, logs.String(), nearFullMsg); got != "info" {
		t.Errorf("a read stream near its ceiling logged at %q; want info", got)
	}
}
