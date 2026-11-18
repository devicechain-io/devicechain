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

// Old-version edges deliberately share one publication pass: flushing between
// them would hide a clear whose raise the gate suppressed.
func TestPublishBatchedLateThresholdPairDoesNotClearSuccessor(t *testing.T) {
	for _, k := range []int{1, 4} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			rp, w := handoffProcessor(t, k, handoffRule(t, "v1", rules.TypeThreshold, false))
			handoffApply(t, rp, handoffMessage(t, 1, "v1", "dev1", "90", ""))
			require.True(t, rp.checkpoint(context.Background()))
			handoffActivate(t, rp, handoffRule(t, "v2", rules.TypeThreshold, false))
			handoffApply(t, rp, handoffMessage(t, 2, "v1", "dev1", "90", ""))
			handoffApply(t, rp, handoffMessage(t, 3, "v1", "dev1", "20", ""))
			require.Len(t, rp.pendingDets, 2)
			rp.flushDetections(context.Background())
			require.Len(t, w.events, 1, "the dropped late raise must not publish its paired clear")
			handoffApply(t, rp, handoffMessage(t, 4, "v2", "dev1", "90", ""))
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 1, "alarm must remain active through a v2 breach")
			handoffApply(t, rp, handoffMessage(t, 5, "v2", "dev1", "20", ""))
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 2, "only the v2 recovery may clear the alarm")
			require.Equal(t, runtime.EdgeResolved, w.events[1].Edge)
			require.Equal(t, "acme/p1@v2/r1", w.events[1].RuleID)
		})
	}
}

func TestPublishLateThresholdLatchDoesNotRelatchRecoveredSuccessor(t *testing.T) {
	for _, k := range []int{1, 4} {
		for _, deferred := range []bool{false, true} {
			t.Run(fmt.Sprintf("shards=%d/deferred=%v", k, deferred), func(t *testing.T) {
				rp, w := handoffProcessor(t, k, handoffRule(t, "v1", rules.TypeThreshold, true))
				handoffApply(t, rp, handoffMessage(t, 1, "v1", "dev1", "90", ""))
				if !deferred {
					require.True(t, rp.checkpoint(context.Background()))
				}
				handoffActivate(t, rp, handoffRule(t, "v2", rules.TypeThreshold, true))
				if deferred {
					require.False(t, rp.engine.IsRaised(dc.SeriesKey{Rule: "acme/p1@v2/r1", Series: "dev1"}))
					require.True(t, rp.checkpoint(context.Background()))
				}
				require.True(t, rp.engine.IsRaised(dc.SeriesKey{Rule: "acme/p1@v2/r1", Series: "dev1"}))
				handoffApply(t, rp, handoffMessage(t, 2, "v1", "dev1", "90", ""))
				handoffApply(t, rp, handoffMessage(t, 3, "v2", "dev1", "20", ""))
				require.Len(t, rp.pendingDets, 2)
				require.True(t, rp.checkpoint(context.Background()))
				require.Len(t, w.events, 3)
				require.Equal(t, runtime.EdgeResolved, w.events[2].Edge)
				handoffApply(t, rp, handoffMessage(t, 4, "v2", "dev1", "90", ""))
				require.True(t, rp.checkpoint(context.Background()))
				require.Len(t, w.events, 4, "a stale v1 latch must not suppress the next v2 breach")
				require.Equal(t, runtime.EdgeRaised, w.events[3].Edge)
				require.Equal(t, "acme/p1@v2/r1", w.events[3].RuleID)
			})
		}
	}
}

// The handoff is still deferred when v2 emits its own complete episode. Its
// recovery must cancel the handoff even though neither endpoint is pending later.
func TestPublishDeferredThresholdRecoveryCancelsHandoff(t *testing.T) {
	for _, k := range []int{1, 4} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			rp, w := handoffProcessor(t, k, handoffRule(t, "v1", rules.TypeThreshold, true))
			handoffApply(t, rp, handoffMessage(t, 1, "v1", "dev1", "90", ""))
			handoffActivate(t, rp, handoffRule(t, "v2", rules.TypeThreshold, true))
			handoffApply(t, rp, handoffMessage(t, 2, "v2", "dev1", "90", ""))
			handoffApply(t, rp, handoffMessage(t, 3, "v2", "dev1", "20", ""))
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 3)
			require.Equal(t, runtime.EdgeResolved, w.events[2].Edge)
			handoffApply(t, rp, handoffMessage(t, 4, "v2", "dev1", "90", ""))
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 4, "published successor recovery must cancel the deferred handoff")
			require.Equal(t, runtime.EdgeRaised, w.events[3].Edge)
		})
	}
}

func TestReplayThresholdRecoveryPreventsPostReplayCarry(t *testing.T) {
	for _, k := range []int{1, 4} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			rp, w := handoffProcessor(t, k, handoffRule(t, "v1", rules.TypeThreshold, false))
			handoffApply(t, rp, handoffMessage(t, 1, "v1", "dev1", "90", ""))
			require.True(t, rp.checkpoint(context.Background()))
			handoffActivate(t, rp, handoffRule(t, "v2", rules.TypeThreshold, false))
			require.True(t, rp.checkpoint(context.Background()))
			rp.closeDetector()
			require.NoError(t, rp.restore(context.Background()))
			require.NoError(t, rp.startDeadmanGate(context.Background()))
			rp.Replay = &fakeReplayOpener{msgs: []messaging.Message{
				handoffMessage(t, 2, "v1", "dev1", "90", ""),
				handoffMessage(t, 3, "v2", "dev1", "20", ""),
			}, head: 3}
			require.NoError(t, rp.replayToHead())
			require.Len(t, w.events, 3)
			require.Equal(t, runtime.EdgeResolved, w.events[2].Edge)
			require.NoError(t, rp.reconcileDeadmanArming(context.Background()))
			handoffApply(t, rp, handoffMessage(t, 4, "v2", "dev1", "90", ""))
			require.True(t, rp.checkpoint(context.Background()))
			require.Len(t, w.events, 4, "post-replay carry must not relatch a successor whose recovery was published")
			require.Equal(t, runtime.EdgeRaised, w.events[3].Edge)
		})
	}
}
