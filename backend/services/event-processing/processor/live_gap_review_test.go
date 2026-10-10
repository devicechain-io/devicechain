// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// pendingProbe is a backlog probe that reports a fixed pending count and counts how often it
// was asked.
type pendingProbe struct {
	calls   atomic.Int32
	pending uint64
}

func (p *pendingProbe) Backlog(context.Context) (uint64, uint64, error) {
	p.calls.Add(1)
	return p.pending, 0, nil
}

// Idle-advance must not run on a wall-clock frontier ahead of events that are not yet applied.
//
// The rule here arms a timer (a duration hold), so idle-advance has pending work and gets as
// far as the broker probe when nothing stops it; the control half proves that. A rule that
// arms nothing would make the parked half pass at the pending-work check, whatever the gap
// check did.
func TestIdleAdvanceStaysOffWhileParkedOnAGap(t *testing.T) {
	arid := []dmmodel.GroupRef{{GroupToken: "arid-areas", Version: 1}}
	g := newGapRigWith(t, &flakyRanger{failOpens: 100}, scopedDurationReg(t))
	g.rp.cfg.IdleAdvanceGuard = time.Nanosecond
	g.rp.cfg.CheckpointInterval = time.Nanosecond
	probe := &pendingProbe{pending: 1} // not caught up: the probe is consulted and nothing advances
	g.rp.backlogProbe = probe

	g.rp.handle(measuredMsgScoped(t, 1, "acme", "d1", "p@1", "temperature", "90", &fakeAck{}, arid))
	if !g.rp.engine.HasPendingWork() {
		t.Fatal("the hold armed no timer, so this test would pass at the pending-work check")
	}
	later := time.Now().Add(time.Hour)

	g.rp.idleAdvance(context.Background(), later)
	if probe.calls.Load() != 1 {
		t.Fatalf("control: idle-advance reached the probe %d times with no gap, want 1", probe.calls.Load())
	}

	g.rp.handle(measuredMsgScoped(t, 5, "acme", "d1", "p@1", "temperature", "90", &fakeAck{}, arid))
	if g.rp.gapHeld == nil {
		t.Fatal("not parked")
	}
	if !g.rp.engine.HasPendingWork() {
		t.Fatal("the parked engine has no pending work, so the gap check is not what stops idle-advance")
	}
	g.rp.idleAdvance(context.Background(), later)
	if probe.calls.Load() != 1 {
		t.Fatal("idle-advance consulted the broker while parked on a gap")
	}
}

// scriptedRanger answers every range read with a fixed list, whatever the range.
type scriptedRanger struct {
	fakeReplayOpener
	script []messaging.Message
}

func (o *scriptedRanger) NewRangeReader(string, uint64, uint64) (messaging.ReplayReader, error) {
	return &fakeReplayReader{msgs: o.script}, nil
}

// A reader that returns sequences out of order, or the same one twice, is not trusted: the
// fill fails and the loop parks. The first half of each script is accepted before the
// offending message arrives, which is why the engine moves to it; nothing out of order is
// applied.
func TestARangeReaderOutOfOrderFailsTheFill(t *testing.T) {
	for _, c := range []struct {
		name      string
		script    []uint64
		published []uint64
		lastSeq   uint64
	}{
		{"descending", []uint64{3, 2}, []uint64{1, 3}, 3},
		{"repeated", []uint64{2, 2}, []uint64{1, 2}, 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := newGapRig(t, &scriptedRanger{script: stream(t, c.script...)})
			g.rp.handle(hot(t, 1, &fakeAck{}))
			held := &fakeAck{}
			g.rp.handle(hot(t, 5, held)) // 2..4 missing
			if g.rp.gapHeld == nil {
				t.Fatal("a reader that broke the order was accepted: the loop did not park")
			}
			g.commit(t)
			equal(t, "published", g.rec.published(), series(c.published...))
			if got := g.rp.engine.LastSeq(); got != c.lastSeq {
				t.Fatalf("LastSeq = %d, want %d", got, c.lastSeq)
			}
			equal(t, "failed", g.fills(gapFillFailed), 1.0)
			equal(t, "filled", g.fills(gapFillFilled), 0.0)
			if held.acks != 0 {
				t.Fatal("the held message was acked")
			}
		})
	}
}

// A message held for a gap belongs to the term that fetched it. A new term must not
// re-handle and ack it: it was never applied under the new term's engine.
func TestANewTermDropsTheMessageHeldForAGap(t *testing.T) {
	opener := &flakyRanger{failOpens: 1}
	opener.msgs = stream(t, span(1, 4)...)
	g := newGapRig(t, opener)
	held := &fakeAck{}
	g.rp.handle(hot(t, 5, held))
	if g.rp.gapHeld == nil {
		t.Fatal("not parked")
	}
	g.rp.resetForTerm()
	if g.rp.gapHeld != nil {
		t.Fatal("a new term kept the message held by the previous one")
	}
	g.rp.retryGapFill() // the first tick of the new term
	g.commit(t)
	if held.acks != 0 {
		t.Fatal("the previous term's held message was acked by the new one")
	}
	equal(t, "published", g.rec.published(), []string(nil))
}

// A long fill commits as it goes, every CheckpointEvents applied messages, instead of holding
// every detection and the whole range's state until its end.
func TestALongFillCheckpointsAsItGoes(t *testing.T) {
	opener := &fakeReplayOpener{msgs: stream(t, span(2, 13)...)}
	g := newGapRig(t, opener)
	g.rp.cfg.CheckpointEvents = 5
	g.rp.handle(hot(t, 1, &fakeAck{}))
	g.rp.handle(hot(t, 14, &fakeAck{})) // 2..13 missing: applied 2..6, checkpoint, 7..11, checkpoint, 12..13

	snap, ok, err := g.rp.Store.Load(context.Background(), "singleton")
	if err != nil || !ok {
		t.Fatalf("no checkpoint was committed during the fill: ok=%v err=%v", ok, err)
	}
	if snap.StreamSeq != 11 {
		t.Fatalf("last mid-fill checkpoint at %d, want 11", snap.StreamSeq)
	}
	equal(t, "published by then", g.rec.published(), series(span(1, 11)...))
}

// Once a retried fill succeeds, the held message is applied and, when enough acks are
// buffered, checkpointed and acked on that same tick.
func TestTheCheckpointAfterASuccessfulRetryAcksTheHeldMessage(t *testing.T) {
	opener := &flakyRanger{failOpens: 1}
	opener.msgs = stream(t, 2)
	g := newGapRig(t, opener)
	g.rp.cfg.CheckpointEvents = 1
	g.rp.handle(hot(t, 1, &fakeAck{}))
	held := &fakeAck{}
	g.rp.handle(hot(t, 3, held)) // 2 missing; the broker is unreachable
	if g.rp.gapHeld == nil {
		t.Fatal("not parked")
	}
	g.rp.retryGapFill()
	if g.rp.gapHeld != nil {
		t.Fatal("the retry did not complete")
	}
	if held.acks != 1 {
		t.Fatalf("held message acked %d times by the retry's own checkpoint, want 1", held.acks)
	}
	snap, ok, _ := g.rp.Store.Load(context.Background(), "singleton")
	if !ok || snap.StreamSeq != 3 {
		t.Fatalf("checkpoint after the retry = %+v (ok=%v), want seq 3", snap, ok)
	}
}

// Poison inside the gap, and poison as the gapped message itself: the poison is dropped (and
// the delivered one acked), everything else around it is applied in order.
func TestPoisonInsideAGapAndAsTheGappedMessage(t *testing.T) {
	poison := func(seq uint64, ack *fakeAck) messaging.Message {
		m := messaging.NewConsumedMessage(testSubject, []byte("not-a-proto"), 0, nil, ack)
		m.StreamSeq = seq
		registerPending(t, ack, seq)
		return m
	}
	// The stream: 1 hot, 2 hot, 3 poison, 4 hot, 5 hot, 6 poison, 7 hot.
	held6 := &fakeAck{}
	var msgs []messaging.Message
	for s := uint64(1); s <= 7; s++ {
		switch s {
		case 3, 6:
			msgs = append(msgs, poison(s, &fakeAck{}))
		default:
			msgs = append(msgs, hot(t, s, &fakeAck{}))
		}
	}
	g := newGapRig(t, &fakeReplayOpener{msgs: msgs})

	g.rp.handle(hot(t, 1, &fakeAck{}))
	g.rp.handle(poison(6, held6)) // 2..5 missing, 3 of them poison, and the message itself is poison
	if g.rp.gapHeld != nil {
		t.Fatal("parked on a readable gap")
	}
	g.rp.handle(hot(t, 7, &fakeAck{}))
	g.commit(t)

	equal(t, "published", g.rec.published(), series(1, 2, 4, 5, 7))
	if held6.acks != 1 {
		t.Fatalf("the poison message was acked %d times, want 1", held6.acks)
	}
	// Poison advances the engine's sequence, so the message after the gapped poison one is not a
	// gap: one fill, which applied 2, 4 and 5 and recorded the poison 3 as skipped.
	equal(t, "applied in fills", g.seqs(gapSeqApplied), 3.0)
	equal(t, "skipped in fills", g.seqs(gapSeqSkipped), 1.0)
	equal(t, "fills", g.fills(gapFillFilled), 1.0)
}

// A fill that fails because the term is ending is not a failure of the gap: it is not counted,
// it parks nothing, and the message stays unacked for whoever leads next.
func TestAFillCutShortByShutdownIsNotAFailure(t *testing.T) {
	g := newGapRig(t, &flakyRanger{failOpens: 100})
	g.rp.handle(hot(t, 1, &fakeAck{}))
	g.rp.procCancel()
	held := &fakeAck{}
	g.rp.handle(hot(t, 4, held))
	if g.rp.gapHeld != nil {
		t.Fatal("shutdown parked the loop on the gap")
	}
	equal(t, "failed", g.fills(gapFillFailed), 0.0)
	if g.rp.gapFailLogged {
		t.Fatal("shutdown was logged as a gap the loop could not read")
	}
	if g.rp.engine.LastSeq() != 1 {
		t.Fatalf("LastSeq = %d: the message was applied past the gap", g.rp.engine.LastSeq())
	}
	if held.acks != 0 {
		t.Fatal("the held message was acked")
	}
}

// Sequences found absent before a fill fails part-way are counted once, by the pass that
// moved the engine past them.
func TestAbsentSequencesBeforeAPartWayFailureAreCountedOnce(t *testing.T) {
	// 2..3 purged, 4 and 5 present; the reader fails after delivering 4 and 5... then the
	// retry reads 6..8 (6 purged, 7 present, 8 absent at the tail).
	opener := &flakyRanger{failReadAfter: 2}
	opener.msgs = stream(t, 4, 5, 7)
	g := newGapRig(t, opener)
	g.rp.handle(hot(t, 1, &fakeAck{}))
	g.rp.handle(hot(t, 9, &fakeAck{})) // 2..8 missing; fails after 4 and 5
	if g.rp.gapHeld == nil {
		t.Fatal("not parked")
	}
	equal(t, "absent after the failed pass", g.seqs(gapSeqAbsent), 2.0) // 2 and 3: behind applied 4
	equal(t, "applied after the failed pass", g.seqs(gapSeqApplied), 2.0)
	g.rp.retryGapFill()
	if g.rp.gapHeld != nil {
		t.Fatal("the retry did not complete")
	}
	equal(t, "absent in total", g.seqs(gapSeqAbsent), 4.0) // + 6 and 8
	equal(t, "applied in total", g.seqs(gapSeqApplied), 3.0)
}

// The durable the live loop reads is filtered to the stream's one subject. The fill reads the
// stream by sequence with no filter of its own, so a narrower durable would make every fill
// apply messages the durable would never have handed over. This pins the equality so that
// narrowing the filter is a deliberate change that has to revisit the fill.
func TestTheLiveDurableIsFilteredToTheWholeStream(t *testing.T) {
	t.Parallel()
	b := startDetectBroker(t)
	b.detectManager(t, newTestStore(t))
	js, err := b.nc.JetStream()
	require.NoError(t, err)
	durable := b.resolvedDurable()
	var info *nats.ConsumerInfo
	require.Eventually(t, func() bool {
		ci, err := js.ConsumerInfo(messaging.StreamName(b.instance, streams.ResolvedEvents), durable)
		if err != nil {
			return false
		}
		info = ci
		return true
	}, 10*time.Second, 50*time.Millisecond, "the durable was never created")
	require.Empty(t, info.Config.FilterSubjects, "a multi-subject filter narrows the durable")
	require.Equal(t, messaging.StreamSubjects(b.instance, streams.ResolvedEvents)[0], info.Config.FilterSubject)
}
