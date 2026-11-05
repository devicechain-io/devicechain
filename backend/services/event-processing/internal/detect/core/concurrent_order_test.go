// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"fmt"
	"testing"
	"time"
)

func idsOf(ds []Detection) string {
	s := ""
	for _, d := range ds {
		s += fmt.Sprintf("%s/%s/%d@%d ", d.RuleID, d.Series, d.Edge, d.At.UnixNano())
	}
	return s
}

// Two rules on one device that fire on the same message are emitted by the single engine in the
// order the message carried their events, whichever shards own them. Rules whose owners are in the
// OPPOSITE order to the events are the case an ordering by (operation, shard) gets wrong.
func TestConcurrentTwoRulesOnOneDeviceFireInTheEnginesOrder(t *testing.T) {
	const k = 4
	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	var rules []Rule
	for i := 0; i < 12; i++ {
		rules = append(rules, Rule{ID: fmt.Sprintf("ta/p@1/two%02d", i), Kind: Threshold})
	}
	// Events in descending shard order: the shards' own order is the reverse of the message's.
	var evs []Event
	for sh := k - 1; sh >= 0; sh-- {
		for _, r := range rules {
			if shardOf(r.ID, "dev", k, 0) == sh {
				evs = append(evs, Event{Key: SeriesKey{Rule: r.ID, Series: "dev"}, Time: base, Value: 1, HasValue: true, Match: true})
			}
		}
	}
	owners := map[int]bool{}
	for _, e := range evs {
		owners[shardOf(e.Key.Rule, "dev", k, 0)] = true
	}
	if len(owners) < 3 {
		t.Fatalf("the rules landed on only %d shards: the check means nothing", len(owners))
	}
	plain := NewEngine(rules, diffLateness)
	c := NewConcurrentSharded(k, rules, diffLateness)
	defer c.Close()
	plain.ProcessResolved(1, base, evs)
	c.ProcessResolved(1, base, evs)
	want, got := plain.Drain(), c.Drain()
	if len(want) != len(evs) {
		t.Fatalf("the engine raised %d of %d", len(want), len(evs))
	}
	if idsOf(want) != idsOf(got) {
		t.Fatalf("emission order differs from the engine's:\n engine: %s\n shards: %s", idsOf(want), idsOf(got))
	}
}

// The whole emission sequence, not just each series' part of it, is the engine's: across seeds, shard
// counts, windows of operations, with timers, panes and released samples firing between messages.
func TestConcurrentEmissionOrderIsTheEnginesAcrossSeries(t *testing.T) {
	for _, k := range []int{2, 4, 7} {
		for _, seed := range diffSeeds {
			for _, every := range []int{1, 40} {
				k, seed, every := k, seed, every
				t.Run(fmt.Sprintf("K=%d/seed=%d/every=%d", k, seed, every), func(t *testing.T) {
					g := newDiffGen(seed)
					plain := NewEngine(g.rulesNow(), diffLateness)
					c := NewConcurrentSharded(k, g.rulesNow(), diffLateness)
					defer c.Close()
					var want []Detection
					seq := uint64(0)
					check := func(at int) {
						got := detectionsOf(append(c.Collect(), func() []TaggedDetection { c.Sync(); return c.Collect() }()...))
						if idsOf(want) != idsOf(got) {
							t.Fatalf("op %d: emission order differs (%d vs %d)\n engine: %.600s\n shards: %.600s", at, len(want), len(got), idsOf(want), idsOf(got))
						}
						want = nil
					}
					for i := 0; i < 600; i++ {
						if i%9 == 8 {
							w := g.maxT.Add(g.dur(40_000))
							plain.Advance(w)
							c.Advance(w)
						} else {
							_, tm, evs := g.message()
							seq++
							plain.ProcessResolved(seq, tm, evs)
							c.ProcessResolved(seq, tm, evs)
						}
						want = append(want, plain.Drain()...)
						if (i+1)%every == 0 {
							check(i)
						}
					}
				})
			}
		}
	}
}
