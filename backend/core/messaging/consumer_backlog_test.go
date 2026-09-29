// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
	"time"
)

// A reader durable's backlog, read the way a scrape reads it: through the registry the
// service's /metrics serves, from the sampler pass every service runs, against a real
// embedded JetStream server — the numbers are the broker's (NumPending, NumAckPending), and a
// fake would restate them rather than check them.

const (
	pendingSeries     = "jetstream_consumer_pending_messages"
	ackPendingSeries  = "jetstream_consumer_ack_pending_messages"
	unreadRatioSeries = "jetstream_consumer_unread_ratio"
)

// readNoAck hands out n messages without acknowledging any of them.
func (g *unreadRig) readNoAck(n int) {
	g.t.Helper()
	for i := 0; i < n; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := g.reader.ReadMessage(ctx)
		cancel()
		if err != nil {
			g.t.Fatalf("read %d of %d: %v", i+1, n, err)
		}
	}
}

// Before the first sample there is no backlog series at all — not a 0, which would claim
// "nothing waiting" before anything was measured.
func TestConsumerBacklogIsAbsentUntilSampled(t *testing.T) {
	g := newUnreadRig(t, 0)
	g.publish("t1", 10)
	for _, s := range []string{pendingSeries, ackPendingSeries, unreadRatioSeries} {
		if v, found := g.series(s); found {
			t.Errorf("%s is exported as %v before any sample; want it absent", s, v)
		}
	}
	// The unread pair IS created at 0 when the reader is, so the rig can see a series that
	// exists: the absence above is this pair's, not the rig's.
	g.mustSeries(gapSeries)
}

// Messages the durable has not been handed are pending, and nothing is ack-pending.
func TestConsumerBacklogReportsUndelivered(t *testing.T) {
	g := newUnreadRig(t, 0)
	g.publish("t1", 10)
	g.sample()
	if got := g.mustSeries(pendingSeries); got != 10 {
		t.Errorf("pending = %v; want 10", got)
	}
	if got := g.mustSeries(ackPendingSeries); got != 0 {
		t.Errorf("ack pending = %v; want 0", got)
	}
}

// Messages handed out and not acknowledged move from pending to ack-pending. The reader
// fetches in batches, so it may have been handed more than it gave out; the sum and the
// lower bound are what the broker guarantees.
func TestConsumerBacklogReportsHandedOutUnacked(t *testing.T) {
	g := newUnreadRig(t, 0)
	g.publish("t1", 10)
	g.readNoAck(4)
	g.sample()
	pending, ack := g.mustSeries(pendingSeries), g.mustSeries(ackPendingSeries)
	if pending+ack != 10 {
		t.Errorf("pending %v + ack pending %v = %v; want 10", pending, ack, pending+ack)
	}
	if ack < 4 {
		t.Errorf("ack pending = %v; want at least the 4 handed out", ack)
	}
}

// A durable that read and acknowledged everything reads 0 on both: the same series that
// was absent before its first sample CAN read zero once it has been measured.
func TestConsumerBacklogDrainsToZero(t *testing.T) {
	g := newUnreadRig(t, 0)
	g.publish("t1", 10)
	g.sample()
	if got := g.mustSeries(pendingSeries); got != 10 {
		t.Fatalf("pending before the read = %v; want 10", got)
	}
	g.read(10)
	g.sample()
	if got := g.mustSeries(pendingSeries); got != 0 {
		t.Errorf("pending = %v; want 0", got)
	}
	if got := g.mustSeries(ackPendingSeries); got != 0 {
		t.Errorf("ack pending = %v; want 0", got)
	}
}

// A durable that cannot be read is not measured, so its backlog is withdrawn rather than
// left at the last value: a frozen gauge would keep claiming a backlog (or its absence)
// nobody is measuring any more.
func TestConsumerBacklogIsWithdrawnWhenTheSampleFails(t *testing.T) {
	g := newUnreadRig(t, 0)
	g.publish("t1", 10)
	g.sample()
	if got := g.mustSeries(pendingSeries); got != 10 {
		t.Fatalf("pending = %v; want 10 before the durable goes", got)
	}
	if err := g.nmgr.js.DeleteConsumer(g.stream, g.durable); err != nil {
		t.Fatalf("delete consumer: %v", err)
	}
	g.sample()
	for _, s := range []string{pendingSeries, ackPendingSeries, unreadRatioSeries} {
		if v, found := g.series(s); found {
			t.Errorf("%s is still exported as %v for a durable that could not be read; want it absent", s, v)
		}
	}
}

// The same when it is the durable's STREAM that cannot be read: the durable is sampled
// against its stream's StreamInfo, so a failed StreamInfo leaves it unmeasured too, and
// its backlog is withdrawn with the stream's own series rather than frozen at the last pass.
func TestConsumerBacklogIsWithdrawnWhenItsStreamCannotBeRead(t *testing.T) {
	g := newUnreadRig(t, 0)
	g.publish("t1", 10)
	g.sample()
	if got := g.mustSeries(pendingSeries); got != 10 {
		t.Fatalf("pending = %v; want 10 before the stream goes", got)
	}
	if err := g.nmgr.js.DeleteStream(g.stream); err != nil {
		t.Fatalf("delete stream: %v", err)
	}
	g.sample()
	for _, s := range []string{pendingSeries, ackPendingSeries, unreadRatioSeries} {
		if v, found := g.series(s); found {
			t.Errorf("%s is still exported as %v for a durable whose stream could not be read; want it absent", s, v)
		}
	}
}
