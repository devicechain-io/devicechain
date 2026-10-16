// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"
)

// TestLiveLoopHandsTheEngineEachMessagesReceipt drives the live loop, not the engine: a threshold
// match dated behind the frontier raises when its message reached the resolved stream promptly
// after the platform received it, and is refused when it was stored a redelivery's AckWait later
// or carries no receipt time. Each decision reads the event's ProcessedTime and the message's
// broker append time, so a loop that handed the engine anything else — no receipt, or one built
// from the device's own time — fails here though every engine test passes.
func TestLiveLoopHandsTheEngineEachMessagesReceipt(t *testing.T) {
	ctx := context.Background()
	w := &derivedCapture{}
	rp := stampingProcessor(t, thresholdReg(t), w)

	wall := testBase.Add(time.Hour)
	// A live device carries event time an hour on.
	rp.handle(stampedMsg(t, 1, "live", "20", wall, wall, wall))
	behind := testBase.Add(time.Minute) // every reading below is dated an hour behind the fleet
	// A lagging device's reading, received now and stored a second later.
	rp.handle(stampedMsg(t, 2, "lagging", "90", behind, wall, wall.Add(time.Second)))
	// A redelivery: received an hour ago with the reading, stored again now.
	rp.handle(stampedMsg(t, 3, "redelivered", "90", behind, behind, wall))
	// No ProcessedTime: the loop cannot tell, so the frontier bound holds.
	rp.handle(stampedMsg(t, 4, "unknown", "90", behind, time.Time{}, wall))
	rp.checkpoint(ctx)

	got := map[string]int{}
	for _, de := range w.events {
		got[de.Series]++
	}
	if got["lagging"] != 1 {
		t.Errorf("the lagging device's prompt reading raised %d times, want 1 (published %+v)", got["lagging"], w.events)
	}
	if got["redelivered"] != 0 || got["unknown"] != 0 {
		t.Errorf("a possible redelivery behind the frontier raised: %+v", got)
	}
	if len(w.events) != 1 {
		t.Fatalf("published %d derived events, want only the lagging device's raise: %+v", len(w.events), w.events)
	}
	if !w.events[0].OccurredTime.Equal(behind) {
		t.Fatalf("the raise is stamped %v, want the reading's own time %v", w.events[0].OccurredTime, behind)
	}
}
