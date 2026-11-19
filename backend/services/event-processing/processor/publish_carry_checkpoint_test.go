// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"testing"

	dc "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/require"
)

// Rebuild the processor, including its transient armer, from the persisted row.
// Replay itself checkpoints; another restart proves its cleanup is durable too.
func restartCarryCheckpoint(t *testing.T, previous *ResolvedEventsProcessor, k int, head uint64, msgs ...messaging.Message) (*ResolvedEventsProcessor, *derivedCapture) {
	t.Helper()
	previous.closeDetector()
	rp, w := handoffProcessor(t, k,
		handoffRule(t, "v1", rules.TypeThreshold, true),
		handoffRule(t, "v2", rules.TypeThreshold, true))
	rp.closeDetector()
	rp.Store = previous.Store
	rp.RosterStore = previous.RosterStore
	rp.ProfileActiveStore = previous.ProfileActiveStore
	require.NoError(t, rp.restore(context.Background()))
	require.NoError(t, rp.startDeadmanGate(context.Background()))
	rp.Replay = &fakeReplayOpener{msgs: msgs, head: head}
	require.NoError(t, rp.replayToHead())
	require.NoError(t, rp.reconcileDeadmanArming(context.Background()))
	return rp, w
}

func TestPublishDeferredThresholdCheckpointRestart(t *testing.T) {
	for _, k := range []int{1, 4} {
		for _, replayResolve := range []bool{false, true} {
			t.Run(fmt.Sprintf("shards=%d/replay-resolve=%v", k, replayResolve), func(t *testing.T) {
				ctx := context.Background()
				rp, w := handoffProcessor(t, k, handoffRule(t, "v1", rules.TypeThreshold, true))
				handoffApply(t, rp, handoffMessage(t, 1, "v1", "dev1", "90", ""))
				require.Len(t, rp.pendingDets, 1)
				seq := uint64(2)
				var replay []messaging.Message
				if replayResolve {
					// Keep the last durable checkpoint before the deferred handoff.
					// The old key has fresh pending output at the live handoff, and
					// replay must rebuild its latch without any deferred-pair record.
					require.True(t, rp.checkpoint(ctx))
					replay = []messaging.Message{
						handoffMessage(t, 2, "v1", "dev1", "20", ""),
						handoffMessage(t, 3, "v1", "dev1", "90", ""),
					}
					for _, msg := range replay {
						handoffApply(t, rp, msg)
					}
					seq = 4
				}
				handoffActivate(t, rp, handoffRule(t, "v2", rules.TypeThreshold, true))
				require.True(t, rp.engine.IsRaised(dc.SeriesKey{Rule: "acme/p1@v1/r1", Series: "dev1"}))
				require.False(t, rp.engine.IsRaised(dc.SeriesKey{Rule: "acme/p1@v2/r1", Series: "dev1"}), "pending v1 output defers the carry")
				raise := handoffMessage(t, seq, "v2", "dev1", "90", "")
				handoffApply(t, rp, raise)
				resolve := handoffMessage(t, seq+1, "v2", "dev1", "20", "")
				if !replayResolve {
					handoffApply(t, rp, resolve)
					require.True(t, rp.checkpoint(ctx))
				}
				snap, found, err := rp.Store.Load(ctx, "singleton")
				require.NoError(t, err)
				require.True(t, found, "restart must use a persisted checkpoint")
				if replayResolve {
					require.Equal(t, int64(1), snap.StreamSeq)
					replay = append(replay, raise, resolve)
					rp, w = restartCarryCheckpoint(t, rp, k, seq+1, replay...)
					require.Len(t, w.events, 4)
					require.Equal(t, runtime.EdgeResolved, w.events[3].Edge)
					// Reconcile never carries restored latches. A second restart
					// must also preserve the recovered successor state.
					snap, found, err = rp.Store.Load(ctx, "singleton")
					require.NoError(t, err)
					require.True(t, found)
				} else {
					require.Len(t, w.events, 3)
					require.Equal(t, runtime.EdgeResolved, w.events[2].Edge)
				}
				require.Equal(t, int64(seq+1), snap.StreamSeq)
				rp, w = restartCarryCheckpoint(t, rp, k, seq+1)
				require.Empty(t, w.events, "no messages replayed after the recovery checkpoint")
				handoffApply(t, rp, handoffMessage(t, seq+2, "v2", "dev1", "90", ""))
				require.True(t, rp.checkpoint(ctx))
				require.Len(t, w.events, 1, "a checkpointed predecessor must not relatch the recovered successor")
				require.Equal(t, runtime.EdgeRaised, w.events[0].Edge)
				require.Equal(t, "acme/p1@v2/r1", w.events[0].RuleID)
			})
		}
	}
}

func TestPublishPreHandoffCheckpointAllowsFreshRaise(t *testing.T) {
	for _, k := range []int{1, 4} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			rp, w := handoffProcessor(t, k, handoffRule(t, "v1", rules.TypeThreshold, true))
			handoffApply(t, rp, handoffMessage(t, 1, "v1", "dev1", "90", ""))
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 1)
			handoffActivate(t, rp, handoffRule(t, "v2", rules.TypeThreshold, true))
			// Crash before the handoff is checkpointed, with no successor edge.
			rp, w = restartCarryCheckpoint(t, rp, k, 1)
			handoffApply(t, rp, handoffMessage(t, 2, "v2", "dev1", "90", ""))
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 1, "an uncertain restored handoff must allow another action")
			require.Equal(t, runtime.EdgeRaised, w.events[0].Edge)
			require.Equal(t, "acme/p1@v2/r1", w.events[0].RuleID)
		})
	}
}
