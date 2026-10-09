// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// A LIVE-PATH GAP MUST NOT BECOME A SILENT LOSS.
//
// The durable hands DETECT messages in delivery order, not stream order, and nothing on the
// live path checks that the sequence it is about to apply follows the one it applied last.
// The interleaving this reproduces, with the real broker's mechanics replaced by a script:
//
//  1. The engine has applied through seq 100 (the term's replay ran to that head).
//  2. A Fetch(64) is answered with 101..164 and the broker starts AckWait on all 64. The
//     connection drops mid-response; the client receives only 101..120 and nats.go returns
//     that partial batch with no error.
//  3. The client reconnects inside the lease, so the term continues with no replay. The
//     broker counts 121..164 as delivered, so the next Fetch hands out 165..170. The engine
//     applies them and LastSeq becomes 170.
//  4. At AckWait the broker redelivers 121..164 (NumDelivered 2). applyResolved sees
//     StreamSeq <= LastSeq, drops each as a duplicate, and the checkpoint ACKS it.
//
// Nothing about step 4 is visible: no replay, no dead letter, no metric. The events were
// never applied, and now the broker will never offer them again.
//
// The assertion is by value: every event in the gap comes from its OWN device and carries a
// reading that raises the threshold rule (temperature > 80), and every event outside the gap
// comes from one other device with a reading that does not. So the set of devices the engine
// published a Raised detection for is exactly the set of gap events it applied: one series
// per event, and no falling edge, because no device in the gap reports again. On a tree with
// no gap handling the set is empty; it must be gap-121..gap-164.
func TestALiveGapIsAppliedNotDroppedAsADuplicate(t *testing.T) {
	const (
		replayedHead = 100 // the head the term's startup replay reaches
		partialEnd   = 120 // the last message of the partial batch the client received
		gapEnd       = 164 // the last message the broker counted as delivered but the client lost
		streamHead   = 170 // the last message of the next Fetch
	)
	hot := func(seq uint64) bool { return seq > partialEnd && seq <= gapEnd }
	device := func(seq uint64) string {
		if hot(seq) {
			return fmt.Sprintf("gap-%d", seq)
		}
		return "cold"
	}
	value := func(seq uint64) string {
		if hot(seq) {
			return "90" // > 80: raises acme/hot
		}
		return "50"
	}
	msg := func(seq uint64, deliveries int, ack *fakeAck) messaging.Message {
		m := measuredMsg(t, seq, "acme", device(seq), "p@1", "temperature", value(seq), ack)
		m.NumDelivered = deliveries
		return m
	}

	// The stream as the broker holds it: every sequence 1..170, contiguous. An ordered
	// replay from any start sees exactly these.
	stream := make([]messaging.Message, 0, streamHead)
	for seq := uint64(1); seq <= streamHead; seq++ {
		stream = append(stream, msg(seq, 1, &fakeAck{}))
	}
	opener := &movingHeadOpener{stream: stream}
	opener.head.Store(replayedHead)

	// What the durable hands the live loop, in delivery order.
	var script []readResult
	for seq := uint64(replayedHead + 1); seq <= partialEnd; seq++ { // the partial batch
		script = append(script, readResult{msg: msg(seq, 1, &fakeAck{})})
	}
	for seq := uint64(gapEnd + 1); seq <= streamHead; seq++ { // the next Fetch
		script = append(script, readResult{msg: msg(seq, 1, &fakeAck{})})
	}
	redelivered := map[uint64]*fakeAck{}
	for seq := uint64(partialEnd + 1); seq <= gapEnd; seq++ { // the AckWait redelivery
		a := &fakeAck{}
		redelivered[seq] = a
		script = append(script, readResult{msg: msg(seq, 2, a)})
	}
	script = append(script, readResult{err: io.EOF})

	// The stream grows past the replay head only once live consumption has begun: the
	// first live read moves it, so the startup replay stops at 100 exactly as a term whose
	// build captured head 100 does.
	reader := &headMovingReader{fakeReader: fakeReader{results: script}, onFirstRead: func() {
		opener.head.Store(streamHead)
	}}

	store := newTestStore(t)
	reg := thresholdReg(t)
	w := &derivedRecorder{}
	rp := &ResolvedEventsProcessor{
		ResolvedEventsReader: reader,
		Replay:               opener,
		Store:                store,
		cfg: Config{
			PartitionId:        "singleton",
			Suffix:             "resolved-events",
			CheckpointEvents:   1000, // only the final checkpoint at EOF commits and acks
			CheckpointInterval: time.Hour,
			TickInterval:       time.Hour,
			Clock:              detectcore.RealClock{},
		},
		registry:  reg,
		publisher: runtime.NewPublisher(w, reg, (*DetectMetrics)(nil)),
		clock:     detectcore.RealClock{},
	}
	ctx := context.Background()
	if err := rp.ExecuteInitialize(ctx); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := rp.ExecuteStart(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	rp.readerWG.Wait() // the loop returns on EOF after its final checkpoint

	// Precondition: the run went where the scenario says, or the assertions below say
	// nothing. The checkpoint reached the stream head and every redelivered copy was acked
	// (it is no longer the broker's to offer again).
	snap, ok, err := store.Load(ctx, "singleton")
	if err != nil || !ok {
		t.Fatalf("load final checkpoint: ok=%v err=%v", ok, err)
	}
	if snap.StreamSeq != streamHead {
		t.Fatalf("final checkpoint = %d, want %d", snap.StreamSeq, streamHead)
	}
	for seq := uint64(partialEnd + 1); seq <= gapEnd; seq++ {
		if a := redelivered[seq]; a.acks != 1 {
			t.Fatalf("redelivered seq %d acked %d times, want 1 — the scenario did not run as written", seq, a.acks)
		}
	}

	// THE LOSS. Each gap event is acked above; each must also have been applied.
	var want []string
	for seq := uint64(partialEnd + 1); seq <= gapEnd; seq++ {
		want = append(want, device(seq))
	}
	sort.Strings(want)
	got := w.raisedSeries(t)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("raised detections for %d of the %d gap events (seq %d..%d); got series %v: the live "+
			"loop advanced LastSeq past a range it never applied, then dropped the range's redelivery as "+
			"a duplicate and acked it", len(got), len(want), partialEnd+1, gapEnd, got)
	}
	if w.other != 0 {
		t.Fatalf("published %d detections that are not a Raised for a gap device; the cold device must "+
			"never fire", w.other)
	}
}

// derivedRecorder is a derived-event writer that keeps what it was given, so a test can say
// WHICH detections were published rather than how many.
type derivedRecorder struct {
	events []runtime.DerivedEvent
	other  int
}

func (w *derivedRecorder) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	for _, m := range msgs {
		var de runtime.DerivedEvent
		if err := json.Unmarshal(m.Value, &de); err != nil {
			return err
		}
		w.events = append(w.events, de)
	}
	return nil
}

func (w *derivedRecorder) WriteToDevice(ctx context.Context, _ string, msgs ...messaging.Message) error {
	return w.WriteMessages(ctx, msgs...)
}

func (w *derivedRecorder) HandleResponse(error) {}

// raisedSeries is the sorted set of series with a Raised detection; anything else published
// is counted in other.
func (w *derivedRecorder) raisedSeries(t *testing.T) []string {
	t.Helper()
	seen := map[string]bool{}
	var out []string
	for _, de := range w.events {
		if de.Edge != runtime.EdgeRaised || de.RuleID != "acme/hot" || seen[de.Series] {
			w.other++
			continue
		}
		seen[de.Series] = true
		out = append(out, de.Series)
	}
	sort.Strings(out)
	return out
}

// movingHeadOpener is an ordered replay over a fixed stream whose head the test moves, so the
// term's startup replay can stop at one head and a later replay (a gap fill) can see past it.
type movingHeadOpener struct {
	stream     []messaging.Message
	head       atomic.Uint64
	opens      atomic.Int32
	rangeReads atomic.Int32
}

func (o *movingHeadOpener) NewReplayReader(_ string, startSeq uint64) (messaging.ReplayReader, uint64, error) {
	o.opens.Add(1)
	head := o.head.Load()
	var out []messaging.Message
	for _, m := range o.stream {
		if m.StreamSeq >= startSeq && m.StreamSeq <= head {
			out = append(out, m)
		}
	}
	return &fakeReplayReader{msgs: out}, head, nil
}

// NewRangeReader is the gap-fill seam: an ordered read of exactly [from, to], skipping any
// sequence the stream no longer holds. The live loop's gap fill calls it, and this
// fake answers it from the same stream the term's replay reads.
func (o *movingHeadOpener) NewRangeReader(_ string, from, to uint64) (messaging.ReplayReader, error) {
	o.rangeReads.Add(1)
	var out []messaging.Message
	for _, m := range o.stream {
		if m.StreamSeq >= from && m.StreamSeq <= to {
			out = append(out, m)
		}
	}
	return &fakeReplayReader{msgs: out}, nil
}

// headMovingReader runs a hook on its first read, which is the moment live consumption
// starts: after the term's build, before the first live message is applied.
type headMovingReader struct {
	fakeReader
	onFirstRead func()
	fired       bool
}

func (r *headMovingReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	if !r.fired {
		r.fired = true
		r.onFirstRead()
	}
	return r.fakeReader.ReadMessage(ctx)
}
