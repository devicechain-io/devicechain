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

// The cost of sharding, against the plain engine, on the same stream. K=1 is the production default
// and must cost ~nothing; K>1 here is the synchronous overhead only (routing, K ticks per message)
// with no parallelism, so it is expected to be SLOWER than plain until shards run concurrently.
func BenchmarkShardedProcessResolved(b *testing.B) {
	const devices = 1000
	msgs := shardedWorkload(shardedBenchRules, devices, 4096)
	run := func(b *testing.B, process func(seq uint64, t time.Time, evs []Event), drain func()) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			evs := msgs[i%len(msgs)]
			process(uint64(i+1), base.Add(time.Duration(i)*time.Millisecond), evs)
			drain()
		}
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
