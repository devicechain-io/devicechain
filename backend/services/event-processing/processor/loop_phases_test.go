// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"io"
	"os"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

func phaseMetrics() *DetectMetrics {
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	return NewDetectMetrics(ms)
}

func phaseSeconds(m *DetectMetrics, p loopPhase) float64 {
	return testutil.ToFloat64(m.loopSeconds[p])
}

func totalPhaseSeconds(m *DetectMetrics) float64 {
	var sum float64
	for p := loopPhase(0); p < numLoopPhases; p++ {
		sum += phaseSeconds(m, p)
	}
	return sum
}

// stepClock is a clock that moves one step on every read and never otherwise. Under it a phase
// lasts exactly as many steps as there are clock reads inside it, so the stopwatch's totals can be
// asserted exactly, with no epsilon and no dependence on how coarse the machine's real clock is
// (on Windows it ticks in steps of about half a millisecond, which is why these tests do not use
// one).
type stepClock struct {
	mu    sync.Mutex
	t     time.Time
	step  time.Duration
	reads int
}

func newStepClock() *stepClock {
	return &stepClock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC), step: time.Second}
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(c.step)
	c.reads++
	return c.t
}

func (c *stepClock) readCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads
}

// Every phase exists at zero from construction, so an alert or a dashboard on the rate sees a
// series on a healthy instance rather than none.
func TestLoopPhaseSeriesExistAtZero(t *testing.T) {
	m := phaseMetrics()
	equal(t, "phases", len(loopPhaseNames), int(numLoopPhases))
	seen := map[string]bool{}
	for p := loopPhase(0); p < numLoopPhases; p++ {
		if loopPhaseNames[p] == "" || seen[loopPhaseNames[p]] {
			t.Fatalf("phase %d has a missing or repeated name %q", p, loopPhaseNames[p])
		}
		seen[loopPhaseNames[p]] = true
		equal(t, "seconds{"+loopPhaseNames[p]+"}", phaseSeconds(m, p), 0.0)
	}
	equal(t, "histogram series", testutil.CollectAndCount(m.checkpointDuration.(prometheus.Collector)), 1)
}

// A stopwatch that was never armed does nothing, which is what lets the same checkpoint code run
// during replay and at the end of a term without touching the loop's accounting.
func TestLoopPhasesUnarmedAreInert(t *testing.T) {
	var nilPhases *loopPhases
	nilPhases.start(phaseMetrics())
	equal(t, "nil enter", nilPhases.enter(phaseApply), phaseApply)
	nilPhases.flush()
	nilPhases.stop()

	var l loopPhases
	l.start(nil) // no metrics: stays unarmed
	equal(t, "unarmed enter", l.enter(phaseApply), phaseApply)
	l.flush()

	// A checkpoint outside the loop (replay, the end-of-term flush) on a processor WITH metrics
	// counts nothing either: only run arms the stopwatch.
	g := newGapRig(t, nil)
	g.rp.handle(hot(t, 1, &fakeAck{}))
	g.rp.checkpoint(g.rp.pctx())
	equal(t, "checkpoint outside the loop", totalPhaseSeconds(g.metrics), 0.0)
}

// Time goes to the phase that was current, and flush credits the phase that is STILL running, so
// a loop parked in one phase is seen to be there at the next tick and not only once it wakes.
func TestStopwatchCreditsTheRunningPhaseOnFlushAndStop(t *testing.T) {
	m, clk := phaseMetrics(), newStepClock()
	l := &loopPhases{now: clk.Now}
	l.start(m) // read 0, in fetch_wait

	equal(t, "enter returns the phase it left", l.enter(phaseApply), phaseWait) // read 1 credits fetch_wait
	equal(t, "counters move only at a flush", phaseSeconds(m, phaseWait), 0.0)
	l.flush() // read 2 credits apply, then writes the counters
	equal(t, "wait after flush", phaseSeconds(m, phaseWait), 1.0)
	equal(t, "apply after flush", phaseSeconds(m, phaseApply), 1.0)
	l.flush() // read 3: apply is still running
	equal(t, "apply keeps accruing", phaseSeconds(m, phaseApply), 2.0)

	l.enter(phaseSave) // read 4 credits apply; save is now running
	l.stop()           // read 5 credits save, flushes, disarms
	equal(t, "apply", phaseSeconds(m, phaseApply), 3.0)
	equal(t, "save credited by stop", phaseSeconds(m, phaseSave), 1.0)
	equal(t, "everything accounted for", totalPhaseSeconds(m), 5.0) // reads 0 to 5
	l.enter(phaseAck)
	l.flush()
	equal(t, "disarmed after stop", totalPhaseSeconds(m), 5.0)
}

// A checkpoint borrows the stopwatch for its own phases and hands it back, so the work after it
// is credited to the phase it was in before, and each of its three stages is its own phase.
func TestCheckpointCreditsItsStagesAndHandsTheStopwatchBack(t *testing.T) {
	g := newGapRig(t, nil)
	rp := g.rp
	clk := newStepClock()
	rp.phases.now = clk.Now
	rp.phases.start(g.metrics) // read 0
	defer rp.phases.stop()

	rp.handle(hot(t, 1, &fakeAck{})) // enters plan (read 1) and apply (read 2)
	if !rp.checkpoint(rp.pctx()) {
		t.Fatal("checkpoint did not commit")
	}
	equal(t, "phase after checkpoint", rp.phases.cur, phaseApply)
	// enter(publish) read 3 credited apply; enter(save) read 4 credited publish; enter(ack)
	// read 5 credited save; flush read 6 credited ack and wrote the counters; the hand-back read 7
	// is not credited until the next change.
	equal(t, "fetch_wait", phaseSeconds(g.metrics, phaseWait), 1.0)
	equal(t, "plan", phaseSeconds(g.metrics, phasePlan), 1.0)
	equal(t, "apply", phaseSeconds(g.metrics, phaseApply), 1.0)
	equal(t, "publish", phaseSeconds(g.metrics, phasePublish), 1.0)
	equal(t, "save", phaseSeconds(g.metrics, phaseSave), 1.0)
	equal(t, "ack", phaseSeconds(g.metrics, phaseAck), 1.0)
}

// The message held back by a gap is decoded after the gap is read, not applied: the stretch
// between the last filled message and the held one's fan-out is decode.
func TestAMessageHeldByAGapIsDecodedAfterTheFill(t *testing.T) {
	opener := &gatedOpener{open: true}
	opener.msgs = stream(t, 1, 2)
	g := newGapRig(t, opener)
	rp := g.rp
	clk := newStepClock()
	rp.phases.now = clk.Now
	rp.phases.start(g.metrics) // read 0

	rp.handle(hot(t, 3, &fakeAck{}))
	rp.phases.stop()
	// fillGap enters gapfill (read 1); messages 1 and 2 each enter plan and apply (reads 2 to 5);
	// the fill ends by entering decode (read 6); message 3 enters plan (read 7) and apply (read 8);
	// stop (read 9) credits apply.
	equal(t, "fetch_wait", phaseSeconds(g.metrics, phaseWait), 1.0)
	equal(t, "gapfill", phaseSeconds(g.metrics, phaseGapFill), 1.0)
	equal(t, "decode", phaseSeconds(g.metrics, phaseDecode), 1.0)
	equal(t, "plan", phaseSeconds(g.metrics, phasePlan), 3.0)
	equal(t, "apply", phaseSeconds(g.metrics, phaseApply), 3.0)
}

// tickGatedEOFReader serves its results and then holds the end of the stream back until ticked
// reports true, so the loop is guaranteed to have been through at least one tick before it ends.
// The wait is bounded by the read context, which the test's own deadline cancels.
type tickGatedEOFReader struct {
	fakeReader
	ticked func() bool
}

func (r *tickGatedEOFReader) ReadMessage(ctx context.Context) (messaging.Message, error) {
	if r.idx >= len(r.results) {
		for !r.ticked() {
			select {
			case <-ctx.Done():
				return messaging.Message{}, io.EOF
			case <-time.After(time.Millisecond):
			}
		}
	}
	return r.fakeReader.ReadMessage(ctx)
}

// The phases PARTITION the loop's time: whatever it is doing it is in exactly one, so across a
// run they add up to the run EXACTLY (the first clock read to the last), and a loop that is not
// saturated is visibly mostly in fetch_wait. The clock is a step clock, so "exactly" means exactly.
func TestLoopPhasesPartitionTheLoopsTimeExactly(t *testing.T) {
	g := newGapRig(t, nil)
	rp := g.rp
	const messages = 210 // not a multiple of the checkpoint size, so the exit checkpoint has work
	acks := make([]*fakeAck, messages)
	results := make([]readResult, messages)
	for i := range acks {
		acks[i] = &fakeAck{acked: make(chan struct{}, 4)}
		results[i] = readResult{msg: hot(t, uint64(i+1), acks[i])}
	}
	// The reader runs dry and reports EOF, which ends the loop through its end-of-stream exit, but
	// only once a tick has been credited to control. Control is entered only by the ticker, and a
	// loop that drains 210 messages faster than one tick interval would otherwise reach the
	// end of the stream before any tick fired (a fast Linux runner does), leaving the phase at zero
	// through no fault of the stopwatch.
	rp.ResolvedEventsReader = &tickGatedEOFReader{
		fakeReader: fakeReader{results: results, readEOFImmediately: true},
		ticked:     func() bool { return phaseSeconds(g.metrics, phaseControl) > 0 },
	}
	rp.cfg.TickInterval = 2 * time.Millisecond
	rp.cfg.CheckpointEvents = 50
	clk := newStepClock()
	rp.phases.now = clk.Now
	rp.readerWG.Add(1)
	go rp.run()

	done := make(chan struct{})
	go func() { rp.readerWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the loop did not end at the end of the stream")
	}
	for i, a := range acks {
		select {
		case <-a.acked:
		default:
			t.Fatalf("message %d was not acked", i+1)
		}
	}
	// readCount-1 steps lie between the first read (start) and the last (stop), and every one of
	// them was credited to exactly one phase.
	equal(t, "every step accounted for", totalPhaseSeconds(g.metrics), float64(clk.readCount()-1))
	// Each message is one step of decode and exactly one of plan (no clock read lies inside either), and
	// the loop returns to its select after every one, so each is followed by a step of waiting.
	// The end-of-stream item is itself received in decode, and the exit checkpoint hands the
	// stopwatch back to it, so decode is the messages plus those two.
	if d := phaseSeconds(g.metrics, phaseDecode); d < messages || d > messages+2 {
		t.Errorf("decode is %v steps for %d messages and an end of stream", d, messages)
	}
	equal(t, "plan: one step per message", phaseSeconds(g.metrics, phasePlan), float64(messages))
	if w := phaseSeconds(g.metrics, phaseWait); w < messages {
		t.Errorf("fetch_wait is %v steps across %d messages: the loop's return to its select is not being counted", w, messages)
	}
	for _, p := range []loopPhase{phaseWait, phaseDecode, phasePlan, phaseApply, phasePublish, phaseSave, phaseAck, phaseControl} {
		if phaseSeconds(g.metrics, p) <= 0 {
			t.Errorf("phase %s was never credited across %d messages, their checkpoints and the idle ticks", loopPhaseNames[p], messages)
		}
	}
	if n := testutil.ToFloat64(g.metrics.checkpointsTotal); n < 5 {
		t.Fatalf("expected the periodic checkpoints and the exit one to commit; got %v", n)
	}
	equal(t, "checkpoint durations observed", histogramCount(t, g.metrics.checkpointDuration), uint64(testutil.ToFloat64(g.metrics.checkpointsTotal)))
}

// A loop parked on a gap it cannot read is `parked`, not `fetch_wait`; the ticker's own broker
// round trips are `probe`; and every attempt at the gap is exactly one step of `gapfill`, with the
// rest of the tick credited to `control` and not left in gapfill after the retry.
func TestParkedLoopAndTickerProbesAreTheirOwnPhases(t *testing.T) {
	opener := &gatedOpener{}
	opener.msgs = stream(t, 1, 2)
	acks := map[uint64]*fakeAck{3: {acked: make(chan struct{}, 4)}, 4: {acked: make(chan struct{}, 4)}}
	reader := &feedReader{fakeReader: fakeReader{results: []readResult{
		{msg: hot(t, 3, acks[3])},
		{msg: hot(t, 4, acks[4])},
	}}}
	g := newGapRig(t, opener)
	rp := g.rp
	probe := &countingProbe{}
	rp.backlogProbe = probe
	rp.ResolvedEventsReader = reader
	rp.cfg.TickInterval = 2 * time.Millisecond
	rp.cfg.CheckpointInterval = time.Millisecond // the lag gauge is sampled at this cadence: every tick
	clk := newStepClock()
	rp.phases.now = clk.Now
	rp.readerWG.Add(1)
	go rp.run()

	waitFor(t, "three failed attempts at the gap", func() bool { return opener.attempts.Load() >= 3 })
	opener.mu.Lock()
	opener.open = true // the broker comes back: the stream is read and applied
	opener.mu.Unlock()
	for _, seq := range []uint64{3, 4} {
		select {
		case <-acks[seq].acked:
		case <-time.After(10 * time.Second):
			t.Fatalf("message %d was never acked after the gap was readable", seq)
		}
	}
	rp.pcancel()
	rp.readerWG.Wait()

	if phaseSeconds(g.metrics, phaseParked) <= 0 {
		t.Error("the loop sat parked on a gap and never counted a parked step")
	}
	equal(t, "every step accounted for", totalPhaseSeconds(g.metrics), float64(clk.readCount()-1))
	equal(t, "gapfill: one step per attempt", phaseSeconds(g.metrics, phaseGapFill), float64(opener.attempts.Load()))
	if probe.calls.Load() == 0 {
		t.Fatal("the ticker never probed the backlog")
	}
	equal(t, "probe: one step per round trip", phaseSeconds(g.metrics, phaseProbe), float64(probe.calls.Load()))
}

func histogramCount(t *testing.T, o prometheus.Observer) uint64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 1)
	o.(prometheus.Collector).Collect(ch)
	var d dto.Metric
	if err := (<-ch).Write(&d); err != nil {
		t.Fatalf("write histogram: %v", err)
	}
	return d.GetHistogram().GetSampleCount()
}

// BenchmarkLoopPhasesStopwatch is the stopwatch alone: the four phase changes the live loop makes
// around one message (wait, decode, plan, apply) and a flush every thousand messages, which is the
// whole of what it adds to a message's cost.
func BenchmarkLoopPhasesStopwatch(b *testing.B) {
	m := phaseMetrics()
	var l loopPhases
	l.start(m)
	defer l.stop()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		l.enter(phaseWait)
		l.enter(phaseDecode)
		l.enter(phasePlan)
		l.enter(phaseApply)
		if i%1000 == 999 {
			l.flush()
		}
	}
}

// TestLoopPhaseOverhead measures what the stopwatch costs the loop per message, as the same
// handle cycle the live loop runs with the stopwatch armed and unarmed, interleaved over several
// rounds and reported as the median because a single pair is dominated by noise. Checkpoints are
// left out so that their store writes do not drown the difference. It is a measurement, not a
// gate, and runs only on request:
//
//	DC_LOOP_PHASE_BENCH=1 go test ./processor -run TestLoopPhaseOverhead -v -count=1
func TestLoopPhaseOverhead(t *testing.T) {
	if os.Getenv("DC_LOOP_PHASE_BENCH") == "" {
		t.Skip("set DC_LOOP_PHASE_BENCH=1 to measure the stopwatch's per-message cost")
	}
	const devices = 256
	msgs := make([]messaging.Message, 0, 2*devices)
	for _, v := range []string{"90", "70"} { // alternate, so every message raises or resolves
		for d := 0; d < devices; d++ {
			msgs = append(msgs, measuredMsg(t, 1, "acme", "dev-"+strconv.Itoa(d), "p@1", "temperature", v, &fakeAck{}))
		}
	}
	measure := func(armed bool) float64 {
		res := testing.Benchmark(func(b *testing.B) {
			g := newGapRig(t, nil)
			rp := g.rp
			if armed {
				rp.phases.start(g.metrics)
				defer rp.phases.stop()
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m := msgs[i%devices+devices*((i/devices)%2)]
				m.StreamSeq = uint64(i + 1)
				// The same stopwatch calls the live loop makes around a message.
				rp.phases.enter(phaseWait)
				rp.phases.enter(phaseDecode)
				rp.handle(m)
				if i%1000 == 999 {
					rp.phases.flush()
					rp.pendingAcks = rp.pendingAcks[:0]
					rp.pendingDets = rp.pendingDets[:0]
				}
			}
		})
		return float64(res.NsPerOp())
	}
	const rounds = 9
	offs, ons, deltas := make([]float64, rounds), make([]float64, rounds), make([]float64, rounds)
	for i := 0; i < rounds; i++ {
		offs[i], ons[i] = measure(false), measure(true)
		deltas[i] = ons[i] - offs[i]
	}
	median := func(x []float64) float64 {
		c := append([]float64(nil), x...)
		sort.Float64s(c)
		return c[len(c)/2]
	}
	t.Logf("per message over %d rounds: unarmed median %.0f ns, armed median %.0f ns, stopwatch median delta %+.0f ns (%+.2f%%); all deltas %v",
		rounds, median(offs), median(ons), median(deltas), median(deltas)/median(offs)*100, deltas)
}

// The early returns hand the stopwatch back too: a snapshot retry backoff declines before the
// save, and a forced checkpoint (the final flush, a tenant eviction) goes through it and is
// credited by stage like any other.
func TestBackedOffAndForcedCheckpointsKeepThePhasesExact(t *testing.T) {
	g := newGapRig(t, nil)
	rp := g.rp
	clk := newStepClock()
	rp.phases.now = clk.Now
	rp.phases.start(g.metrics) // read 0
	defer rp.phases.stop()

	rp.handle(hot(t, 1, &fakeAck{})) // plan (read 1), apply (read 2)
	rp.snapshotRetryAfter = time.Now().Add(time.Hour)
	if rp.checkpoint(rp.pctx()) {
		t.Fatal("a checkpoint inside the retry backoff committed")
	}
	equal(t, "phase after the backed-off checkpoint", rp.phases.cur, phaseApply)

	if !rp.forceCheckpoint(rp.pctx()) {
		t.Fatal("a forced checkpoint did not commit through the backoff")
	}
	equal(t, "phase after the forced checkpoint", rp.phases.cur, phaseApply)
	equal(t, "save", phaseSeconds(g.metrics, phaseSave), 1.0)
	equal(t, "ack", phaseSeconds(g.metrics, phaseAck), 1.0)
}
