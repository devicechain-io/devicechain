// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// These tests drive the live loop's gap fill (live_gap.go). Every message is HOT (it raises
// acme/hot) and comes from a device of its own, so the published Raised detections name
// exactly the messages the engine applied, in the order it applied them.

// lockedRecorder is derivedRecorder safe to read while the loop is running.
type lockedRecorder struct {
	mu sync.Mutex
	r  derivedRecorder
}

func (w *lockedRecorder) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.r.WriteMessages(ctx, msgs...)
}

func (w *lockedRecorder) WriteToDevice(ctx context.Context, _ string, msgs ...messaging.Message) error {
	return w.WriteMessages(ctx, msgs...)
}

func (w *lockedRecorder) HandleResponse(error) {}

// published is the series of every Raised detection, in publication order.
func (w *lockedRecorder) published() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, 0, len(w.r.events))
	for _, de := range w.r.events {
		out = append(out, de.Series)
	}
	return out
}

func hot(t *testing.T, seq uint64, ack *fakeAck) messaging.Message {
	t.Helper()
	return measuredMsg(t, seq, "acme", fmt.Sprintf("dev-%d", seq), "p@1", "temperature", "90", ack)
}

func series(seqs ...uint64) []string {
	out := make([]string, len(seqs))
	for i, s := range seqs {
		out[i] = fmt.Sprintf("dev-%d", s)
	}
	return out
}

// stream is the hot messages for seqs, as an opener's stream holds them.
func stream(t *testing.T, seqs ...uint64) []messaging.Message {
	t.Helper()
	out := make([]messaging.Message, 0, len(seqs))
	for _, s := range seqs {
		out = append(out, hot(t, s, &fakeAck{}))
	}
	return out
}

func span(from, to uint64, except ...uint64) []uint64 {
	skip := map[uint64]bool{}
	for _, e := range except {
		skip[e] = true
	}
	var out []uint64
	for s := from; s <= to; s++ {
		if !skip[s] {
			out = append(out, s)
		}
	}
	return out
}

type gapRig struct {
	rp      *ResolvedEventsProcessor
	rec     *lockedRecorder
	metrics *DetectMetrics
}

func newGapRig(t *testing.T, opener ReplayOpener) *gapRig {
	t.Helper()
	return newGapRigWith(t, opener, thresholdReg(t))
}

func newGapRigWith(t *testing.T, opener ReplayOpener, reg *runtime.RuleRegistry) *gapRig {
	t.Helper()
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	metrics := NewDetectMetrics(ms)
	rec := &lockedRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rp := &ResolvedEventsProcessor{
		Replay: opener,
		Store:  newTestStore(t),
		cfg: Config{
			PartitionId:        "singleton",
			Suffix:             "resolved-events",
			CheckpointEvents:   1000,
			CheckpointInterval: time.Hour,
			TickInterval:       time.Hour,
			Clock:              detectcore.RealClock{},
		},
		registry:     reg,
		publisher:    runtime.NewPublisher(rec, reg, metrics),
		clock:        detectcore.RealClock{},
		metrics:      metrics,
		procCtx:      ctx,
		procCancel:   cancel,
		tenantPurges: make(chan tenantPurgeRequest),
		ruleUpdates:  make(chan ruleUpdate, 4),
	}
	if err := rp.restore(context.Background()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	return &gapRig{rp: rp, rec: rec, metrics: metrics}
}

func (g *gapRig) fills(outcome string) float64 {
	return testutil.ToFloat64(g.metrics.gapFills.WithLabelValues(outcome))
}

func (g *gapRig) seqs(outcome string) float64 {
	return testutil.ToFloat64(g.metrics.gapSequences.WithLabelValues(outcome))
}

func (g *gapRig) commit(t *testing.T) {
	t.Helper()
	if !g.rp.checkpoint(context.Background()) {
		t.Fatal("checkpoint did not commit")
	}
}

func equal(t *testing.T, what string, got, want any) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s:\n got  %v\n want %v", what, got, want)
	}
}

// Contiguous traffic costs no read at all: the check is one comparison.
func TestNoGapNoRangeRead(t *testing.T) {
	opener := &fakeReplayOpener{}
	g := newGapRig(t, opener)
	for s := uint64(1); s <= 10; s++ {
		g.rp.handle(hot(t, s, &fakeAck{}))
	}
	g.commit(t)
	equal(t, "range reads", opener.rangeReads, [][2]uint64(nil))
	equal(t, "published", g.rec.published(), series(span(1, 10)...))
	for _, o := range gapFillOutcomes {
		if g.fills(o) != 0 {
			t.Fatalf("fills{%s} = %v with no gap", o, g.fills(o))
		}
	}
}

// A gap whose range is partly purged: what the stream holds is applied in order before the
// message, what it does not is counted absent, and it costs ONE read.
func TestAPartlyPurgedGapAppliesWhatExistsAndCountsTheRest(t *testing.T) {
	purged := append(span(10, 19), 25)
	opener := &fakeReplayOpener{msgs: stream(t, span(2, 39, purged...)...)}
	g := newGapRig(t, opener)

	g.rp.handle(hot(t, 1, &fakeAck{}))
	held := &fakeAck{}
	g.rp.handle(hot(t, 40, held)) // 2..39 never arrived
	// The fill's messages are not deliveries of the durable: only the two delivered messages
	// wait for an ack.
	var buffered []uint64
	for _, m := range g.rp.pendingAcks {
		buffered = append(buffered, m.StreamSeq)
	}
	equal(t, "messages awaiting an ack", buffered, []uint64{1, 40})
	g.commit(t)

	equal(t, "range reads", opener.rangeReads, [][2]uint64{{2, 39}})
	equal(t, "published, in order", g.rec.published(), series(append(append([]uint64{1}, span(2, 39, purged...)...), 40)...))
	if got := g.rp.engine.LastSeq(); got != 40 {
		t.Fatalf("LastSeq = %d, want 40", got)
	}
	if held.acks != 1 {
		t.Fatalf("the delivered message was acked %d times, want 1", held.acks)
	}
	equal(t, "applied", g.seqs(gapSeqApplied), float64(38-len(purged)))
	equal(t, "absent", g.seqs(gapSeqAbsent), float64(len(purged)))
	equal(t, "skipped", g.seqs(gapSeqSkipped), 0.0)
	equal(t, "filled", g.fills(gapFillFilled), 1.0)
	equal(t, "failed", g.fills(gapFillFailed), 0.0)
}

// Legitimate holes cost one bounded read each, never a replay: a stream with every fifth
// sequence purged, read live without ever seeing those sequences. The opener's NewReplayReader
// would be the resync a storm of these must not trigger.
func TestPurgedSequencesCostOneReadEachAndNoReplay(t *testing.T) {
	var present, purged []uint64
	for s := uint64(1); s <= 59; s++ { // a purge at the very tail leaves no later message to reveal it
		if s%5 == 0 {
			purged = append(purged, s)
		} else {
			present = append(present, s)
		}
	}
	opener := &fakeReplayOpener{msgs: stream(t, present...)}
	g := newGapRig(t, opener)
	for _, s := range present {
		g.rp.handle(hot(t, s, &fakeAck{}))
	}
	g.commit(t)

	equal(t, "published", g.rec.published(), series(present...))
	if len(opener.rangeReads) != len(purged) {
		t.Fatalf("%d range reads for %d purged sequences: %v", len(opener.rangeReads), len(purged), opener.rangeReads)
	}
	for i, r := range opener.rangeReads {
		if r != [2]uint64{purged[i], purged[i]} {
			t.Fatalf("read %d was %v, want [%d %d]", i, r, purged[i], purged[i])
		}
	}
	if opener.lastStart != 0 {
		t.Fatalf("a replay was opened from %d; a purged sequence must not cost one", opener.lastStart)
	}
	equal(t, "absent", g.seqs(gapSeqAbsent), float64(len(purged)))
	equal(t, "applied", g.seqs(gapSeqApplied), 0.0)
	equal(t, "absent_only", g.fills(gapFillAbsentOnly), float64(len(purged)))
	equal(t, "filled", g.fills(gapFillFilled), 0.0)
}

// flakyRanger is an opener whose range reads fail on demand.
type flakyRanger struct {
	fakeReplayOpener
	failOpens     int // the next N opens return an error
	failReadAfter int // when > 0, the next reader errors after this many messages
	opens         int
}

func (f *flakyRanger) NewRangeReader(suffix string, from, to uint64) (messaging.ReplayReader, error) {
	f.opens++
	if f.failOpens > 0 {
		f.failOpens--
		return nil, errors.New("broker unreachable")
	}
	rd, err := f.fakeReplayOpener.NewRangeReader(suffix, from, to)
	if err != nil {
		return nil, err
	}
	if f.failReadAfter > 0 {
		n := f.failReadAfter
		f.failReadAfter = 0
		return &failingReader{ReplayReader: rd, after: n}, nil
	}
	return rd, nil
}

type failingReader struct {
	messaging.ReplayReader
	after int
}

func (r *failingReader) Read(ctx context.Context) (messaging.Message, error) {
	if r.after == 0 {
		return messaging.Message{}, errors.New("connection reset")
	}
	r.after--
	return r.ReplayReader.Read(ctx)
}

// A fill that fails applies nothing past the gap, acks nothing, and parks; the retry applies
// the gap and then the held message, in order, exactly once.
func TestAGapThatCannotBeReadParksAndResumesWithNoLoss(t *testing.T) {
	opener := &flakyRanger{failOpens: 2}
	opener.msgs = stream(t, span(1, 9)...)
	g := newGapRig(t, opener)

	for s := uint64(1); s <= 4; s++ {
		g.rp.handle(hot(t, s, &fakeAck{}))
	}
	heldAck := &fakeAck{}
	g.rp.handle(hot(t, 10, heldAck)) // 5..9 missing, and the broker is unreachable
	if g.rp.gapHeld == nil || g.rp.gapHeld.StreamSeq != 10 {
		t.Fatalf("the loop did not park on seq 10: %+v", g.rp.gapHeld)
	}
	if got := g.rp.engine.LastSeq(); got != 4 {
		t.Fatalf("LastSeq = %d after a failed fill, want 4: nothing may be applied past the gap", got)
	}
	g.rp.retryGapFill() // still unreachable
	if g.rp.gapHeld == nil {
		t.Fatal("a second failed attempt released the park")
	}
	equal(t, "failed", g.fills(gapFillFailed), 2.0)

	g.rp.retryGapFill() // the broker is back
	if g.rp.gapHeld != nil {
		t.Fatal("the park was not released once the range could be read")
	}
	g.commit(t)
	if heldAck.acks != 1 {
		t.Fatalf("held message acked %d times, want 1", heldAck.acks)
	}
	equal(t, "published", g.rec.published(), series(span(1, 10)...))
	equal(t, "filled", g.fills(gapFillFilled), 1.0)
	equal(t, "applied", g.seqs(gapSeqApplied), 5.0)
}

// A reader that fails part-way has already advanced the engine over what it delivered, so the
// retry resumes after it and nothing is applied twice or skipped.
func TestAFillThatFailsPartWayResumesWhereItStopped(t *testing.T) {
	opener := &flakyRanger{failReadAfter: 3}
	opener.msgs = stream(t, span(1, 9)...)
	g := newGapRig(t, opener)

	g.rp.handle(hot(t, 10, &fakeAck{})) // 1..9 missing
	if g.rp.gapHeld == nil {
		t.Fatal("the loop did not park")
	}
	if got := g.rp.engine.LastSeq(); got != 3 {
		t.Fatalf("LastSeq = %d, want 3 (the part of the range delivered before the failure)", got)
	}
	g.rp.retryGapFill()
	if g.rp.gapHeld != nil {
		t.Fatal("the retry did not complete")
	}
	g.commit(t)
	equal(t, "published", g.rec.published(), series(span(1, 10)...))
	equal(t, "ranges read", opener.rangeReads, [][2]uint64{{1, 9}, {4, 9}})
	equal(t, "applied", g.seqs(gapSeqApplied), 9.0)
}

// With no way to read the stream a gap is refused loudly, never applied around.
func TestAGapWithNoRangeReaderIsNotApplied(t *testing.T) {
	g := newGapRig(t, nil)
	g.rp.Replay = nil
	g.rp.handle(hot(t, 1, &fakeAck{}))
	ack := &fakeAck{}
	g.rp.handle(hot(t, 5, ack))
	if g.rp.gapHeld == nil {
		t.Fatal("the loop applied past a gap it could not read")
	}
	g.commit(t)
	if ack.acks != 0 {
		t.Fatal("the held message was acked")
	}
	equal(t, "published", g.rec.published(), series(1))
	equal(t, "failed", g.fills(gapFillFailed), 1.0)
}

// A range reader that returns a sequence outside the range it was asked for is not trusted.
func TestARangeReaderOutsideItsRangeFailsTheFill(t *testing.T) {
	opener := &fakeReplayOpener{msgs: stream(t, 2, 3, 99)}
	g := newGapRig(t, &outOfRange{opener})
	g.rp.handle(hot(t, 1, &fakeAck{}))
	g.rp.handle(hot(t, 5, &fakeAck{}))
	if g.rp.gapHeld == nil {
		t.Fatal("a reader that strayed outside the range was accepted")
	}
	if g.rp.engine.LastSeq() != 3 {
		t.Fatalf("LastSeq = %d, want 3: what was in range is applied, the stray 99 is not", g.rp.engine.LastSeq())
	}
}

// outOfRange answers every range with the whole of its stream.
type outOfRange struct{ *fakeReplayOpener }

func (o *outOfRange) NewRangeReader(string, uint64, uint64) (messaging.ReplayReader, error) {
	return &fakeReplayReader{msgs: o.msgs}, nil
}

type countingProbe struct{ calls atomic.Int32 }

func (p *countingProbe) Backlog(context.Context) (uint64, uint64, error) {
	p.calls.Add(1)
	return 0, 0, nil
}

// The new counters exist at zero from construction, so an alert on their rate sees a series
// rather than none.
func TestGapMetricsExistAtZero(t *testing.T) {
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	m := NewDetectMetrics(ms)
	equal(t, "fill series", testutil.CollectAndCount(m.gapFills), 3)
	equal(t, "sequence series", testutil.CollectAndCount(m.gapSequences), 3)
	for _, o := range gapFillOutcomes {
		equal(t, "fills{"+o+"}", testutil.ToFloat64(m.gapFills.WithLabelValues(o)), 0.0)
	}
	for _, o := range gapSeqOutcomes {
		equal(t, "sequences{"+o+"}", testutil.ToFloat64(m.gapSequences.WithLabelValues(o)), 0.0)
	}
}

// gatedOpener fails every range read until its gate opens.
type gatedOpener struct {
	fakeReplayOpener
	mu       sync.Mutex
	open     bool
	attempts atomic.Int32
}

func (o *gatedOpener) NewRangeReader(suffix string, from, to uint64) (messaging.ReplayReader, error) {
	o.attempts.Add(1)
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.open {
		return nil, errors.New("broker unreachable")
	}
	return o.fakeReplayOpener.NewRangeReader(suffix, from, to)
}

func (o *gatedOpener) release() {
	o.mu.Lock()
	o.open = true
	o.mu.Unlock()
}

// feedReader hands the loop a fixed script and then waits for the loop to stop.
type feedReader struct {
	fakeReader
	reads atomic.Int32
}

func (r *feedReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	r.reads.Add(1)
	return r.fakeReader.ReadMessage(ctx)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The whole loop: while parked on an unreadable gap the engine applies nothing, the parked
// message is not overtaken by the one behind it, a tenant purge is still answered, and when the
// broker returns the stream is applied in order with every delivered message acked once.
func TestTheLoopParksOnAnUnreadableGapAndStillServesPurges(t *testing.T) {
	opener := &gatedOpener{}
	opener.msgs = stream(t, 1, 2)
	acks := map[uint64]*fakeAck{3: {}, 4: {}}
	reader := &feedReader{fakeReader: fakeReader{results: []readResult{
		{msg: hot(t, 3, acks[3])}, // 1..2 never arrived
		{msg: hot(t, 4, acks[4])},
	}}}
	g := newGapRig(t, opener)
	rp := g.rp
	rp.ResolvedEventsReader = reader
	rp.cfg.TickInterval = 5 * time.Millisecond
	rp.readerWG.Add(1)
	go rp.run()
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			rp.pcancel()
			rp.readerWG.Wait()
		}
	}
	t.Cleanup(stop)

	waitFor(t, "three failed fills (the ticker retries)", func() bool { return opener.attempts.Load() >= 3 })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := rp.EvictTenant(ctx, "other"); err != nil {
		t.Fatalf("a tenant purge was not served while the loop was parked: %v", err)
	}
	equal(t, "published while parked", g.rec.published(), []string(nil))

	// A rule change is also served while parked: the loop takes it off its channel and installs
	// it (the channel is buffered, so an empty one means the loop took it).
	thr := 95.0 // never fires on the 90s these tests send
	extra, err := rules0.Compile(rules0.Rule{
		ID: "acme/extra", Name: "extra", Type: rules0.TypeThreshold,
		When: rules0.Condition{Metric: "temperature", Op: rules0.OpGt, Threshold: &thr},
	}, rules0.Limits{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	rp.ruleUpdates <- ruleUpdate{upserts: []runtime.ScopedRule{{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: extra}}}
	waitFor(t, "the loop to take a rule update while parked", func() bool { return len(rp.ruleUpdates) == 0 })
	if opener.attempts.Load() < 3 {
		t.Fatal("the loop was not parked while the rule update was served")
	}

	opener.release()
	// The pump's third read happens only once the loop has taken the second message, which it
	// does only after the park is released and the held one applied.
	waitFor(t, "the loop to take the message behind the held one", func() bool { return reader.reads.Load() >= 3 })
	stop() // the final checkpoint publishes and acks
	if _, ok := rp.registry.Lookup("acme/extra"); !ok {
		t.Fatal("the rule update sent while parked was not installed")
	}
	equal(t, "published", g.rec.published(), series(1, 2, 3, 4))
	for s, a := range acks {
		if a.acks != 1 {
			t.Fatalf("seq %d acked %d times, want 1", s, a.acks)
		}
	}
}

// A WHOLE LOST PULL, AT THE WIDTHS THE FETCH BATCH CAN BE CONFIGURED TO. With one request in
// flight a dropped connection can cost one full batch, and the batch may be set as high as 256
// (infrastructure.nats.fetch.batch): the range it leaves must come back in ONE range read, with
// every sequence applied in order before the message that exposed the gap, and no replay.
func TestAWholeLostBatchIsFilledByOneRangeRead(t *testing.T) {
	for _, batch := range []uint64{64, 128, 256} {
		t.Run(fmt.Sprintf("batch-%d", batch), func(t *testing.T) {
			last := batch + 1 // the lost pull is 2..batch+1
			opener := &fakeReplayOpener{msgs: stream(t, span(2, last)...)}
			g := newGapRig(t, opener)

			g.rp.handle(hot(t, 1, &fakeAck{}))
			g.rp.handle(hot(t, last+1, &fakeAck{}))
			g.commit(t)

			equal(t, "range reads", opener.rangeReads, [][2]uint64{{2, last}})
			equal(t, "published, in order", g.rec.published(), series(span(1, last+1)...))
			equal(t, "applied", g.seqs(gapSeqApplied), float64(batch))
			equal(t, "absent", g.seqs(gapSeqAbsent), 0.0)
			equal(t, "filled", g.fills(gapFillFilled), 1.0)
			equal(t, "failed", g.fills(gapFillFailed), 0.0)
		})
	}
}
