// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/streams"
)

// 🔑 THE RESOLVED-EVENTS DURABLE ACKNOWLEDGES AT A FLOOR.
//
// One ack per checkpoint, for the committed snapshot's sequence, deletes the broker's pending
// entry of EVERY message at or below it, handled or not. These tests pin what makes that safe
// and what it must never do, against the real broker where the behaviour is the broker's and
// against fakes where it is the processor's:
//
//   - the floor is the COMMITTED sequence, reached only after the snapshot commits, so a crash
//     between the two redelivers from the old floor and the sequence guard absorbs it;
//   - it names the highest committed message, not the last one handled;
//   - poison advances it like any message (Engine.Skip), and a message with no sequence never
//     names it;
//   - a range the broker sent and this process never received (a gap) is read and applied
//     BEFORE the floor passes it, because the floor ack deletes it outright;
//   - a replica that lost its lease between the Save and the ack sends nothing;
//   - the durable is created at the committed floor, and replaces the explicit-ack durable the
//     previous build used (the policy of an existing durable cannot be changed in place).

// ---------------------------------------------------------------------------------------------
// fixtures

// afterSave runs a function once, right after the next write to the snapshot table: the
// statement has run and its transaction has not yet committed, so what the function does
// happens BETWEEN the loop deciding to commit and the acknowledgement that follows the commit.
type afterSave struct{ fn atomic.Pointer[func()] }

func (h *afterSave) arm(f func()) { h.fn.Store(&f) }

func (h *afterSave) fire(db *gorm.DB) {
	if db.Statement == nil || !strings.Contains(db.Statement.Table, "detect_snapshot") {
		return
	}
	if p := h.fn.Swap(nil); p != nil {
		(*p)()
	}
}

// hookedStore is newTestStore with an afterSave hook on its writes.
func hookedStore(t *testing.T) (*model.SnapshotStore, *afterSave) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.DetectSnapshot{}))
	h := &afterSave{}
	cb := db.Callback()
	require.NoError(t, cb.Create().After("gorm:create").Register("test:aftersave", h.fire))
	require.NoError(t, cb.Update().After("gorm:update").Register("test:aftersave", h.fire))
	return model.NewSnapshotStore(&rdb.RdbManager{Database: db}), h
}

// syncDerived records the detections published, safely: the loop writes while the test reads.
type syncDerived struct {
	mu     sync.Mutex
	raised map[string]int
	other  int
}

func newSyncDerived() *syncDerived { return &syncDerived{raised: map[string]int{}} }

func (w *syncDerived) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, m := range msgs {
		var de runtime.DerivedEvent
		if err := json.Unmarshal(m.Value, &de); err != nil {
			return err
		}
		if de.Edge == runtime.EdgeRaised && de.RuleID == "acme/hot" {
			w.raised[de.Series]++
		} else {
			w.other++
		}
	}
	return nil
}

func (w *syncDerived) WriteToDevice(ctx context.Context, _ string, msgs ...messaging.Message) error {
	return w.WriteMessages(ctx, msgs...)
}

func (w *syncDerived) HandleResponse(error) {}

// counts is how many Raised detections were published per series.
func (w *syncDerived) counts() map[string]int {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]int, len(w.raised))
	for k, v := range w.raised {
		out[k] = v
	}
	return out
}

// hotProcessor is liveDetect with the threshold rule (temperature > 80 raises acme/hot), a
// recorder for what it publishes, and a checkpoint every checkpointEvents messages or 50ms.
func (b *detectBroker) hotProcessor(t *testing.T, reader messaging.MessageReader, replay ReplayOpener,
	store *model.SnapshotStore, checkpointEvents int) (*ResolvedEventsProcessor, *syncDerived) {
	t.Helper()
	reg := thresholdReg(t)
	w := newSyncDerived()
	rp := &ResolvedEventsProcessor{
		ResolvedEventsReader: reader,
		Replay:               replay,
		Store:                store,
		cfg: Config{
			PartitionId:        "singleton",
			Suffix:             streams.ResolvedEvents,
			CheckpointEvents:   checkpointEvents,
			CheckpointInterval: 50 * time.Millisecond,
			TickInterval:       50 * time.Millisecond,
			Clock:              detectcore.RealClock{},
		},
		registry:  reg,
		publisher: runtime.NewPublisher(w, reg, (*DetectMetrics)(nil)),
		clock:     detectcore.RealClock{},
	}
	ctx := context.Background()
	require.NoError(t, rp.ExecuteInitialize(ctx))
	require.NoError(t, rp.ExecuteStart(ctx))
	t.Cleanup(func() { _ = rp.ExecuteStop(context.Background()) })
	return rp, w
}

// publishHot appends a resolved event from device dev-n that raises acme/hot, and returns its
// stream sequence.
func (b *detectBroker) publishHot(t *testing.T, n uint64) uint64 {
	t.Helper()
	return b.publishBytes(t, measuredMsg(t, n, "acme", fmt.Sprintf("dev-%d", n), "p@1", "temperature", "90", &fakeAck{}).Value)
}

func (b *detectBroker) publishBytes(t *testing.T, payload []byte) uint64 {
	t.Helper()
	js, err := b.nc.JetStream()
	require.NoError(t, err)
	ack, err := js.Publish(messaging.ScopedSubject(b.instance, "acme", streams.ResolvedEvents), payload)
	require.NoError(t, err)
	return ack.Sequence
}

func (b *detectBroker) resolvedStream() string {
	return messaging.StreamName(b.instance, streams.ResolvedEvents)
}

func (b *detectBroker) resolvedInfo(t *testing.T) *nats.ConsumerInfo {
	t.Helper()
	js, err := b.nc.JetStream()
	require.NoError(t, err)
	ci, err := js.ConsumerInfo(b.resolvedStream(), b.resolvedDurable())
	require.NoError(t, err)
	return ci
}

// seenCount is how many times the loop was handed each stream sequence.
// brokerStore is a snapshot store for a test whose broker side runs on other goroutines: one
// connection, so every goroutine sees the one in-memory database. (newTestStore's second
// connection would open an empty one.)
func brokerStore(t *testing.T) *model.SnapshotStore {
	t.Helper()
	store, _ := hookedStore(t)
	return store
}

func (h *handedOut) seenCount() map[uint64]int {
	out := map[uint64]int{}
	for _, s := range h.snapshot() {
		out[s]++
	}
	return out
}

// ---------------------------------------------------------------------------------------------
// the real broker

// A crash between the snapshot and the ack. The old floor stays where it was; the restart
// restores the snapshot and replays by sequence; the broker redelivers from the OLD floor and
// from nothing below it; the guard absorbs the redelivery, so nothing is derived twice; and the
// next checkpoint's single ack clears what the crash left pending.
func TestACrashBetweenTheSnapshotAndTheAckRedeliversFromTheFloorAndNothingBelowIt(t *testing.T) {
	t.Parallel()
	b := startDetectBroker(t)
	store, save := hookedStore(t)
	nmgr1, reader1 := b.detectManager(t, store)
	rp1, w1 := b.hotProcessor(t, reader1, nmgr1, store, 1)

	b.publishHot(t, 1)
	waitForCheckpoint(t, store, 1, 10*time.Second)
	b.waitForAck(t, 1) // the floor is 1

	// The process dies once the next snapshot has been written and before it can acknowledge.
	save.arm(func() { nmgr1.Conn().Close() })
	b.publishHot(t, 2)
	waitForCheckpoint(t, store, 2, 10*time.Second)
	require.EqualValues(t, 1, b.resolvedInfo(t).AckFloor.Stream,
		"vacuous: the ack went out, so there was no crash between the snapshot and the ack")
	require.Equal(t, map[string]int{"dev-1": 1, "dev-2": 1}, w1.counts(), "the first process did not derive both events")
	require.NoError(t, rp1.ExecuteStop(context.Background()))

	// The successor starts after the broker's AckWait on seq 2 has run out, so the delivery that
	// never got its ack is offered again, and restores the snapshot at 2.
	time.Sleep(2 * brokerAckWait)
	nmgr2, reader2 := b.detectManager(t, store)
	seen := &handedOut{MessageReader: reader2}
	_, w2 := b.hotProcessor(t, seen, nmgr2, store, 1)

	require.Eventually(t, func() bool { return seen.seenCount()[2] >= 1 },
		10*time.Second, 50*time.Millisecond,
		"the broker did not redeliver seq 2, the one message above the old floor; handed out: %v", seen.snapshot())
	b.waitForAck(t, 2) // the redelivery is acknowledged by the next checkpoint, and the floor moves
	for seq := range seen.seenCount() {
		require.GreaterOrEqual(t, seq, uint64(2), "the broker redelivered seq %d, which is at or below the floor", seq)
	}
	require.Empty(t, w2.counts(), "the successor derived something at or below the snapshot again")
	require.Eventually(t, func() bool { return b.resolvedInfo(t).NumAckPending == 0 },
		5*time.Second, 50*time.Millisecond, "the next checkpoint's floor ack did not clear what the crash left pending")

	// And it carries on from there.
	b.publishHot(t, 3)
	waitForCheckpoint(t, store, 3, 10*time.Second)
	b.waitForAck(t, 3)
	require.Equal(t, map[string]int{"dev-3": 1}, w2.counts())
}

// A range the broker SENT and this process never received is deleted by the floor ack unless
// the loop has read it from the stream first. A thief takes the first four deliveries off the
// durable (a delivery lost on the way, or a zombie's share), the loop is handed what comes
// after, and every one of the four must still be derived exactly once, with the floor passing
// them and nothing left pending or redelivered.
func TestAGapIsFilledBeforeTheFloorPassesIt(t *testing.T) {
	t.Parallel()
	b := startDetectBroker(t)
	store := brokerStore(t)
	nmgr, reader := b.detectManager(t, store)

	b.publishHot(t, 1) // the term's startup replay reaches this head
	thief := &thiefReader{MessageReader: reader, steal: func() {
		for n := uint64(2); n <= 7; n++ {
			b.publishHot(t, n)
		}
		require.Eventually(t, func() bool { return b.resolvedInfo(t).NumPending >= 7 },
			10*time.Second, 20*time.Millisecond, "the stream's events never reached the durable")
		js, err := b.nc.JetStream()
		require.NoError(t, err)
		sub, err := js.PullSubscribe(messaging.StreamSubject(b.instance, streams.ResolvedEvents), b.resolvedDurable(),
			nats.Bind(b.resolvedStream(), b.resolvedDurable()))
		require.NoError(t, err)
		got, err := sub.Fetch(4, nats.MaxWait(5*time.Second))
		require.NoError(t, err)
		require.Len(t, got, 4, "the thief did not take the first four deliveries")
	}}
	seen := &handedOut{MessageReader: thief}
	_, w := b.hotProcessor(t, seen, nmgr, store, 1)

	waitForCheckpoint(t, store, 7, 15*time.Second)
	b.waitForAck(t, 7)
	want := map[string]int{}
	for n := 1; n <= 7; n++ {
		want[fmt.Sprintf("dev-%d", n)] = 1
	}
	require.Equal(t, want, w.counts(), "an event in the lost range was not derived exactly once")
	for seq, n := range seen.seenCount() {
		require.Equal(t, 1, n, "seq %d was handed to the loop %d times", seq, n)
	}
	// The stolen deliveries were never acked on their own. The floor ack removed them.
	require.Eventually(t, func() bool { ci := b.resolvedInfo(t); return ci.NumAckPending == 0 },
		5*time.Second, 50*time.Millisecond, "the floor ack left the lost range pending")
	time.Sleep(2 * brokerAckWait)
	for seq := range seen.seenCount() {
		require.NotContains(t, []uint64{1, 2, 3, 4}, seq, "the lost range %d was redelivered to the loop after the floor passed it", seq)
	}
	require.Zero(t, b.resolvedInfo(t).NumRedelivered, "the broker redelivered something after the floor passed it")
}

// thiefReader runs steal once, before the first live read: the moment the term's replay is
// done and the loop begins reading the durable.
type thiefReader struct {
	messaging.MessageReader
	once  sync.Once
	steal func()
}

func (r *thiefReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	r.once.Do(r.steal)
	return r.MessageReader.ReadMessage(ctx)
}

// Poison with a sequence advances the floor like any message. A batch of valid events and
// poison, ending in poison above everything valid, is acknowledged by ONE ack whose ceiling is
// the last poison, with nothing redelivered and nothing left pending.
func TestPoisonIsAckedThroughTheFloor(t *testing.T) {
	t.Parallel()
	b := startDetectBroker(t)
	store := brokerStore(t)
	nmgr, reader := b.detectManager(t, store)
	seen := &handedOut{MessageReader: reader}
	// Three to a checkpoint, so the burst below is one or two batches, not six.
	_, w := b.hotProcessor(t, seen, nmgr, store, 3)

	b.publishHot(t, 1)
	b.publishBytes(t, []byte("not-a-proto"))
	b.publishHot(t, 3)
	b.publishHot(t, 4)
	b.publishBytes(t, []byte("not-a-proto"))
	last := b.publishBytes(t, []byte("not-a-proto"))
	require.EqualValues(t, 6, last)

	waitForCheckpoint(t, store, 6, 15*time.Second)
	b.waitForAck(t, 6)
	time.Sleep(2 * brokerAckWait) // a delivery that was not acked would come back inside this
	require.Equal(t, map[string]int{"dev-1": 1, "dev-3": 1, "dev-4": 1}, w.counts())
	for seq, n := range seen.seenCount() {
		require.Equal(t, 1, n, "seq %d was delivered %d times: it was not acknowledged", seq, n)
	}
	ci := b.resolvedInfo(t)
	require.Zero(t, ci.NumAckPending)
	require.Zero(t, ci.NumRedelivered)
}

// The durable is created at the committed floor. A durable that is gone (a rebuilt cluster, an
// operator's delete) comes back at the snapshot's sequence plus one, not at the start of the
// stream, so nothing at or below the snapshot is delivered again.
func TestTheDurableIsRecreatedAtTheCommittedFloor(t *testing.T) {
	t.Parallel()
	b := startDetectBroker(t)
	store := brokerStore(t)
	nmgr1, reader1 := b.detectManager(t, store)
	rp1, _ := b.hotProcessor(t, reader1, nmgr1, store, 1)
	for n := uint64(1); n <= 3; n++ {
		b.publishHot(t, n)
	}
	waitForCheckpoint(t, store, 3, 10*time.Second)
	b.waitForAck(t, 3)
	require.NoError(t, rp1.ExecuteStop(context.Background()))
	nmgr1.Conn().Close()

	b.publishHot(t, 4)
	b.publishHot(t, 5)
	js, err := b.nc.JetStream()
	require.NoError(t, err)
	require.NoError(t, js.DeleteConsumer(b.resolvedStream(), b.resolvedDurable()))

	nmgr2, reader2 := b.detectManager(t, store)
	ci := b.resolvedInfo(t)
	require.Equal(t, nats.AckAllPolicy, ci.Config.AckPolicy)
	require.Equal(t, nats.DeliverByStartSequencePolicy, ci.Config.DeliverPolicy)
	require.EqualValues(t, 4, ci.Config.OptStartSeq, "the durable was not created after the committed snapshot")
	seen := &handedOut{MessageReader: reader2}
	_, w := b.hotProcessor(t, seen, nmgr2, store, 1)
	waitForCheckpoint(t, store, 5, 15*time.Second)
	b.waitForAck(t, 5)
	require.Equal(t, map[string]int{"dev-4": 1, "dev-5": 1}, w.counts())
	require.Eventually(t, func() bool { c := seen.seenCount(); return c[4] >= 1 && c[5] >= 1 },
		10*time.Second, 50*time.Millisecond)
	for seq := range seen.seenCount() {
		require.Greater(t, seq, uint64(3), "seq %d, at or below the snapshot, was delivered to the recreated durable", seq)
	}
}

// An instance that ran the previous build has an explicit-ack durable under the ordinary name.
// The ack policy of an existing consumer cannot be changed, so this build binds a durable of
// its own name and retires the old one when it takes the partition (BindTerm), never before.
func TestTheExplicitAckDurableIsReplacedWhenATermStarts(t *testing.T) {
	t.Parallel()
	b := startDetectBroker(t)
	// The stream and the previous build's durable, as they stand on an upgraded instance.
	_, _, _ = b.brokerManager(t, func(m *messaging.NatsManager) error {
		_, err := m.NewWriter(streams.ResolvedEvents)
		return err
	})
	js, err := b.nc.JetStream()
	require.NoError(t, err)
	legacy := messaging.DurableName(b.instance, "event-processing", streams.ResolvedEvents)
	_, err = js.AddConsumer(b.resolvedStream(), &nats.ConsumerConfig{
		Durable: legacy, AckPolicy: nats.AckExplicitPolicy, DeliverPolicy: nats.DeliverAllPolicy,
		FilterSubject: messaging.StreamSubject(b.instance, streams.ResolvedEvents),
	})
	require.NoError(t, err)

	_, reader := b.detectManager(t, brokerStore(t))
	_, err = js.ConsumerInfo(b.resolvedStream(), legacy)
	require.NoError(t, err, "the explicit-ack durable was deleted before this replica led")
	require.Equal(t, nats.AckAllPolicy, b.resolvedInfo(t).Config.AckPolicy)

	require.NoError(t, reader.(messaging.TermBoundReader).BindTerm())
	_, err = js.ConsumerInfo(b.resolvedStream(), legacy)
	require.ErrorIs(t, err, nats.ErrConsumerNotFound, "the explicit-ack durable outlived the term start")
	require.Equal(t, nats.AckAllPolicy, b.resolvedInfo(t).Config.AckPolicy, "the ack-floor durable went with it")
	// And again: a second term finds nothing to delete and is not an error.
	require.NoError(t, reader.(messaging.TermBoundReader).BindTerm())
}

// ---------------------------------------------------------------------------------------------
// the processor against fakes

func totalFloorAcks(acks ...*fakeAck) int {
	n := 0
	for _, a := range acks {
		n += a.floorAcks
	}
	return n
}

// One ack per checkpoint, and it names the HIGHEST committed message, not the last one handled.
// The batch ends with a redelivery of a lower sequence, which is what a broker's redelivery
// order produces and what a "ack the last message" implementation would name.
func TestAFloorAckNamesTheHighestCommittedMessageNotTheLastHandled(t *testing.T) {
	ctx := context.Background()
	rp := newTestProcessor(newTestStore(t), nil, 100)
	require.NoError(t, rp.restore(ctx))
	first := []*fakeAck{{}, {}, {}}
	for i, a := range first {
		rp.handle(msgAt(t, uint64(i+1), a))
	}
	rp.checkpoint(ctx)
	require.Equal(t, 1, totalFloorAcks(first...), "the first checkpoint sent more or fewer than one ack")
	require.Equal(t, 1, first[2].floorAcks, "the first ack did not name seq 3")

	a4, a5, redelivered := &fakeAck{}, &fakeAck{}, &fakeAck{}
	rp.handle(msgAt(t, 4, a4))
	rp.handle(msgAt(t, 5, a5))
	dup := msgAt(t, 2, redelivered)
	dup.NumDelivered = 2
	rp.handle(dup) // handled LAST, and below the engine's sequence
	rp.checkpoint(ctx)

	require.Equal(t, 1, totalFloorAcks(a4, a5, redelivered), "the second checkpoint sent more or fewer than one ack")
	require.Equal(t, 1, a5.floorAcks, "the ack did not name the highest committed message (seq 5)")
	require.Zero(t, redelivered.floorAcks, "the ack named the last message handled, a redelivery of seq 2")
	require.Equal(t, 1, a4.acks, "seq 4 sits below the floor and is deleted by it")
	require.Equal(t, uint64(5), rp.committedSeq)
}

// A redelivered message below the floor changes nothing: it is not reprocessed, the engine is
// not dirtied, and the checkpoint it rides in on sends an ack that does not go backward.
func TestARedeliveredMessageBelowTheFloorIsNotReprocessed(t *testing.T) {
	ctx := context.Background()
	reg := thresholdReg(t)
	w := newSyncDerived()
	rp := newTestProcessor(newTestStore(t), nil, 100)
	rp.registry = reg
	rp.publisher = runtime.NewPublisher(w, reg, (*DetectMetrics)(nil))
	require.NoError(t, rp.restore(ctx))
	for n := uint64(1); n <= 3; n++ {
		rp.handle(measuredMsg(t, n, "acme", fmt.Sprintf("dev-%d", n), "p@1", "temperature", "90", &fakeAck{}))
	}
	rp.checkpoint(ctx)
	require.Equal(t, map[string]int{"dev-1": 1, "dev-2": 1, "dev-3": 1}, w.counts())

	redelivered := &fakeAck{}
	m := measuredMsg(t, 2, "acme", "dev-2", "p@1", "temperature", "90", redelivered)
	m.NumDelivered = 2
	rp.handle(m)
	require.False(t, rp.dirty, "a redelivery below the floor dirtied the engine")
	require.EqualValues(t, 3, rp.engine.LastSeq())
	rp.checkpoint(ctx)
	require.Equal(t, map[string]int{"dev-1": 1, "dev-2": 1, "dev-3": 1}, w.counts(), "a redelivery below the floor was derived again")
	require.Equal(t, 1, redelivered.floorAcks, "the only message in the batch is the one the ack names")
	require.Equal(t, uint64(3), rp.committedSeq, "the ceiling moved off the committed sequence")
}

// Poison with a sequence is committed by Engine.Skip, and the floor lands on it when it is the
// highest. A message with no sequence is never the one acked: 0 names no floor.
func TestPoisonNamesTheFloorAndAnUnsequencedMessageNeverDoes(t *testing.T) {
	ctx := context.Background()
	rp := newTestProcessor(newTestStore(t), nil, 100)
	require.NoError(t, rp.restore(ctx))

	valid, poison, unsequenced := &fakeAck{}, &fakeAck{}, &fakeAck{}
	rp.handle(msgAt(t, 1, valid))
	rp.handle(garbageAt(t, 2, poison))
	un := msgAt(t, 3, unsequenced)
	un.StreamSeq = 0
	rp.handle(un)
	rp.checkpoint(ctx)

	require.Equal(t, uint64(2), rp.committedSeq, "the poison's sequence is part of the commit")
	require.Equal(t, 1, totalFloorAcks(valid, poison, unsequenced), "more or fewer than one ack was sent")
	require.Equal(t, 1, poison.floorAcks, "the ack did not name the poison, the highest committed sequence")
	require.Zero(t, unsequenced.floorAcks, "a message with no sequence named the floor")
	require.Zero(t, unsequenced.acks, "a message with no sequence was reported acked")
}

// A replica that held the partition at the top of the checkpoint and lost it before the ack
// sends nothing. The snapshot it committed stands (the successor restores it); the messages
// redeliver to the leader and are absorbed there.
func TestALeaseLostBetweenTheSnapshotAndTheAckSendsNoAck(t *testing.T) {
	store, save := hookedStore(t)
	rp := newTestProcessor(store, nil, 1)
	require.NoError(t, rp.restore(context.Background()))
	rp.Lease = &messaging.DistributedLease{}
	rp.Gate = NewTermGate()
	var held atomic.Bool
	held.Store(true)
	rp.Gate.Enter(held.Load)
	save.arm(func() { held.Store(false) }) // the lease lapses as the snapshot is written

	ack := &fakeAck{}
	rp.handle(msgAt(t, 1, ack))
	rp.checkpoint(context.Background())

	snap, found, err := store.Load(context.Background(), "singleton")
	require.NoError(t, err)
	require.True(t, found, "vacuous: the snapshot was not committed, so the ack was never reached")
	require.EqualValues(t, 1, snap.StreamSeq)
	require.Zero(t, ack.floorAcks, "an ack was sent after the lease was lost")
	require.Zero(t, ack.acks)
}

// The start position of the durable is read from the committed row.
func TestTheFloorStartIsTheCommittedSequencePlusOne(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	start := CommittedFloorStart(store, "singleton")
	got, err := start(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, got, "with no snapshot the durable starts at the first sequence")

	require.NoError(t, store.Save(ctx, &model.DetectSnapshot{PartitionId: "singleton", StreamSeq: 41, Payload: []byte("x")}))
	got, err = start(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 42, got)

	_, err = CommittedFloorStart(failingSeqLoader{}, "singleton")(ctx)
	require.Error(t, err, "a store that cannot be read must fail the bind, not start the durable at 1")
}

type failingSeqLoader struct{}

func (failingSeqLoader) LoadCommittedSeq(context.Context, string) (int64, bool, error) {
	return 0, false, errors.New("store down")
}

// A stream recreated under a snapshot that is ahead of it. The durable is created at the stale
// row's sequence plus one, past everything the new stream holds; the term build clears the row
// and must put the durable back at the new stream's start, or nothing is delivered until the
// head passes the old position.
func TestAStreamRecreatedUnderAnAheadSnapshotDeliversFromItsStart(t *testing.T) {
	t.Parallel()
	b := startDetectBroker(t)
	store := brokerStore(t)
	nmgr1, reader1 := b.detectManager(t, store)
	rp1, _ := b.hotProcessor(t, reader1, nmgr1, store, 1)
	for n := uint64(1); n <= 3; n++ {
		b.publishHot(t, n)
	}
	waitForCheckpoint(t, store, 3, 10*time.Second)
	b.waitForAck(t, 3)
	require.NoError(t, rp1.ExecuteStop(context.Background()))
	nmgr1.Conn().Close()

	// The stream is recreated: the new one starts again at sequence 1 and the snapshot says 3.
	js, err := b.nc.JetStream()
	require.NoError(t, err)
	require.NoError(t, js.DeleteStream(b.resolvedStream()))
	nmgr2, reader2 := b.detectManager(t, store)
	require.EqualValues(t, 4, b.resolvedInfo(t).Config.OptStartSeq, "vacuous: the durable was not created past the new stream's head")
	b.publishHot(t, 11)
	b.publishHot(t, 12)

	seen := &recreateSeen{MessageReader: reader2}
	_, w := b.hotProcessor(t, seen, nmgr2, store, 1)
	waitForCheckpoint(t, store, 2, 15*time.Second)
	b.waitForAck(t, 2)
	require.Equal(t, map[string]int{"dev-11": 1, "dev-12": 1}, w.counts(), "the new stream's events were not derived")
	require.EqualValues(t, 1, b.resolvedInfo(t).Config.OptStartSeq, "the durable was not recreated at the new stream's start")
	require.Eventually(t, func() bool { return b.resolvedInfo(t).NumAckPending == 0 },
		5*time.Second, 50*time.Millisecond)
}

// recreateSeen forwards the AckFloorRecreator of the reader it wraps, which an embedded
// interface value does not.
type recreateSeen struct{ messaging.MessageReader }

func (r *recreateSeen) RecreateAtFloor() error {
	return r.MessageReader.(messaging.AckFloorRecreator).RecreateAtFloor()
}

// THE SAME LOSS, AT THE WIDTH FETCH-AHEAD MAKES POSSIBLE, THROUGH THE REAL READER AND THE REAL
// RANGE READ. The reader fetches 128 at a time with the next pull already in flight; a thief
// takes a whole pull's worth off the durable (the deliveries the broker counted as sent that
// a dropped connection never delivered), and the floor ack would delete them unless the loop
// reads them from the stream first. Every one must be derived exactly once, in a single range
// read that takes the direct path, with the floor passing them and nothing left pending.
func TestAWholeLostBatchIsFilledWithFetchAheadOn(t *testing.T) {
	t.Parallel()
	const total, batch = 400, 128
	b := startDetectBroker(t)
	b.fetch = mscfg.NatsFetchConfiguration{Batch: batch, Ahead: true}
	store := brokerStore(t)
	nmgr, reader := b.detectManager(t, store)

	b.publishHot(t, 1) // the term's startup replay reaches this head
	thief := &thiefReader{MessageReader: reader, steal: func() {
		for n := uint64(2); n <= total; n++ {
			b.publishHot(t, n)
		}
		require.Eventually(t, func() bool { return b.resolvedInfo(t).NumPending >= total },
			10*time.Second, 20*time.Millisecond, "the stream's events never reached the durable")
		js, err := b.nc.JetStream()
		require.NoError(t, err)
		sub, err := js.PullSubscribe(messaging.StreamSubject(b.instance, streams.ResolvedEvents), b.resolvedDurable(),
			nats.Bind(b.resolvedStream(), b.resolvedDurable()))
		require.NoError(t, err)
		got, err := sub.Fetch(batch, nats.MaxWait(5*time.Second))
		require.NoError(t, err)
		require.Len(t, got, batch, "the thief did not take a whole pull")
	}}
	seen := &handedOut{MessageReader: thief}
	_, w := b.hotProcessor(t, seen, nmgr, store, 50)

	waitForCheckpoint(t, store, total, 30*time.Second)
	b.waitForAck(t, total)
	want := map[string]int{}
	for n := 1; n <= total; n++ {
		want[fmt.Sprintf("dev-%d", n)] = 1
	}
	require.Equal(t, want, w.counts(), "an event in the lost range was not derived exactly once")
	for seq := uint64(1); seq <= batch; seq++ { // the thief took the durable's first pull: 1..batch
		require.Zero(t, seen.seenCount()[seq], "the stolen sequence %d was handed to the loop by the reader", seq)
	}
	require.Eventually(t, func() bool { return b.resolvedInfo(t).NumAckPending == 0 },
		5*time.Second, 50*time.Millisecond, "the floor ack left the lost range pending")
}
