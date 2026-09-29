// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
)

// These tests pin what a full ingest stream does with an event its reader has not read yet:
// it keeps it, and refuses the next one. Before, the stream discarded its oldest message at
// the ceiling whatever the reader had read, so under a backlog larger than the stream the
// first events a device had been told were accepted were the ones that went.
//
// 🔴 They run against a real embedded JetStream server, because what is being measured is
// the broker's: which sequence DiscardOld removes, what ConsumerInfo reports as pending, and
// how the stream's byte accounting (subject, headers, overhead) compares to its ceiling.

// bpService is one service's manager on a shared embedded server: built by NewNatsManager
// and initialised, not started. Its gates' sampling loop runs every few seconds in the
// background, reading the same broker state; the tests measure explicitly, after each
// publish, so what they assert does not depend on when the loop ticks.
type bpService struct {
	nmgr *NatsManager
	reg  *prometheus.Registry
}

type bpBounds struct {
	maxMsgs  int64
	maxBytes int64
}

func newBPService(t *testing.T, srv *natsserver.Server, area string, b bpBounds) *bpService {
	t.Helper()
	ms := testMicroservice(t, srv, area)
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	if b.maxMsgs > 0 {
		ms.InstanceConfiguration.Infrastructure.Nats.StreamMaxMsgs = b.maxMsgs
	}
	if b.maxBytes > 0 {
		ms.InstanceConfiguration.Infrastructure.Nats.StreamMaxBytes = b.maxBytes
		ms.InstanceConfiguration.Infrastructure.Nats.StreamMaxBytesCold = b.maxBytes
	}
	nmgr := NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*NatsManager) error { return nil })
	if err := nmgr.Initialize(context.Background()); err != nil {
		t.Fatalf("initialize %s: %v", area, err)
	}
	t.Cleanup(func() { nmgr.closeConn() })
	return &bpService{nmgr: nmgr, reg: reg}
}

func (s *bpService) writer(t *testing.T, suffix string) MessageWriter {
	t.Helper()
	w, err := s.nmgr.NewWriter(suffix)
	if err != nil {
		t.Fatalf("NewWriter(%s): %v", suffix, err)
	}
	return w
}

func (s *bpService) reader(t *testing.T, suffix string) *natsReader {
	t.Helper()
	r, err := s.nmgr.NewReader(suffix)
	if err != nil {
		t.Fatalf("NewReader(%s): %v", suffix, err)
	}
	return r.(*natsReader)
}

func (s *bpService) sample(suffix string) {
	s.nmgr.sampleBackpressure(context.Background(), suffix)
}

// metric reads one series by name and stream label from the registry /metrics serves.
func (s *bpService) metric(t *testing.T, name, stream string) (float64, bool) {
	t.Helper()
	families, err := s.reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	want := "devicechain_" + strings.ReplaceAll(s.nmgr.Microservice.FunctionalArea, "-", "") + "_" + name
	for _, f := range families {
		if f.GetName() != want {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "stream" && lp.GetValue() == stream {
					if c := m.GetCounter(); c != nil {
						return c.GetValue(), true
					}
					return m.GetGauge().GetValue(), true
				}
			}
		}
	}
	return 0, false
}

func tenantCtx() context.Context { return core.WithTenant(context.Background(), "acme") }

// fill publishes one message at a time, measuring the gate after each as the background
// sampler would, until attempts run out. It returns how many were refused and the error
// of the first refusal. Every error that is not a refusal fails the test.
func fill(t *testing.T, svc *bpService, w MessageWriter, suffix string, attempts int, value []byte) (refused int, first error) {
	t.Helper()
	for i := 0; i < attempts; i++ {
		err := w.WriteMessages(tenantCtx(), Message{Value: value})
		if err != nil {
			if !errors.Is(err, ErrStreamBackpressure) {
				t.Fatalf("publish %d failed for a reason that is not backpressure: %v", i+1, err)
			}
			refused++
			if first == nil {
				first = err
			}
		}
		svc.sample(suffix)
	}
	return refused, first
}

func streamState(t *testing.T, nmgr *NatsManager, suffix string) nats.StreamState {
	t.Helper()
	info, err := nmgr.js.StreamInfo(StreamName("test", suffix))
	if err != nil {
		t.Fatalf("stream info: %v", err)
	}
	return info.State
}

// seqOneRetained reports whether the stream still holds its FIRST message: the one DiscardOld
// takes first, and the one a reader that has read nothing has not read.
func seqOneRetained(t *testing.T, nmgr *NatsManager, suffix string) bool {
	t.Helper()
	_, err := nmgr.js.GetMsg(StreamName("test", suffix), 1)
	if errors.Is(err, nats.ErrMsgNotFound) {
		return false
	}
	if err != nil {
		t.Fatalf("get seq 1: %v", err)
	}
	return true
}

// device-management's durable on inbound-events has read nothing, and a producer keeps
// publishing. The stream holds 20 messages. Before this change every publish succeeded and
// the stream discarded seq 1 at the 21st, an event nothing had resolved. Now the producer is
// refused once the unread backlog reaches 90% of the ceiling, and seq 1 is still there.
func TestUnreadInboundEventsAreRefusedNotEvicted(t *testing.T) {
	srv := startEmbeddedServer(t)
	bounds := bpBounds{maxMsgs: 20}
	dm := newBPService(t, srv, "device-management", bounds)
	dm.reader(t, streams.InboundEvents) // the gating durable; never reads
	es := newBPService(t, srv, "event-sources", bounds)
	w := es.writer(t, streams.InboundEvents)

	const attempts = 40
	refused, first := fill(t, es, w, streams.InboundEvents, attempts, []byte("m"))

	st := streamState(t, es.nmgr, streams.InboundEvents)
	if !seqOneRetained(t, es.nmgr, streams.InboundEvents) {
		t.Fatalf("seq 1 was evicted before device-management read it (stream first=%d last=%d): the full "+
			"stream discarded an unread event instead of refusing a new one", st.FirstSeq, st.LastSeq)
	}
	// The gate closes at 18 unread of 20; every later attempt is refused.
	if st.LastSeq != 18 {
		t.Fatalf("the stream's last sequence is %d; want 18, where the unread backlog reached 90%% of the "+
			"20-message ceiling and the gate closed", st.LastSeq)
	}
	if refused != attempts-18 {
		t.Fatalf("%d of %d publishes were refused; want %d", refused, attempts, attempts-18)
	}
	var bpe *BackpressureError
	if !errors.As(first, &bpe) || bpe.Durable != DurableName("test", "device-management", streams.InboundEvents) {
		t.Fatalf("the refusal = %v; want one naming device-management's durable", first)
	}
	if v, ok := es.metric(t, "jetstream_publish_refused_total", StreamName("test", streams.InboundEvents)); !ok || v != float64(refused) {
		t.Fatalf("jetstream_publish_refused_total = %v (present %v); want %d", v, ok, refused)
	}
	if v, ok := es.metric(t, "jetstream_backpressure_engaged", StreamName("test", streams.InboundEvents)); !ok || v != 1 {
		t.Fatalf("jetstream_backpressure_engaged = %v (present %v) while refusing; want 1", v, ok)
	}
}

// The same on the BYTE ceiling, which is the one that binds in production (1 GiB at a few
// hundred bytes a message is well under the 5M message ceiling), against the broker's own
// byte accounting rather than a message count.
func TestUnreadInboundEventsAreRefusedNotEvictedOnTheByteCeiling(t *testing.T) {
	srv := startEmbeddedServer(t)
	bounds := bpBounds{maxMsgs: 1_000_000, maxBytes: 16 << 10}
	dm := newBPService(t, srv, "device-management", bounds)
	dm.reader(t, streams.InboundEvents)
	es := newBPService(t, srv, "event-sources", bounds)
	w := es.writer(t, streams.InboundEvents)

	const attempts = 400
	refused, _ := fill(t, es, w, streams.InboundEvents, attempts, []byte(strings.Repeat("x", 200)))

	st := streamState(t, es.nmgr, streams.InboundEvents)
	if !seqOneRetained(t, es.nmgr, streams.InboundEvents) {
		t.Fatalf("seq 1 was evicted on the byte ceiling (stream first=%d last=%d, %d bytes)", st.FirstSeq, st.LastSeq, st.Bytes)
	}
	if refused == 0 || st.LastSeq >= attempts {
		t.Fatalf("no publish was refused (last seq %d of %d attempts, %d bytes held)", st.LastSeq, attempts, st.Bytes)
	}
	if st.Bytes > uint64(bounds.maxBytes) {
		t.Fatalf("the stream holds %d bytes, over its %d-byte ceiling", st.Bytes, bounds.maxBytes)
	}
}

// NEGATIVE CONTROL for the two tests above: the same fixture on a stream that applies no
// backpressure still fills and still evicts seq 1. Without it, a fixture that never filled
// would pass the tests above for the wrong reason.
func TestAStreamWithoutBackpressureStillEvictsUnread(t *testing.T) {
	srv := startEmbeddedServer(t)
	bounds := bpBounds{maxMsgs: 20}
	nm := newBPService(t, srv, "notification-management", bounds)
	nm.reader(t, streams.AlarmEvents) // never reads
	dm := newBPService(t, srv, "device-management", bounds)
	w := dm.writer(t, streams.AlarmEvents)

	refused, _ := fill(t, dm, w, streams.AlarmEvents, 40, []byte("m"))
	if refused != 0 {
		t.Fatalf("%d publishes to a stream with no backpressure were refused", refused)
	}
	if seqOneRetained(t, dm.nmgr, streams.AlarmEvents) {
		t.Fatal("the control did not evict seq 1: the fixture never filled the stream")
	}
	if st := streamState(t, dm.nmgr, streams.AlarmEvents); st.LastSeq != 40 {
		t.Fatalf("last seq = %d; want 40", st.LastSeq)
	}
}

// Only the declared readers gate. device-state and event-processing read resolved-events
// too, but a backlog of theirs is a stale projection or a checkpoint that replays, and
// letting either refuse every tenant's events would turn a slow projection into an outage.
// event-management's backlog is the one that loses persisted events.
func TestOnlyTheDeclaredReaderGatesResolvedEvents(t *testing.T) {
	srv := startEmbeddedServer(t)
	bounds := bpBounds{maxMsgs: 20}
	ds := newBPService(t, srv, "device-state", bounds)
	ds.reader(t, streams.ResolvedEvents) // never reads
	ep := newBPService(t, srv, "event-processing", bounds)
	ep.reader(t, streams.ResolvedEvents) // never reads
	dm := newBPService(t, srv, "device-management", bounds)
	w := dm.writer(t, streams.ResolvedEvents)

	if refused, _ := fill(t, dm, w, streams.ResolvedEvents, 19, []byte("m")); refused != 0 {
		t.Fatalf("device-state's and event-processing's backlogs refused %d publishes; neither gates", refused)
	}

	em := newBPService(t, srv, "event-management", bounds)
	em.reader(t, streams.ResolvedEvents) // DeliverAll: all 19 are unread to it
	dm.sample(streams.ResolvedEvents)
	err := w.WriteMessages(tenantCtx(), Message{Value: []byte("m")})
	var bpe *BackpressureError
	if !errors.As(err, &bpe) || bpe.Durable != DurableName("test", "event-management", streams.ResolvedEvents) {
		t.Fatalf("with event-management 19 of 20 behind, the publish answered %v; want a refusal naming it", err)
	}
}

// A durable left behind by an area the instance no longer deploys (a profile narrowed from
// default to ingest-only keeps event-management's durable, which nothing reads again) must
// not close the gate. The deployed set comes from the per-area configuration mount; when it
// cannot be read, or is not the mount it claims to be, every declared reader counts.
func TestADurableOfAnAreaNotDeployedDoesNotGate(t *testing.T) {
	srv := startEmbeddedServer(t)
	bounds := bpBounds{maxMsgs: 20}
	em := newBPService(t, srv, "event-management", bounds)
	em.reader(t, streams.ResolvedEvents) // orphaned: never reads
	dm := newBPService(t, srv, "device-management", bounds)
	g := dm.nmgr.backpressure()
	g.enabledAreas = func() ([]string, error) {
		return []string{"device-management", "event-sources", "user-management"}, nil
	}
	w := dm.writer(t, streams.ResolvedEvents)
	if refused, _ := fill(t, dm, w, streams.ResolvedEvents, 19, []byte("m")); refused != 0 {
		t.Fatalf("an orphaned event-management durable refused %d publishes on an ingest-only instance", refused)
	}

	for name, areas := range map[string]func() ([]string, error){
		"deployed":             func() ([]string, error) { return []string{"device-management", "event-management"}, nil },
		"mount unreadable":     func() ([]string, error) { return nil, errors.New("no such directory") },
		"not this pod's mount": func() ([]string, error) { return []string{"user-management"}, nil },
	} {
		g.enabledAreas = areas
		dm.sample(streams.ResolvedEvents)
		if err := w.WriteMessages(tenantCtx(), Message{Value: []byte("m")}); !errors.Is(err, ErrStreamBackpressure) {
			t.Fatalf("%s: with event-management 19 of 20 behind the publish answered %v; want a refusal", name, err)
		}
	}
}

// fullGate closes inbound-events' gate: device-management's durable exists and has read
// nothing, and a writer has put 18 of 20 messages in front of it.
func fullGate(t *testing.T, srv *natsserver.Server, bounds bpBounds) (dm, es *bpService, w MessageWriter) {
	t.Helper()
	dm = newBPService(t, srv, "device-management", bounds)
	dm.reader(t, streams.InboundEvents)
	es = newBPService(t, srv, "event-sources", bounds)
	w = es.writer(t, streams.InboundEvents)
	if refused, _ := fill(t, es, w, streams.InboundEvents, 18, []byte("m")); refused != 0 {
		t.Fatalf("the fixture was refused before it filled: %d", refused)
	}
	if err := es.nmgr.Backpressure(streams.InboundEvents); err == nil {
		t.Fatal("precondition: the gate should be closed at 18 of 20 unread")
	}
	return dm, es, w
}

// A batch is refused whole, and a presence transition is admitted past a closed gate while
// a reading is not.
func TestAClosedGateRefusesTelemetryAndAdmitsPresence(t *testing.T) {
	srv := startEmbeddedServer(t)
	_, es, w := fullGate(t, srv, bpBounds{maxMsgs: 20})

	err := w.WriteMessages(tenantCtx(), Message{Value: []byte("a")}, Message{Value: []byte("b")}, Message{Value: []byte("c")})
	if !errors.Is(err, ErrStreamBackpressure) {
		t.Fatalf("a batch of readings under a closed gate answered %v", err)
	}
	mixed := w.WriteMessages(tenantCtx(), Message{Value: []byte("p"), BypassBackpressure: true}, Message{Value: []byte("r")})
	if !errors.Is(mixed, ErrStreamBackpressure) {
		t.Fatalf("a batch mixing a presence transition with a reading answered %v; a reading must not ride along", mixed)
	}
	if st := streamState(t, es.nmgr, streams.InboundEvents); st.LastSeq != 18 {
		t.Fatalf("refused batches stored something: last seq %d, want 18", st.LastSeq)
	}
	if err := w.WriteMessages(tenantCtx(), Message{Value: []byte("presence"), BypassBackpressure: true}); err != nil {
		t.Fatalf("a presence transition was refused under a closed gate: %v", err)
	}
	if st := streamState(t, es.nmgr, streams.InboundEvents); st.LastSeq != 19 {
		t.Fatalf("the presence transition was not stored: last seq %d, want 19", st.LastSeq)
	}
}

// A reader whose area forwards into a gated stream parks BEFORE fetching while that gate is
// closed: the message it would have fetched is not delivered, so it spends none of its
// MaxDeliver deliveries. When the gate opens it is delivered for the first time.
//
// The forwarding is device-management's (inbound-events into resolved-events), declared in
// core/streams and applied by NewReader from the area alone: nothing here passes an option.
func TestAForwardingReaderParksWithoutSpendingDeliveries(t *testing.T) {
	srv := startEmbeddedServer(t)
	bounds := bpBounds{maxMsgs: 20}
	em := newBPService(t, srv, "event-management", bounds)
	emr := em.reader(t, streams.ResolvedEvents)
	up := newBPService(t, srv, "event-processing", bounds)
	rw := up.writer(t, streams.ResolvedEvents)
	if refused, _ := fill(t, up, rw, streams.ResolvedEvents, 18, []byte("m")); refused != 0 {
		t.Fatalf("resolved-events refused before it filled: %d", refused)
	}

	dm := newBPService(t, srv, "device-management", bounds)
	in := dm.reader(t, streams.InboundEvents)
	if in.downstream != streams.ResolvedEvents {
		t.Fatalf("device-management's inbound reader forwards into %q; want %q from the declaration", in.downstream, streams.ResolvedEvents)
	}
	es := newBPService(t, srv, "event-sources", bounds)
	if err := es.writer(t, streams.InboundEvents).WriteMessages(tenantCtx(), Message{Value: []byte("e")}); err != nil {
		t.Fatalf("publish to inbound: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, err := in.ReadMessage(ctx)
	cancel()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("a forwarding reader handed a message out under a closed downstream gate: %v", err)
	}
	ci, err := dm.nmgr.js.ConsumerInfo(in.stream, in.durable)
	if err != nil {
		t.Fatal(err)
	}
	if ci.Delivered.Consumer != 0 || ci.NumAckPending != 0 {
		t.Fatalf("the parked reader fetched: delivered %d, ack pending %d; want 0 and 0", ci.Delivered.Consumer, ci.NumAckPending)
	}

	// event-management catches up past the open threshold; the gate opens.
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		m, err := emr.ReadMessage(ctx)
		cancel()
		if err != nil {
			t.Fatalf("drain resolved %d: %v", i, err)
		}
		if err := m.Ack(); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for dm.nmgr.Backpressure(streams.ResolvedEvents) != nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		dm.sample(streams.ResolvedEvents)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	m, err := in.ReadMessage(ctx)
	cancel()
	if err != nil {
		t.Fatalf("the reader did not resume once the gate opened: %v", err)
	}
	if m.StreamSeq != 1 || m.NumDelivered != 1 {
		t.Fatalf("resumed with seq %d delivery %d; want seq 1 on its FIRST delivery", m.StreamSeq, m.NumDelivered)
	}
}

// The ordered writer is a forwarding hop's publisher, and its source message is already
// fetched: it must never refuse, or the message is left unacked to spend its deliveries.
func TestTheOrderedWriterDoesNotRefuse(t *testing.T) {
	srv := startEmbeddedServer(t)
	_, es, _ := fullGate(t, srv, bpBounds{maxMsgs: 20})
	ow, err := es.nmgr.NewOrderedWriter(streams.InboundEvents, 4)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	ow.Publish(tenantCtx(), Message{Value: []byte("fwd")}, func(err error) { done <- err })
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the ordered writer's publish settled with %v under a closed gate", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the ordered writer's publish never settled")
	}
	ow.Close()
}

// A writer built on a healthy broker is not born refusing: NewWriter measures the gate
// once, so the first publish goes through without waiting for a background sample. A
// second writer on the same stream (the connection callback can run again) reuses the gate.
func TestAFreshWriterIsNotBornRefusing(t *testing.T) {
	srv := startEmbeddedServer(t)
	es := newBPService(t, srv, "event-sources", bpBounds{maxMsgs: 20})
	w := es.writer(t, streams.InboundEvents)
	if err := w.WriteMessages(tenantCtx(), Message{Value: []byte("first")}); err != nil {
		t.Fatalf("the first publish of a fresh writer was refused: %v", err)
	}
	es.writer(t, streams.InboundEvents)
	g := es.nmgr.backpressure()
	g.mu.Lock()
	n := len(g.gates)
	g.mu.Unlock()
	if n != 1 {
		t.Fatalf("two writers on one stream made %d gates; want 1", n)
	}
}

// When the backlog can no longer be measured, a closed gate stays closed (a failed sample is
// not "no durables, nothing unread"), the gate goes stale after 30 s, and the engaged
// series reports it at scrape time, not from a value a sampler last set.
func TestAGateThatCannotMeasureStaysClosedAndSaysSo(t *testing.T) {
	srv := startEmbeddedServer(t)
	clk := newTestClock()
	bounds := bpBounds{maxMsgs: 20}
	dm := newBPService(t, srv, "device-management", bounds)
	dm.reader(t, streams.InboundEvents)
	es := newBPService(t, srv, "event-sources", bounds)
	es.nmgr.backpressure().now = clk.now
	w := es.writer(t, streams.InboundEvents)
	if refused, _ := fill(t, es, w, streams.InboundEvents, 18, []byte("m")); refused != 0 {
		t.Fatalf("refused before full: %d", refused)
	}
	stream := StreamName("test", streams.InboundEvents)
	if v, ok := es.metric(t, "jetstream_backpressure_unread_ratio", stream); !ok || v < 0.9 {
		t.Fatalf("jetstream_backpressure_unread_ratio = %v (present %v); want >= 0.9", v, ok)
	}

	srv.Shutdown()
	es.sample(streams.InboundEvents)
	err := es.nmgr.Backpressure(streams.InboundEvents)
	var bpe *BackpressureError
	if !errors.As(err, &bpe) || bpe.Stale || bpe.Durable == "" {
		t.Fatalf("a failed sample changed a closed gate's answer to %v; want it still closed on its last measurement", err)
	}
	if v, ok := es.metric(t, "jetstream_backpressure_unread_ratio", stream); ok {
		t.Fatalf("jetstream_backpressure_unread_ratio still reads %v after a failed sample; an old reading "+
			"must be withdrawn, not exported as a current one", v)
	}
	clk.advance(31 * time.Second)
	es.sample(streams.InboundEvents)
	if err := es.nmgr.Backpressure(streams.InboundEvents); !errors.As(err, &bpe) || !bpe.Stale {
		t.Fatalf("31 s without a successful sample the gate answered %v; want stale", err)
	}
	if v, ok := es.metric(t, "jetstream_backpressure_engaged", stream); !ok || v != 1 {
		t.Fatalf("jetstream_backpressure_engaged = %v (present %v) while the gate is stale; want 1", v, ok)
	}
}

// The broker answers StreamInfo but not one gating durable's ConsumerInfo, as when that call
// times out or is denied: the sample FAILS, so a closed gate stays closed on its last
// measurement and goes stale after 30 s. Skipping the durable instead would take the gate's
// maximum over the durables that did answer (here none), read "nothing unread", and open a
// gate whose reader is full.
func TestAGateWhoseConsumerCannotBeMeasuredStaysClosed(t *testing.T) {
	srv := startEmbeddedServer(t)
	clk := newTestClock()
	bounds := bpBounds{maxMsgs: 20}
	dm := newBPService(t, srv, "device-management", bounds)
	dm.reader(t, streams.InboundEvents)
	es := newBPService(t, srv, "event-sources", bounds)
	g := es.nmgr.backpressure()
	g.now = clk.now
	var deny atomic.Bool
	answer := g.consumerInfo
	g.consumerInfo = func(ctx context.Context, stream, durable string) (*nats.ConsumerInfo, error) {
		if deny.Load() {
			return nil, nats.ErrTimeout
		}
		return answer(ctx, stream, durable)
	}
	w := es.writer(t, streams.InboundEvents)
	if refused, _ := fill(t, es, w, streams.InboundEvents, 18, []byte("m")); refused != 0 {
		t.Fatalf("refused before full: %d", refused)
	}
	var bpe *BackpressureError
	if err := es.nmgr.Backpressure(streams.InboundEvents); !errors.As(err, &bpe) || bpe.Stale {
		t.Fatalf("precondition: the gate should be closed on a measurement at 18 of 20 unread, got %v", err)
	}

	deny.Store(true)
	stream := StreamName("test", streams.InboundEvents)
	if _, err := es.nmgr.js.StreamInfo(stream); err != nil {
		t.Fatalf("precondition: StreamInfo must still answer, so only ConsumerInfo fails: %v", err)
	}
	es.sample(streams.InboundEvents)
	err := es.nmgr.Backpressure(streams.InboundEvents)
	if !errors.As(err, &bpe) || bpe.Stale || bpe.Durable != DurableName("test", "device-management", streams.InboundEvents) {
		t.Fatalf("a ConsumerInfo that failed changed a closed gate's answer to %v; want it still closed on "+
			"device-management's last measurement", err)
	}
	if v, ok := es.metric(t, "jetstream_backpressure_unread_ratio", stream); ok {
		t.Fatalf("jetstream_backpressure_unread_ratio still reads %v; a sample that could not measure a "+
			"durable must fail, and withdraw the series", v)
	}
	clk.advance(31 * time.Second)
	es.sample(streams.InboundEvents)
	if err := es.nmgr.Backpressure(streams.InboundEvents); !errors.As(err, &bpe) || !bpe.Stale {
		t.Fatalf("31 s of failed ConsumerInfo the gate answered %v; want stale", err)
	}
}

// Nothing in this test measures by hand after the writer is built: the background loop is
// what closes the gate once the reader falls behind, and the loop ends once the connection
// closes. Without the loop every gate goes stale 30 s after start and refuses everything.
func TestTheSamplingLoopMeasuresWithoutBeingAsked(t *testing.T) {
	srv := startEmbeddedServer(t)
	bounds := bpBounds{maxMsgs: 20}
	dm := newBPService(t, srv, "device-management", bounds)
	dm.reader(t, streams.InboundEvents)
	es := newBPService(t, srv, "event-sources", bounds)
	w := es.writer(t, streams.InboundEvents)
	g := es.nmgr.backpressure()
	g.mu.Lock()
	registered := g.gates[streams.InboundEvents].sampledAt
	g.mu.Unlock()
	for i := 0; i < 18; i++ {
		if err := w.WriteMessages(tenantCtx(), Message{Value: []byte("m")}); err != nil {
			t.Fatalf("publish %d on a gate measured open: %v", i+1, err)
		}
	}

	var bpe *BackpressureError
	deadline := time.Now().Add(3 * backpressureSampleEvery)
	for {
		if err := es.nmgr.Backpressure(streams.InboundEvents); errors.As(err, &bpe) && !bpe.Stale {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no background sample closed the gate within %s of the reader falling 18 of 20 behind",
				3*backpressureSampleEvery)
		}
		time.Sleep(100 * time.Millisecond)
	}
	g.mu.Lock()
	sampled := g.gates[streams.InboundEvents].sampledAt
	g.mu.Unlock()
	if !sampled.After(registered) {
		t.Fatalf("the gate closed but its last sample (%v) is not after registration's (%v)", sampled, registered)
	}

	es.nmgr.closeConn()
	select {
	case <-g.loopDone:
	case <-time.After(3 * backpressureSampleEvery):
		t.Fatalf("the sampling loop was still running %s after its connection closed", 3*backpressureSampleEvery)
	}
}

// NEGATIVE CONTROL for the engaged series: 0 while the gate is open.
func TestTheEngagedSeriesIsZeroWhileOpen(t *testing.T) {
	srv := startEmbeddedServer(t)
	es := newBPService(t, srv, "event-sources", bpBounds{maxMsgs: 20})
	es.writer(t, streams.InboundEvents)
	if v, ok := es.metric(t, "jetstream_backpressure_engaged", StreamName("test", streams.InboundEvents)); !ok || v != 0 {
		t.Fatalf("jetstream_backpressure_engaged = %v (present %v) on an open gate; want 0", v, ok)
	}
	if v, ok := es.metric(t, "jetstream_publish_refused_total", StreamName("test", streams.InboundEvents)); !ok || v != 0 {
		t.Fatalf("jetstream_publish_refused_total = %v (present %v) before any refusal; want an explicit 0", v, ok)
	}
}

// No stream is reconfigured by this: every declared stream is still created discarding OLD.
// Discard-new on a Limits stream with a week of retention refuses on read history; this pins
// the decision against someone "finishing" the backpressure by flipping the broker policy.
func TestEveryDeclaredStreamIsStillCreatedDiscardOld(t *testing.T) {
	srv := startEmbeddedServer(t)
	svc := newBPService(t, srv, "device-management", bpBounds{})
	for _, s := range streams.All {
		name, err := svc.nmgr.ensureStream(s.Suffix)
		if err != nil {
			t.Fatalf("ensure %s: %v", s.Suffix, err)
		}
		info, err := svc.nmgr.js.StreamInfo(name)
		if err != nil {
			t.Fatal(err)
		}
		if info.Config.Discard != nats.DiscardOld {
			t.Errorf("stream %s is created %v; every stream discards old, and backpressure is the platform's", s.Suffix, info.Config.Discard)
		}
	}
}
