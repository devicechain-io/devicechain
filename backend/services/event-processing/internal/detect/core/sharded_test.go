// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"fmt"
	"testing"
	"time"
)

// shardedWorkload builds one message per step, fanning out to every rule for one device.
func shardedWorkload(rules []Rule, devices, messages int) [][]Event {
	msgs := make([][]Event, messages)
	for i := range msgs {
		d := fmt.Sprintf("dev%d", i%devices)
		for _, r := range rules {
			msgs[i] = append(msgs[i], Event{Key: SeriesKey{Rule: r.ID, Series: d}, Time: base.Add(time.Duration(i) * time.Millisecond), Value: float64(i % 97), Match: true})
		}
	}
	return msgs
}

var shardedBenchRules = []Rule{
	{ID: "t1/abs", Kind: Absence, Timeout: 30 * time.Second},
	{ID: "t1/thr", Kind: Threshold},
	{ID: "t1/sld", Kind: SlidingAgg, Window: 5 * time.Second, Agg: AggMax, Op: GT, Thresh: 1e9},
	{ID: "t2/agg", Kind: Aggregate, Window: 2 * time.Second, Agg: AggSum, Op: GT, Thresh: 1e9},
}

// TestShardedRemoveMatchingCountsLikeOneEngine pins what the differential harness does not: it
// compares only whether a purge found anything, but the count is what restarts the coordinator's
// settle window, and the rule set is replicated, so a naive sum reports K times the rules removed.
func TestShardedRemoveMatchingCountsLikeOneEngine(t *testing.T) {
	msgs := shardedWorkload(shardedBenchRules, 40, 200)
	prefixes := []string{"t1/", "t2/", "t3/", "t1/sld"}
	for _, k := range []int{1, 2, 5, 16} {
		for _, prefix := range prefixes {
			match := func(id string) bool { return len(id) >= len(prefix) && id[:len(prefix)] == prefix }
			plain, sharded := NewEngine(shardedBenchRules, 0), NewSharded(k, shardedBenchRules, 0)
			for i, evs := range msgs {
				plain.ProcessResolved(uint64(i+1), evs[0].Time, evs)
				sharded.ProcessResolved(uint64(i+1), evs[0].Time, evs)
			}
			if want, got := plain.RemoveMatching(match), sharded.RemoveMatching(match); want != got {
				t.Errorf("K=%d prefix %q: purge count %d, single engine %d", k, prefix, got, want)
			}
		}
	}
}

// TestShardedRefusesToSnapshotMisalignedShards: the watermark and position are shared, and a
// checkpoint must never write a state that no single engine could be in.
func TestShardedRefusesToSnapshotMisalignedShards(t *testing.T) {
	s := NewSharded(3, shardedBenchRules, 0)
	s.ProcessResolved(1, base, nil)
	s.shards[1].ProcessResolved(2, base.Add(time.Second), nil) // bypasses the façade: one shard ahead
	if _, err := s.Snapshot(); err == nil {
		t.Fatal("snapshot of shards that disagree on the frontier was accepted")
	}
}

// TestShardedClampsK keeps a bad configured value from producing a zero-shard engine.
func TestShardedClampsK(t *testing.T) {
	for k, want := range map[int]int{-3: 1, 0: 1, 1: 1, 64: 64, 65: 64, 1000: 64} {
		if got := NewSharded(k, nil, 0).Shards(); got != want {
			t.Errorf("K=%d gave %d shards, want %d", k, got, want)
		}
	}
}

// retime stamps every sample of a reused message with the message time, so a workload built once
// stays current however many times it wraps. The fixed times shardedWorkload assigns are only right
// for the first pass: after it, Aggregate panes sit behind the watermark and SlidingAgg samples are
// older than the window, so the engine declines them and the run measures rejection, not upkeep.
// It rewrites in place (four stores a message, no allocation); the messages are consumed
// synchronously and the engine keeps times by value, so reuse is safe.
func retime(evs []Event, t time.Time) {
	for i := range evs {
		evs[i].Time = t
	}
}

// drivePast runs n messages through process, reusing msgs modulo its length. With fresh set each
// reused message is retimed to the frontier; without it the original times are replayed, which is
// the late-data workload.
func drivePast(msgs [][]Event, n int, fresh bool, process func(seq uint64, t time.Time, evs []Event), drain func()) {
	for i := 0; i < n; i++ {
		evs := msgs[i%len(msgs)]
		t := base.Add(time.Duration(i) * time.Millisecond)
		if fresh {
			retime(evs, t)
		}
		process(uint64(i+1), t, evs)
		drain()
	}
}

// shardedState is what a sustained run should be holding: open aggregate panes, retained sliding
// samples, and the late-sample count since the last read.
func shardedState(engines []*Engine) (panes, sliding int, late uint64) {
	for _, e := range engines {
		panes += len(e.panes)
		sliding += e.RetainedSampleCounts()["t1/sld"]
		late += e.DrainLateSamples()
	}
	return
}

// TestShardedWorkloadStaysCurrentAcrossWraps pins what BenchmarkShardedProcessResolved measures:
// well past three wraps of the reused workload the windows still hold live state and nothing is
// declined as late. Without the retime, the same run ends with no open panes and thousands of
// late sliding samples (TestShardedStaleWorkloadIsLate), i.e. it would be timing rejection.
func TestShardedWorkloadStaysCurrentAcrossWraps(t *testing.T) {
	const devices, messages, wraps = 50, 2048, 4
	for _, k := range []int{0, 1, 4} { // 0 = the plain engine
		msgs := shardedWorkload(shardedBenchRules, devices, messages)
		var engines []*Engine
		var process func(uint64, time.Time, []Event)
		var drain func()
		if k == 0 {
			e := NewEngine(shardedBenchRules, 0)
			engines, process, drain = []*Engine{e}, e.ProcessResolved, func() { e.Drain() }
		} else {
			s := NewSharded(k, shardedBenchRules, 0)
			engines, process, drain = s.shards, s.ProcessResolved, func() { s.Drain() }
		}
		drivePast(msgs, messages*wraps+17, true, process, drain)
		panes, sliding, late := shardedState(engines)
		if late != 0 {
			t.Errorf("K=%d: %d samples declined as late in a fresh workload", k, late)
		}
		if panes == 0 {
			t.Errorf("K=%d: no open aggregate panes after %d wraps", k, wraps)
		}
		if sliding == 0 {
			t.Errorf("K=%d: no sliding samples retained after %d wraps", k, wraps)
		}
	}
}

// TestShardedStaleWorkloadIsLate is the negative control for the test above, and the reason the
// late-data benchmark exists: replaying the original times past the first wrap IS rejected, so
// the fresh test is not passing for want of a way to fail.
func TestShardedStaleWorkloadIsLate(t *testing.T) {
	const devices, messages, wraps = 50, 2048, 4
	msgs := shardedWorkload(shardedBenchRules, devices, messages)
	e := NewEngine(shardedBenchRules, 0)
	drivePast(msgs, messages*wraps, false, e.ProcessResolved, func() { e.Drain() })
	if panes, _, late := shardedState([]*Engine{e}); late == 0 || panes != 0 {
		t.Errorf("stale replay: %d late samples, %d panes; want late > 0 and no panes", late, panes)
	}
}

// The cost of sharding, against the plain engine, on the same stream. K=1 is the production default
// and must cost ~nothing; K>1 here is the synchronous overhead only (routing, K ticks per message)
// with no parallelism, so it is expected to be SLOWER than plain until shards run concurrently.
//
// The workload is kept current (see retime), so this times sustained window upkeep at any b.N.
// It is the engine alone: no planning, no checkpoints, so it is not pipeline capacity.
func BenchmarkShardedProcessResolved(b *testing.B) { benchSharded(b, true) }

// BenchmarkShardedProcessResolvedLate replays the original sample times, so past the first wrap
// the engine is declining stale samples. It prices late-data rejection and depends on b.N; do not
// compare it with the fresh benchmark as if they were one workload.
func BenchmarkShardedProcessResolvedLate(b *testing.B) { benchSharded(b, false) }

// BenchmarkShardedRetime is the cost of the retime step alone, which the fresh benchmark pays
// inside its timed region.
func BenchmarkShardedRetime(b *testing.B) {
	msgs := shardedWorkload(shardedBenchRules, 1000, 4096)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		retime(msgs[i%len(msgs)], base.Add(time.Duration(i)*time.Millisecond))
	}
}

func benchSharded(b *testing.B, fresh bool) {
	const devices = 1000
	msgs := shardedWorkload(shardedBenchRules, devices, 4096)
	run := func(b *testing.B, process func(seq uint64, t time.Time, evs []Event), drain func()) {
		b.ReportAllocs()
		b.ResetTimer()
		drivePast(msgs, b.N, fresh, process, drain)
	}
	b.Run("plain", func(b *testing.B) {
		e := NewEngine(shardedBenchRules, 0)
		run(b, e.ProcessResolved, func() { e.Drain() })
	})
	for _, k := range []int{1, 4} {
		b.Run(fmt.Sprintf("sharded-K=%d", k), func(b *testing.B) {
			e := NewSharded(k, shardedBenchRules, 0)
			run(b, e.ProcessResolved, func() { e.Drain() })
		})
	}
}
