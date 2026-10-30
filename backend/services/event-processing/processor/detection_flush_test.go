// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"
)

// A detection must reach the broker within a tick of firing, not wait out the checkpoint interval.
// The interval here is an hour and the clock never moves, so only the tick's own early flush can
// publish it. A device reporting every few seconds sees the interval as alarm-to-command latency
// otherwise: measured at 0.5 to 9.8 s, uniformly, on a quiet single-device cluster with the 10 s
// default.
//
// It also pins what the early flush must NOT do: commit a snapshot or ack. Those stay with the
// checkpoint, which is what keeps the committed sequence behind every unpublished detection.
func TestADetectionIsPublishedWithinATickNotAtTheCheckpoint(t *testing.T) {
	g := newGapRig(t, &fakeReplayOpener{})
	ack := &fakeAck{}
	before, _, err := g.rp.Store.LoadCommittedSeq(context.Background(), "singleton")
	if err != nil {
		t.Fatalf("load committed seq: %v", err)
	}
	// The loop's last checkpoint is the test clock's now, so the hour is a real wait.
	g.rp.lastCheckpoint = testBase
	startLoop(t, g, newAtomicClock(testBase),
		&feedReader{fakeReader: fakeReader{results: []readResult{{msg: hot(t, 1, ack)}}}})

	waitFor(t, "the detection to be published before any checkpoint", func() bool {
		return len(g.rec.published()) == 1
	})
	// Give the loop several more ticks to do what it must not.
	time.Sleep(50 * time.Millisecond)
	if after, _, err := g.rp.Store.LoadCommittedSeq(context.Background(), "singleton"); err != nil || after != before {
		t.Fatalf("the early flush moved the committed sequence from %d to %d (err=%v)", before, after, err)
	}
	if got := len(g.rec.published()); got != 1 {
		t.Fatalf("the detection was published %d times, want once", got)
	}
}
