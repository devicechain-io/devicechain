// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	dccore "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

// The oracle tests. Each one is two-sided: the path as it stands passes the oracle, and each of
// the two defects a faster output path is most likely to introduce is planted into it and must
// FAIL that oracle, and only that one. A check is worth nothing until it has been shown to fail.

// oracleFixture: two tenants, four devices each, two rules per tenant (so a lane spans rules),
// an edge on every sample, and enough inputs for three batch-count checkpoints.
var oracleFixture = outputFixture{tenants: []string{"acme", "beta"}, devices: 4, rulesPerTenant: 2, firePeriod: 1}

const (
	oracleInputs     = 1200
	oracleCheckpoint = 500
)

// reorderVariant plants an ORDERING defect: before each checkpoint, in every lane, the first
// detection and the first one from a LATER input swap places. It is what an overlapping publisher
// that let a lane's successor go ahead of its predecessor would put on the broker. Timing is
// untouched: every publish still completes before the checkpoint saves.
var reorderVariant = outputVariant{name: "planted-reorder", checkpoint: func(ctx context.Context, rp *ResolvedEventsProcessor) bool {
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
	return baselineCheckpoint(ctx, rp)
}}

// heldWriter takes a write as done the moment it is SUBMITTED and sends it to the broker only when
// released: submission mistaken for a positive outcome.
type heldWriter struct {
	inner messaging.MessageWriter
	mu    sync.Mutex // the pipelined publisher writes from several goroutines
	held  []heldWrite
}

type heldWrite struct {
	tenant string
	msgs   []messaging.Message
}

func (w *heldWriter) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	tenant, _ := dccore.TenantFromContext(ctx)
	w.mu.Lock()
	w.held = append(w.held, heldWrite{tenant: tenant, msgs: append([]messaging.Message(nil), msgs...)})
	w.mu.Unlock()
	return nil
}

func (w *heldWriter) WriteToDevice(ctx context.Context, _ string, msgs ...messaging.Message) error {
	return w.WriteMessages(ctx, msgs...)
}

func (w *heldWriter) HandleResponse(error) {}

// release sends everything held, in submission order: nothing is lost or reordered, only late.
func (w *heldWriter) release() error {
	for _, h := range w.held {
		if err := w.inner.WriteMessages(dccore.WithTenant(context.Background(), h.tenant), h.msgs...); err != nil {
			return err
		}
	}
	w.held = w.held[:0]
	return nil
}

// earlyAckVariant plants an EARLY-ACK defect: each checkpoint saves its snapshot and acknowledges
// its inputs while the derived events it covers have only been submitted, and the broker
// acknowledges them afterwards. Order and content are untouched.
func earlyAckVariant(tb testing.TB) outputVariant {
	var w *heldWriter
	return outputVariant{
		name: "planted-early-ack",
		wrapWriter: func(_ testing.TB, inner messaging.MessageWriter) messaging.MessageWriter {
			w = &heldWriter{inner: inner}
			return w
		},
		checkpoint: func(ctx context.Context, rp *ResolvedEventsProcessor) bool {
			ok := baselineCheckpoint(ctx, rp)
			if err := w.release(); err != nil {
				tb.Errorf("release held writes: %v", err)
			}
			return ok
		},
	}
}

func runOracleFixture(t *testing.T, variant outputVariant, broker messaging.MessageWriter) (*outputRig, outputVerdict) {
	t.Helper()
	r := newOutputRig(t, outputRigSpec{fx: oracleFixture, broker: broker, variant: variant, checkpointEvents: oracleCheckpoint,
		stats: newSqliteRuleStatStore(t, nil)})
	r.run(t, oracleFixture.messages(t, oracleInputs, r.log))
	return r, judgeOutput(oracleFixture, oracleInputs, r.log.snapshot())
}

// The positive control. The baseline passes the oracle over a source whose detections are
// counted from its readings, and the counts are not vacuous: thousands of detections, three
// checkpoints, the last input acknowledged.
func TestTheOutputOracleAcceptsTheBaseline(t *testing.T) {
	r, v := runOracleFixture(t, variantA, nil)
	want := len(oracleFixture.expectedDetections(oracleInputs))
	// An edge on every sample but each device's first (it starts low, as the engine's state does):
	// one detection per input per rule, less the first input of every lane.
	lanes := len(oracleFixture.tenants) * oracleFixture.devices
	require.Equal(t, (oracleInputs-lanes)*oracleFixture.rulesPerTenant, want, "the fixture's own count")
	require.True(t, v.clean(), "the baseline failed the oracle: %s", v)
	require.Equal(t, want, v.published, "the broker acknowledged a different number of detections than the source produces")
	require.Zero(t, v.duplicates)
	require.Equal(t, want, v.stats, "one rule-health fire per acknowledged detection")
	require.Equal(t, 3, r.checkpoints)
	t.Logf("verdict: %s", v)
	acked := uint64(0)
	for _, e := range r.log.snapshot() {
		if e.kind == evInputAck && e.seq > acked {
			acked = e.seq
		}
	}
	require.Equal(t, uint64(oracleInputs), acked, "the final checkpoint did not acknowledge the last input")
}

// The rule-health projection the baseline writes agrees with the source: one fire per detection,
// and each rule's last edge is its last detection's.
func TestTheBaselineRuleStatsMatchTheSource(t *testing.T) {
	stats := newSqliteRuleStatStore(t, nil)
	r := newOutputRig(t, outputRigSpec{fx: oracleFixture, variant: variantA, checkpointEvents: oracleCheckpoint, stats: stats})
	r.run(t, oracleFixture.messages(t, oracleInputs, r.log))

	fires := map[string]int64{}
	last := map[string]detKey{}
	for _, k := range oracleFixture.expectedDetections(oracleInputs) {
		fires[k.rule]++
		if k.at >= last[k.rule].at {
			last[k.rule] = k
		}
	}
	for _, tenant := range oracleFixture.tenants {
		var ids []string
		for k := 0; k < oracleFixture.rulesPerTenant; k++ {
			ids = append(ids, oracleFixture.ruleID(tenant, k))
		}
		got, err := stats.LoadByIDs(context.Background(), tenant, ids)
		require.NoError(t, err)
		for _, id := range ids {
			require.Equal(t, fires[id], got[id].FireCount, "fire count of %s", id)
			require.Equal(t, last[id].edge, got[id].LastEdge, "last edge of %s", id)
		}
	}
}

// 🔴 NEGATIVE CONTROL 1. A lane reordered at the broker fails the ORDERING oracle — and only it:
// the defect leaves timing alone, so the durability oracle must stay clean, which is what shows
// the two are measuring different things.
func TestTheOutputOracleCatchesAPlantedReorder(t *testing.T) {
	_, v := runOracleFixture(t, reorderVariant, nil)
	t.Logf("verdict: %s", v)
	require.NotEmpty(t, v.ordering, "a lane published out of order passed the ordering oracle: %s", v)
	require.Empty(t, v.earlyDurable, "a pure reorder tripped the durability oracle: %s", v)
	require.Zero(t, v.missing)
}

// 🔴 NEGATIVE CONTROL 2. Snapshots saved and inputs acknowledged while their derived events were
// only submitted fail the DURABILITY oracle, and the rule-health fires recorded on those
// submissions fail the fire oracle — but not the ordering one: the same events reach the broker,
// in the same order, just after the checkpoint instead of before it.
func TestTheOutputOracleCatchesAPlantedEarlyAck(t *testing.T) {
	_, v := runOracleFixture(t, earlyAckVariant(t), nil)
	t.Logf("verdict: %s", v)
	require.NotEmpty(t, v.earlyDurable, "a checkpoint ahead of its broker outcomes passed the durability oracle: %s", v)
	require.NotEmpty(t, v.statAhead, "fires recorded on mere submissions passed the fire oracle: %s", v)
	require.Empty(t, v.ordering, "a pure timing defect tripped the ordering oracle: %s", v)
	require.Zero(t, v.missing, "the held writes were released; nothing should be missing")
}

// The durability oracle's second half, an input acknowledged ahead of the snapshot that covers it,
// is not reached by the planted early-ack (which acks after its save, as the loop does), so it is
// shown to fire here on a log built by hand, beside the same log in the legal order.
func TestTheDurabilityOracleRefusesAnAckAheadOfItsSnapshot(t *testing.T) {
	fx := outputFixture{tenants: []string{"acme"}, devices: 1, rulesPerTenant: 1, firePeriod: 0}
	legal := []outputEvent{{kind: evSaved, seq: 4}, {kind: evInputAck, seq: 4}}
	early := []outputEvent{{kind: evInputAck, seq: 4}, {kind: evSaved, seq: 4}}
	require.True(t, judgeOutput(fx, 4, legal).clean(), "%s", judgeOutput(fx, 4, legal))
	v := judgeOutput(fx, 4, early)
	require.NotEmpty(t, v.earlyDurable, "an ack ahead of its snapshot passed: %s", v)
}

// The benchmark's baseline adapter carries its own copy of the publish loop so that "A" survives
// later changes to production. For as long as production is meant to BE the baseline, the two
// produce the identical log: the same derived events in the same order, the same snapshots and
// the same input acknowledgements, interleaved the same way.
//
// Production publishes through the pipelined publisher, and at window 1 that is the serial path
// exactly, which is what this test holds it to (C against A; see
// TestWindowOneIsTheSerialPathWithBatchedStats for C against B). The default window is wider and
// interleaves differently on purpose, so this runs production at window 1 explicitly; wider
// windows are judged by the oracle instead.
func TestTheBaselineAdapterIsTheProductionPath(t *testing.T) {
	a, va := runOracleFixture(t, variantA, nil)
	p, vp := runOracleFixture(t, variantProduction, nil)
	require.True(t, va.clean(), "%s", va)
	require.True(t, vp.clean(), "%s", vp)
	require.Equal(t, a.log.snapshot(), p.log.snapshot(), "the baseline adapter and production wrote different logs")
}

// The timing splits the benchmark reads come from the production metrics, and they account for
// what happened: one acknowledgement timing per published event, one rule-health timing per
// fire, and one serialize and one save timing per committed snapshot.
func TestTheOutputTimingSplitsAccountForTheRun(t *testing.T) {
	r, v := runOracleFixture(t, variantA, latencyWriter{})
	require.True(t, v.clean(), "%s", v)
	acks, _ := histogramStats(t, r.metrics.derivedPublishAck)
	writes, _ := histogramStats(t, r.metrics.ruleStatWrite)
	serial, serialSum := histogramStats(t, r.metrics.checkpointSerialize)
	saves, saveSum := histogramStats(t, r.metrics.checkpointSave)
	ckpts, ckptSum := histogramStats(t, r.metrics.checkpointDuration)
	require.Equal(t, uint64(v.published), acks, "acknowledgement timings")
	require.Equal(t, uint64(v.published), writes, "rule-health write timings")
	require.Equal(t, uint64(r.checkpoints), ckpts, "committed checkpoints")
	require.Equal(t, ckpts, serial, "serialize timings")
	require.Equal(t, ckpts, saves, "save timings")
	require.LessOrEqual(t, serialSum+saveSum, ckptSum*1.01+1e-6, "the two parts outweigh the whole")
}

// The same three verdicts on a real JetStream server. The broker's acknowledgement is now a real
// PubAck, and the stream's STORED order is judged as well as the order the acknowledgements came
// back in: they agree for a serial writer, and a faster one will have to keep them agreeing.
func TestTheOutputOracleOnARealBroker(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                   string
		variant                func(testing.TB) outputVariant
		wantOrdering, wantEarl bool
	}{
		{"baseline", func(testing.TB) outputVariant { return variantA }, false, false},
		{"pipelined-window-1", func(testing.TB) outputVariant { return variantPipelined(1) }, false, false},
		{"pipelined-window-32", func(testing.TB) outputVariant { return variantPipelined(32) }, false, false},
		{"planted-reorder", func(testing.TB) outputVariant { return reorderVariant }, true, false},
		{"planted-early-ack", earlyAckVariant, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := startDetectBroker(t)
			nmgr, _, _ := b.brokerManager(t, func(*messaging.NatsManager) error { return nil })
			w, err := nmgr.NewWriter(streams.DerivedEvents)
			require.NoError(t, err)

			_, v := runOracleFixture(t, tc.variant(t), w)
			require.Equal(t, tc.wantOrdering, len(v.ordering) > 0, "ordering verdict: %s", v)
			require.Equal(t, tc.wantEarl, len(v.earlyDurable) > 0, "durability verdict: %s", v)
			require.Zero(t, v.missing, "%s", v)

			stored := storedDerived(t, b)
			sv := judgeOutput(oracleFixture, oracleInputs, stored)
			require.Equal(t, v.published, sv.published, "the stream holds a different count than the broker acknowledged")
			require.Equal(t, tc.wantOrdering, len(sv.ordering) > 0, "stored-order verdict: %s", sv)
			require.Zero(t, sv.missing+sv.unexpected+sv.duplicates, "%s", sv)
		})
	}
}

// storedDerived reads the instance's derived-event stream back in stored order, as the oracle's
// published events.
func storedDerived(t *testing.T, b *detectBroker) []outputEvent {
	t.Helper()
	js, err := b.nc.JetStream()
	require.NoError(t, err)
	stream := messaging.StreamName(b.instance, streams.DerivedEvents)
	info, err := js.StreamInfo(stream)
	require.NoError(t, err)
	out := make([]outputEvent, 0, info.State.Msgs)
	for seq := info.State.FirstSeq; seq <= info.State.LastSeq && info.State.Msgs > 0; seq++ {
		m, err := js.GetMsg(stream, seq)
		require.NoError(t, err, fmt.Sprintf("stream %s seq %d", stream, seq))
		var de runtime.DerivedEvent
		require.NoError(t, json.Unmarshal(m.Data, &de))
		out = append(out, outputEvent{kind: evPublished, det: keyOf(de)})
	}
	return out
}
