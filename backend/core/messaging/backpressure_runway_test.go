// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	nats "github.com/nats-io/nats.go"
)

// These tests pin the runway rule against a real embedded JetStream server: on a full stream,
// DiscardOld removes the history a gating consumer has already read before it reaches the
// messages it has not, and the gate refuses before that history runs out.
//
// 🔴 The byte ceiling is what makes the ratio rule blind here. The broker reports a stream's
// total bytes, not the unread messages' bytes, so the ratio prices every unread message at the
// stream's average size, which at a full stream is the unread share of the message COUNT. A
// burst of large events over a history of small ones fills the stream while that share is
// about a half.

// measuredByHand makes s's gates measure only when the test says so, on clk: no sampling
// loop, no volume-triggered measurement. It must run before the first writer or reader.
func measuredByHand(s *bpService, clk *testClock) {
	g := s.nmgr.backpressure()
	g.now = clk.now
	g.kickShare = 0
	withoutSamplingLoop(s)
}

// inboundReader is device-management's reader on inbound-events, the gating durable these
// tests read through. It forwards into resolved-events and parks while that stream's gate
// cannot be measured, so resolved-events is created first.
func inboundReader(t *testing.T, dm *bpService) *natsReader {
	t.Helper()
	if _, err := dm.nmgr.ensureStream(streams.ResolvedEvents); err != nil {
		t.Fatalf("ensure resolved-events: %v", err)
	}
	return dm.reader(t, streams.InboundEvents)
}

// readAndAck reads n messages from r and acks each.
func readAndAck(t *testing.T, r *natsReader, n int) []Message {
	t.Helper()
	out := make([]Message, 0, n)
	for i := 0; i < n; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		m, err := r.ReadMessage(ctx)
		cancel()
		if err != nil {
			t.Fatalf("read %d of %d: %v", i+1, n, err)
		}
		if err := m.Ack(); err != nil {
			t.Fatalf("ack %d: %v", i+1, err)
		}
		out = append(out, m)
	}
	return out
}

// awaitAckFloor waits until the broker's ack floor for r's durable is want.
func awaitAckFloor(t *testing.T, nmgr *NatsManager, r *natsReader, want uint64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		ci, err := nmgr.js.ConsumerInfo(r.stream, r.durable)
		if err != nil {
			t.Fatalf("consumer info: %v", err)
		}
		if ci.AckFloor.Stream == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the ack floor is %d; want %d", ci.AckFloor.Stream, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func publishOK(t *testing.T, w MessageWriter, ctx context.Context, n int, value []byte) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := w.WriteMessages(ctx, Message{Value: value}); err != nil {
			t.Fatalf("publish %d of %d: %v", i+1, n, err)
		}
	}
}

func seqRetained(t *testing.T, nmgr *NatsManager, suffix string, seq uint64) bool {
	t.Helper()
	_, err := nmgr.js.GetMsg(StreamName("test", suffix), seq)
	if errors.Is(err, nats.ErrMsgNotFound) {
		return false
	}
	if err != nil {
		t.Fatalf("get seq %d: %v", seq, err)
	}
	return true
}

// largeOverSmall is the arithmetic of the two tests below, on the file store's record size
// (22 + subject + 4 + headers + data + 8 bytes): a 16-byte event is a record of about 143
// bytes and an 8 KiB one about 8.3 KiB. On a 256 KiB stream, 1000 small events fill a little
// over half; the 15th large one fills it, and from then each large one discards about 58 of
// the small ones. Before the runway rule the ratio read about 0.5 one publish before the large
// events reached the first one device-management had not read, measured after every publish.
const (
	largeOverSmallHistory  = 1000
	largeOverSmallAttempts = 80
	largeOverSmallMaxBytes = 256 << 10
)

var (
	smallEvent = []byte(strings.Repeat("s", 16))
	largeEvent = []byte(strings.Repeat("L", 8<<10))
)

// device-management has read and acked 1000 small events; then 8 KiB events arrive and it
// reads none of them. Measured after every publish, five seconds apart, the gate must refuse
// before the large events push out the first of them, seq 1001. Before the runway rule it
// never refused: the ratio read about 0.5 at the last publish that fit, and the next one
// evicted seq 1001.
func TestLargeUnreadOverSmallHistoryIsRefusedNotEvicted(t *testing.T) {
	srv := startEmbeddedServer(t)
	bounds := bpBounds{maxMsgs: 1_000_000, maxBytes: largeOverSmallMaxBytes}
	dm := newBPService(t, srv, "device-management", bounds)
	r := inboundReader(t, dm)
	es := newBPService(t, srv, "event-sources", bounds)
	clk := newTestClock()
	measuredByHand(es, clk)
	w := es.writer(t, streams.InboundEvents)

	publishOK(t, w, tenantCtx(), largeOverSmallHistory, smallEvent)
	readAndAck(t, r, largeOverSmallHistory)
	awaitAckFloor(t, dm.nmgr, r, largeOverSmallHistory)
	es.sample(t, streams.InboundEvents)
	if err := es.nmgr.Backpressure(streams.InboundEvents); err != nil {
		t.Fatalf("precondition: with every event read the gate refuses: %v", err)
	}

	stream := StreamName("test", streams.InboundEvents)
	refused, first, ratioAtFirst := 0, error(nil), -1.0
	for i := 0; i < largeOverSmallAttempts; i++ {
		if err := w.WriteMessages(tenantCtx(), Message{Value: largeEvent}); err != nil {
			if !errors.Is(err, ErrStreamBackpressure) {
				t.Fatalf("large publish %d failed for a reason that is not backpressure: %v", i+1, err)
			}
			refused++
			if first == nil {
				first = err
				ratioAtFirst, _ = es.metric(t, "jetstream_backpressure_unread_ratio", stream)
			}
		}
		clk.advance(5 * time.Second)
		es.sample(t, streams.InboundEvents)
	}

	st := streamState(t, es.nmgr, streams.InboundEvents)
	if st.FirstSeq <= 1 {
		t.Fatalf("the stream never reached its ceiling (first seq %d, %d bytes): the fixture discarded nothing", st.FirstSeq, st.Bytes)
	}
	if !seqRetained(t, es.nmgr, streams.InboundEvents, largeOverSmallHistory+1) {
		t.Fatalf("seq %d, the first event device-management had not read, was evicted (stream %d..%d, %d bytes, "+
			"%d of %d large publishes refused): large unread events pushed it out of a full stream",
			largeOverSmallHistory+1, st.FirstSeq, st.LastSeq, st.Bytes, refused, largeOverSmallAttempts)
	}
	if refused == 0 {
		t.Fatalf("no publish was refused (stream %d..%d)", st.FirstSeq, st.LastSeq)
	}
	t.Logf("stream %d..%d; %d of %d large publishes refused, the first with the unread ratio at %.2f: %v",
		st.FirstSeq, st.LastSeq, refused, largeOverSmallAttempts, ratioAtFirst, first)
	var bpe *BackpressureError
	if !errors.As(first, &bpe) || !bpe.Runway || bpe.Durable != DurableName("test", "device-management", streams.InboundEvents) {
		t.Fatalf("the first refusal = %#v; want the runway rule naming device-management's durable", first)
	}
	if bpe.History == 0 || bpe.Rate <= 0 {
		t.Fatalf("the runway refusal carries history %d and rate %v; want the read history left and a positive rate",
			bpe.History, bpe.Rate)
	}
	// It is the runway rule that closed it, well before the ratio would have.
	if ratioAtFirst < 0 || ratioAtFirst >= backpressureCloseRatio {
		t.Fatalf("the unread ratio was %v when the gate closed; want it measured and under %v", ratioAtFirst, backpressureCloseRatio)
	}
	if v, ok := es.metric(t, "jetstream_backpressure_history_runway_seconds", stream); !ok || v >= backpressureRunwayOpen.Seconds() {
		t.Fatalf("jetstream_backpressure_history_runway_seconds = %v (present %v) while refusing; want it under %v",
			v, ok, backpressureRunwayOpen.Seconds())
	}
}

// NEGATIVE CONTROL for the test above: the identical sequence on a stream with no
// backpressure loses the first unread event. Without it, a fixture that never reached its
// ceiling, or whose large events never reached the unread ones, would pass the test above for
// the wrong reason. alarm-events' subject is two characters shorter than inbound-events', a
// rounding error against 8 KiB records.
func TestLargeUnreadOverSmallHistoryWithoutBackpressureStillEvicts(t *testing.T) {
	srv := startEmbeddedServer(t)
	bounds := bpBounds{maxMsgs: 1_000_000, maxBytes: largeOverSmallMaxBytes}
	nm := newBPService(t, srv, "notification-management", bounds)
	r := nm.reader(t, streams.AlarmEvents)
	dm := newBPService(t, srv, "device-management", bounds)
	w := dm.writer(t, streams.AlarmEvents)

	publishOK(t, w, tenantCtx(), largeOverSmallHistory, smallEvent)
	readAndAck(t, r, largeOverSmallHistory)
	awaitAckFloor(t, nm.nmgr, r, largeOverSmallHistory)
	publishOK(t, w, tenantCtx(), largeOverSmallAttempts, largeEvent)

	if seqRetained(t, dm.nmgr, streams.AlarmEvents, largeOverSmallHistory+1) {
		st := streamState(t, dm.nmgr, streams.AlarmEvents)
		t.Fatalf("the control kept seq %d (stream %d..%d, %d bytes): its large events never reached the unread ones",
			largeOverSmallHistory+1, st.FirstSeq, st.LastSeq, st.Bytes)
	}
}

// The same burst with nothing measured by hand: the background loop's 5 s tick alone, before
// this change, never saw the stream between "far from 90%" and "discarding unread events".
// The writer's own volume now brings the measurement forward (at most every 100 ms), so the
// gate closes inside the window the history buys.
//
// Margin: on a 1 MiB stream, 4000 small events leave room for about 57 large ones (about 1.1
// s at one every 20 ms), and the history then lasts about 69 more (about 1.4 s). With a
// measurement at most 100 ms apart that is about 14 chances to see the discarding; the 5 s
// tick lands in that window less than a third of the time, and a tick there reads the ratio
// well under 0.9 for all but the last 20 ms of it.
func TestABurstBetweenTimedMeasurementsIsRefusedNotEvicted(t *testing.T) {
	const history = 4000
	srv := startEmbeddedServer(t)
	bounds := bpBounds{maxMsgs: 1_000_000, maxBytes: 1 << 20}
	dm := newBPService(t, srv, "device-management", bounds)
	r := inboundReader(t, dm)
	es := newBPService(t, srv, "event-sources", bounds)
	w := es.writer(t, streams.InboundEvents)

	publishOK(t, w, tenantCtx(), history, smallEvent)
	readAndAck(t, r, history)
	awaitAckFloor(t, dm.nmgr, r, history)
	es.sample(t, streams.InboundEvents)

	refused, first := 0, error(nil)
	for i := 0; i < 200; i++ {
		if err := w.WriteMessages(tenantCtx(), Message{Value: largeEvent}); err != nil {
			if !errors.Is(err, ErrStreamBackpressure) {
				t.Fatalf("large publish %d failed for a reason that is not backpressure: %v", i+1, err)
			}
			refused++
			if first == nil {
				first = err
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	st := streamState(t, es.nmgr, streams.InboundEvents)
	if st.FirstSeq <= 1 {
		t.Fatalf("the stream never reached its ceiling (first seq %d)", st.FirstSeq)
	}
	if !seqRetained(t, es.nmgr, streams.InboundEvents, history+1) {
		t.Fatalf("seq %d, the first event device-management had not read, was evicted (stream %d..%d, %d of 200 "+
			"refused): the burst outran the gate's measurements", history+1, st.FirstSeq, st.LastSeq, refused)
	}
	var bpe *BackpressureError
	if !errors.As(first, &bpe) || !bpe.Runway {
		t.Fatalf("the first refusal = %#v; want the runway rule", first)
	}
}

// A full stream whose reader keeps pace is never refused, though the reader is always a few
// messages behind, as it is under any load. Its history is constant: every message the stream
// discards is replaced by one the reader has finished with.
//
// This is the runway rule's liveness. A rule that took the discard rate from FirstSeq alone
// would read one message discarded per 100 ms here, so 10 a second, against about 190 messages
// of history, under 30 s of it: it would refuse a healthy stream.
func TestAFullStreamWhoseReaderKeepsUpIsNotRefused(t *testing.T) {
	const behind = 5
	srv := startEmbeddedServer(t)
	bounds := bpBounds{maxMsgs: 1_000_000, maxBytes: 64 << 10}
	dm := newBPService(t, srv, "device-management", bounds)
	r := inboundReader(t, dm)
	es := newBPService(t, srv, "event-sources", bounds)
	clk := newTestClock()
	measuredByHand(es, clk)
	w := es.writer(t, streams.InboundEvents)
	event := []byte(strings.Repeat("e", 200))
	stream := StreamName("test", streams.InboundEvents)

	// Fill past the ceiling with history, leaving the last few unacked.
	var held []Message
	step := func(i int) error {
		err := w.WriteMessages(tenantCtx(), Message{Value: event})
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		m, rerr := r.ReadMessage(ctx)
		cancel()
		if rerr != nil {
			t.Fatalf("read at step %d: %v", i, rerr)
		}
		held = append(held, m)
		if len(held) > behind {
			if err := held[0].Ack(); err != nil {
				t.Fatalf("ack: %v", err)
			}
			held = held[1:]
		}
		return nil
	}
	settle := func() {
		if st := streamState(t, es.nmgr, streams.InboundEvents); st.LastSeq > behind {
			awaitAckFloor(t, dm.nmgr, r, st.LastSeq-behind)
		}
		clk.advance(100 * time.Millisecond)
		es.sample(t, streams.InboundEvents)
	}
	for i := 0; i < 250; i++ {
		if err := step(i); err != nil {
			t.Fatalf("filling, publish %d: %v", i+1, err)
		}
		settle()
	}
	if st := streamState(t, es.nmgr, streams.InboundEvents); st.FirstSeq <= 1 {
		t.Fatalf("precondition: the stream is not discarding yet (first seq %d)", st.FirstSeq)
	}

	refused := 0
	for i := 0; i < 200; i++ {
		if err := step(i); err != nil {
			if !errors.Is(err, ErrStreamBackpressure) {
				t.Fatalf("publish %d: %v", i+1, err)
			}
			refused++
		}
		settle()
	}
	if refused != 0 {
		t.Fatalf("%d of 200 publishes to a full stream whose reader keeps up were refused", refused)
	}
	v, ok := es.metric(t, "jetstream_backpressure_history_runway_seconds", stream)
	if !ok || !(math.IsInf(v, 1) || v >= backpressureRunwayClose.Seconds()) {
		t.Fatalf("jetstream_backpressure_history_runway_seconds = %v (present %v); want +Inf or at least %v",
			v, ok, backpressureRunwayClose.Seconds())
	}
}

// Deleting a tenant purges its messages from the middle of the stream: the history the reader
// had read falls in one step, and the stream has more room, not less. Neither a stream below
// its ceiling, nor a full one losing a small share, may read that as a discard rate and refuse
// every other tenant. (A tenant whose messages are the oldest a full stream holds is the case
// that can: see nextDrain.)
func TestATenantPurgeDoesNotCloseTheGate(t *testing.T) {
	for _, tc := range []struct {
		name string
		// msgs are published, the first read of them read and acked.
		msgs, read int
		// purged says which of them belong to the tenant that is deleted.
		purged func(i int) bool
	}{
		// 600 of 1000 messages: well below the ceiling. Half the stream is purged, seq 1
		// with it, so FirstSeq moves too.
		{name: "below the ceiling", msgs: 600, read: 500, purged: func(i int) bool { return i%2 == 0 }},
		// 1000 of 1000: at the ceiling, and half of it is purged.
		{name: "half of a full stream", msgs: 1000, read: 900, purged: func(i int) bool { return i%2 == 0 }},
		// 1000 of 1000: at the ceiling before and after. One message in 40 is purged, from the
		// middle: seq 1 is kept, so FirstSeq does not move.
		{name: "a small share of a full stream", msgs: 1000, read: 900, purged: func(i int) bool { return i%40 == 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := startEmbeddedServer(t)
			bounds := bpBounds{maxMsgs: 1000}
			dm := newBPService(t, srv, "device-management", bounds)
			r := inboundReader(t, dm)
			es := newBPService(t, srv, "event-sources", bounds)
			clk := newTestClock()
			measuredByHand(es, clk)
			w := es.writer(t, streams.InboundEvents)
			gone := core.WithTenant(context.Background(), "gone")
			for i := 0; i < tc.msgs; i++ {
				ctx := tenantCtx()
				if tc.purged(i) {
					ctx = gone
				}
				if err := w.WriteMessages(ctx, Message{Value: []byte("m")}); err != nil {
					t.Fatalf("publish %d: %v", i+1, err)
				}
			}
			readAndAck(t, r, tc.read)
			awaitAckFloor(t, dm.nmgr, r, uint64(tc.read))
			es.sample(t, streams.InboundEvents)
			if err := es.nmgr.Backpressure(streams.InboundEvents); err != nil {
				t.Fatalf("precondition: the gate refuses before the purge: %v", err)
			}

			res, err := PurgeTenant(context.Background(), es.nmgr.nc, "test", "gone")
			if err != nil {
				t.Fatalf("purge: %v", err)
			}
			if res.Messages == 0 {
				t.Fatal("the purge removed nothing")
			}
			// Unsettled: the wait's count assumes nothing was deleted from the middle of the
			// stream, which is exactly what a purge does. Nothing is being published, so there
			// is nothing for it to wait for; the sample is checked to have succeeded instead.
			clk.advance(100 * time.Millisecond)
			es.sampleUnsettled(streams.InboundEvents)
			g := es.nmgr.backpressure()
			g.mu.Lock()
			sampled := g.gates[streams.InboundEvents].sampledAt
			g.mu.Unlock()
			if !sampled.Equal(clk.now()) {
				t.Fatalf("the sample after the purge did not succeed (last success %v)", sampled)
			}
			if err := es.nmgr.Backpressure(streams.InboundEvents); err != nil {
				t.Fatalf("after purging %d messages the gate refuses: %v", res.Messages, err)
			}
			stream := StreamName("test", streams.InboundEvents)
			if v, ok := es.metric(t, "jetstream_backpressure_history_runway_seconds", stream); !ok || !math.IsInf(v, 1) {
				t.Fatalf("jetstream_backpressure_history_runway_seconds = %v (present %v) after the purge; want +Inf: "+
					"nothing is being discarded", v, ok)
			}
		})
	}
}

// A sample that finishes after a later-started one has been applied is discarded whole: its
// reading is older. Volume-triggered measurement makes overlapping samples routine.
func TestAnOlderSampleDoesNotOverwriteANewerOne(t *testing.T) {
	srv := startEmbeddedServer(t)
	bounds := bpBounds{maxMsgs: 20}
	dm := newBPService(t, srv, "device-management", bounds)
	dm.reader(t, streams.InboundEvents)
	es := newBPService(t, srv, "event-sources", bounds)
	clk := newTestClock()
	measuredByHand(es, clk)
	g := es.nmgr.backpressure()
	answer := g.consumerInfo
	var stall atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	g.consumerInfo = func(ctx context.Context, stream, durable string) (*nats.ConsumerInfo, error) {
		ci, err := answer(ctx, stream, durable)
		if err != nil || !stall.CompareAndSwap(true, false) {
			return ci, err
		}
		// The stalled sample read a full backlog a while ago.
		close(entered)
		<-release
		old := *ci
		old.NumPending = 19
		return &old, nil
	}
	es.writer(t, streams.InboundEvents)

	stall.Store(true)
	done := make(chan struct{})
	go func() {
		defer close(done)
		es.sampleUnsettled(streams.InboundEvents)
	}()
	<-entered
	clk.advance(time.Second)
	es.sample(t, streams.InboundEvents) // the newer sample: nothing unread
	close(release)
	<-done
	if err := es.nmgr.Backpressure(streams.InboundEvents); err != nil {
		t.Fatalf("an older sample that finished last closed the gate over a newer one: %v", err)
	}
}

// The writer's own volume asks the loop for a measurement, and the loop spaces them at least
// 100 ms apart without dropping the last one asked for.
func TestPublishingVolumeTriggersAMeasurement(t *testing.T) {
	srv := startEmbeddedServer(t)
	bounds := bpBounds{maxMsgs: 1_000_000, maxBytes: 64 << 10} // a 64-byte threshold
	es := newBPService(t, srv, "event-sources", bounds)
	g := es.nmgr.backpressure()
	g.tick = time.Hour // only volume measures
	w := es.writer(t, streams.InboundEvents)
	g.mu.Lock()
	gs := g.gates[streams.InboundEvents]
	g.mu.Unlock()
	started := func() uint64 {
		g.mu.Lock()
		defer g.mu.Unlock()
		return gs.started
	}
	value := []byte(strings.Repeat("v", 100))

	base := started()
	time.Sleep(300 * time.Millisecond)
	if n := started(); n != base {
		t.Fatalf("%d measurements with nothing published and an hour-long tick", n-base)
	}

	publishOK(t, w, tenantCtx(), 1, value)
	deadline := time.Now().Add(2 * time.Second)
	for started() == base {
		if time.Now().After(deadline) {
			t.Fatal("a publish over the volume threshold was not measured within 2 s")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// A paced burst: 30 publishes 10 ms apart, each over the threshold. At most one
	// measurement per 100 ms, plus one in flight at each end.
	time.Sleep(200 * time.Millisecond)
	before := started()
	begun := time.Now()
	for i := 0; i < 30; i++ {
		publishOK(t, w, tenantCtx(), 1, value)
		time.Sleep(10 * time.Millisecond)
	}
	// The last publish's volume is measured: a request inside the gap waits it out rather
	// than being dropped, which would leave it for the hour-long tick.
	deadline = time.Now().Add(time.Second)
	for gs.pubBytes.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the burst's last publish was never measured: %d bytes still uncounted", gs.pubBytes.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	took := time.Since(begun)
	n := started() - before
	most := uint64(math.Ceil(float64(took)/float64(backpressureKickMinGap))) + 2
	if n < 2 || n > most {
		t.Fatalf("a burst measured over %s made %d measurements; want between 2 and %d (one per %s)",
			took, n, most, backpressureKickMinGap)
	}
}

// A gate the runway rule closed stays closed while its reader does not move: the remembered
// discard rate does not fade while the gate refuses. If it faded as it does while the gate is
// open (a 60 s e-folding time), the history, which a stalled reader leaves constant, would
// outlast the faded rate within about a minute; the gate would reopen, admit a measurement's
// worth of large events, close again, and spend the history a step at a time until it reached
// the unread events: the defect, slowed down.
func TestARunwayRefusalHoldsWhileTheReaderDoesNotMove(t *testing.T) {
	srv := startEmbeddedServer(t)
	bounds := bpBounds{maxMsgs: 1_000_000, maxBytes: largeOverSmallMaxBytes}
	dm := newBPService(t, srv, "device-management", bounds)
	r := inboundReader(t, dm)
	es := newBPService(t, srv, "event-sources", bounds)
	clk := newTestClock()
	measuredByHand(es, clk)
	w := es.writer(t, streams.InboundEvents)

	publishOK(t, w, tenantCtx(), largeOverSmallHistory, smallEvent)
	readAndAck(t, r, largeOverSmallHistory)
	awaitAckFloor(t, dm.nmgr, r, largeOverSmallHistory)
	es.sample(t, streams.InboundEvents)

	var bpe *BackpressureError
	for i := 0; i < largeOverSmallAttempts && bpe == nil; i++ {
		if err := w.WriteMessages(tenantCtx(), Message{Value: largeEvent}); err != nil {
			if !errors.As(err, &bpe) {
				t.Fatalf("large publish %d failed for a reason that is not backpressure: %v", i+1, err)
			}
			break
		}
		clk.advance(5 * time.Second)
		es.sample(t, streams.InboundEvents)
	}
	if bpe == nil || !bpe.Runway || bpe.History == 0 || bpe.Rate <= 0 {
		t.Fatalf("precondition: the runway rule did not close the gate with history left: %#v", bpe)
	}
	// How long a fading rate would take to reopen it (history >= rate * 60 s): the hold below
	// must outlast that by a margin, or it cannot tell a frozen rate from a fading one.
	reopen := time.Duration(backpressureDrainMemory.Seconds()*
		math.Log(bpe.Rate*backpressureRunwayOpen.Seconds()/float64(bpe.History))) * time.Second
	const hold = 10 * time.Minute
	if reopen <= 0 || 2*reopen > hold {
		t.Fatalf("precondition: a fading rate would reopen the gate after %s (history %d, rate %.1f/s); "+
			"the %s hold cannot tell it from a frozen one", reopen, bpe.History, bpe.Rate, hold)
	}

	for held := time.Duration(0); held < hold; held += 5 * time.Second {
		clk.advance(5 * time.Second)
		es.sample(t, streams.InboundEvents)
		err := es.nmgr.Backpressure(streams.InboundEvents)
		var now *BackpressureError
		if !errors.As(err, &now) || !now.Runway {
			t.Fatalf("%s after the runway rule closed the gate, with the reader stalled, it reads %v; "+
				"want it still refusing on the runway rule (a fading rate reopens it after %s)", held+5*time.Second, err, reopen)
		}
	}
}

// What a writer publishes while a sample is in flight counts toward the NEXT measurement: the
// volume is reset when a sample starts, not when it finishes. Reset at the finish, a burst
// written during a slow measurement would be forgotten, and the next volume-triggered
// measurement would come that much late.
func TestPublishingDuringASampleCountsTowardTheNext(t *testing.T) {
	srv := startEmbeddedServer(t)
	bounds := bpBounds{maxMsgs: 1_000_000, maxBytes: 64 << 20}
	dm := newBPService(t, srv, "device-management", bounds)
	inboundReader(t, dm)
	es := newBPService(t, srv, "event-sources", bounds)
	withoutSamplingLoop(es) // volume is counted; nothing consumes the requests it makes
	g := es.nmgr.backpressure()
	answer := g.consumerInfo
	var stall atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	g.consumerInfo = func(ctx context.Context, stream, durable string) (*nats.ConsumerInfo, error) {
		if stall.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		return answer(ctx, stream, durable)
	}
	w := es.writer(t, streams.InboundEvents)
	g.mu.Lock()
	gs := g.gates[streams.InboundEvents]
	g.mu.Unlock()
	if gs.pubBytes.Load() != 0 || gs.pubMsgs.Load() != 0 {
		t.Fatalf("precondition: %d bytes, %d messages counted before anything was published",
			gs.pubBytes.Load(), gs.pubMsgs.Load())
	}

	stall.Store(true)
	done := make(chan struct{})
	go func() {
		defer close(done)
		es.sampleUnsettled(streams.InboundEvents)
	}()
	<-entered
	publishOK(t, w, tenantCtx(), 3, smallEvent)
	close(release)
	<-done
	if gs.pubMsgs.Load() != 3 || gs.pubBytes.Load() <= int64(3*len(smallEvent)) {
		t.Fatalf("after 3 publishes during a sample, %d messages and %d bytes are counted toward the next one; "+
			"want 3 messages and more than %d bytes", gs.pubMsgs.Load(), gs.pubBytes.Load(), 3*len(smallEvent))
	}
}

// Every writer into a gated stream asks for a measurement by its volume: the plain writer, and
// the ordered writer a forwarding hop publishes with, which is the only writer into
// resolved-events (device-management's) and one of event-sources' into inbound-events. The
// volume is the message as sent, headers included: here a one-byte event's subject and data
// stay under the threshold and its headers take it over.
func TestEveryWriterAsksForAMeasurementByVolume(t *testing.T) {
	for _, tc := range []struct {
		name    string
		publish func(t *testing.T, es *bpService)
	}{
		{name: "writer", publish: func(t *testing.T, es *bpService) {
			publishOK(t, es.writer(t, streams.InboundEvents), tenantCtx(), 1, []byte("x"))
		}},
		{name: "ordered writer", publish: func(t *testing.T, es *bpService) {
			ow, err := es.nmgr.NewOrderedWriter(streams.InboundEvents, 4)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			ow.Publish(tenantCtx(), Message{Value: []byte("x")}, func(err error) { done <- err })
			if err := <-done; err != nil {
				t.Fatalf("ordered publish: %v", err)
			}
			ow.Close()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := startEmbeddedServer(t)
			bounds := bpBounds{maxMsgs: 1_000_000, maxBytes: 64 << 10} // a 64-byte threshold
			es := newBPService(t, srv, "event-sources", bounds)
			g := es.nmgr.backpressure()
			g.tick = time.Hour                  // only volume measures
			es.writer(t, streams.InboundEvents) // registers the gate
			g.mu.Lock()
			gs := g.gates[streams.InboundEvents]
			base := gs.started
			g.mu.Unlock()

			subject, err := es.nmgr.tenantSubject(tenantCtx(), streams.InboundEvents, "")
			if err != nil {
				t.Fatal(err)
			}
			threshold := gs.kickBytes.Load()
			if bare, sent := int64(len(subject)+1), int64(natsMsg(subject, Message{Value: []byte("x")}).Size()); bare >= threshold || sent < threshold {
				t.Fatalf("precondition: subject and data are %d bytes and the message as sent %d, against a %d-byte "+
					"threshold; want the headers to be what crosses it", bare, sent, threshold)
			}

			tc.publish(t, es)
			deadline := time.Now().Add(2 * time.Second)
			for {
				g.mu.Lock()
				n := gs.started
				g.mu.Unlock()
				if n != base && gs.pubBytes.Load() == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("a publish over the volume threshold was not measured within 2 s "+
						"(%d measurements started, %d bytes uncounted)", n-base, gs.pubBytes.Load())
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}
