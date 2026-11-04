// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"testing"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/eventenvelope"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
)

// guardedProcessor is a processor with the duplicate-event guard ON (event envelopes switched on),
// over store, reading its replay from opener.
func guardedProcessor(t *testing.T, w *derivedCapture, store *model.SnapshotStore, opener ReplayOpener, retention time.Duration, maxEntries int, guard bool) *ResolvedEventsProcessor {
	t.Helper()
	rp := &ResolvedEventsProcessor{
		Store: store,
		cfg: Config{
			PartitionId:        "singleton",
			CheckpointEvents:   1000,
			CheckpointInterval: time.Hour,
			TickInterval:       time.Hour,
			Clock:              detectcore.RealClock{},
			Output:             serialOutput,
			AppliedGuard:       guard,
			AppliedRetention:   retention,
			AppliedMaxEntries:  maxEntries,
		},
		registry:  diffRegistry(t),
		clock:     detectcore.RealClock{},
		procCtx:   context.Background(),
		Replay:    opener,
		publisher: nil,
	}
	rp.publisher = runtime.NewPublisher(w, rp.registry, (*DetectMetrics)(nil))
	if err := rp.restore(context.Background()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	return rp
}

func resolvedMessage(t *testing.T, e diffEvent, seq uint64, appended time.Time, ack *fakeAck) messaging.Message {
	t.Helper()
	b, err := resolvedevent.MarshalResolvedEvent(diffResolved(t, e))
	if err != nil {
		t.Fatal(err)
	}
	m := messaging.NewConsumedMessage("dc.acme.resolved-events", b, 0, nil, ack)
	m.StreamSeq = seq
	m.AppendTime = appended
	return m
}

// deliverWithCopies feeds the corpus as envelopes of k members (k = 0: legacy messages) and, after
// every copyEvery-th message, re-delivers its events under later sequences in the OTHER shape (a
// legacy copy of an enveloped event and the reverse), the way a settlement residual or a rolling
// upgrade republish stores one event twice. It returns the snapshot with the stream position
// normalised.
func deliverWithCopies(t *testing.T, corpus []diffEvent, k, copyEvery int, guard bool) []byte {
	t.Helper()
	w := &derivedCapture{}
	rp := guardedProcessor(t, w, newTestStore(t), &fakeReplayOpener{}, 0, 0, true)
	ack := &fakeAck{}
	seq := uint64(0)
	reset := func() {
		if !guard {
			rp.applied = runtime.NewAppliedGuard(0, 0) // negative control: remembers nothing across messages
		}
	}
	legacy := func(e diffEvent) {
		seq++
		reset()
		rp.handle(resolvedMessage(t, e, seq, e.occurred, ack))
	}
	envelope := func(es []diffEvent) {
		var events []eventenvelope.Event
		for i, e := range es {
			b, err := resolvedevent.MarshalResolvedEvent(diffResolved(t, e))
			if err != nil {
				t.Fatal(err)
			}
			events = append(events, eventenvelope.Event{Index: uint32(i), Bytes: b})
		}
		h := eventenvelope.Header{Kind: eventenvelope.KindResolved, Tenant: "acme", SourceSeq: seq, Round: 1}
		msg, err := eventenvelope.Encode(h, events)
		if err != nil {
			t.Fatal(err)
		}
		m := messaging.NewConsumedMessage("dc.acme.resolved-events", msg, 0,
			map[string]string{eventenvelope.HeaderName: eventenvelope.HeaderValue}, ack)
		seq++
		m.StreamSeq = seq
		m.AppendTime = es[0].occurred
		reset()
		rp.handle(m)
	}
	step := max(k, 1)
	n := 0
	for start := 0; start < len(corpus); start += step {
		group := corpus[start:min(start+step, len(corpus))]
		if k == 0 {
			legacy(group[0])
		} else {
			envelope(group)
		}
		n++
		if n%copyEvery == 0 {
			if k == 0 {
				envelope(group) // a legacy event re-delivered inside an envelope
			} else {
				for _, e := range group {
					legacy(e) // an enveloped event re-delivered as legacy messages
				}
			}
		}
	}
	rp.checkpoint(context.Background())
	snap, err := rp.snapshotEngine()
	if err != nil {
		t.Fatal(err)
	}
	return lastSeqPattern.ReplaceAll(snap, []byte(`"lastSeq":0`))
}

// An event delivered a second time under a later sequence, in either shape and in any mix of
// them, leaves detection exactly where one delivery does. The negative control runs the same
// deliveries with a guard that remembers nothing and must differ, or the corpus proves nothing.
func TestADuplicateEventIsNotAppliedTwice(t *testing.T) {
	corpus := diffCorpus(150)
	for _, k := range []int{0, 1, 4, 7} {
		clean, _ := deliver(t, corpus, k)
		got := deliverWithCopies(t, corpus, k, 3, true)
		if string(got) != string(clean) {
			t.Errorf("k=%d: re-delivered events changed the engine state (%d vs %d bytes)", k, len(got), len(clean))
		}
		if k > 0 {
			ungated := deliverWithCopies(t, corpus, k, 3, false)
			if string(ungated) == string(clean) {
				t.Errorf("k=%d: negative control: without the guard the copies changed nothing, so the corpus does not exercise it", k)
			}
		}
	}
}

// A stream with no duplicates is unchanged by the guard.
func TestDistinctEventsAreAllApplied(t *testing.T) {
	corpus := diffCorpus(60)
	clean, _ := deliver(t, corpus, 0)
	if got := deliverWithCopies(t, corpus, 0, 1000, true); string(got) != string(clean) {
		t.Fatal("a stream with no duplicates must be unchanged by the guard")
	}
}

// openerRecord wraps the fake replay with a record of every read it was asked for, and can hide
// the history before a sequence (the priming read), which is what removing primeApplied does.
type openerRecord struct {
	*fakeReplayOpener
	starts      []uint64
	hideBelow   uint64 // reads that start at or below it return nothing
	hiddenReads int
}

func (o *openerRecord) NewReplayReader(suffix string, startSeq uint64) (messaging.ReplayReader, uint64, error) {
	o.starts = append(o.starts, startSeq)
	if o.hideBelow != 0 && startSeq <= o.hideBelow {
		o.hiddenReads++
		return &fakeReplayReader{}, o.head, nil
	}
	return o.fakeReplayOpener.NewReplayReader(suffix, startSeq)
}

// restartStream is a corpus as messages whose APPEND times differ from the events' own times, with
// duplicates placed so that one straddles the checkpoint (its original is applied before it, its
// copy after) and one is far outside the guard's retention.
func restartStream(t *testing.T) ([]messaging.Message, int) {
	t.Helper()
	corpus := diffCorpus(160)
	const checkpointAfter = 65
	ack := &fakeAck{}
	var ordered []diffEvent
	for i, e := range corpus {
		ordered = append(ordered, e)
		switch i {
		case 64: // the last message before the checkpoint (seq 65); its copy is the first after it
			ordered = append(ordered, corpus[64])
		case 72: // an ordinary duplicate, well after the checkpoint
			ordered = append(ordered, corpus[72])
		case 150:
			ordered = append(ordered, corpus[2]) // far outside the 60 s retention: applies
		}
	}
	base := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC) // not the events' own times
	var msgs []messaging.Message
	for i, e := range ordered {
		msgs = append(msgs, resolvedMessage(t, e, uint64(i+1), base.Add(time.Duration(i)*3*time.Second), ack))
	}
	return msgs, checkpointAfter
}

// guardSnapshot is the engine's state plus every derived event the given captures hold, in order:
// a duplicate's effect is often on the alarms it raised, long gone from the final state.
func guardSnapshot(t *testing.T, rp *ResolvedEventsProcessor, ws ...*derivedCapture) string {
	t.Helper()
	s, err := rp.snapshotEngine()
	if err != nil {
		t.Fatal(err)
	}
	out := string(s)
	for _, w := range ws {
		for i, ev := range w.events {
			out += fmt.Sprintf("|%s,%s,%v,%v,%s", ev.RuleID, ev.Series, ev.Edge, ev.OccurredTime, w.ids[i])
		}
	}
	return out
}

// THE CALLER, NOT THE FUNCTION: a processor that applied the start of a stream, checkpointed and
// was rebuilt through restore -> replayToHead reaches byte for byte the state of one that never
// stopped, including a duplicate whose original was applied before the restart.
func TestARestartedProcessorMatchesAnUninterruptedOne(t *testing.T) {
	msgs, k := restartStream(t)
	retention := 60 * time.Second

	ww := &derivedCapture{}
	whole := guardedProcessor(t, ww, newTestStore(t), &fakeReplayOpener{}, retention, 0, true)
	for _, m := range msgs {
		whole.handle(m)
	}
	whole.checkpoint(context.Background())
	want := guardSnapshot(t, whole, ww)

	run := func(blockPriming, guardOff bool) string {
		store := newTestStore(t)
		w1, w2 := &derivedCapture{}, &derivedCapture{}
		first := guardedProcessor(t, w1, store, &fakeReplayOpener{}, retention, 0, true)
		for _, m := range msgs[:k] {
			first.handle(m)
		}
		first.checkpoint(context.Background())

		op := &openerRecord{fakeReplayOpener: &fakeReplayOpener{msgs: msgs, head: uint64(len(msgs))}}
		if blockPriming {
			op.hideBelow = uint64(k)
		}
		second := guardedProcessor(t, w2, store, op, retention, 0, !guardOff)
		if second.engine.LastSeq() != uint64(k) {
			t.Fatalf("restored at seq %d, want %d", second.engine.LastSeq(), k)
		}
		if err := second.replayToHead(); err != nil {
			t.Fatal(err)
		}
		t.Logf("blockPriming=%v guardOff=%v: reads %v, remembered %v", blockPriming, guardOff, op.starts, func() int {
			if second.applied == nil {
				return -1
			}
			return second.applied.Len()
		}())
		return guardSnapshot(t, second, w1, w2)
	}

	if got := run(false, false); got != want {
		t.Fatal("a restarted processor's state differs from an uninterrupted one's")
	}
	// Negative controls: the same restart with priming removed, or with no guard rebuilt at all
	// (the restore that does not reset it), must differ, or the straddling duplicate proves nothing.
	if got := run(true, false); got == want {
		t.Fatal("negative control: without priming the straddling duplicate changed nothing")
	}
	if got := run(false, true); got == want {
		t.Fatal("negative control: with no guard after the restore the straddling duplicate changed nothing")
	}
}

// With envelopes off, nothing is built, primed or consulted: a duplicate applies, and no read of
// the stream is made on its account.
func TestWithTheSwitchOffNothingIsGuardedOrPrimed(t *testing.T) {
	msgs, k := restartStream(t)
	store := newTestStore(t)
	w1, w2, w3 := &derivedCapture{}, &derivedCapture{}, &derivedCapture{}
	first := guardedProcessor(t, w1, store, &fakeReplayOpener{}, 0, 0, false)
	if first.applied != nil {
		t.Fatal("a guard was built with the switch off")
	}
	for _, m := range msgs[:k] {
		first.handle(m)
	}
	first.checkpoint(context.Background())
	op := &openerRecord{fakeReplayOpener: &fakeReplayOpener{msgs: msgs, head: uint64(len(msgs))}}
	second := guardedProcessor(t, w2, store, op, 0, 0, false)
	if err := second.replayToHead(); err != nil {
		t.Fatal(err)
	}
	if len(op.starts) != 1 || op.starts[0] != uint64(k)+1 {
		t.Fatalf("reads = %v; with the switch off the only read is the replay from LastSeq+1 (%d)", op.starts, k+1)
	}
	whole := guardedProcessor(t, w3, newTestStore(t), &fakeReplayOpener{}, 0, 0, true)
	for _, m := range msgs {
		whole.handle(m)
	}
	whole.checkpoint(context.Background())
	if guardSnapshot(t, second, w1, w2) == guardSnapshot(t, whole, w3) {
		t.Fatal("with the switch off the duplicates were skipped anyway")
	}
}

// When the entry cap would bind, priming starts at the cap boundary and does not read the window
// whole.
func TestPrimingStartsAtTheCapBoundary(t *testing.T) {
	msgs, k := restartStream(t)
	store := newTestStore(t)
	first := guardedProcessor(t, &derivedCapture{}, store, &fakeReplayOpener{}, time.Hour, 10, true)
	for _, m := range msgs[:k] {
		first.handle(m)
	}
	first.checkpoint(context.Background())
	op := &openerRecord{fakeReplayOpener: &fakeReplayOpener{msgs: msgs, head: uint64(len(msgs))}}
	second := guardedProcessor(t, &derivedCapture{}, store, op, time.Hour, 10, true)
	if err := second.replayToHead(); err != nil {
		t.Fatal(err)
	}
	// The read before the final replay read is the priming read.
	prime := op.starts[len(op.starts)-2]
	if want := uint64(k) + 1 - 10; prime != want {
		t.Fatalf("priming read from seq %d, want the cap boundary %d (reads %v)", prime, want, op.starts)
	}
}

// A stream trimmed past the retention is said, not hidden.
func TestPrimingSaysWhenTheStreamNoLongerReachesBack(t *testing.T) {
	msgs, k := restartStream(t)
	store := newTestStore(t)
	first := guardedProcessor(t, &derivedCapture{}, store, &fakeReplayOpener{}, 5*time.Minute, 0, true)
	for _, m := range msgs[:k] {
		first.handle(m)
	}
	first.checkpoint(context.Background())
	// Messages 1..39 aged out: the oldest held is 40, about two minutes before LastSeq, which is
	// less than the five minutes the guard should remember.
	op := &openerRecord{fakeReplayOpener: &fakeReplayOpener{msgs: msgs[39:], head: uint64(len(msgs))}}
	second := guardedProcessor(t, &derivedCapture{}, store, op, 5*time.Minute, 0, true)
	if err := second.primeApplied(); err != nil {
		t.Fatal(err)
	}
	if !second.primedPartial {
		t.Fatal("a stream that no longer reaches back the retention was primed without saying so")
	}
	// A complete stream says nothing.
	op = &openerRecord{fakeReplayOpener: &fakeReplayOpener{msgs: msgs, head: uint64(len(msgs))}}
	third := guardedProcessor(t, &derivedCapture{}, store, op, 5*time.Minute, 0, true)
	if err := third.primeApplied(); err != nil {
		t.Fatal(err)
	}
	if third.primedPartial {
		t.Fatal("a stream that starts at message 1 was reported as trimmed")
	}
}

// LastSeq may itself have been deleted: the anchor is the newest message at or before it.
func TestPrimingAnchorsOnTheNewestMessageAtOrBeforeLastSeq(t *testing.T) {
	msgs, _ := restartStream(t)
	var without []messaging.Message
	for _, m := range msgs {
		if m.StreamSeq != 60 {
			without = append(without, m)
		}
	}
	rp := guardedProcessor(t, &derivedCapture{}, newTestStore(t), &fakeReplayOpener{msgs: without, head: uint64(len(msgs))}, 0, 0, true)
	got, ok, err := rp.appendTimeAtOrBefore(60)
	if err != nil || !ok {
		t.Fatalf("anchor = %v, %v", ok, err)
	}
	if want := msgs[58].AppendTime; !got.Equal(want) {
		t.Fatalf("anchor time %v, want message 59's %v", got, want)
	}
}
