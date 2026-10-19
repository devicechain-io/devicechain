// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

// The pipelined publisher (derived_publish.go), judged by the same output oracle as the serial
// baseline: per-lane order, nothing durable ahead of its output, no fire ahead of its
// acknowledgement, nothing missing. Window 1 must be the serial path exactly; a wider window
// must overlap independent lanes, keep one publish per lane outstanding, and on a failure
// retire only the settled prefix and retry the same bytes.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"reflect"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/messaging"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// overlapWriter is a broker whose acknowledgements genuinely overlap: each write waits on its
// own caller's goroutine for a fixed time plus a deterministic jitter taken from the dedup id, so
// concurrent writes complete in an order other than the one they were sent in. It records what
// it saw: the most writes outstanding at once, the most outstanding for any one lane, and how
// often a write completed ahead of one sent before it.
type overlapWriter struct {
	ack, jitter time.Duration

	mu          sync.Mutex
	inFlight    int
	maxInFlight int
	lanes       map[string]int
	maxLane     int
	sent        uint64
	open        map[uint64]bool // sends not yet complete, by send number
	overtakes   int             // completions while an EARLIER send was still open
}

func newOverlapWriter(ack, jitter time.Duration) *overlapWriter {
	return &overlapWriter{ack: ack, jitter: jitter, lanes: map[string]int{}, open: map[uint64]bool{}}
}

func laneOfPayload(b []byte) string {
	var de runtime.DerivedEvent
	_ = json.Unmarshal(b, &de)
	return de.Tenant + "|" + de.Series
}

func (w *overlapWriter) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	lane := laneOfPayload(msgs[0].Value)
	w.mu.Lock()
	w.sent++
	n := w.sent
	w.open[n] = true
	w.inFlight++
	w.maxInFlight = max(w.maxInFlight, w.inFlight)
	w.lanes[lane]++
	w.maxLane = max(w.maxLane, w.lanes[lane])
	w.mu.Unlock()

	d := w.ack
	if w.jitter > 0 {
		h := fnv.New32a()
		_, _ = h.Write([]byte(msgs[0].DedupID))
		d += time.Duration(h.Sum32()%1000) * w.jitter / 1000
	}
	if d > 0 {
		time.Sleep(d)
	}

	w.mu.Lock()
	delete(w.open, n)
	for m := range w.open {
		if m < n {
			w.overtakes++
			break
		}
	}
	w.inFlight--
	w.lanes[lane]--
	w.mu.Unlock()
	return nil
}

func (w *overlapWriter) WriteToDevice(ctx context.Context, _ string, msgs ...messaging.Message) error {
	return w.WriteMessages(ctx, msgs...)
}

func (w *overlapWriter) HandleResponse(error) {}

func (w *overlapWriter) seen() (maxInFlight, maxLane, overtakes int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.maxInFlight, w.maxLane, w.overtakes
}

// attempt is one write a faultWriter was asked to make.
type attempt struct {
	rule    string
	dedupID string
	payload []byte
	lane    string
	edge    string
	at      time.Time
}

// faultWriter sits above the oracle's writer and decides, per attempt, what the broker does:
// store it and acknowledge (nil), store it and lose the acknowledgement (stored, err), or
// refuse it without storing (err). It records every attempt in order.
type faultWriter struct {
	inner messaging.MessageWriter
	// decide sees the attempt, its 1-based position among all attempts, and how many earlier
	// attempts had the same dedup id.
	decide func(a attempt, index, earlier int) (store bool, err error)
	// stored, when set, is called after an attempt the broker stored and before its outcome is
	// returned to the caller.
	stored func(a attempt)

	mu       sync.Mutex
	attempts []attempt
	byID     map[string]int
}

func (w *faultWriter) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	var de runtime.DerivedEvent
	_ = json.Unmarshal(msgs[0].Value, &de)
	a := attempt{rule: de.RuleID, dedupID: msgs[0].DedupID, payload: append([]byte(nil), msgs[0].Value...),
		lane: de.Tenant + "|" + de.Series, edge: de.Edge, at: de.OccurredTime}
	w.mu.Lock()
	if w.byID == nil {
		w.byID = map[string]int{}
	}
	earlier := w.byID[a.dedupID]
	w.byID[a.dedupID]++
	w.attempts = append(w.attempts, a)
	index := len(w.attempts)
	w.mu.Unlock()
	store, err := true, error(nil)
	if w.decide != nil {
		store, err = w.decide(a, index, earlier)
	}
	if store {
		if ierr := w.inner.WriteMessages(ctx, msgs...); ierr != nil {
			return ierr
		}
		if w.stored != nil {
			w.stored(a)
		}
	}
	return err
}

func (w *faultWriter) WriteToDevice(ctx context.Context, _ string, msgs ...messaging.Message) error {
	return w.WriteMessages(ctx, msgs...)
}

func (w *faultWriter) HandleResponse(error) {}

func (w *faultWriter) snapshot() []attempt {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]attempt(nil), w.attempts...)
}

func faultVariant(window int, fw **faultWriter, decide func(attempt, int, int) (bool, error)) outputVariant {
	v := variantPipelined(window)
	v.wrapWriter = func(_ testing.TB, inner messaging.MessageWriter) messaging.MessageWriter {
		*fw = &faultWriter{inner: inner, decide: decide}
		return *fw
	}
	return v
}

var pipelineWindows = []int{2, 8, 32, 1024}

// Window 1 is the serial path exactly, and with batched rule-health stats too: the pipelined
// publisher at window 1 (C) writes the same log as the serial adapter (B), fires and all, and
// the table ends the same. (TestTheBaselineAdapterIsTheProductionPath holds it to A with inline
// stats.)
func TestWindowOneIsTheSerialPathWithBatchedStats(t *testing.T) {
	bStore, cStore := newSqliteRuleStatStore(t, nil), newSqliteRuleStatStore(t, nil)
	b := newOutputRig(t, outputRigSpec{fx: oracleFixture, variant: variantB, checkpointEvents: oracleCheckpoint, batch: bStore})
	b.run(t, oracleFixture.messages(t, oracleInputs, b.log))
	c := newOutputRig(t, outputRigSpec{fx: oracleFixture, variant: variantPipelined(1), checkpointEvents: oracleCheckpoint, batch: cStore})
	c.run(t, oracleFixture.messages(t, oracleInputs, c.log))
	vb, vc := judgeOutput(oracleFixture, oracleInputs, b.log.snapshot()), judgeOutput(oracleFixture, oracleInputs, c.log.snapshot())
	require.True(t, vb.clean(), "%s", vb)
	require.True(t, vc.clean(), "%s", vc)
	require.Equal(t, b.log.snapshot(), c.log.snapshot(), "window 1 published, saved, acknowledged or admitted fires differently from the serial path")
	requireStatsEqual(t, loadFixtureStats(t, oracleFixture, bStore), loadFixtureStats(t, oracleFixture, cStore), "C against B")
}

// Above window 1 the oracle still passes, on a broker that really overlaps acknowledgements and
// completes them out of order, and the window is used: several lanes outstanding at once, never
// more than the window, never two of one lane. The rule-health table ends exactly as the source
// implies, last edges included, because fires are recorded in buffer order, not completion order.
func TestTheOutputOracleAcceptsEveryWindow(t *testing.T) {
	for _, window := range pipelineWindows {
		t.Run(fmt.Sprintf("window=%d", window), func(t *testing.T) {
			t.Parallel()
			broker := newOverlapWriter(100*time.Microsecond, 400*time.Microsecond)
			store := newSqliteRuleStatStore(t, nil)
			r := newOutputRig(t, outputRigSpec{fx: oracleFixture, broker: broker, variant: variantPipelined(window),
				checkpointEvents: oracleCheckpoint, batch: store})
			r.run(t, oracleFixture.messages(t, oracleInputs, r.log))
			v := judgeOutput(oracleFixture, oracleInputs, r.log.snapshot())
			want := len(oracleFixture.expectedDetections(oracleInputs))
			require.True(t, v.clean(), "window %d failed the oracle: %s", window, v)
			require.Equal(t, want, v.published)
			require.Equal(t, want, v.stats, "one fire per acknowledged detection")
			require.Zero(t, v.duplicates)
			require.Equal(t, 3, r.checkpoints)
			requireStatsEqual(t, expectedRuleStats(oracleFixture, oracleInputs), loadFixtureStats(t, oracleFixture, store),
				fmt.Sprintf("window %d", window))

			maxInFlight, maxLane, overtakes := broker.seen()
			lanes := len(oracleFixture.tenants) * oracleFixture.devices
			require.Equal(t, 1, maxLane, "two publishes of one lane were outstanding at once")
			require.LessOrEqual(t, maxInFlight, window, "more publishes outstanding than the window")
			// Filling the window exactly depends on preparing the next detections faster than the
			// broker answers, which a slow (race-instrumented) run need not do; overlap is the claim.
			require.LessOrEqual(t, maxInFlight, lanes)
			require.Greater(t, maxInFlight, 1, "no two publishes were ever outstanding at once; the window was not used")
			require.Positive(t, overtakes, "no acknowledgement ever overtook an earlier one; the test proves nothing about completion order")
			acks, _ := histogramStats(t, r.metrics.derivedPublishAck)
			require.Equal(t, uint64(want), acks, "one acknowledgement timing per published event")
			require.Zero(t, testutil.ToFloat64(r.metrics.publishStale))
			require.Zero(t, testutil.ToFloat64(r.metrics.publishOutstanding), "outstanding gauge left non-zero")
			t.Logf("window %d: %s; max in flight %d, overtakes %d, lane waits %.0f", window, v, maxInFlight, overtakes,
				testutil.ToFloat64(r.metrics.publishLaneWaits))
		})
	}
}

// The window-full time is the wait a wider window would remove: at window 1 nearly all of the
// publish wait, at a window narrower than the lanes some of it, and none at all once every lane
// can have its publish outstanding (the lanes, not the window, are then the limit).
func TestWindowFullTimeIsTheWaitAWiderWindowWouldRemove(t *testing.T) {
	full := map[int]float64{}
	for _, window := range []int{1, 2, 32} {
		r := newOutputRig(t, outputRigSpec{fx: oracleFixture, broker: latencyWriter{ack: 200 * time.Microsecond},
			variant: variantPipelined(window), checkpointEvents: oracleCheckpoint, stats: newSqliteRuleStatStore(t, nil)})
		r.run(t, oracleFixture.messages(t, oracleInputs, r.log))
		v := judgeOutput(oracleFixture, oracleInputs, r.log.snapshot())
		require.True(t, v.clean(), "%s", v)
		full[window] = testutil.ToFloat64(r.metrics.publishWindowFull)
		_, ackSum := histogramStats(t, r.metrics.derivedPublishAck)
		if window == 1 {
			require.Greater(t, full[window], ackSum/2, "window 1: most of the acknowledgement wait is window-full")
		}
	}
	require.Positive(t, full[2], "window 2 over 8 lanes was never full")
	require.Less(t, full[2], full[1])
	require.Zero(t, full[32], "with every lane outstanding the window cannot be what is full")
}

// 🔴 NEGATIVE CONTROLS, at a wide window. The two planted defects still fail the oracle when the
// rest of the path is the pipelined one, so a clean verdict above is not the oracle going blind
// under concurrency.
func TestThePlantedDefectsStillFailAtAWideWindow(t *testing.T) {
	t.Run("reorder", func(t *testing.T) {
		v := variantPipelined(32)
		v.name = "planted-reorder-D32"
		v.checkpoint = func(ctx context.Context, rp *ResolvedEventsProcessor) bool {
			swapFirstTwoInputsPerLane(rp)
			return rp.checkpoint(ctx)
		}
		r := newOutputRig(t, outputRigSpec{fx: oracleFixture, broker: newOverlapWriter(50*time.Microsecond, 200*time.Microsecond),
			variant: v, checkpointEvents: oracleCheckpoint, stats: newSqliteRuleStatStore(t, nil)})
		r.run(t, oracleFixture.messages(t, oracleInputs, r.log))
		verdict := judgeOutput(oracleFixture, oracleInputs, r.log.snapshot())
		require.NotEmpty(t, verdict.ordering, "a reordered lane passed at window 32: %s", verdict)
		require.Empty(t, verdict.earlyDurable, "%s", verdict)
	})
	t.Run("early-ack", func(t *testing.T) {
		var held *heldWriter
		v := variantPipelined(32)
		v.name = "planted-early-ack-D32"
		v.wrapWriter = func(_ testing.TB, inner messaging.MessageWriter) messaging.MessageWriter {
			held = &heldWriter{inner: inner}
			return held
		}
		v.checkpoint = func(ctx context.Context, rp *ResolvedEventsProcessor) bool {
			ok := rp.checkpoint(ctx)
			require.NoError(t, held.release())
			return ok
		}
		r := newOutputRig(t, outputRigSpec{fx: oracleFixture, variant: v, checkpointEvents: oracleCheckpoint,
			stats: newSqliteRuleStatStore(t, nil)})
		r.run(t, oracleFixture.messages(t, oracleInputs, r.log))
		verdict := judgeOutput(oracleFixture, oracleInputs, r.log.snapshot())
		require.NotEmpty(t, verdict.earlyDurable, "an early ack passed at window 32: %s", verdict)
		require.NotEmpty(t, verdict.statAhead, "%s", verdict)
	})
}

// swapFirstTwoInputsPerLane is the planted reorder: in every lane, the first buffered detection
// trades places with the first one from a later input.
func swapFirstTwoInputsPerLane(rp *ResolvedEventsProcessor) {
	firstInLane := map[string]int{}
	done := map[string]bool{}
	for i := range rp.pendingDets {
		d := rp.pendingDets[i].Detection
		tenant, _ := runtime.RuleTenant(d.RuleID)
		lane := tenant + "|" + d.Series
		j, ok := firstInLane[lane]
		switch {
		case done[lane]:
		case !ok:
			firstInLane[lane] = i
		case !rp.pendingDets[j].At.Equal(d.At):
			rp.pendingDets[i], rp.pendingDets[j] = rp.pendingDets[j], rp.pendingDets[i]
			done[lane] = true
		}
	}
}

// pipelineRig is a rig driven by hand: the inputs handled with no checkpoint, then checkpoints
// called one at a time so a test can look between them.
func pipelineRig(t *testing.T, window int, fw **faultWriter, decide func(attempt, int, int) (bool, error)) (*outputRig, []messaging.Message) {
	t.Helper()
	r := newOutputRig(t, outputRigSpec{fx: oracleFixture, variant: faultVariant(window, fw, decide),
		checkpointEvents: 1 << 30, batch: newSqliteRuleStatStore(t, nil)})
	msgs := oracleFixture.messages(t, 400, r.log)
	for _, m := range msgs {
		r.rp.handle(m)
	}
	require.NotEmpty(t, r.rp.pendingDets)
	return r, msgs
}

func logKinds(events []outputEvent) (published, saved, acked int) {
	for _, e := range events {
		switch e.kind {
		case evPublished:
			published++
		case evSaved:
			saved++
		case evInputAck:
			acked++
		}
	}
	return
}

// An UNKNOWN outcome — the broker stored the event and the acknowledgement was lost — defers the
// checkpoint (nothing saved, nothing acknowledged), and the retry sends the SAME bytes under the
// SAME dedup id, so the broker can collapse it. Everything that did get acknowledged is never
// sent again, even the detections after the failed one in the buffer, and each detection's fire
// is recorded exactly once.
func TestAnUnknownOutcomeIsRetriedWithTheSameBytesAndDedupID(t *testing.T) {
	for _, window := range []int{1, 2, 8, 32} {
		t.Run(fmt.Sprintf("window=%d", window), func(t *testing.T) {
			var fw *faultWriter
			var target atomic.Pointer[pendingDetection]
			var victim atomic.Value
			var r *outputRig
			// later are the attempts of buffer positions after the victim's, other than its lane.
			var later sync.Map
			var storedTotal, laterStored atomic.Int64
			r, _ = pipelineRig(t, window, &fw, func(a attempt, _, earlier int) (bool, error) {
				// The 20th buffered detection's first attempt: stored, acknowledgement lost.
				if d := target.Load(); earlier == 0 && d != nil && a.rule == d.RuleID && a.lane == "acme|"+d.Series && a.at.Equal(d.At) {
					victim.Store(a.dedupID)
					if window > 1 {
						// Fix the completion order: the loss is reported only after an item behind
						// the victim in the buffer was stored AND its acknowledgement applied, so the
						// known-success suffix the test asserts exists by construction, not by luck.
						deadline := time.Now().Add(10 * time.Second)
						for time.Now().Before(deadline) {
							if laterStored.Load() > 0 && testutil.ToFloat64(r.metrics.derivedPublished) >= float64(storedTotal.Load()) {
								break
							}
							time.Sleep(time.Millisecond)
						}
					}
					return true, fmt.Errorf("lost: %w", nats.ErrTimeout)
				}
				return true, nil
			})
			fw.stored = func(a attempt) {
				storedTotal.Add(1)
				if _, ok := later.Load(a.rule + "|" + a.lane + "|" + strconv.FormatInt(a.at.UnixNano(), 10)); ok {
					laterStored.Add(1)
				}
			}
			k := 19
			for mustTenant(t, r.rp.pendingDets[k].RuleID) != "acme" {
				k++
			}
			d := r.rp.pendingDets[k]
			for _, o := range r.rp.pendingDets[k+1:] {
				if tenant := mustTenant(t, o.RuleID); tenant+"|"+o.Series != "acme|"+d.Series {
					later.Store(o.RuleID+"|"+tenant+"|"+o.Series+"|"+strconv.FormatInt(o.At.UnixNano(), 10), true)
				}
			}
			target.Store(&d)
			ctx := context.Background()
			require.False(t, r.rp.checkpoint(ctx), "a checkpoint with an unknown publish outcome committed")
			_, saved, acked := logKinds(r.log.snapshot())
			require.Zero(t, saved, "a snapshot was saved ahead of an unknown publish")
			require.Zero(t, acked, "inputs were acknowledged ahead of an unknown publish")
			require.Equal(t, 1.0, testutil.ToFloat64(r.metrics.publishFailuresBy[publishOutcomeUnknown]))
			if window > 1 {
				var ackedAhead int
				for _, d := range r.rp.pendingDets {
					if d.state == outputAcked {
						ackedAhead++
					}
				}
				require.Positive(t, ackedAhead, "no known-success suffix was retained; the test proves nothing about it")
			}

			require.True(t, r.rp.checkpoint(ctx), "the retry did not commit")
			require.Empty(t, r.rp.pendingDets)
			id := victim.Load().(string)
			var tries []attempt
			for _, a := range fw.snapshot() {
				if a.dedupID == id {
					tries = append(tries, a)
				}
			}
			require.Len(t, tries, 2, "the unknown publish was not retried exactly once")
			require.True(t, bytes.Equal(tries[0].payload, tries[1].payload), "the retry sent different bytes")
			for dedup, n := range fw.byID {
				if dedup != id {
					require.Equal(t, 1, n, "a detection that was acknowledged was sent again")
				}
			}
			r.acc.Stop(context.Background(), true)
			v := judgeOutput(oracleFixture, 400, r.log.snapshot())
			want := len(oracleFixture.expectedDetections(400))
			require.True(t, v.clean(), "%s", v)
			require.Equal(t, 1, v.duplicates, "the broker stored the lost-ack publish and its retry; the fake does not dedup")
			require.Equal(t, want, v.stats, "a fire was recorded twice or not at all")
		})
	}
}

// A REFUSED raise holds back its whole lane: nothing after it in that lane is sent until it is
// acknowledged, though other lanes carry on. Then the retry succeeds and the lane follows in
// order.
func TestAFailedPublishHoldsBackItsLaneOnly(t *testing.T) {
	var fw *faultWriter
	var victimLane atomic.Value
	var claimed atomic.Bool
	refused := errors.New("refused")
	r, _ := pipelineRig(t, 8, &fw, func(a attempt, _, earlier int) (bool, error) {
		if earlier == 0 && a.edge == runtime.EdgeRaised && claimed.CompareAndSwap(false, true) {
			victimLane.Store(a.lane)
			return false, refused
		}
		return true, nil
	})
	ctx := context.Background()
	require.False(t, r.rp.checkpoint(ctx))
	lane := victimLane.Load().(string)
	first := fw.snapshot()
	var inLane, otherLanes int
	for _, a := range first {
		if a.lane == lane {
			inLane++
		} else {
			otherLanes++
		}
	}
	require.Equal(t, 1, inLane, "a detection behind the refused one in its lane was sent")
	require.Positive(t, otherLanes)
	require.Equal(t, 1.0, testutil.ToFloat64(r.metrics.publishFailuresBy[publishOutcomeFailed]))

	require.True(t, r.rp.checkpoint(ctx))
	r.acc.Stop(context.Background(), true)
	v := judgeOutput(oracleFixture, 400, r.log.snapshot())
	require.True(t, v.clean(), "%s", v)
	require.Zero(t, v.duplicates, "a refused publish is not stored, so its retry is its only copy")
}

// Losing the partition mid-pass sends nothing more: what is in flight settles, nothing is saved,
// nothing is acknowledged, and the pass's workers are gone when the checkpoint returns.
func TestLosingThePartitionMidPassStopsSending(t *testing.T) {
	var held atomic.Bool
	held.Store(true)
	var fw *faultWriter
	r, _ := pipelineRig(t, 4, &fw, func(a attempt, index, _ int) (bool, error) {
		if index == 6 {
			held.Store(false)
		}
		time.Sleep(time.Millisecond)
		return true, nil
	})
	r.rp.Lease = &messaging.DistributedLease{}
	r.rp.Gate = NewTermGate()
	r.rp.Gate.Enter(held.Load)
	total := len(r.rp.pendingDets)
	before := goruntime.NumGoroutine()
	require.False(t, r.rp.checkpoint(context.Background()), "a checkpoint that lost its partition committed")
	sent := len(fw.snapshot())
	require.GreaterOrEqual(t, sent, 6)
	require.LessOrEqual(t, sent, 6+4, "sends continued after the partition was lost")
	require.Less(t, sent, total)
	_, saved, acked := logKinds(r.log.snapshot())
	require.Zero(t, saved)
	require.Zero(t, acked)
	require.Zero(t, testutil.ToFloat64(r.metrics.publishOutstanding))
	requireGoroutinesBack(t, before)
}

func mustTenant(t *testing.T, ruleID string) string {
	t.Helper()
	tenant, ok := runtime.RuleTenant(ruleID)
	require.True(t, ok, ruleID)
	return tenant
}

// requireGoroutinesBack waits briefly for the goroutine count to return to what it was.
func requireGoroutinesBack(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for goruntime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	require.LessOrEqual(t, goruntime.NumGoroutine(), before, "a publish pass left goroutines behind")
}

// A pass that fails leaves no worker behind either.
func TestAFailedPassLeavesNoWorkers(t *testing.T) {
	var fw *faultWriter
	r, _ := pipelineRig(t, 32, &fw, func(a attempt, index, earlier int) (bool, error) {
		if earlier == 0 && index%7 == 0 {
			return false, errors.New("refused")
		}
		return true, nil
	})
	before := goruntime.NumGoroutine()
	passes := 1
	for ; !r.rp.checkpoint(context.Background()); passes++ {
		require.Less(t, passes, 1000, "the buffer never drained: %d left", len(r.rp.pendingDets))
	}
	require.Greater(t, passes, 2, "the failures did not fail any pass; the test proves nothing")
	r.acc.Stop(context.Background(), true)
	v := judgeOutput(oracleFixture, 400, r.log.snapshot())
	require.True(t, v.clean(), "%s", v)
	requireGoroutinesBack(t, before)
}

// An acknowledged detection waiting behind a failed one is a known positive: a later pass that
// finds its rule gone does not turn it into a drop (its fire is still recorded), while the
// failed one, whose outcome was never positive, gets the orphan disposition like any detection
// whose rule is gone.
func TestAKnownSuccessSurvivesItsRuleBeingRemoved(t *testing.T) {
	var fw *faultWriter
	var once atomic.Bool
	var first atomic.Value
	r, _ := pipelineRig(t, 8, &fw, func(a attempt, _, _ int) (bool, error) {
		if l, _ := first.Load().(string); a.lane == l && once.CompareAndSwap(false, true) {
			return false, errors.New("refused")
		}
		return true, nil
	})
	// The buffer's first detection's lane: its first send fails, while the next seven in the
	// window are already out, so a suffix is acknowledged behind the failure.
	first.Store(mustTenant(t, r.rp.pendingDets[0].RuleID) + "|" + r.rp.pendingDets[0].Series)
	ctx := context.Background()
	require.False(t, r.rp.checkpoint(ctx))
	var acked, unsettled int
	for _, d := range r.rp.pendingDets {
		if d.state == outputAcked {
			acked++
		} else {
			unsettled++
		}
	}
	require.Positive(t, acked)
	sentBefore := len(fw.snapshot())
	for _, id := range r.rp.registry.IDs() {
		r.rp.registry.Remove(id)
	}
	require.True(t, r.rp.checkpoint(ctx), "a pass with only drops and known positives left did not commit")
	require.Equal(t, sentBefore, len(fw.snapshot()), "something was sent after every rule was removed")
	require.Equal(t, float64(unsettled), testutil.ToFloat64(r.metrics.derivedRejected.WithLabelValues(string(runtime.RejectOrphan))),
		"every detection without a positive outcome is an orphan drop, and only those")
	r.acc.Stop(context.Background(), true)
	v := judgeOutput(oracleFixture, 400, r.log.snapshot())
	require.Equal(t, v.published, v.stats, "a known positive's fire was lost, or a drop's was recorded")
	require.Empty(t, v.statAhead)
}

// A tenant purge between passes removes that tenant's buffered detections whatever their state,
// acknowledged ones included, so none of its fires is recorded after the purge; the other
// tenant's detections settle as usual.
func TestATenantPurgeRemovesItsKnownSuccessesToo(t *testing.T) {
	var fw *faultWriter
	var once atomic.Bool
	r, _ := pipelineRig(t, 8, &fw, func(a attempt, _, _ int) (bool, error) {
		if a.lane == "acme|d0" && once.CompareAndSwap(false, true) {
			return false, errors.New("refused")
		}
		return true, nil
	})
	ctx := context.Background()
	require.False(t, r.rp.checkpoint(ctx))
	var betaAcked int
	for _, d := range r.rp.pendingDets {
		if t0, _ := runtime.RuleTenant(d.RuleID); t0 == "beta" && d.state == outputAcked {
			betaAcked++
		}
	}
	require.Positive(t, betaAcked, "no acknowledged beta detection was waiting; the test proves nothing")
	r.rp.dropTenantBuffers(func(id string) bool { t0, _ := runtime.RuleTenant(id); return t0 == "beta" }, "beta")
	require.True(t, r.rp.checkpoint(ctx))
	r.acc.Stop(context.Background(), true)
	v := judgeOutput(oracleFixture, 400, r.log.snapshot())
	require.Empty(t, v.statAhead)
	require.Equal(t, v.published-betaAcked, v.stats, "a purged tenant's waiting fire was recorded")
}

// The byte budget bounds what is outstanding; a single event over the whole budget is sent alone.
func TestTheByteBudgetBoundsWhatIsOutstanding(t *testing.T) {
	probe := runtime.NewPublisher(nil, oracleFixture.registry(t), nil)
	po, ok := probe.Prepare(detectcore.Detection{RuleID: oracleFixture.ruleID("acme", 0), Series: "d0", At: outputBase,
		Edge: detectcore.EdgeRaised, Kind: detectcore.Threshold}, outputBase)
	require.True(t, ok)
	size := int64(len(po.Payload))
	for _, tc := range []struct {
		name    string
		budget  int64
		maxSeen int
	}{
		{"every event over the budget", 1, 1},
		{"room for two", 2*size + size/2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broker := newOverlapWriter(2*time.Millisecond, 0)
			r := newOutputRig(t, outputRigSpec{fx: oracleFixture, broker: broker, variant: variantPipelined(32),
				checkpointEvents: oracleCheckpoint, stats: newSqliteRuleStatStore(t, nil),
				output: runtime.OutputConfig{MaxInFlight: 32, MaxBufferedBytes: tc.budget}})
			r.run(t, oracleFixture.messages(t, oracleInputs, r.log))
			v := judgeOutput(oracleFixture, oracleInputs, r.log.snapshot())
			require.True(t, v.clean(), "%s", v)
			maxInFlight, _, _ := broker.seen()
			require.LessOrEqual(t, maxInFlight, tc.maxSeen)
			require.Equal(t, tc.maxSeen, maxInFlight, "the budget allowed less than it should")
		})
	}
}

// An outcome that does not belong to an outstanding publish of the current pass is refused and
// counted, never applied.
func TestAStaleOutcomeIsRefused(t *testing.T) {
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.pendingDets = pending([]detectcore.Detection{{RuleID: "acme/p@1/r", Series: "d"}})
	p := &publishPass{rp: rp, pub: rp.publisher, id: 7, status: []itemStatus{itemInFlight}, next: []int32{-1}, inFlight: 1}
	for _, o := range []publishOutcome{{pass: 6, ord: 0}, {pass: 7, ord: 1}, {pass: 7, ord: -1}} {
		p.apply(o)
	}
	require.Equal(t, 3, p.stale)
	require.Equal(t, itemInFlight, p.status[0])
	require.Equal(t, 1, p.inFlight)
	p.status[0] = itemAcked
	p.apply(publishOutcome{pass: 7, ord: 0}) // a duplicate of a settled outcome
	require.Equal(t, 4, p.stale)
}

// The supersession gate never drops a detection that was already acknowledged: a known positive
// is not converted into a drop afterwards, and its latch is not cleared.
func TestTheSupersessionGateKeepsAKnownPositive(t *testing.T) {
	id := runtime.ComposeRuleID("acme", "p1@v1/r1")
	rp, _ := armedProcessor(t, id)
	rp.armer.LoadMembership(nil, []runtime.ActiveEntry{{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: testBase}})
	rp.pendingDets = pending([]detectcore.Detection{
		{RuleID: id, Series: "gone", Kind: detectcore.Absence, Edge: detectcore.EdgeRaised},
		{RuleID: id, Series: "gone2", Kind: detectcore.Absence, Edge: detectcore.EdgeRaised},
	})
	rp.pendingDets[0].state = outputAcked
	rp.dropSupersededDetections()
	require.Len(t, rp.pendingDets, 1, "the gate dropped a known positive, or kept a stale raise")
	require.Equal(t, "gone", rp.pendingDets[0].Series)
}

// The window's bound is the messaging layer's.
func TestOutputConfigBounds(t *testing.T) {
	require.NoError(t, runtime.DefaultOutputConfig().Validate())
	require.Equal(t, 32, runtime.DefaultOutputConfig().MaxInFlight, "the default window moved; the documentation states 32")
	for _, c := range []runtime.OutputConfig{
		{MaxInFlight: 0, MaxBufferedBytes: runtime.DefaultMaxBufferedBytes},
		{MaxInFlight: 1025, MaxBufferedBytes: runtime.DefaultMaxBufferedBytes},
		{MaxInFlight: 1, MaxBufferedBytes: runtime.MinMaxBufferedBytes - 1},
		{MaxInFlight: 1, MaxBufferedBytes: runtime.MaxMaxBufferedBytes + 1},
	} {
		require.Error(t, c.Validate(), "%+v", c)
	}
}

// The lowest buffer position goes first: the window's first sends are the lanes' first
// detections in buffer order, so the retirement cursor advances as early as it can and a
// window narrower than the lanes favours the oldest work. The writer holds every send until
// the window is full, so the first sends are exactly the ones the first admission chose.
func TestTheLowestBufferPositionGoesFirst(t *testing.T) {
	const window = 4
	var fw *faultWriter
	var arrived atomic.Int32
	full := make(chan struct{})
	r, _ := pipelineRig(t, window, &fw, func(attempt, int, int) (bool, error) {
		if arrived.Add(1) == window {
			close(full)
		}
		select {
		case <-full:
		case <-time.After(5 * time.Second):
		}
		return true, nil
	})
	seen := map[string]bool{}
	var heads []string
	for _, d := range r.rp.pendingDets {
		lane := mustTenant(t, d.RuleID) + "|" + d.Series
		if !seen[lane] {
			seen[lane] = true
			heads = append(heads, fmt.Sprintf("%s %s %d", d.RuleID, lane, d.At.UnixNano()))
		}
	}
	require.Greater(t, len(heads), window, "fewer lanes than the window; the test proves nothing about priority")
	require.True(t, r.rp.checkpoint(context.Background()))
	var first []string
	for _, a := range fw.snapshot()[:window] {
		first = append(first, fmt.Sprintf("%s %s %d", a.rule, a.lane, a.at.UnixNano()))
	}
	require.ElementsMatch(t, heads[:window], first, "the first sends were not the lanes' first detections in buffer order")
}

// The first failed outcome stops new sends: what was already in flight settles, nothing more
// is sent in that pass, and the next pass carries on. The failed send is answered at once and
// every other one takes a few milliseconds, so it is settled long before the window could turn
// over even once.
func TestTheFirstFailureStopsNewSends(t *testing.T) {
	const window, victim = 8, 5
	var fw *faultWriter
	r, _ := pipelineRig(t, window, &fw, func(_ attempt, index, earlier int) (bool, error) {
		if index == victim && earlier == 0 {
			return false, errors.New("refused")
		}
		time.Sleep(2 * time.Millisecond)
		return true, nil
	})
	total := len(r.rp.pendingDets)
	require.False(t, r.rp.checkpoint(context.Background()))
	sent := len(fw.snapshot())
	require.Less(t, sent, victim+3*window, "sends went on after a failed outcome: %d of %d", sent, total)
	require.True(t, r.rp.checkpoint(context.Background()))
}

// A retired detection does not keep its payload reachable: the buffer's backing array outlives the
// checkpoint, so slicing it off is not enough. The entries past the new length are zeroed.
func TestARetiredDetectionsPayloadIsNotRetained(t *testing.T) {
	for _, window := range []int{1, 8} {
		t.Run(fmt.Sprintf("window=%d", window), func(t *testing.T) {
			var fw *faultWriter
			// The victim is chosen by its place in the buffer, not by the order attempts reach the
			// writer: at a window above 1 that order is the scheduler's, and a victim that happened
			// to be the buffer's first detection would leave nothing to retire.
			const victim = 5
			var victimRule, victimLane atomic.Value
			var victimAt atomic.Int64
			r, _ := pipelineRig(t, window, &fw, func(a attempt, _, earlier int) (bool, error) {
				rule, _ := victimRule.Load().(string)
				lane, _ := victimLane.Load().(string)
				if earlier == 0 && a.rule == rule && a.lane == lane && a.at.UnixNano() == victimAt.Load() {
					return false, errors.New("refused")
				}
				return true, nil
			})
			ctx := context.Background()
			before := r.rp.pendingDets
			total := len(before)
			require.Greater(t, total, victim+1)
			v := before[victim]
			victimRule.Store(v.RuleID)
			victimLane.Store(mustTenant(t, v.RuleID) + "|" + v.Series)
			victimAt.Store(v.At.UnixNano())

			// A failed pass compacts: the retired prefix and the dropped entries leave the buffer.
			require.False(t, r.rp.checkpoint(ctx))
			kept := r.rp.pendingDets
			require.Less(t, len(kept), total, "nothing was retired; the test proves nothing")
			for i, d := range before[len(kept):total] {
				require.True(t, reflect.ValueOf(d).IsZero(), "slot %d past the buffer's length still holds a retired detection", len(kept)+i)
			}
			for i, d := range kept {
				if d.state == outputAcked {
					require.Nil(t, d.out.Payload, "an acknowledged detection %d still holds its payload", i)
				}
			}

			// A fully retired buffer is zeroed as well.
			before = r.rp.pendingDets
			require.True(t, r.rp.checkpoint(ctx))
			require.Empty(t, r.rp.pendingDets)
			for i, d := range before[:len(before)] {
				require.True(t, reflect.ValueOf(d).IsZero(), "slot %d still holds a retired detection", i)
			}
		})
	}
}

// A publish whose outcome may have been stored is labelled unknown; one the broker refused is
// failed. The retry treats both alike, so this is the operator's signal only.
func TestPublishFailureOutcomeClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"deadline", context.DeadlineExceeded, publishOutcomeUnknown},
		{"wrapped deadline", fmt.Errorf("publish: %w", context.DeadlineExceeded), publishOutcomeUnknown},
		{"canceled", context.Canceled, publishOutcomeUnknown},
		{"nats timeout", nats.ErrTimeout, publishOutcomeUnknown},
		{"connection closed", fmt.Errorf("x: %w", nats.ErrConnectionClosed), publishOutcomeUnknown},
		{"disconnected", nats.ErrDisconnected, publishOutcomeUnknown},
		{"draining", nats.ErrConnectionDraining, publishOutcomeUnknown},
		{"duplicate in process", fmt.Errorf("x: %w", &nats.APIError{Code: 409, ErrorCode: 10158, Description: "duplicate message id is in process"}), publishOutcomeUnknown},
		{"other api error", &nats.APIError{Code: 400, ErrorCode: 10077, Description: "maximum messages exceeded"}, publishOutcomeFailed},
		{"no responders", nats.ErrNoResponders, publishOutcomeFailed},
		{"plain", errors.New("refused"), publishOutcomeFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, publishFailureOutcome(tc.err))
		})
	}
}

// The byte budget counts every prepared, unacknowledged payload, not only the ones in flight:
// payloads kept from an earlier failed pass count against a new pass before any is sent.
func TestTheByteBudgetCountsRetainedPreparedPayloads(t *testing.T) {
	var fw *faultWriter
	r, _ := pipelineRig(t, 32, &fw, func(attempt, int, int) (bool, error) { return false, errors.New("refused") })
	require.False(t, r.rp.checkpoint(context.Background()))
	var prepared int
	var size int64
	for _, d := range r.rp.pendingDets {
		if d.state == outputPrepared {
			prepared++
			size = int64(len(d.out.Payload))
		}
	}
	require.GreaterOrEqual(t, prepared, 4, "too few payloads were retained; the test proves nothing")
	sent := func(budget int64) int {
		r.rp.cfg.Output = runtime.OutputConfig{MaxInFlight: 32, MaxBufferedBytes: budget}
		p := r.rp.newPublishPass(context.Background(), len(r.rp.pendingDets))
		p.jobs = make(chan publishJob, 32)
		p.admit()
		return len(p.jobs)
	}
	require.Greater(t, sent(int64(prepared)*size*2), 1, "control: a roomy budget should send several at once")
	// Room for two payloads, but `prepared` of them are already held: only the first goes.
	require.Equal(t, 1, sent(2*size+size/2), "the retained payloads were not counted against the budget")
}

// drivePass runs publishPending's loop by hand, calling check after every admit and every
// applied outcome, so a test can hold the pass to an invariant at each step.
func drivePass(t *testing.T, rp *ResolvedEventsProcessor, check func(*publishPass)) bool {
	t.Helper()
	rp.dropSupersededDetections()
	n := len(rp.pendingDets)
	if n == 0 {
		return true
	}
	rp.publishPasses++
	p := rp.newPublishPass(context.Background(), n)
	check(p)
	if p.cfg.MaxInFlight > 1 {
		workers := min(p.cfg.MaxInFlight, n)
		p.jobs = make(chan publishJob, p.cfg.MaxInFlight)
		p.results = make(chan publishOutcome, p.cfg.MaxInFlight)
		p.workers.Add(workers)
		for range workers {
			go p.work()
		}
	}
	for {
		p.admit()
		check(p)
		if p.inFlight == 0 {
			break
		}
		p.await()
		check(p)
	}
	if p.jobs != nil {
		close(p.jobs)
		p.workers.Wait()
	}
	return p.finish()
}

// heldNow is what the byte budget should count at this moment: every payload prepared, not
// positively acknowledged and not dropped.
func heldNow(p *publishPass) int64 {
	var sum int64
	for i := range p.status {
		d := &p.rp.pendingDets[i]
		if d.state == outputPrepared && p.status[i] != itemDropped {
			sum += int64(len(d.out.Payload))
		}
	}
	return sum
}

// The budget's count is exact at every step of every pass: a first pass that fails part-way, a
// retry pass that starts with retained payloads and drops some on recheck (their rules are gone),
// and a pass that settles everything, where it ends at zero.
func TestTheByteBudgetCountIsExactAtEveryStep(t *testing.T) {
	for _, window := range []int{1, 8, 32} {
		t.Run(fmt.Sprintf("window %d", window), func(t *testing.T) {
			var fw *faultWriter
			r, _ := pipelineRig(t, window, &fw, func(_ attempt, index, earlier int) (bool, error) {
				if earlier == 0 && index%3 == 0 && index <= 90 {
					return index%2 == 0, errors.New("refused")
				}
				return true, nil
			})
			check := func(p *publishPass) {
				require.Equal(t, heldNow(p), p.held, "the budget's count drifted from what is held")
			}
			r.rp.cfg.Output = runtime.OutputConfig{MaxInFlight: window, MaxBufferedBytes: 1 << 20}
			require.False(t, drivePass(t, r.rp, check), "the first pass should fail part-way")
			var retained int
			for _, d := range r.rp.pendingDets {
				if d.state == outputPrepared {
					retained++
				}
			}
			require.Positive(t, retained, "nothing was retained; the retry pass proves nothing")
			ids := r.rp.registry.IDs()
			for _, id := range ids[:len(ids)/2] {
				r.rp.registry.Remove(id)
			}
			for range 1000 {
				if drivePass(t, r.rp, check) {
					break
				}
			}
			require.Empty(t, r.rp.pendingDets, "the passes never settled the buffer")
		})
	}
}

// The budget is inclusive: an item that brings what is held to exactly the budget goes beside
// what is in flight; one byte less and it waits.
func TestTheByteBudgetAdmitsAnItemThatFillsItExactly(t *testing.T) {
	var fw *faultWriter
	r, _ := pipelineRig(t, 32, &fw, nil)
	admitted := func(budget int64) []publishJob {
		for i := range r.rp.pendingDets {
			if d := &r.rp.pendingDets[i]; d.state == outputPrepared {
				d.state, d.out = outputNew, runtime.PreparedOutput{}
			}
		}
		r.rp.cfg.Output = runtime.OutputConfig{MaxInFlight: 32, MaxBufferedBytes: budget}
		p := r.rp.newPublishPass(context.Background(), len(r.rp.pendingDets))
		require.Zero(t, p.held)
		p.jobs = make(chan publishJob, 32)
		p.admit()
		close(p.jobs)
		var jobs []publishJob
		for j := range p.jobs {
			jobs = append(jobs, j)
		}
		return jobs
	}
	all := admitted(1 << 30)
	require.Greater(t, len(all), 2, "control: a roomy budget should send several at once")
	exact := int64(len(all[0].out.Payload) + len(all[1].out.Payload))
	require.Len(t, admitted(exact), 2, "an item that fills the budget exactly was held back")
	require.Len(t, admitted(exact-1), 1, "an item that overfills the budget by a byte was sent")
}

// The bound is the budget plus the largest single payload, however the sizes mix: once what is
// held already exceeds the budget, nothing more is prepared until something settles. (Preparing
// the top item anyway let a lower-numbered item from another lane, promoted by an acknowledgement,
// be prepared as well, so with mixed sizes the count crept past one payload's worth.)
func TestTheByteBudgetBoundHoldsWithMixedPayloadSizes(t *testing.T) {
	for _, window := range []int{8, 32} {
		t.Run(fmt.Sprintf("window %d", window), func(t *testing.T) {
			var fw *faultWriter
			r, _ := pipelineRig(t, window, &fw, nil)
			// Pad every series by an amount fixed by its name, so a lane keeps its identity while
			// payload sizes differ widely between lanes.
			for i := range r.rp.pendingDets {
				d := &r.rp.pendingDets[i]
				var sum int
				for _, c := range d.Detection.Series {
					sum += int(c)
				}
				d.Detection.Series += strings.Repeat("x", 1+(sum%7)*300)
			}
			budget := int64(1200)
			r.rp.cfg.Output = runtime.OutputConfig{MaxInFlight: window, MaxBufferedBytes: budget}
			var largest int64
			peak := int64(0)
			check := func(p *publishPass) {
				for i := range p.status {
					d := &r.rp.pendingDets[i]
					if d.state == outputPrepared {
						largest = max(largest, int64(len(d.out.Payload)))
					}
				}
				peak = max(peak, p.held)
				require.LessOrEqual(t, p.held, budget+largest, "held exceeded the budget plus the largest payload")
			}
			for range 1000 {
				if drivePass(t, r.rp, check) {
					break
				}
			}
			require.Empty(t, r.rp.pendingDets, "the passes never settled the buffer")
			require.Greater(t, peak, budget, "control: the budget was never reached, so the bound was not tested")
		})
	}
}
