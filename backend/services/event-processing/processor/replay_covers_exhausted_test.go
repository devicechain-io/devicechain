// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/glebarez/sqlite"
)

// 🔑 WHAT THIS FILE PROVES, and why it is the premise of a declaration rather than a test of one.
//
// The resolved-events stream declares event-processing's durable as COVERED BY REPLAY
// (streams.Stream.ReplayCovered), and a declaration there tells the max-delivery recorder to
// write no dead letter for that durable's exhausted deliveries. That is safe only if DETECT
// really does re-read an event whose deliveries ran out. DETECT acks only after a checkpoint,
// so a checkpoint outage longer than AckWait x MaxDeliver exhausts every delivery in the window
// — this is the ordinary shape of a database outage, not an edge case — and without the
// declaration each of those would be a dead letter about an event that was never lost.
//
// So each test here drives the REAL live loop over a REAL durable on an embedded broker, holds
// the snapshot store down until the broker has published its own max-delivery advisory for
// every event in the window, and then checks that every one of them is in a committed
// checkpoint anyway. The three tests are the three ways the window can end:
//
//   - the pod keeps running and the store comes back: the loop applied each event on its FIRST
//     delivery, so the next checkpoint covers it and the redeliveries were duplicates;
//   - the pod dies during the outage: the successor restores the last committed checkpoint and
//     replays the STREAM by sequence, which never consults the durable's ack state;
//   - the loop is blocked, not failing, so delivered events sit unapplied in its buffer: a pull
//     consumer only redelivers on a pull, and the reader only pulls once its buffer is empty, so
//     no delivery is spent on an event the loop has not taken — nothing can run out there.
//
// What none of them covers, stated so nobody reads it in: an event the stream itself evicted
// (retention or DiscardOld) before a restart replayed it. That is a stream-fill loss, visible on
// the unread-loss counter and the near-full alert, and a no-outcome letter would not have held
// the event either — resolved-events is a Hot stream, whose letters are pointer-only.

// storeOutage fails every statement on the snapshot store's database while it is down, and
// HANGS every statement while it is hung — the two shapes a real database outage takes (an
// error back fast, or a black hole).
type storeOutage struct {
	down atomic.Bool
	hung atomic.Pointer[chan struct{}]
}

var errStoreDown = errors.New("snapshot store is down (test outage)")

func (o *storeOutage) intercept(db *gorm.DB) {
	if ch := o.hung.Load(); ch != nil {
		<-*ch
	}
	if o.down.Load() {
		_ = db.AddError(errStoreDown)
	}
}

// outageStore is newTestStore with an outage switch on it.
func outageStore(t *testing.T) (*model.SnapshotStore, *storeOutage) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	// One connection: every connection to ":memory:" is its own empty database, so a second
	// one opened while a hung statement holds the first would see no table at all.
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.DetectSnapshot{}))
	o := &storeOutage{}
	cb := db.Callback()
	require.NoError(t, cb.Query().Before("gorm:query").Register("test:outage", o.intercept))
	require.NoError(t, cb.Create().Before("gorm:create").Register("test:outage", o.intercept))
	require.NoError(t, cb.Update().Before("gorm:update").Register("test:outage", o.intercept))
	require.NoError(t, cb.Delete().Before("gorm:delete").Register("test:outage", o.intercept))
	return model.NewSnapshotStore(&rdb.RdbManager{Database: db}), o
}

// committedSeq reads the committed checkpoint the way restore does. It must not be called
// while the store is down.
func committedSeq(t *testing.T, store *model.SnapshotStore) int64 {
	t.Helper()
	snap, ok, err := store.Load(context.Background(), "singleton")
	require.NoError(t, err)
	if !ok {
		return 0
	}
	return snap.StreamSeq
}

func waitForCheckpoint(t *testing.T, store *model.SnapshotStore, want int64, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	got := int64(0)
	for time.Now().Before(deadline) {
		if got = committedSeq(t, store); got >= want {
			require.Equal(t, want, got)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("committed checkpoint = %d, want %d within %s", got, want, within)
}

// handedOut wraps the live reader and records the stream sequence of every message it hands
// the loop, in order. The loop applies a message iff its sequence is above the engine's, so an
// event was applied exactly when its FIRST hand-out came after every lower sequence's — which
// is what firstSightInOrder checks. A redelivered copy after that is a duplicate by design.
type handedOut struct {
	messaging.MessageReader
	mu   sync.Mutex
	seqs []uint64
}

func (h *handedOut) ReadMessage(ctx context.Context) (messaging.Message, error) {
	m, err := h.MessageReader.ReadMessage(ctx)
	if err == nil {
		h.mu.Lock()
		h.seqs = append(h.seqs, m.StreamSeq)
		h.mu.Unlock()
	}
	return m, err
}

func (h *handedOut) snapshot() []uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]uint64(nil), h.seqs...)
}

// firstSightInOrder asserts that 1..n each reached the loop, and that each reached it for the
// first time after every lower sequence had — so none was guard-dropped as a "duplicate" of a
// higher sequence it had never been applied before.
func firstSightInOrder(t *testing.T, seqs []uint64, n uint64) {
	t.Helper()
	seen := map[uint64]bool{}
	var firsts []uint64
	for _, s := range seqs {
		if !seen[s] {
			seen[s] = true
			firsts = append(firsts, s)
		}
	}
	want := make([]uint64, 0, n)
	for i := uint64(1); i <= n; i++ {
		want = append(want, i)
	}
	require.Equal(t, want, firsts, "the order in which each event FIRST reached the loop (all hand-outs: %v)", seqs)
}

// detectBroker is one embedded JetStream broker shared by the managers of one test.
type detectBroker struct {
	srv  *natsserver.Server
	host string
	port uint32
	nc   *nats.Conn
}

func startDetectBroker(t *testing.T) *detectBroker {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(),
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "embedded nats server not ready")
	t.Cleanup(srv.Shutdown)
	u, err := url.Parse(srv.ClientURL())
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return &detectBroker{srv: srv, host: u.Hostname(), port: uint32(port), nc: nc}
}

const coveredInstance = "detectreplay"

// detectManager is one event-processing process's broker side: a manager with the production
// resolved-events reader on it (the same NewReader call main.go makes, less the term gate that
// a lease adds) and the platform's max-delivery recorder, which a manager with readers refuses
// to start without.
func (b *detectBroker) detectManager(t *testing.T) (*messaging.NatsManager, messaging.MessageReader) {
	t.Helper()
	nmgr, reader, _ := b.detectManagerWithMetrics(t)
	return nmgr, reader
}

// detectManagerWithMetrics is detectManager that also returns the registry its /metrics serves.
func (b *detectBroker) detectManagerWithMetrics(t *testing.T) (*messaging.NatsManager, messaging.MessageReader, *prometheus.Registry) {
	t.Helper()
	ms := &core.Microservice{InstanceId: coveredInstance, FunctionalArea: "event-processing"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: b.host, Port: b.port}
	var reader messaging.MessageReader
	nmgr := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(m *messaging.NatsManager) error {
		r, err := m.NewReader(streams.ResolvedEvents)
		reader = r
		return err
	})
	nmgr.RecordMaxDeliveries(deadletter.MaxDeliveryRecorder(deadletter.NewProducer(ms)))
	nmgr.SetAckWaitForTesting(t, time.Second)
	require.NoError(t, nmgr.Initialize(context.Background()))
	require.NoError(t, nmgr.Start(context.Background()))
	t.Cleanup(func() {
		if c := nmgr.Conn(); c != nil && !c.IsClosed() {
			c.Close()
		}
	})
	return nmgr, reader, reg
}

// publish appends one resolved event for tenant acme and returns its stream sequence.
func (b *detectBroker) publish(t *testing.T, occurred time.Time) uint64 {
	t.Helper()
	js, err := b.nc.JetStream()
	require.NoError(t, err)
	ack, err := js.Publish(messaging.ScopedSubject(coveredInstance, "acme", streams.ResolvedEvents),
		resolvedBytes(t, occurred))
	require.NoError(t, err)
	return ack.Sequence
}

// exhaustions collects the broker's own max-delivery advisories for the DETECT durable: the
// broker's word, not the test's, that a delivery ran out.
type exhaustions struct {
	mu   sync.Mutex
	seqs map[uint64]bool
}

func (b *detectBroker) watchExhaustions(t *testing.T) *exhaustions {
	t.Helper()
	e := &exhaustions{seqs: map[uint64]bool{}}
	subject := messaging.AdvisorySubject(
		messaging.StreamName(coveredInstance, streams.ResolvedEvents),
		messaging.DurableName(coveredInstance, "event-processing", streams.ResolvedEvents))
	sub, err := b.nc.Subscribe(subject, func(m *nats.Msg) {
		var adv struct {
			StreamSeq uint64 `json:"stream_seq"`
		}
		if json.Unmarshal(m.Data, &adv) == nil {
			e.mu.Lock()
			e.seqs[adv.StreamSeq] = true
			e.mu.Unlock()
		}
	})
	require.NoError(t, err)
	require.NoError(t, b.nc.Flush())
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	return e
}

func (e *exhaustions) waitFor(t *testing.T, seqs []uint64, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		all := true
		for _, s := range seqs {
			all = all && e.seqs[s]
		}
		e.mu.Unlock()
		if all {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	t.Fatalf("the broker never exhausted deliveries for %v (advisories seen: %v)", seqs, e.seqs)
}

func (e *exhaustions) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.seqs)
}

// liveDetect builds the processor exactly as TestRunLoopCheckpointsAndAcksOnEOF does, over the
// real reader and the real replay opener, with a checkpoint after every message and a ticker
// that retries a failed one every 50ms — so the loop spends the outage trying to commit.
func liveDetect(t *testing.T, reader messaging.MessageReader, replay ReplayOpener, store *model.SnapshotStore) *ResolvedEventsProcessor {
	t.Helper()
	reg := runtime.NewRuleRegistry(nil)
	rp := &ResolvedEventsProcessor{
		ResolvedEventsReader: reader,
		Replay:               replay,
		Store:                store,
		cfg: Config{
			PartitionId:        "singleton",
			Suffix:             streams.ResolvedEvents,
			CheckpointEvents:   1,
			CheckpointInterval: 50 * time.Millisecond,
			TickInterval:       50 * time.Millisecond,
			Clock:              detectcore.RealClock{},
		},
		registry:  reg,
		publisher: runtime.NewPublisher(&captureWriter{}, reg, (*DetectMetrics)(nil)),
		clock:     detectcore.RealClock{},
	}
	ctx := context.Background()
	require.NoError(t, rp.ExecuteInitialize(ctx))
	require.NoError(t, rp.ExecuteStart(ctx))
	return rp
}

// exhaustWithin bounds the wait for the broker to give up on a delivery: MaxDeliver deliveries
// one AckWait apart, the pull that follows the last, and slack for the pump's fetch timeout.
const exhaustWithin = time.Duration(messaging.MaxDeliver+6) * time.Second

// replayCoveredCount reads devicechain_eventprocessing_max_delivery_records_total for
// outcome=replay-covered from the registry a scrape reads; -1 when the series is absent.
func replayCoveredCount(t *testing.T, reg *prometheus.Registry) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, f := range families {
		if f.GetName() != "devicechain_eventprocessing_max_delivery_records_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "outcome" && lp.GetValue() == "replay-covered" {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return -1
}

// deadLettersHeld is how many letters the instance's dead-letter stream holds (0 when no
// letter has ever created it).
func (b *detectBroker) deadLettersHeld(t *testing.T) uint64 {
	t.Helper()
	js, err := b.nc.JetStream()
	require.NoError(t, err)
	info, err := js.StreamInfo(messaging.StreamName(coveredInstance, streams.DeadLetters))
	if errors.Is(err, nats.ErrStreamNotFound) {
		return 0
	}
	require.NoError(t, err)
	return info.State.Msgs
}

// TestAnExhaustedEventIsCheckpointedWhenTheStoreReturns is the pod that keeps running. It is
// also the end-to-end check of the declaration this file justifies, through the production
// recorder (deadletter.MaxDeliveryRecorder): the three exhaustions are counted as
// replay-covered and none of them becomes a dead letter.
func TestAnExhaustedEventIsCheckpointedWhenTheStoreReturns(t *testing.T) {
	require.True(t, streams.ReplayCoveredBy(streams.ResolvedEvents, "event-processing"),
		"the declaration this file proves is gone; the recorder would dead-letter every event of a checkpoint outage")
	b := startDetectBroker(t)
	exhausted := b.watchExhaustions(t)
	nmgr, reader, reg := b.detectManagerWithMetrics(t)
	require.Zero(t, replayCoveredCount(t, reg), "the replay-covered series must exist at zero before the first one")
	store, outage := outageStore(t)
	seen := &handedOut{MessageReader: reader}
	rp := liveDetect(t, seen, nmgr, store)
	t.Cleanup(func() { _ = rp.ExecuteStop(context.Background()) })

	b.publish(t, testBase.Add(time.Second))
	waitForCheckpoint(t, store, 1, 10*time.Second)

	outage.down.Store(true)
	window := []uint64{
		b.publish(t, testBase.Add(2*time.Second)),
		b.publish(t, testBase.Add(3*time.Second)),
		b.publish(t, testBase.Add(4*time.Second)),
	}
	exhausted.waitFor(t, window, exhaustWithin)

	deadline := time.Now().Add(10 * time.Second)
	for replayCoveredCount(t, reg) < float64(len(window)) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	require.Equal(t, float64(len(window)), replayCoveredCount(t, reg),
		"each exhausted delivery must be counted once as replay-covered")
	require.Zero(t, b.deadLettersHeld(t), "an exhausted delivery of a replay-covered durable was dead-lettered")

	outage.down.Store(false)
	waitForCheckpoint(t, store, 4, 10*time.Second)
	firstSightInOrder(t, seen.snapshot(), 4)
}

// TestAnExhaustedEventIsReplayedAfterACrash is the pod that dies during the outage.
func TestAnExhaustedEventIsReplayedAfterACrash(t *testing.T) {
	b := startDetectBroker(t)
	exhausted := b.watchExhaustions(t)
	nmgr1, reader1 := b.detectManager(t)
	store, outage := outageStore(t)
	rp1 := liveDetect(t, reader1, nmgr1, store)

	b.publish(t, testBase.Add(time.Second))
	waitForCheckpoint(t, store, 1, 10*time.Second)

	outage.down.Store(true)
	window := []uint64{
		b.publish(t, testBase.Add(2*time.Second)),
		b.publish(t, testBase.Add(3*time.Second)),
		b.publish(t, testBase.Add(4*time.Second)),
	}
	exhausted.waitFor(t, window, exhaustWithin)

	// The crash: the loop stops with the store still down (its final checkpoint fails, as a
	// SIGKILL's would never have been attempted), and the process's connection goes with it.
	require.NoError(t, rp1.ExecuteStop(context.Background()))
	nmgr1.Conn().Close()
	outage.down.Store(false)
	require.EqualValues(t, 1, committedSeq(t, store), "vacuous: the outage let a checkpoint past seq 1 through")

	// The durable will not hand these out again: the broker has given up on each of them, and
	// nothing on it is undelivered. Whatever brings them back, it is not the durable.
	js, err := b.nc.JetStream()
	require.NoError(t, err)
	ci, err := js.ConsumerInfo(messaging.StreamName(coveredInstance, streams.ResolvedEvents),
		messaging.DurableName(coveredInstance, "event-processing", streams.ResolvedEvents))
	require.NoError(t, err)
	require.Zero(t, ci.NumPending, "the durable still had undelivered messages; the crash case is vacuous")

	// The successor process: a new connection, the same durable, restore + replay.
	nmgr2, reader2 := b.detectManager(t)
	seen := &handedOut{MessageReader: reader2}
	rp2 := liveDetect(t, seen, nmgr2, store)
	t.Cleanup(func() { _ = rp2.ExecuteStop(context.Background()) })
	waitForCheckpoint(t, store, 4, 10*time.Second)
	require.Empty(t, seen.snapshot(),
		"the durable handed the successor messages, so the checkpoint may not have come from replay")
}

// TestABlockedLoopSpendsNoDeliveries is the event that has not been applied at all: it sits in
// the pump or the reader's buffer while the loop is stuck inside a checkpoint that hangs. The
// claim is structural — the reader pulls only when its buffer is empty, and the broker spends a
// delivery only on a pull — so it is checked by the broker's own counters: across a stall
// longer than AckWait x MaxDeliver, no advisory, and every event applied on its first sight
// once the loop moves again.
func TestABlockedLoopSpendsNoDeliveries(t *testing.T) {
	b := startDetectBroker(t)
	exhausted := b.watchExhaustions(t)
	nmgr, reader := b.detectManager(t)
	store, outage := outageStore(t)
	seen := &handedOut{MessageReader: reader}
	rp := liveDetect(t, seen, nmgr, store)
	t.Cleanup(func() { _ = rp.ExecuteStop(context.Background()) })

	b.publish(t, testBase.Add(time.Second))
	waitForCheckpoint(t, store, 1, 10*time.Second)

	release := make(chan struct{})
	outage.hung.Store(&release)
	for i := 2; i <= 4; i++ {
		b.publish(t, testBase.Add(time.Duration(i)*time.Second))
	}
	stall := time.Duration(messaging.MaxDeliver+3) * time.Second
	time.Sleep(stall)
	require.Zero(t, exhausted.count(), "a delivery ran out while the loop was stalled")

	outage.hung.Store(nil)
	close(release)
	waitForCheckpoint(t, store, 4, 15*time.Second)
	firstSightInOrder(t, seen.snapshot(), 4)
}
