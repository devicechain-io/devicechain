// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"context"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// garbageAt builds an unparseable payload on a tenanted subject at stream sequence seq.
func garbageAt(seq uint64, ack *fakeAck) messaging.Message {
	m := messaging.NewConsumedMessage(testSubject, []byte("not-a-proto"), 0, nil, ack)
	m.StreamSeq = seq
	return m
}

// untenantedAt builds a well-formed payload on a subject that carries no tenant.
func untenantedAt(seq uint64, ack *fakeAck, payload []byte) messaging.Message {
	m := messaging.NewConsumedMessage("no-tenant-here", payload, 0, nil, ack)
	m.StreamSeq = seq
	return m
}

func snapshotBytes(t *testing.T, rp *ResolvedEventsProcessor) []byte {
	t.Helper()
	b, err := rp.engine.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return b
}

// Poison in the middle of a batch advances LastSeq in order, the valid messages around it
// are still applied, and the checkpoint records the advanced sequence.
func TestPoisonMidBatchAdvancesLastSeqInOrder(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	rp := newTestProcessor(store, nil, 100)
	if err := rp.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}

	acks := []*fakeAck{{}, {}, {}, {}}
	rp.handle(msgAt(t, 1, acks[0]))
	if got := rp.engine.LastSeq(); got != 1 {
		t.Fatalf("after valid 1: lastSeq = %d", got)
	}
	rp.handle(garbageAt(2, acks[1]))
	if got := rp.engine.LastSeq(); got != 2 {
		t.Fatalf("poison 2 must advance lastSeq to 2, got %d", got)
	}
	wmAfterValid := rp.engine.Watermark()
	rp.handle(untenantedAt(3, acks[2], resolvedBytes(t, testBase)))
	if got := rp.engine.LastSeq(); got != 3 {
		t.Fatalf("untenanted 3 must advance lastSeq to 3, got %d", got)
	}
	if !rp.engine.Watermark().Equal(wmAfterValid) {
		t.Fatal("skipping poison must not move the watermark")
	}
	rp.handle(msgAt(t, 4, acks[3]))
	if got := rp.engine.LastSeq(); got != 4 {
		t.Fatalf("valid 4 after poison: lastSeq = %d, want 4", got)
	}
	if want := testBase.Add(4 * time.Second); !rp.engine.Watermark().Equal(want) {
		t.Fatalf("valid message after poison not applied: watermark %v want %v", rp.engine.Watermark(), want)
	}

	rp.checkpoint(ctx)
	snap, ok, err := store.Load(ctx, "singleton")
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if snap.StreamSeq != 4 {
		t.Fatalf("checkpoint seq = %d, want 4", snap.StreamSeq)
	}
	for i, a := range acks {
		if a.acks != 1 {
			t.Fatalf("message %d acks = %d, want 1 (explicit-ack behaviour unchanged)", i+1, a.acks)
		}
	}
}

// Poison at the tail of a batch: LastSeq ends at the poison's sequence and a snapshot taken
// then records it, even though no valid message follows.
func TestPoisonAtTailEndsLastSeqAtPoison(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	rp := newTestProcessor(store, nil, 100)
	if err := rp.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	rp.handle(msgAt(t, 1, &fakeAck{}))
	rp.handle(msgAt(t, 2, &fakeAck{}))
	tail := &fakeAck{}
	rp.handle(garbageAt(3, tail))
	if got := rp.engine.LastSeq(); got != 3 {
		t.Fatalf("tail poison: lastSeq = %d, want 3", got)
	}
	rp.checkpoint(ctx)
	snap, _, _ := store.Load(ctx, "singleton")
	if snap.StreamSeq != 3 {
		t.Fatalf("snapshot seq = %d, want 3", snap.StreamSeq)
	}
	if tail.acks != 1 {
		t.Fatalf("tail poison acks = %d, want 1", tail.acks)
	}

	// A batch that is ONLY poison still checkpoints (the loop is dirty), so the floor moves.
	store2 := newTestStore(t)
	rp2 := newTestProcessor(store2, nil, 100)
	if err := rp2.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	rp2.handle(garbageAt(7, &fakeAck{}))
	if !rp2.dirty {
		t.Fatal("poison must mark the loop dirty so the advanced LastSeq is snapshotted")
	}
	rp2.checkpoint(ctx)
	snap2, ok, _ := store2.Load(ctx, "singleton")
	if !ok || snap2.StreamSeq != 7 {
		t.Fatalf("poison-only checkpoint: ok=%v seq=%d, want 7", ok, snap2.StreamSeq)
	}
}

// A valid message with a LOWER sequence that arrives after a skipped higher poison (a
// redelivery out of order) is dropped: LastSeq is the single definition of "done", and the
// engine's guard already drops a lower sequence behind a higher VALID one, so skipping adds
// no new case. It is not applied and does not move LastSeq or the watermark.
func TestValidLowerSeqAfterSkippedPoisonIsDropped(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	rp := newTestProcessor(store, nil, 100)
	if err := rp.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	rp.handle(garbageAt(5, &fakeAck{}))
	rp.checkpoint(ctx)
	before := snapshotBytes(t, rp)

	late := &fakeAck{}
	rp.handle(msgAt(t, 3, late))
	if got := rp.engine.LastSeq(); got != 5 {
		t.Fatalf("lastSeq = %d, want 5 (never backwards)", got)
	}
	if rp.dirty {
		t.Fatal("a dropped lower-seq message must not mark the loop dirty")
	}
	if !rp.engine.Watermark().IsZero() {
		t.Fatalf("a dropped message must not move the watermark, got %v", rp.engine.Watermark())
	}
	if !bytes.Equal(before, snapshotBytes(t, rp)) {
		t.Fatal("engine state changed by a dropped lower-seq message")
	}
	rp.checkpoint(ctx)
	if late.acks != 1 {
		t.Fatalf("dropped message acks = %d, want 1", late.acks)
	}
}

// A duplicate at or below LastSeq is dropped, and the drop happens BEFORE the tenant parse
// and the protobuf decode: duplicates never reach the decode, where a message above LastSeq does.
func TestDuplicateIsDroppedBeforeDecode(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	rp := newTestProcessor(store, nil, 100)
	if err := rp.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	rp.handle(msgAt(t, 1, &fakeAck{}))
	rp.handle(msgAt(t, 2, &fakeAck{}))
	rp.checkpoint(ctx)

	decodes := 0
	rp.decodeResolved = func(b []byte) (*dmmodel.ResolvedEvent, error) {
		decodes++
		return dmproto.UnmarshalResolvedEvent(b)
	}

	if rp.applyResolved(msgAt(t, 2, &fakeAck{})) {
		t.Fatal("duplicate must not report an advance")
	}
	if rp.applyResolved(garbageAt(1, &fakeAck{})) {
		t.Fatal("poison duplicate must not report an advance")
	}
	if rp.engine.LastSeq() != 2 || rp.dirty {
		t.Fatalf("duplicate moved state: lastSeq=%d dirty=%v", rp.engine.LastSeq(), rp.dirty)
	}
	if decodes != 0 {
		t.Fatalf("duplicates reached the decode %d times; the guard must run first", decodes)
	}

	// Control: a message above LastSeq does reach the decode.
	if !rp.applyResolved(garbageAt(3, &fakeAck{})) {
		t.Fatal("poison above LastSeq must be skipped (advance)")
	}
	if decodes != 1 {
		t.Fatalf("control did not exercise the decode path: decodes=%d", decodes)
	}
}

// Seq 0 (unreadable broker metadata) is never recorded as a position.
func TestSeqZeroIsNeverSkipped(t *testing.T) {
	rp := newTestProcessor(newTestStore(t), nil, 100)
	if err := rp.restore(context.Background()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if rp.applyResolved(garbageAt(0, &fakeAck{})) || rp.engine.LastSeq() != 0 || rp.dirty {
		t.Fatal("seq 0 must neither advance the engine nor dirty the loop")
	}
}

// Crash after a checkpoint that followed a Skip: the restored engine starts at the skipped
// sequence, replay resumes after it (not at the last valid message), and the final state
// equals a run that never crashed.
func TestRestoreAfterSkipResumesAfterSkippedSeq(t *testing.T) {
	ctx := context.Background()
	stream := func() []messaging.Message {
		return []messaging.Message{
			msgAt(t, 1, &fakeAck{}),
			garbageAt(2, &fakeAck{}),
			msgAt(t, 3, &fakeAck{}),
			garbageAt(4, &fakeAck{}),
			msgAt(t, 5, &fakeAck{}),
		}
	}

	// Control: no crash.
	control := newTestProcessor(newTestStore(t), nil, 100)
	if err := control.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, m := range stream() {
		control.handle(m)
	}
	control.checkpoint(ctx)

	// Crashed run: checkpoint after the first four (ending on a skip), then restart.
	store := newTestStore(t)
	p1 := newTestProcessor(store, nil, 100)
	if err := p1.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, m := range stream()[:4] {
		p1.handle(m)
	}
	p1.checkpoint(ctx)

	p2 := newTestProcessor(store, nil, 100)
	if err := p2.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if p2.engine.LastSeq() != 4 {
		t.Fatalf("restored lastSeq = %d, want 4 (the skipped poison)", p2.engine.LastSeq())
	}
	opener := &fakeReplayOpener{msgs: stream(), head: 5}
	p2.Replay = opener
	if err := p2.replayToHead(); err != nil {
		t.Fatalf("replayToHead: %v", err)
	}
	if opener.lastStart != 5 {
		t.Fatalf("replay started at %d, want 5 (after the skipped seq)", opener.lastStart)
	}
	if !bytes.Equal(snapshotBytes(t, control), snapshotBytes(t, p2)) {
		t.Fatal("restored+replayed state differs from the no-crash run")
	}

	// Replaying the WHOLE stream from empty re-skips the poison at the same sequences and
	// ends in the same state.
	p3 := newTestProcessor(newTestStore(t), nil, 100)
	if err := p3.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	p3.Replay = &fakeReplayOpener{msgs: stream(), head: 5}
	if err := p3.replayToHead(); err != nil {
		t.Fatalf("replayToHead: %v", err)
	}
	if !bytes.Equal(snapshotBytes(t, control), snapshotBytes(t, p3)) {
		t.Fatal("full replay with poison in the stream differs from live")
	}
}

// A stream that ENDS in poison (an undecodable payload and an untenanted subject) must be
// deterministic: the live run, a full replay from empty, and a restore from a snapshot taken
// at the end followed by replay all produce byte-identical engine snapshots, and that snapshot
// records the final poison's sequence. Without Skip on the decode-failure path the engine
// would stop at the last valid sequence (5), and the restored replay would start at 6, not 7.
func TestStreamEndingInPoisonIsDeterministicAcrossLiveReplayAndRestore(t *testing.T) {
	ctx := context.Background()
	stream := func() []messaging.Message {
		return []messaging.Message{
			msgAt(t, 1, &fakeAck{}),
			garbageAt(2, &fakeAck{}),
			msgAt(t, 3, &fakeAck{}),
			untenantedAt(4, &fakeAck{}, resolvedBytes(t, testBase)),
			msgAt(t, 5, &fakeAck{}),
			garbageAt(6, &fakeAck{}),
			garbageAt(7, &fakeAck{}),
		}
	}
	const last = 7

	// Live.
	store := newTestStore(t)
	live := newTestProcessor(store, nil, 100)
	if err := live.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, m := range stream() {
		live.handle(m)
	}
	live.checkpoint(ctx)
	if live.engine.LastSeq() != last {
		t.Fatalf("live lastSeq = %d, want %d", live.engine.LastSeq(), last)
	}
	want := snapshotBytes(t, live)
	if !bytes.Contains(want, []byte(`"lastSeq":7`)) {
		t.Fatalf("snapshot does not record lastSeq 7: %s", want)
	}

	// Full replay from empty.
	full := newTestProcessor(newTestStore(t), nil, 100)
	if err := full.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	full.Replay = &fakeReplayOpener{msgs: stream(), head: last}
	if err := full.replayToHead(); err != nil {
		t.Fatalf("replayToHead: %v", err)
	}
	if !bytes.Equal(want, snapshotBytes(t, full)) {
		t.Fatal("full replay differs from live")
	}

	// Restore from the live run's final snapshot, then replay: nothing is left to replay.
	rest := newTestProcessor(store, nil, 100)
	if err := rest.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if rest.engine.LastSeq() != last {
		t.Fatalf("restored lastSeq = %d, want %d", rest.engine.LastSeq(), last)
	}
	opener := &fakeReplayOpener{msgs: stream(), head: last}
	rest.Replay = opener
	if err := rest.replayToHead(); err != nil {
		t.Fatalf("replayToHead: %v", err)
	}
	if opener.lastStart != last+1 {
		t.Fatalf("replay started at %d, want %d", opener.lastStart, last+1)
	}
	if !bytes.Equal(want, snapshotBytes(t, rest)) {
		t.Fatal("restore+replay differs from live")
	}

	// Restore from a snapshot taken mid-stream (ending on poison 4), then replay the rest.
	store2 := newTestStore(t)
	pre := newTestProcessor(store2, nil, 100)
	if err := pre.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, m := range stream()[:4] {
		pre.handle(m)
	}
	pre.checkpoint(ctx)
	mid := newTestProcessor(store2, nil, 100)
	if err := mid.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	mid.Replay = &fakeReplayOpener{msgs: stream(), head: last}
	if err := mid.replayToHead(); err != nil {
		t.Fatalf("replayToHead: %v", err)
	}
	if !bytes.Equal(want, snapshotBytes(t, mid)) {
		t.Fatal("mid-stream restore+replay differs from live")
	}
}
