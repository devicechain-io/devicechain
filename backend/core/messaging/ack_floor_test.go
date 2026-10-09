// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	nats "github.com/nats-io/nats.go"
)

const floorSuffix = streams.AlarmEvents

// floorFixture is one manager on an embedded server with a writer on floorSuffix. Readers are
// built on the same manager, so they share one durable name.
type floorFixture struct {
	t       *testing.T
	nmgr    *NatsManager
	writer  MessageWriter
	ctx     context.Context
	stream  string
	durable string
	n       int
}

func newFloorFixture(t *testing.T, ackWait time.Duration) *floorFixture {
	t.Helper()
	srv := startEmbeddedServer(t)
	area := uniqueArea("ackfloor")
	ctx := core.WithTenant(context.Background(), "acme")
	f := &floorFixture{t: t, ctx: ctx,
		stream: StreamName("test", floorSuffix), durable: DurableName("test", area, floorSuffix)}
	f.nmgr = NewNatsManager(testMicroservice(t, srv, area), core.NewNoOpLifecycleCallbacks(),
		func(n *NatsManager) error {
			w, err := n.NewWriter(floorSuffix)
			f.writer = w
			return err
		})
	if ackWait > 0 {
		f.nmgr.SetAckWaitForTesting(t, ackWait)
	}
	if err := f.nmgr.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := f.nmgr.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = f.nmgr.Stop(ctx) })
	return f
}

// publish appends n messages and returns the stream sequence of the last one.
func (f *floorFixture) publish(n int) {
	f.t.Helper()
	for i := 0; i < n; i++ {
		f.n++
		if err := f.writer.WriteMessages(f.ctx, Message{Value: []byte(fmt.Sprintf("m%d", f.n))}); err != nil {
			f.t.Fatalf("write: %v", err)
		}
	}
}

func (f *floorFixture) info() *nats.ConsumerInfo {
	f.t.Helper()
	info, err := f.nmgr.js.ConsumerInfo(f.stream, f.durable)
	if err != nil {
		f.t.Fatalf("consumer info: %v", err)
	}
	return info
}

func (f *floorFixture) reader(opts ...ReaderOption) (*natsReader, error) {
	r, err := f.nmgr.NewReader(floorSuffix, opts...)
	if err != nil {
		return nil, err
	}
	return r.(*natsReader), nil
}

func fixedStart(seq uint64) ReaderOption {
	return ReaderWithAckFloor(func(context.Context) (uint64, error) { return seq, nil })
}

func (f *floorFixture) read(r MessageReader) Message {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
	defer cancel()
	m, err := r.ReadMessage(ctx)
	if err != nil {
		f.t.Fatalf("read: %v", err)
	}
	return m
}

func (f *floorFixture) readN(r MessageReader, n int) []Message {
	f.t.Helper()
	out := make([]Message, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, f.read(r))
	}
	return out
}

func (f *floorFixture) seqs(msgs []Message) []uint64 {
	out := make([]uint64, len(msgs))
	for i, m := range msgs {
		out[i] = m.StreamSeq
	}
	return out
}

// waitFor polls the durable until cond holds.
func (f *floorFixture) waitFor(what string, cond func(*nats.ConsumerInfo) bool) *nats.ConsumerInfo {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		info := f.info()
		if cond(info) {
			return info
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("timed out waiting for %s; ack floor %d, ack pending %d, delivered %d",
				what, info.AckFloor.Stream, info.NumAckPending, info.Delivered.Stream)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// settle gives an ack that was NOT supposed to be sent time to show up if it was.
func settle() { time.Sleep(400 * time.Millisecond) }

// A new durable is created AckAll, at the start sequence the callback returned, and the first
// delivery is that sequence.
func TestAckFloorCreatesTheDurableAtTheStartSequence(t *testing.T) {
	f := newFloorFixture(t, 0)
	f.publish(5)
	var calls atomic.Int32
	r, err := f.reader(ReaderWithAckFloor(func(context.Context) (uint64, error) {
		calls.Add(1)
		return 3, nil
	}))
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	info := f.info()
	if info.Config.AckPolicy != nats.AckAllPolicy {
		t.Fatalf("ack policy = %v, want AckAll", info.Config.AckPolicy)
	}
	if info.Config.DeliverPolicy != nats.DeliverByStartSequencePolicy || info.Config.OptStartSeq != 3 {
		t.Fatalf("deliver policy %v start seq %d, want by-start-sequence at 3",
			info.Config.DeliverPolicy, info.Config.OptStartSeq)
	}
	if calls.Load() != 1 {
		t.Fatalf("start called %d times, want 1", calls.Load())
	}
	got := f.seqs(f.readN(r, 3))
	if !reflect.DeepEqual(got, []uint64{3, 4, 5}) {
		t.Fatalf("delivered %v, want [3 4 5] (sequences below the start must not be delivered)", got)
	}
}

func TestAckFloorRefusesAZeroStart(t *testing.T) {
	f := newFloorFixture(t, 0)
	f.publish(1)
	_, err := f.reader(fixedStart(0))
	if err == nil {
		t.Fatal("a start of sequence 0 was accepted")
	}
	if _, ierr := f.nmgr.js.ConsumerInfo(f.stream, f.durable); !errors.Is(ierr, nats.ErrConsumerNotFound) {
		t.Fatalf("a durable was created for a refused start (info err %v)", ierr)
	}
}

// Binding an existing AckAll durable neither recreates nor reconfigures it, and does not even
// ask for a start position. A default-config AddConsumer against it would be refused by the
// server, so success is itself proof that the path avoided it; the timestamps and config
// make that explicit.
func TestAckFloorBindsAnExistingDurableWithoutTouchingIt(t *testing.T) {
	f := newFloorFixture(t, 0)
	f.publish(4)
	r, err := f.reader(fixedStart(1))
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	msgs := f.readN(r, 4)
	if _, err := AckThrough(msgs, 2); err != nil {
		t.Fatalf("ack through: %v", err)
	}
	before := f.waitFor("floor 2", func(i *nats.ConsumerInfo) bool { return i.AckFloor.Stream == 2 })

	var called atomic.Int32
	r.ackFloorStart = func(context.Context) (uint64, error) { called.Add(1); return 99, nil }
	if err := r.bind(); err != nil {
		t.Fatalf("rebind of an existing AckAll durable: %v", err)
	}
	if called.Load() != 0 {
		t.Fatal("start was called although the durable already existed")
	}
	after := f.info()
	if !after.Created.Equal(before.Created) {
		t.Fatalf("durable recreated: created %v -> %v", before.Created, after.Created)
	}
	if !reflect.DeepEqual(after.Config, before.Config) {
		t.Fatalf("durable reconfigured:\n before %+v\n after  %+v", before.Config, after.Config)
	}
	if after.AckFloor.Stream != 2 {
		t.Fatalf("ack floor moved on rebind: %d, want 2", after.AckFloor.Stream)
	}
	// And the rebound reader works: a fresh publish arrives, past everything already delivered.
	f.publish(1)
	if m := f.read(r); m.StreamSeq != 5 {
		t.Fatalf("first message after rebind has seq %d, want 5", m.StreamSeq)
	}
}

// A moved filter is reconciled with an update that carries the existing ack policy and start
// sequence, which the server would otherwise refuse to change.
func TestAckFloorReconcilesAMovedFilterKeepingTheStartFields(t *testing.T) {
	js := scratchJetStream(t, "floor-filter")
	if _, err := js.AddStream(&nats.StreamConfig{
		Name: "FLOORFILTER", Subjects: []string{"test.*.responses", "test.*.responses.*"},
	}); err != nil {
		t.Fatalf("add stream: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := js.Publish("test.acme.responses", []byte("x")); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	start := func(context.Context) (uint64, error) { return 2, nil }
	old := readerAt(js, "FLOORFILTER", "mover", "test.*.responses")
	old.ackFloorStart = start
	if err := old.bind(); err != nil {
		t.Fatalf("first bind: %v", err)
	}
	moved := readerAt(js, "FLOORFILTER", "mover", "test.*.responses.*")
	moved.ackFloorStart = func(context.Context) (uint64, error) { return 3, nil }
	if err := moved.bind(); err != nil {
		t.Fatalf("bind with a moved filter: %v", err)
	}
	info, err := js.ConsumerInfo("FLOORFILTER", "mover")
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	if info.Config.FilterSubject != "test.*.responses.*" {
		t.Fatalf("filter = %q, want the moved filter", info.Config.FilterSubject)
	}
	if info.Config.AckPolicy != nats.AckAllPolicy || info.Config.OptStartSeq != 2 {
		t.Fatalf("policy %v start %d after reconcile, want AckAll and the original start 2",
			info.Config.AckPolicy, info.Config.OptStartSeq)
	}
}

// An existing explicit-ack durable is a loud refusal, never a fallback.
func TestAckFloorRefusesAnExistingExplicitDurable(t *testing.T) {
	f := newFloorFixture(t, 0)
	f.publish(2)
	if _, err := f.reader(); err != nil {
		t.Fatalf("plain reader: %v", err)
	}
	var called atomic.Int32
	_, err := f.reader(ReaderWithAckFloor(func(context.Context) (uint64, error) { called.Add(1); return 1, nil }))
	if !errors.Is(err, ErrAckFloorPolicyMismatch) {
		t.Fatalf("err = %v, want ErrAckFloorPolicyMismatch", err)
	}
	if called.Load() != 0 {
		t.Fatal("start was consulted for a durable that already existed")
	}
	if got := f.info().Config.AckPolicy; got != nats.AckExplicitPolicy {
		t.Fatalf("the existing durable's policy is now %v; it must be left alone", got)
	}
}

func TestAckFloorCannotBeCombinedWithOptionsItContradicts(t *testing.T) {
	f := newFloorFixture(t, 0)
	if _, err := f.reader(fixedStart(1), ReaderWithDeliverNew()); err == nil {
		t.Fatal("ReaderWithAckFloor + ReaderWithDeliverNew was accepted")
	}
	if _, err := f.reader(fixedStart(1), ReaderWithCapacity(2)); err == nil {
		t.Fatal("ReaderWithAckFloor + ReaderWithCapacity was accepted")
	}
}

// A bare Ack on an ack-floor reader errors and sends nothing.
func TestAckFloorMessageAckIsRefused(t *testing.T) {
	f := newFloorFixture(t, 0)
	f.publish(3)
	r, err := f.reader(fixedStart(1))
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	msgs := f.readN(r, 3)
	if got := msgs[2].Ack(); !errors.Is(got, ErrAckFloorReader) {
		t.Fatalf("Ack() = %v, want ErrAckFloorReader", got)
	}
	settle()
	info := f.info()
	if info.NumAckPending != 3 || info.AckFloor.Stream != 0 {
		t.Fatalf("ack pending %d, floor %d after a refused ack; want 3 and 0", info.NumAckPending, info.AckFloor.Stream)
	}
}

// AckThrough acks exactly what is at or below the ceiling, picking the highest sequence even
// when it is not the last message in the slice, and the rest is redelivered after AckWait.
func TestAckThroughAcksExactlyTheFloor(t *testing.T) {
	f := newFloorFixture(t, time.Second)
	f.publish(6)
	r, err := f.reader(fixedStart(1))
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	msgs := f.readN(r, 6)
	// Reverse so the LAST message is the lowest sequence: acking msgs[len-1] would ack seq 1.
	rev := make([]Message, len(msgs))
	for i, m := range msgs {
		rev[len(msgs)-1-i] = m
	}
	res, err := AckThrough(rev, 4)
	if err != nil {
		t.Fatalf("AckThrough: %v", err)
	}
	if res.AckedSeq != 4 || res.Above != 2 || res.Unsequenced != 0 {
		t.Fatalf("result %+v, want acked 4, 2 above", res)
	}
	info := f.waitFor("floor 4", func(i *nats.ConsumerInfo) bool { return i.AckFloor.Stream == 4 })
	if info.NumAckPending != 2 {
		t.Fatalf("ack pending %d, want 2 (seq 5 and 6)", info.NumAckPending)
	}
	// Only what is above the floor comes back.
	redelivered := f.seqs(f.readN(r, 2))
	if !reflect.DeepEqual(redelivered, []uint64{5, 6}) {
		t.Fatalf("redelivered %v, want [5 6]", redelivered)
	}
}

// Nothing at or below the ceiling, a zero ceiling, an empty batch, and a sequence-0 message
// all send no ack, and sequence 0 is never the one acked.
func TestAckThroughWithNothingToAckDoesNothing(t *testing.T) {
	f := newFloorFixture(t, 0)
	f.publish(3)
	r, err := f.reader(fixedStart(2)) // delivers 2 and 3
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	msgs := f.readN(r, 2)

	for name, ceiling := range map[string]uint64{"floor below every message": 1, "ceiling 0": 0} {
		res, err := AckThrough(msgs, ceiling)
		if err != nil || res.AckedSeq != 0 {
			t.Fatalf("%s: result %+v err %v, want nothing acked", name, res, err)
		}
	}
	if res, err := AckThrough(nil, 10); err != nil || res.AckedSeq != 0 {
		t.Fatalf("empty batch: %+v %v", res, err)
	}
	// A message whose metadata could not be read has sequence 0 and must never be the floor.
	unreadable := Message{ack: floorAck{}, StreamSeq: 0}
	res, err := AckThrough([]Message{unreadable}, 10)
	if err != nil || res.AckedSeq != 0 || res.Unsequenced != 1 {
		t.Fatalf("sequence-0 message: %+v %v, want unsequenced 1 and nothing acked", res, err)
	}
	settle()
	info := f.info()
	// A durable started at sequence 2 begins with its floor at 1.
	if info.NumAckPending != 2 || info.AckFloor.Stream != 1 {
		t.Fatalf("ack pending %d, floor %d; want 2 and 1 — something was acked", info.NumAckPending, info.AckFloor.Stream)
	}
}

// A message that did not come from an ack-floor reader makes AckThrough refuse the whole call.
func TestAckThroughRefusesAForeignMessage(t *testing.T) {
	f := newFloorFixture(t, 0)
	f.publish(2)
	r, err := f.reader(fixedStart(1))
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	msgs := f.readN(r, 2)
	_, err = AckThrough(append(msgs, Message{StreamSeq: 1}), 10)
	if !errors.Is(err, ErrNotAckFloorMessage) {
		t.Fatalf("err = %v, want ErrNotAckFloorMessage", err)
	}
	settle()
	if info := f.info(); info.NumAckPending != 2 {
		t.Fatalf("ack pending %d, want 2 — a refused AckThrough must ack nothing", info.NumAckPending)
	}
}

// The ordinary reader is unchanged: AckExplicit, DeliverAll, and one ack acks one message.
func TestOrdinaryReaderStillAcksIndividually(t *testing.T) {
	f := newFloorFixture(t, 0)
	f.publish(3)
	r, err := f.reader()
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	info := f.info()
	if info.Config.AckPolicy != nats.AckExplicitPolicy || info.Config.DeliverPolicy != nats.DeliverAllPolicy {
		t.Fatalf("ordinary durable is %v / %v, want AckExplicit / DeliverAll",
			info.Config.AckPolicy, info.Config.DeliverPolicy)
	}
	msgs := f.readN(r, 3)
	if err := msgs[2].Ack(); err != nil {
		t.Fatalf("ack: %v", err)
	}
	f.waitFor("one acked", func(i *nats.ConsumerInfo) bool { return i.NumAckPending == 2 })
	if _, err := AckThrough(msgs, 3); !errors.Is(err, ErrNotAckFloorMessage) {
		t.Fatalf("AckThrough on an ordinary reader's messages: %v, want ErrNotAckFloorMessage", err)
	}
}

// A deleted ack-floor durable is recreated at the start callback's CURRENT position, not at
// the original one and not from the beginning of the stream.
func TestAckFloorSelfHealRestartsAtTheStartCallback(t *testing.T) {
	f := newFloorFixture(t, 0)
	f.publish(5)
	var next atomic.Uint64
	next.Store(1)
	r, err := f.reader(ReaderWithAckFloor(func(context.Context) (uint64, error) { return next.Load(), nil }))
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	if m := f.read(r); m.StreamSeq != 1 {
		t.Fatalf("first seq %d, want 1", m.StreamSeq)
	}
	next.Store(4)
	if err := f.nmgr.js.DeleteConsumer(f.stream, f.durable); err != nil {
		t.Fatalf("delete consumer: %v", err)
	}
	// The rest of the first batch (2..5) is already in the reader's buffer. The read after
	// that fetches from the deleted durable, rebinds, and must land on the NEW start.
	if drained := f.seqs(f.readN(r, 4)); !reflect.DeepEqual(drained, []uint64{2, 3, 4, 5}) {
		t.Fatalf("buffered batch %v, want [2 3 4 5]", drained)
	}
	if got := f.read(r).StreamSeq; got != 4 {
		t.Fatalf("first message after self-heal has seq %d, want 4 (the callback's current position)", got)
	}
	if s := f.info().Config.OptStartSeq; s != 4 {
		t.Fatalf("recreated durable start = %d, want 4", s)
	}
}
