// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"testing"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// liveShardedTerm is outageBroker.liveTerm for a processor with the shardedProcessor rule set, at
// k detection shards: initialized through ExecuteInitialize, the partition acquired, the renewer and
// holder watch running and the supervisor parked in awaitTermEnd, so ExecuteStop and a term-context
// cancel reach endTerm the way they do in production. Its derived events go to w.
func (b *outageBroker) liveShardedTerm(t *testing.T, k int, w *valueCapture) *ResolvedEventsProcessor {
	t.Helper()
	rp := shardedProcessor(t, context.Background(), newTestStore(t), w, k, false)
	ms := &core.Microservice{InstanceId: "aa2", FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	rp.metrics = NewDetectMetrics(ms)
	b.leader = b.manager(t)
	dl, err := b.leader.NewDistributedLease(messaging.DefaultLeaseTTL)
	require.NoError(t, err)
	rp.Lease = dl
	rp.Gate = NewTermGate()
	require.NoError(t, rp.ExecuteInitialize(context.Background()))

	lease, err := dl.Acquire(rp.cfg.PartitionId)
	require.NoError(t, err)
	rp.newTermContext()
	handle := &termHandle{lease: lease, ctx: rp.pctx()}
	holder, err := lease.WatchHolder(handle.ctx)
	require.NoError(t, err)
	handle.holder = holder
	rp.Gate.Enter(holder.Held)
	keepAliveDone := make(chan struct{})
	go func() {
		defer close(keepAliveDone)
		if lease.KeepAlive(handle.ctx, renewInterval) != nil {
			rp.pcancel()
		}
	}()
	handle.keepAliveDone = keepAliveDone
	rp.metrics.setLeader(true)
	rp.metrics.setDetectLive(true)
	rp.supWG.Add(1)
	go func() {
		defer rp.supWG.Done()
		rp.awaitTermEnd(handle)
	}()
	t.Cleanup(func() {
		rp.supCancel()
		rp.supWG.Wait()
	})
	return rp
}

// A full leased term at one shard and at four: the same stream, ended by a graceful stop and by a
// lost term. Whatever the term publishes on its way out is the same bytes in each lane, and no shard
// goroutine survives either ending. This goes through ExecuteInitialize and the real teardown
// (endTerm), where the engine's goroutines are stopped.
func TestAFullTermEndsItsShardsAndPublishesTheSameBytesAtAnyShardCount(t *testing.T) {
	for _, ending := range []string{"graceful stop", "lost term"} {
		ending := ending
		t.Run(ending, func(t *testing.T) {
			run := func(k int) []string {
				b := startOutageBroker(t)
				w := &valueCapture{}
				rp := b.liveShardedTerm(t, k, w)
				if k > 1 {
					require.Equal(t, k, shardGoroutines(), "the term's engine should be running %d shards", k)
				}
				for i := uint64(1); i <= 200; i++ {
					rp.handle(shardedMessage(t, i))
				}
				if ending == "graceful stop" {
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					defer cancel()
					require.NoError(t, rp.ExecuteStop(ctx))
				} else {
					rp.pcancel()
					rp.supWG.Wait()
				}
				b.leaderExits()
				require.Zero(t, shardGoroutines(), "shard goroutines outlived the ended term (k=%d)", k)
				return w.laneOrdered()
			}
			control := run(1)
			require.NotEmpty(t, control, "the single-engine term published nothing on its way out: the comparison means nothing")
			require.Equal(t, control, run(4), "a 4-shard term published different bytes from the single engine's")
		})
	}
}

// Without a lease, ExecuteStop ends the engine's shards too.
func TestAnUnleasedStopEndsTheShards(t *testing.T) {
	rp := shardedProcessor(t, context.Background(), newTestStore(t), &valueCapture{}, 4, false)
	require.NoError(t, rp.ExecuteInitialize(context.Background()))
	for i := uint64(1); i <= 20; i++ {
		rp.handle(shardedMessage(t, i))
	}
	require.Equal(t, 4, shardGoroutines())
	require.NoError(t, rp.ExecuteStop(context.Background()))
	require.Zero(t, shardGoroutines(), "ExecuteStop left shard goroutines running")
}

// Two rules on one device that fire on the same message are published to that device's lane in the
// order the single engine emits them, at any shard count. The rules are spread over the shards, so
// an engine that merged the shards' output by shard would hand them over in another order.
func TestTwoRulesOnOneDeviceArePublishedInTheSingleEnginesOrder(t *testing.T) {
	run := func(k int) []string {
		ctx := context.Background()
		var scoped []runtime.ScopedRule
		thr := 50.0
		for i := 0; i < 16; i++ {
			scoped = append(scoped, runtime.ScopedRule{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: compileShardRule(t, rules0.Rule{
				ID: fmt.Sprintf("acme/m%02d", i), Name: fmt.Sprintf("m%02d", i), Type: rules0.TypeThreshold,
				When: rules0.Condition{Metric: "temperature", Op: rules0.OpGt, Threshold: &thr},
			})})
		}
		reg := runtime.NewRuleRegistry(scoped)
		w := &valueCapture{}
		rp := &ResolvedEventsProcessor{
			Store: newTestStore(t), Replay: &fakeReplayOpener{},
			cfg: Config{PartitionId: "singleton", CheckpointEvents: 1 << 20, CheckpointInterval: time.Hour,
				TickInterval: time.Hour, Clock: detectcore.RealClock{}, Shards: k},
			registry: reg, publisher: runtime.NewPublisher(w, reg, (*DetectMetrics)(nil)),
			clock: detectcore.RealClock{}, procCtx: ctx,
		}
		require.NoError(t, rp.restore(ctx))
		defer rp.closeDetector()
		for i := uint64(1); i <= 3; i++ {
			rp.handle(measuredMsg(t, i, "acme", "dev1", "p@1", "temperature", "99", &fakeAck{}))
			rp.handle(measuredMsg(t, 10+i, "acme", "dev1", "p@1", "temperature", "10", &fakeAck{}))
		}
		rp.checkpoint(ctx)
		return w.laneOrdered()
	}
	control := run(1)
	require.GreaterOrEqual(t, len(control), 16, "the rules did not all fire")
	for _, k := range []int{2, 4, 7} {
		require.Equal(t, control, run(k), "k=%d published the device's detections in another order", k)
	}
}
