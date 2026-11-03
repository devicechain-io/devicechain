// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-event-management/model"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/jackc/pgx/v5/pgconn"
)

// FOLLOW-UP F1 (ingest-writer-batching spec, section 11): does a store hold spend the delivery
// budget of the messages that wait OUTSIDE the writer that holds?
//
// The hold beats (InProgress) only the messages a writer holds. The rest of the in-flight
// window sits unbeaten: the hand-off channel, the reader's message in hand, its fetch buffer.
// These tests run the REAL reader against a REAL embedded JetStream server, with a short
// AckWait, behind the real EventPersistenceProcessor and a store that refuses writes, and
// observe the deliveries the broker makes (the NumDelivered each message arrives with).

const (
	// f1AckWait is the broker's AckWait for these tests: short, so time is driven by the
	// configured value and not by a wait sized to luck.
	f1AckWait = time.Second
	f1Tenant  = "acme"
	f1Batch   = 128
)

// windowApi is the real Api with a store that can be taken down and, while it is down, let
// through a fixed number of transactions (a short window in which it answers).
type windowApi struct {
	*model.Api
	down    atomic.Bool
	grant   atomic.Int64
	refused atomic.Int64
	err     error
}

func (a *windowApi) PersistInTx(ctx context.Context, fn func(db *gorm.DB) error) error {
	if a.down.Load() {
		for {
			g := a.grant.Load()
			if g <= 0 {
				a.refused.Add(1)
				return a.err
			}
			if a.grant.CompareAndSwap(g, g-1) {
				break
			}
		}
	}
	return a.Api.PersistInTx(ctx, fn)
}

// spyReader records the highest NumDelivered each stream sequence has been handed out with.
type spyReader struct {
	messaging.MessageReader
	mu   sync.Mutex
	seen map[uint64]int
}

func (s *spyReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	m, err := s.MessageReader.ReadMessage(ctx)
	if err == nil {
		s.mu.Lock()
		if m.NumDelivered > s.seen[m.StreamSeq] {
			s.seen[m.StreamSeq] = m.NumDelivered
		}
		s.mu.Unlock()
	}
	return m, err
}

func (s *spyReader) histogram() (max int, byCount map[int]int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	byCount = map[int]int{}
	for _, d := range s.seen {
		byCount[d]++
		if d > max {
			max = d
		}
	}
	return max, byCount
}

// f1Broker is one embedded JetStream server that outlives the "pods" run against it, plus the
// one event store they all write to.
type f1Broker struct {
	t        *testing.T
	srv      *natsserver.Server
	host     string
	port     uint32
	instance string
	stream   string
	durable  string
	api      *windowApi
	db       *gorm.DB
}

func newF1Broker(t *testing.T) *f1Broker {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dctest.JetStreamStoreDir(t),
		JetStreamMaxStore: dctest.JetStreamMaxStore, NoLog: true, NoSigs: true,
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "embedded nats server not ready")
	t.Cleanup(srv.Shutdown)
	u, err := url.Parse(srv.ClientURL())
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	ep, db, _ := newFencedPersistenceWorker(t)
	singleConnection(t, db)
	api := &windowApi{Api: ep.Api.(*model.Api), err: &pgconn.PgError{Code: "53100", Message: "No space left on device"}}
	instance := "f1hold"
	return &f1Broker{
		t: t, srv: srv, host: u.Hostname(), port: uint32(port), instance: instance, api: api, db: db,
		stream:  messaging.StreamName(instance, streams.ResolvedEvents),
		durable: messaging.DurableName(instance, "event-management", streams.ResolvedEvents),
	}
}

// f1Pod is one event-management pod: a NATS manager, the real reader (behind a spy) and the
// real processor.
type f1Pod struct {
	b     *f1Broker
	nmgr  *messaging.NatsManager
	spy   *spyReader
	eproc *EventPersistenceProcessor
}

func (b *f1Broker) startPod() *f1Pod {
	t := b.t
	t.Helper()
	ms := &core.Microservice{InstanceId: b.instance, FunctionalArea: "event-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{
		Hostname: b.host, Port: b.port,
		StreamMaxBytes: 16 << 20, StreamMaxBytesCold: 4 << 20,
		Fetch: mscfg.NatsFetchConfiguration{Batch: f1Batch, Ahead: mscfg.AheadFlag(true)},
	}
	pod := &f1Pod{b: b}
	pm := NewPersistMetrics(ms)
	nmgr := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(n *messaging.NatsManager) error {
		reader, err := n.NewReader(streams.ResolvedEvents)
		if err != nil {
			return err
		}
		failed, err := n.NewWriter(streams.FailedEvents)
		if err != nil {
			return err
		}
		pod.spy = &spyReader{MessageReader: reader, seen: map[uint64]int{}}
		pod.eproc = NewEventPersistenceProcessor(ms, pod.spy, failed, core.NewNoOpLifecycleCallbacks(), b.api, pm)
		return nil
	})
	nmgr.RecordMaxDeliveries(deadletter.MaxDeliveryRecorder(deadletter.NewProducer(ms)))
	nmgr.SetAckWaitForTesting(t, f1AckWait)
	pod.nmgr = nmgr
	require.NoError(t, nmgr.Initialize(context.Background()))
	require.NoError(t, nmgr.Start(context.Background()))
	require.NotNil(t, pod.eproc, "the manager never built the processor")
	require.NoError(t, pod.eproc.Initialize(context.Background()))
	require.NoError(t, pod.eproc.Start(context.Background()))
	return pod
}

// stop ends the pod the way a restart does as far as the broker can tell: the readers go away
// and nothing buffered is acked. The store must be DOWN when it is called, or the workers
// would drain the buffer into it on the way out.
func (p *f1Pod) stop() {
	p.b.t.Helper()
	require.True(p.b.t, p.b.api.down.Load(), "stop a pod only while the store is down")
	require.NoError(p.b.t, p.eproc.Stop(context.Background()))
	p.nmgr.Conn().Close()
}

// publish puts n resolved events (numbered from) on the instance's resolved-events stream.
func (p *f1Pod) publish(from, n int) {
	t := p.b.t
	t.Helper()
	js, err := p.nmgr.Conn().JetStream()
	require.NoError(t, err)
	for i := from; i < from+n; i++ {
		ev := batchEvent(i, false, batchT0)
		payload, err := resolvedevent.MarshalResolvedEvent(&ev)
		require.NoError(t, err)
		_, err = js.Publish(messaging.ScopedSubject(p.b.instance, f1Tenant, streams.ResolvedEvents), payload)
		require.NoError(t, err)
	}
}

// f1Consumer is the broker's own account of the durable.
type f1Consumer struct {
	ackPending, redelivered, pending int
	deliveredConsumer                uint64
}

func (p *f1Pod) consumer() f1Consumer {
	t := p.b.t
	t.Helper()
	js, err := p.nmgr.Conn().JetStream()
	require.NoError(t, err)
	ci, err := js.ConsumerInfo(p.b.stream, p.b.durable)
	require.NoError(t, err)
	return f1Consumer{ackPending: ci.NumAckPending, redelivered: ci.NumRedelivered, pending: int(ci.NumPending),
		deliveredConsumer: ci.Delivered.Consumer}
}

// failedEvents is how many messages are on the failed-events stream.
func (p *f1Pod) failedEvents() uint64 {
	t := p.b.t
	t.Helper()
	js, err := p.nmgr.Conn().JetStream()
	require.NoError(t, err)
	si, err := js.StreamInfo(messaging.StreamName(p.b.instance, streams.FailedEvents))
	require.NoError(t, err)
	return si.State.Msgs
}

func (p *f1Pod) stored() int64 {
	events, _, _ := rowCounts(p.b.t, p.b.db)
	return events
}

// f1WaitFor polls cond until it holds or the deadline passes.
func f1WaitFor(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", within, what)
}

// holdSteady waits until the store is being refused and the whole pipeline is backed up behind
// it: the hand-off channel full, the writer probing a down store.
func (p *f1Pod) holdSteady() {
	t := p.b.t
	t.Helper()
	f1WaitFor(t, "the hand-off channel to fill behind a held writer", 30*time.Second, func() bool {
		return len(p.eproc.messages) == cap(p.eproc.messages) && p.b.api.refused.Load() > 0
	})
}

// settled waits for the broker to have nothing left for the durable: nothing pending, nothing
// awaiting an ack.
func (p *f1Pod) settled(within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if c := p.consumer(); c.pending == 0 && c.ackPending == 0 {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// f1Elapse waits d of wall-clock time, for holding a store down for a multiple of the
// configured AckWait.
func f1Elapse(d time.Duration) {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		time.Sleep(5 * time.Millisecond)
	}
}

func f1Tune(t *testing.T) {
	t.Helper()
	probeTimings(t, 5*time.Millisecond, 20*time.Millisecond)
	old := holdTuning.ProgressEvery
	holdTuning.ProgressEvery = f1AckWait / 3
	t.Cleanup(func() { holdTuning.ProgressEvery = old })
}

// stopWithStoreDown restarts-as-far-as-the-broker-can-tell: see stop.
func (p *f1Pod) stopWithStoreDown() {
	p.b.api.down.Store(true)
	p.b.api.grant.Store(0)
	p.stop()
}

// ONE LONG HOLD. The store is down for three AckWaits. What the broker does with the messages
// that wait outside the held writer is the first step of the F1 claim.
//
// F1 (a hold spends the delivery budget of unbeaten messages, exhausting MaxDeliver) was
// TESTED AND REFUTED; this is the structural guard. A reader blocked in the hand-off pulls
// nothing, so the broker redelivers nothing however long the hold lasts; one delivery is
// spent at the first pull after recovery.
//
// Measured with a flapping store (down 1.5 x AckWait, answering for a two-transaction window,
// repeat; 1 s AckWait, 1500 events, one pod): batch 16 without fetch-ahead reached a highest
// delivery count of 3 after 16 cycles; production's shape (batch 128, ahead on) reached 4
// after 40 cycles (about 100 AckWaits), for one message in 1500, then plateaued. Nothing went
// to failed-events or the dead-letter stream and 1500 of 1500 events were stored after a
// restart. The margin is thin (4 of 5) and wall-clock driven, so that run is not kept as a
// test. NOT MODELLED: a crash-looping pod with the store down spends one delivery of the
// whole pool per restart.
func TestF1ALongHoldDoesNotRedeliverToABlockedReader(t *testing.T) {
	f1Tune(t)
	b := newF1Broker(t)
	b.api.down.Store(true)
	pod := b.startPod()
	const n = 400
	pod.publish(0, n)
	pod.holdSteady()
	before := pod.consumer()
	f1Elapse(3 * f1AckWait)
	during := pod.consumer()
	t.Logf("before: %+v during(3 x AckWait later): %+v", before, during)

	// The unbeaten window exists: well over the hand-off channel's worth of messages is
	// delivered and unacked, and the writer holds only one batch.
	require.GreaterOrEqual(t, during.ackPending, MESSAGE_BACKLOG_SIZE)
	// A reader blocked in the hand-off pulls nothing, so the broker redelivers nothing: no
	// delivery is spent however long the hold lasts.
	require.Equal(t, 0, during.redelivered, "a delivery was spent during the hold")
	require.Equal(t, before.deliveredConsumer, during.deliveredConsumer, "the broker delivered during the hold")

	b.api.down.Store(false)
	require.True(t, pod.settled(30*time.Second), "the backlog never drained after the store recovered")
	require.EqualValues(t, n, pod.stored())
	max, hist := pod.spy.histogram()
	t.Logf("deliveries per message after one 3 x AckWait hold: max=%d histogram=%v", max, hist)
	require.Zero(t, pod.failedEvents())
	pod.stopWithStoreDown()
}
