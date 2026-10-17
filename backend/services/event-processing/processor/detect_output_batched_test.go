// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

// Variant B of the output-path comparison: the rule-health counters coalesced and written in the
// background (internal/rulehealth) instead of inline. It is judged by the same oracle as A, and
// its final table is held to A's: batching changes when the counts are written, never what they
// end up saying.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/rulehealth"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// expectedRuleStats is what the rule-health table must hold after inputs [0, n): one fire per
// expected detection, and each rule's newest detection's edge (the later one on a tie).
func expectedRuleStats(fx outputFixture, n int) map[string]model.RuleStat {
	out := map[string]model.RuleStat{}
	for _, k := range fx.expectedDetections(n) {
		st := out[k.rule]
		st.FireCount++
		if at := time.Unix(0, k.at); !at.Before(st.LastFiredAt) {
			st.LastFiredAt, st.LastEdge = at, k.edge
		}
		out[k.rule] = st
	}
	return out
}

func loadFixtureStats(t *testing.T, fx outputFixture, store *model.RuleStatStore) map[string]model.RuleStat {
	t.Helper()
	out := map[string]model.RuleStat{}
	for _, tenant := range fx.tenants {
		var ids []string
		for k := 0; k < fx.rulesPerTenant; k++ {
			ids = append(ids, fx.ruleID(tenant, k))
		}
		got, err := store.LoadByIDs(context.Background(), tenant, ids)
		require.NoError(t, err)
		for id, st := range got {
			out[id] = st
		}
	}
	return out
}

func requireStatsEqual(t *testing.T, want, got map[string]model.RuleStat, what string) {
	t.Helper()
	require.Len(t, got, len(want), "%s: rule rows", what)
	for id, w := range want {
		g := got[id]
		require.Equal(t, w.FireCount, g.FireCount, "%s: fire count of %s", what, id)
		require.Equal(t, w.LastEdge, g.LastEdge, "%s: last edge of %s", what, id)
		require.True(t, w.LastFiredAt.Equal(g.LastFiredAt), "%s: last fired of %s: %s, want %s", what, id, g.LastFiredAt, w.LastFiredAt)
	}
}

// The positive control for B: the oracle passes (no fire admitted ahead of its acknowledgement,
// order and durability as A), one admission per acknowledged detection, and after the drain the
// table holds exactly what the source implies — the same table A writes.
func TestTheOutputOracleAcceptsVariantB(t *testing.T) {
	store := newSqliteRuleStatStore(t, nil)
	r := newOutputRig(t, outputRigSpec{fx: oracleFixture, variant: variantB, checkpointEvents: oracleCheckpoint, batch: store})
	r.run(t, oracleFixture.messages(t, oracleInputs, r.log))
	v := judgeOutput(oracleFixture, oracleInputs, r.log.snapshot())
	want := len(oracleFixture.expectedDetections(oracleInputs))
	require.True(t, v.clean(), "variant B failed the oracle: %s", v)
	require.Equal(t, want, v.published)
	require.Equal(t, want, v.stats, "one admission per acknowledged detection")
	require.Zero(t, v.duplicates)
	requireStatsEqual(t, expectedRuleStats(oracleFixture, oracleInputs), loadFixtureStats(t, oracleFixture, store), "variant B")

	aStore := newSqliteRuleStatStore(t, nil)
	a := newOutputRig(t, outputRigSpec{fx: oracleFixture, variant: variantA, checkpointEvents: oracleCheckpoint, stats: aStore})
	a.run(t, oracleFixture.messages(t, oracleInputs, a.log))
	requireStatsEqual(t, loadFixtureStats(t, oracleFixture, aStore), loadFixtureStats(t, oracleFixture, store), "B against A")

	// The derived-event side of the log is identical: B changes nothing a consumer can see.
	var pa, pb []outputEvent
	for _, e := range a.log.snapshot() {
		if e.kind != evStat {
			pa = append(pa, e)
		}
	}
	for _, e := range r.log.snapshot() {
		if e.kind != evStat {
			pb = append(pb, e)
		}
	}
	require.Equal(t, pa, pb, "variant B published, saved or acknowledged differently from A")
}

// 🔴 NEGATIVE CONTROL. The fire oracle sees B's admissions: with submission mistaken for a
// positive outcome (the planted early-ack), B admits fires ahead of their acknowledgements and
// the oracle says so.
func TestTheFireOracleCatchesAnEarlyAdmissionInVariantB(t *testing.T) {
	ea := earlyAckVariant(t)
	r := newOutputRig(t, outputRigSpec{fx: oracleFixture, variant: ea, checkpointEvents: oracleCheckpoint,
		batch: newSqliteRuleStatStore(t, nil)})
	r.run(t, oracleFixture.messages(t, oracleInputs, r.log))
	v := judgeOutput(oracleFixture, oracleInputs, r.log.snapshot())
	require.NotEmpty(t, v.statAhead, "fires admitted on mere submissions passed the fire oracle: %s", v)
	require.NotEmpty(t, v.earlyDurable, "%s", v)
}

// What batching buys, counted on the production callback chain: A pays one fence read and one
// upsert per fire; B pays one of each per TENANT BATCH, whatever the number of fires.
func TestVariantBStatementsPerFire(t *testing.T) {
	timerA, timerB := &statementTimer{}, &statementTimer{}
	aStore, bStore := newSqliteRuleStatStore(t, timerA), newSqliteRuleStatStore(t, timerB)
	for _, tm := range []*statementTimer{timerA, timerB} {
		tm.fenceN.Store(0)
		tm.otherN.Store(0)
	}
	a := newOutputRig(t, outputRigSpec{fx: oracleFixture, variant: variantA, checkpointEvents: oracleCheckpoint, stats: aStore})
	a.run(t, oracleFixture.messages(t, oracleInputs, a.log))
	b := newOutputRig(t, outputRigSpec{fx: oracleFixture, variant: variantB, checkpointEvents: oracleCheckpoint, batch: bStore})
	b.run(t, oracleFixture.messages(t, oracleInputs, b.log))
	fires := int64(len(oracleFixture.expectedDetections(oracleInputs)))
	require.Equal(t, fires, timerA.fenceN.Load(), "A: one fence read per fire")
	require.Equal(t, fires, timerA.otherN.Load(), "A: one upsert per fire")
	// The fixture runs well inside one flush interval, so B's one flush is one batch per tenant.
	tenants := int64(len(oracleFixture.tenants))
	require.Equal(t, tenants, timerB.fenceN.Load(), "B: one fence read per tenant batch")
	require.Equal(t, tenants, timerB.otherN.Load(), "B: one INSERT per tenant batch")
	t.Logf("%d fires: A made %d statements, B made %d", fires, timerA.fenceN.Load()+timerA.otherN.Load(),
		timerB.fenceN.Load()+timerB.otherN.Load())
}

// failingBatchStore fails every batch, or holds every batch until its context ends.
type failingBatchStore struct {
	err  error
	hang bool
}

func (s failingBatchStore) RecordFireBatch(ctx context.Context, _ string, _ []model.RuleFireDelta) (int, error) {
	if s.hang {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	return 0, s.err
}

// FAILURE ISOLATION. A rule-health store that fails every write, or hangs on every write, does
// not touch detection: every detection is published, every checkpoint commits, the oracle is
// clean, and the counters that did not land are counted as discarded, in fires.
func TestARuleHealthStoreOutageLeavesDetectionUntouched(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store failingBatchStore
	}{
		{"failing", failingBatchStore{err: errors.New("database is down")}},
		{"hanging", failingBatchStore{hang: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := rulehealth.DefaultConfig()
			cfg.FlushInterval = rulehealth.MinFlushInterval
			cfg.WriteTimeout = rulehealth.MinWriteTimeout
			r := newOutputRig(t, outputRigSpec{fx: oracleFixture, variant: variantB, checkpointEvents: oracleCheckpoint,
				batch: tc.store, batchCfg: cfg})
			// Not r.run: its drain insists on a healthy store. The same loop, then the stop.
			for _, m := range oracleFixture.messages(t, oracleInputs, r.log) {
				r.rp.handle(m)
				if len(r.rp.pendingAcks) >= r.rp.cfg.CheckpointEvents {
					r.checkpoint(t, context.Background())
				}
			}
			r.checkpoint(t, context.Background())
			r.acc.Stop(context.Background(), true)

			v := judgeOutput(oracleFixture, oracleInputs, r.log.snapshot())
			require.True(t, v.clean(), "a rule-health outage changed detection: %s", v)
			require.Equal(t, len(oracleFixture.expectedDetections(oracleInputs)), v.published)
			admitted := testutil.ToFloat64(r.metrics.ruleStatAdmitted)
			written := testutil.ToFloat64(r.metrics.ruleStatWritten)
			var discarded float64
			for _, reason := range []rulehealth.DropReason{rulehealth.DropWriteFailed, rulehealth.DropShutdown} {
				discarded += testutil.ToFloat64(r.metrics.ruleStatDiscardedBy[reason])
			}
			require.Equal(t, float64(v.published), admitted)
			require.Zero(t, written)
			require.Equal(t, admitted, discarded, "every admitted fire is accounted for as discarded")
			require.Positive(t, testutil.ToFloat64(r.metrics.ruleStatWriteFailures))
		})
	}
}
