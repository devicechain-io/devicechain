// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The pipelined publisher (variants C and D) on a real PostgreSQL and a real JetStream: one
// embedded server, and an in-process three-node cluster with the derived-event stream replicated
// three ways. Run as detect_output_integration_test.go says:
//
//	go test -tags integration -count=1 ./processor -run 'Pipelined|LostAcknowledgement' -v
//	go test -tags integration ./processor -run '^$' -bench BenchmarkDetectOutputPipelineReal \
//	  -benchtime 2000x -count 7 -benchmem | tee abcd-real.txt
package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// storedDerivedOn reads instance's derived-event stream back from srv in stored order.
func storedDerivedOn(t *testing.T, srv *natsserver.Server, instance string) []outputEvent {
	t.Helper()
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	defer nc.Close()
	js, err := nc.JetStream()
	require.NoError(t, err)
	stream := messaging.StreamName(instance, streams.DerivedEvents)
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

// The pipelined publisher on a replicated broker and PostgreSQL: at window 1 and at window 32 the
// oracle passes on what the broker acknowledged AND on what the stream stored, in stored order;
// nothing is stored twice; and the rule-health table holds exactly what the source implies.
func TestThePipelinedOutputOnAReplicatedBroker(t *testing.T) {
	cluster := dctest.StartJetStreamCluster(t, 3)
	for _, window := range []int{1, 32} {
		t.Run(fmt.Sprintf("window=%d", window), func(t *testing.T) {
			w, instance := detperfWriterNamed(t, cluster[0], 3)
			stats := detperfStats(t, nil)
			r := newOutputRig(t, outputRigSpec{fx: oracleFixture, broker: w, batch: stats, snapshots: detperfPostgres(t),
				variant: variantPipelined(window), checkpointEvents: oracleCheckpoint})
			r.run(t, oracleFixture.messages(t, oracleInputs, r.log))

			v := judgeOutput(oracleFixture, oracleInputs, r.log.snapshot())
			want := len(oracleFixture.expectedDetections(oracleInputs))
			require.True(t, v.clean(), "window %d failed the oracle on R3: %s", window, v)
			require.Equal(t, want, v.published)
			require.Equal(t, want, v.stats)
			sv := judgeOutput(oracleFixture, oracleInputs, storedDerivedOn(t, cluster[0], instance))
			require.Equal(t, want, sv.published, "the stream holds a different count than the source produces")
			require.Empty(t, sv.ordering, "stored order: %s", sv)
			require.Zero(t, sv.missing+sv.unexpected+sv.duplicates, "%s", sv)
			requireStatsEqual(t, expectedRuleStats(oracleFixture, oracleInputs), loadFixtureStats(t, oracleFixture, stats),
				fmt.Sprintf("window %d on PostgreSQL", window))
		})
	}
}

// A publish the replicated broker STORED but whose acknowledgement was lost is an unknown
// outcome: the checkpoint defers, the retry sends the same bytes under the same dedup id, and the
// broker collapses it, so the stream holds every detection exactly once and in lane order.
func TestALostAcknowledgementIsCollapsedByTheReplicatedBroker(t *testing.T) {
	cluster := dctest.StartJetStreamCluster(t, 3)
	w, instance := detperfWriterNamed(t, cluster[0], 3)
	var fw *faultWriter
	var lost atomic.Int32
	v := faultVariant(8, &fw, func(a attempt, index, earlier int) (bool, error) {
		if earlier == 0 && index%50 == 0 && lost.Add(1) <= 3 {
			return true, fmt.Errorf("acknowledgement lost: %w", nats.ErrTimeout)
		}
		return true, nil
	})
	r := newOutputRig(t, outputRigSpec{fx: oracleFixture, broker: w, batch: detperfStats(t, nil), snapshots: detperfPostgres(t),
		variant: v, checkpointEvents: 1 << 30})
	const inputs = 400
	for _, m := range oracleFixture.messages(t, inputs, r.log) {
		r.rp.handle(m)
	}
	ctx := context.Background()
	failed := 0
	for !r.rp.checkpoint(ctx) {
		failed++
		require.Less(t, failed, 20, "the checkpoint never committed")
	}
	require.Equal(t, 3, failed, "each lost acknowledgement deferred one checkpoint")
	r.acc.Stop(context.Background(), true)

	ov := judgeOutput(oracleFixture, inputs, r.log.snapshot())
	require.True(t, ov.clean(), "%s", ov)
	require.Equal(t, 3, ov.duplicates, "each lost acknowledgement's retry was acknowledged again")
	sv := judgeOutput(oracleFixture, inputs, storedDerivedOn(t, cluster[0], instance))
	want := len(oracleFixture.expectedDetections(inputs))
	require.Equal(t, want, sv.published, "the broker stored a retry twice, or lost one")
	require.Zero(t, sv.duplicates+sv.missing+sv.unexpected, "%s", sv)
	require.Empty(t, sv.ordering, "%s", sv)
}

// BenchmarkDetectOutputPipelineReal is the four-way comparison over the real stores: PostgreSQL
// (production callback chain) behind the counters and the checkpoint, a real JetStream behind the
// derived events, R1 or R3. A, B, C and D32 run inside the same parent, so -count interleaves
// them; DETPERF_ORDER=BA reverses the order as the position control. In-process over loopback:
// the acknowledgement is a real PubAck, but the absolute figures are a floor, not capacity.
func BenchmarkDetectOutputPipelineReal(b *testing.B) {
	parent := b
	var single *natsserver.Server
	var cluster []*natsserver.Server
	broker := func(topology string) func(b *testing.B) messaging.MessageWriter {
		return func(b *testing.B) messaging.MessageWriter {
			if topology == "R1" {
				if single == nil {
					single = detperfServer(parent)
				}
				return detperfWriter(b, single, 1)
			}
			if cluster == nil {
				cluster = dctest.StartJetStreamCluster(parent, 3)
			}
			return detperfWriter(b, cluster[0], 3)
		}
	}
	var statsMgr, snapMgr *rdb.RdbManager
	reset := func(b *testing.B) {
		if statsMgr == nil {
			statsMgr, snapMgr = detperfPostgres(parent), detperfPostgres(parent)
		}
		for _, table := range []string{"rule_stats", "detect_snapshots", rdb.FenceTable} {
			if err := snapMgr.Database.Exec(`TRUNCATE TABLE "event-processing".` + table).Error; err != nil {
				b.Fatalf("truncate %s: %v", table, err)
			}
		}
	}
	pg := func(b *testing.B) (fireStatStore, *statementTimer) {
		reset(b)
		return model.NewRuleStatStore(statsMgr), nil
	}
	snapshots := func(b *testing.B) *rdb.RdbManager { reset(b); return snapMgr }
	variants := pipelineBenchVariants()
	// A, B, C and D32: the real broker runs are minutes each, so one wide window stands for D.
	variants = []pipelineBenchVariant{variants[0], variants[1], variants[2], variants[4]}
	if benchVariantOrder()[0] == "B" {
		variants = []pipelineBenchVariant{variants[3], variants[2], variants[1], variants[0]}
	}
	for _, topology := range []string{"R1", "R3"} {
		for _, c := range []outputBenchCase{
			{name: "fire=100%/lanes=32", fx: benchFixture(16, 1)},
			{name: "fire=10%/lanes=32", fx: benchFixture(16, 10)},
		} {
			c.broker = broker(topology)
			c.snapshots = snapshots
			c.stats = pg
			for _, v := range variants {
				c := c
				c.variant, c.batched = v.variant, v.batched
				b.Run("topology="+topology+"/variant="+v.name+"/"+c.name, func(b *testing.B) { benchOutput(b, c) })
			}
		}
	}
}
