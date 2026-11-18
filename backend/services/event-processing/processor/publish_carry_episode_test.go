// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"testing"
	"time"

	dc "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/stretchr/testify/require"
)

func TestPublishBatchedRecoveryPreservesFreshPredecessorEpisode(t *testing.T) {
	for _, k := range []int{1, 4} {
		for _, queued := range []bool{false, true} {
			t.Run(fmt.Sprintf("shards=%d/queued=%v", k, queued), func(t *testing.T) {
				ctx := context.Background()
				rp, w := handoffProcessor(t, k, handoffRule(t, "v1", rules.TypeThreshold, true))
				handoffApply(t, rp, handoffMessage(t, 1, "v1", "dev1", "90", ""))
				require.True(t, rp.checkpoint(ctx))
				handoffActivate(t, rp, handoffRule(t, "v2", rules.TypeThreshold, true))
				// No publication between the recovery and a fresh historical raise.
				for _, m := range []struct {
					seq            uint64
					version, value string
				}{{2, "v2", "20"}, {3, "v1", "90"}} {
					msg := handoffMessage(t, m.seq, m.version, "dev1", m.value, "")
					if queued {
						// Hold detections until both engine operations have run.
						// Absorption must preserve the newer latch already present.
						rp.engine.ProcessEvent(dc.Event{Seq: m.seq, Key: dc.SeriesKey{Rule: "acme/p1@" + m.version + "/r1", Series: "dev1"}, Time: testBase.Add(time.Duration(m.seq) * time.Second), Match: m.value == "90"})
					} else {
						handoffApply(t, rp, msg)
					}
				}
				rp.drainDetections(testBase.Add(3*time.Second), true)
				require.True(t, rp.checkpoint(ctx))
				require.Len(t, w.events, 3)
				require.Equal(t, runtime.EdgeResolved, w.events[1].Edge)
				require.Equal(t, runtime.EdgeRaised, w.events[2].Edge)
				old := dc.SeriesKey{Rule: "acme/p1@v1/r1", Series: "dev1"}
				next := dc.SeriesKey{Rule: "acme/p1@v2/r1", Series: "dev1"}
				require.True(t, rp.engine.IsRaised(old) || rp.engine.IsRaised(next), "fresh published episode must retain a latch")
				handoffApply(t, rp, handoffMessage(t, 4, "v1", "dev1", "20", ""))
				handoffApply(t, rp, handoffMessage(t, 5, "v2", "dev1", "20", ""))
				require.True(t, rp.checkpoint(ctx))
				require.Len(t, w.events, 4, "healthy readings must settle the fresh raise")
				require.Equal(t, runtime.EdgeResolved, w.events[3].Edge)
				require.False(t, rp.engine.IsRaised(old))
				require.False(t, rp.engine.IsRaised(next))
			})
		}
	}
}

func TestPublishRollbackBeforeDeferredSettlementKeepsActiveLatch(t *testing.T) {
	for _, k := range []int{1, 4} {
		t.Run(fmt.Sprint(k), func(t *testing.T) {
			ctx := context.Background()
			v1 := handoffRule(t, "v1", rules.TypeThreshold, true)
			rp, w := handoffProcessor(t, k, v1)
			handoffApply(t, rp, handoffMessage(t, 1, "v1", "dev1", "90", ""))
			require.Len(t, rp.pendingDets, 1)
			handoffActivate(t, rp, handoffRule(t, "v2", rules.TypeThreshold, true))
			handoffActivate(t, rp, v1)
			handoffApply(t, rp, handoffMessage(t, 2, "v2", "dev1", "90", ""))
			old := dc.SeriesKey{Rule: v1.Compiled.ID, Series: "dev1"}
			require.True(t, rp.engine.IsRaised(old), "in-flight v2 output must not clear active v1 after rollback")
			require.True(t, rp.checkpoint(ctx))
			require.Len(t, w.events, 1, "rolled-back v2 must not publish a duplicate unbalanced raise")
			require.Equal(t, runtime.EdgeRaised, w.events[0].Edge)
			handoffApply(t, rp, handoffMessage(t, 3, "v1", "dev1", "20", ""))
			require.True(t, rp.checkpoint(ctx))
			require.Len(t, w.events, 2)
			require.Equal(t, runtime.EdgeResolved, w.events[1].Edge)
			require.False(t, rp.engine.IsRaised(old))
		})
	}
}
