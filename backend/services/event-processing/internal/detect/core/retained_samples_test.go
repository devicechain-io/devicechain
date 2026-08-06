// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"testing"
	"time"
)

// TestRetainedSampleCountsSeesWhatLiveKeyCountsCannot is THE control for this gauge. The defect
// it exists to expose is not "the budget reads a bit low" — it is that a long-window rule piling
// samples onto ONE series moves the live-key count by exactly zero, because a window is one map
// entry however much it retains. So the test asserts BOTH halves: the old gauge must stay flat
// (proving the blindness is real and not a strawman) and the new one must climb with the samples.
//
// If a future change makes RetainedSampleCounts flat here too, it has stopped measuring heap and
// started measuring cardinality under a new name — which is the original bug.
func TestRetainedSampleCountsSeesWhatLiveKeyCountsCannot(t *testing.T) {
	const (
		slideID  = "acme/p@1/slide"
		repeatID = "acme/p@1/repeat"
		samples  = 500
	)
	// A window long enough that nothing evicts during the test: the point is retention, and an
	// eviction mid-run would confound "the gauge does not move" with "the samples left".
	window := time.Hour
	e := NewEngine([]Rule{
		{ID: slideID, Kind: SlidingAgg, Window: window, Agg: AggMax, Op: GT, Thresh: 1e9},
		{ID: repeatID, Kind: Repeating, Window: window, Count: 1e9},
	}, 0)

	// One event per rule on ONE series, so key cardinality is fixed from here on.
	e.ProcessEvent(Event{Seq: 1, Key: SeriesKey{Rule: slideID, Series: "d1"}, Time: at(1), Value: 1, HasValue: true, Match: true})
	e.ProcessEvent(Event{Seq: 2, Key: SeriesKey{Rule: repeatID, Series: "d1"}, Time: at(1), Match: true})
	e.Drain()

	keysAfterFirst := e.LiveKeyCounts()
	samplesAfterFirst := e.RetainedSampleCounts()

	seq := uint64(2)
	for i := 2; i <= samples; i++ {
		seq++
		e.ProcessEvent(Event{Seq: seq, Key: SeriesKey{Rule: slideID, Series: "d1"}, Time: at(i), Value: float64(i), HasValue: true, Match: true})
		seq++
		e.ProcessEvent(Event{Seq: seq, Key: SeriesKey{Rule: repeatID, Series: "d1"}, Time: at(i), Match: true})
	}
	e.Drain()

	keys := e.LiveKeyCounts()
	retained := e.RetainedSampleCounts()

	for _, id := range []string{slideID, repeatID} {
		// Half one: the blindness is real. 500× the data, same key count.
		if keys[id] != keysAfterFirst[id] {
			t.Fatalf("%s: live keys moved from %d to %d after %d samples on ONE series — this test no longer "+
				"demonstrates the blindness it guards against; re-derive it", id, keysAfterFirst[id], keys[id], samples)
		}
		if keys[id] != 1 {
			t.Fatalf("%s: want exactly 1 live key for a single series, got %d", id, keys[id])
		}
		// Half two: the new gauge tracks the data. This is the assertion that fails if the
		// retained-sample count is ever reduced to another entry count.
		if retained[id] != samples {
			t.Fatalf("%s: want %d retained samples, got %d (was %d after the first event) — the gauge is not "+
				"following the samples", id, samples, retained[id], samplesAfterFirst[id])
		}
	}
}

// TestPendingTimerCountSeesWhatNeitherMapGaugeCan is the third axis's control, and it is the
// same comparative shape as the test above: an Absence rule resets its deadline on every
// heartbeat, and each reset PUSHES a heap entry while the superseded one lingers until its
// deadline passes. So the heap grows per EVENT on a single series — live keys stay at 1 (one
// wheel entry) and retained samples stay at 0 (absence retains no samples), while the heap
// climbs with the traffic.
//
// This is the defect the retained-sample gauge was added for, reproduced one structure over. If
// this test ever goes flat, the wheel stopped retaining and PendingTimerCount is measuring
// nothing — check that before deleting it.
func TestPendingTimerCountSeesWhatNeitherMapGaugeCan(t *testing.T) {
	const id = "acme/p@1/abs"
	const beats = 200
	// A timeout far longer than the span of heartbeats, so nothing reaches its deadline during
	// the run: the point is that SUPERSEDED entries accumulate, not that live ones do.
	e := NewEngine([]Rule{{ID: id, Kind: Absence, Timeout: time.Hour}}, 0)

	for i := 1; i <= beats; i++ {
		e.ProcessEvent(Event{Seq: uint64(i), Key: SeriesKey{Rule: id, Series: "d1"}, Time: at(i), Match: true})
	}
	e.Drain()

	if got := e.LiveKeyCounts()[id]; got != 1 {
		t.Fatalf("one series must be ONE live key however many heartbeats it sent; got %d — this test "+
			"no longer demonstrates the blindness it guards against", got)
	}
	if got := e.RetainedSampleCounts()[id]; got != 0 {
		t.Fatalf("absence retains no samples, so the retained-sample gauge must read 0; got %d", got)
	}
	// The heap holds the current live timer plus every superseded one that has not come due.
	if got := e.PendingTimerCount(); got != beats {
		t.Fatalf("want %d pending timer entries after %d deadline resets, got %d — if this is 1 the wheel "+
			"now evicts superseded entries eagerly and this gauge is measuring nothing", beats, beats, got)
	}
}

// TestPendingTimerCountFallsWhenTimersComeDue is the counterweight: the gauge must report what is
// pending NOW, not a high-water mark, or it alarms forever after one burst and gets muted.
func TestPendingTimerCountFallsWhenTimersComeDue(t *testing.T) {
	const id = "acme/p@1/abs"
	e := NewEngine([]Rule{{ID: id, Kind: Absence, Timeout: 10 * time.Second}}, 0)
	for i := 1; i <= 50; i++ {
		e.ProcessEvent(Event{Seq: uint64(i), Key: SeriesKey{Rule: id, Series: "d1"}, Time: at(i), Match: true})
	}
	e.Drain()
	if got := e.PendingTimerCount(); got == 0 {
		t.Fatal("heartbeats must leave pending timer entries behind")
	}
	// Advance far past every deadline: popDue drains the heap, discarding the stale entries.
	e.Advance(at(10_000))
	e.Drain()
	if got := e.PendingTimerCount(); got != 0 {
		t.Fatalf("advancing past every deadline must drain the heap; got %d still pending", got)
	}
}

// TestRetainedSampleCountsFollowsEviction proves the gauge falls as well as rises: it reports what
// is retained NOW, not a high-water mark. A monotonically-climbing gauge would alarm forever after
// one burst and get muted, which is how a real budget signal dies.
func TestRetainedSampleCountsFollowsEviction(t *testing.T) {
	const id = "acme/p@1/slide"
	e := NewEngine([]Rule{{ID: id, Kind: SlidingAgg, Window: 10 * time.Second, Agg: AggMax, Op: GT, Thresh: 1e9}}, 0)

	for i := 1; i <= 10; i++ {
		e.ProcessEvent(Event{Seq: uint64(i), Key: SeriesKey{Rule: id, Series: "d1"}, Time: at(i), Value: 1, HasValue: true, Match: true})
	}
	e.Drain()
	if got := e.RetainedSampleCounts()[id]; got != 10 {
		t.Fatalf("want 10 retained samples before eviction, got %d", got)
	}

	// An event far past the window evicts everything older, leaving only the new sample.
	e.ProcessEvent(Event{Seq: 100, Key: SeriesKey{Rule: id, Series: "d1"}, Time: at(1000), Value: 1, HasValue: true, Match: true})
	e.Drain()
	if got := e.RetainedSampleCounts()[id]; got != 1 {
		t.Fatalf("want 1 retained sample after the window slid past the rest, got %d", got)
	}
}

// TestRetainedSampleCountsCountsCorrelationMembers pins the third retaining map. A correlation
// rule's memory is its distinct-member set, which is per-datum, so it belongs on this axis too.
func TestRetainedSampleCountsCountsCorrelationMembers(t *testing.T) {
	const id = "acme/p@1/corr"
	e := NewEngine([]Rule{{ID: id, Kind: Correlation, Window: time.Hour, Count: 1e9, MemberCap: 100}}, 0)
	for i, m := range []string{"mA", "mB", "mC"} {
		e.ProcessEvent(Event{Seq: uint64(i + 1), Key: SeriesKey{Rule: id, Series: "area1"}, Member: m, Time: at(1), Match: true})
	}
	e.Drain()
	if got := e.RetainedSampleCounts()[id]; got != 3 {
		t.Fatalf("want 3 retained correlation members, got %d", got)
	}
}

// TestRetainedSampleCountsIgnoresFixedSizeKinds is the counterweight to the tests above: a gauge
// that counted everything would rise on every kind and tell an operator nothing about WHICH rule
// is holding memory. The kinds that fold into a fixed-size accumulator or latch must contribute
// zero here — their cost is already fully described by LiveKeyCounts.
func TestRetainedSampleCountsIgnoresFixedSizeKinds(t *testing.T) {
	rules := []Rule{
		{ID: "acme/p@1/dur", Kind: Duration, Hold: time.Hour},
		{ID: "acme/p@1/delta", Kind: DeltaRate, Op: GT, Thresh: 1e9},
		{ID: "acme/p@1/agg", Kind: Aggregate, Window: time.Hour, Agg: AggAvg, Op: GT, Thresh: 1e9},
		{ID: "acme/p@1/abs", Kind: Absence, Timeout: time.Hour},
	}
	e := NewEngine(rules, 0)
	for i := 1; i <= 50; i++ {
		for j, r := range rules {
			e.ProcessEvent(Event{Seq: uint64(i*10 + j), Key: SeriesKey{Rule: r.ID, Series: "d1"},
				Time: at(i), Value: float64(i), HasValue: true, Match: true})
		}
	}
	e.Drain()

	retained := e.RetainedSampleCounts()
	for _, r := range rules {
		if retained[r.ID] != 0 {
			t.Fatalf("%s folds events into fixed-size state, so it must contribute 0 retained samples; got %d "+
				"— double-counting it here would hide which rule is actually holding the heap", r.ID, retained[r.ID])
		}
	}
	// ...and those kinds are not invisible to the budget: the OTHER gauge still sees them.
	keys := e.LiveKeyCounts()
	for _, r := range rules {
		if keys[r.ID] == 0 {
			t.Fatalf("%s must still be counted as live keys; got 0", r.ID)
		}
	}
}
