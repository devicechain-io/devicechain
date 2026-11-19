// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	dc "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/stretchr/testify/require"
)

func TestEarlyFlushCollectsQueuedSupersededThresholdClear(t *testing.T) {
	ctx := context.Background()
	rp, w := handoffProcessor(t, 4, handoffRule(t, "v1", rules.TypeThreshold, false))
	handoffApply(t, rp, handoffMessage(t, 1, "v1", "dev1", "90", ""))
	require.True(t, rp.checkpoint(ctx))
	handoffActivate(t, rp, handoffRule(t, "v2", rules.TypeThreshold, false))
	old := dc.SeriesKey{Rule: "acme/p1@v1/r1", Series: "dev1"}
	next := dc.SeriesKey{Rule: "acme/p1@v2/r1", Series: "dev1"}
	require.True(t, rp.engine.IsRaised(next), "successor must hold the published alarm's latch")
	require.False(t, rp.engine.IsRaised(old))

	// Reproduce a collection frontier between the two old-version edges:
	// only T20's raise is in pendingDets when the early flush starts.
	handoffApply(t, rp, handoffMessage(t, 20, "v1", "dev1", "90", ""))
	require.Len(t, rp.pendingDets, 1)
	require.Equal(t, dc.EdgeRaised, rp.pendingDets[0].Edge)
	rp.tagNext(testBase.Add(21*time.Second), true)
	rp.engine.ProcessEvent(dc.Event{Seq: 21, Key: old, Time: testBase.Add(21 * time.Second), Match: false})
	// Also queue an unrelated legitimate edge: collecting after selecting the
	// flush buffer must not silently discard newly absorbed output.
	rp.tagNext(testBase.Add(22*time.Second), true)
	rp.engine.ProcessEvent(dc.Event{Seq: 22, Key: dc.SeriesKey{Rule: old.Rule, Series: "dev2"}, Time: testBase.Add(22 * time.Second), Match: true})
	// IsRaised's barrier completes shard work but does not collect its output.
	// This makes the interleaving deterministic without sleeps or shard timing.
	require.True(t, rp.engine.IsRaised(next))
	require.False(t, rp.engine.IsRaised(old))
	require.Len(t, rp.pendingDets, 1, "queued clear must still be outside the processor buffer")

	rp.flushDetections(ctx)
	require.Empty(t, rp.pendingDets, "already-queued output must settle in the same drop pass")
	require.Len(t, w.events, 2, "only the original alarm and unrelated raise may publish")
	require.Equal(t, "dev2", w.events[1].Series)
	require.Equal(t, runtime.EdgeRaised, w.events[1].Edge)
	rp.flushDetections(ctx)
	require.True(t, rp.checkpoint(ctx))
	require.Len(t, w.events, 2, "a later pass must not leak the superseded clear")
	require.True(t, rp.engine.IsRaised(next))

	handoffApply(t, rp, handoffMessage(t, 23, "v2", "dev1", "20", ""))
	rp.flushDetections(ctx)
	require.Len(t, w.events, 3, "a genuine successor recovery must still clear the alarm")
	require.Equal(t, "acme/p1@v2/r1", w.events[2].RuleID)
	require.Equal(t, runtime.EdgeResolved, w.events[2].Edge)
	require.False(t, rp.engine.IsRaised(next))
}
