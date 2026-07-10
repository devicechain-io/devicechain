// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"testing"
	"time"
)

// ProcessResolved applies a whole message's fan-out as ONE sequenced batch: it advances the
// watermark once, applies every per-rule event, and records the message sequence once — so N
// same-seq events all take effect (a per-event Seq guard would have dropped all but the
// first), and a redelivered message at or below the recorded sequence is dropped whole.
func TestProcessResolvedBatchThenIdempotent(t *testing.T) {
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	e := NewEngine([]Rule{
		{ID: "acme/a", Kind: Threshold},
		{ID: "acme/b", Kind: Threshold},
	}, 0)

	// One message fans out to two matching threshold events sharing seq 1 and the same time.
	e.ProcessResolved(1, base, []Event{
		{Key: SeriesKey{Rule: "acme/a", Series: "d1"}, Time: base, Match: true},
		{Key: SeriesKey{Rule: "acme/b", Series: "d1"}, Time: base, Match: true},
	})
	dets := e.Drain()
	if len(dets) != 2 {
		t.Fatalf("both rules should fire from one batch; got %d", len(dets))
	}
	if e.LastSeq() != 1 {
		t.Fatalf("last seq should be the message seq 1; got %d", e.LastSeq())
	}
	if !e.Watermark().Equal(base) {
		t.Fatalf("watermark should advance to the message time; got %v", e.Watermark())
	}

	// Re-feeding the same sequence is a no-op (idempotent re-feed guard, message level).
	e.ProcessResolved(1, base.Add(time.Hour), []Event{
		{Key: SeriesKey{Rule: "acme/a", Series: "d1"}, Time: base, Match: true},
	})
	if got := e.Drain(); len(got) != 0 {
		t.Fatalf("re-feeding seq 1 must emit nothing; got %d", len(got))
	}
	if !e.Watermark().Equal(base) {
		t.Fatalf("a dropped re-feed must not advance the watermark; got %v", e.Watermark())
	}

	// An empty batch (a message matching no rule) still advances the sequence + watermark.
	e.ProcessResolved(2, base.Add(time.Minute), nil)
	if e.LastSeq() != 2 || !e.Watermark().Equal(base.Add(time.Minute)) {
		t.Fatalf("empty batch should advance seq/watermark; got seq %d wm %v", e.LastSeq(), e.Watermark())
	}
}
