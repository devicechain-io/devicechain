// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"encoding/json"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

// The seam changes only latch stamping, retaining the precursor's exact reader.
func disableBumps(d Detector) {
	switch d := d.(type) {
	case *Engine:
		d.disableEdgeBump = true
	case *Sharded:
		for _, e := range d.shards {
			e.disableEdgeBump = true
		}
	case *ConcurrentSharded:
		d.Sync()
		for _, e := range d.s.shards {
			e.disableEdgeBump = true
		}
	}
}

func TestBumpedSnapshotThroughV17Reader(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: Threshold}}
	key := SeriesKey{Rule: "r", Series: "d"}
	for _, writer := range []string{"engine", "shard4", "concurrent"} {
		for _, reader := range []string{"engine", "shard4", "concurrent"} {
			for _, successor := range []string{"engine", "shard4", "concurrent"} {
				t.Run(writer+"/"+reader+"/"+successor, func(t *testing.T) {
					e := compatRestore(t, writer, rules, []byte(`{}`))
					for i, match := range []bool{true, false, true} {
						when := at(2)
						if i == 0 {
							when = at(1)
						}
						e.ProcessEvent(Event{Seq: uint64(i + 1), Key: key, Time: when, Match: match})
					}
					e.Drain()
					b := compatBytes(t, e)
					var s snapshot
					if err := json.Unmarshal(b, &s); err != nil {
						t.Fatal(err)
					}
					if len(s.Raised) != 1 || s.Raised[0].Input == nil || !s.Raised[0].Input.Equal(at(2)) || !s.Raised[0].At.Equal(at(2).Add(time.Microsecond)) {
						t.Fatalf("writer did not bump: %+v", s.Raised)
					}
					old := compatRestore(t, reader, rules, b)
					disableBumps(old)
					if got := compatBytes(t, old); !reflect.DeepEqual(got, b) {
						t.Fatal("v1.7 reader lost memo or raise input")
					}
					old.ProcessEvent(Event{Seq: 4, Key: key, Time: at(2), Match: false})
					got := old.Drain()
					if len(got) != 1 || got[0].Edge != EdgeResolved || !got[0].At.Equal(at(2).Add(time.Microsecond)) {
						t.Fatalf("v1.7 healthy non-match: %+v", got)
					}
					b = compatBytes(t, old)
					if err := json.Unmarshal(b, &s); err != nil {
						t.Fatal(err)
					}
					if len(s.Raised) != 0 || len(s.Fell) != 1 || s.Fell[0].Input == nil || !s.Fell[0].Input.Equal(at(2)) {
						t.Fatalf("v1.7 lost fall input: %+v", s)
					}
					next := compatRestore(t, successor, rules, b)
					if !reflect.DeepEqual(compatBytes(t, next), b) {
						t.Fatal("successor lost fall memo")
					}
					next.ProcessEvent(Event{Seq: 5, Key: key, Time: at(2), Match: true})
					got = next.Drain()
					if len(got) != 1 || got[0].Edge != EdgeRaised || !got[0].At.Equal(at(2).Add(2*time.Microsecond)) {
						t.Fatalf("successor failed equal-input bump: %+v", got)
					}
					if err := json.Unmarshal(compatBytes(t, next), &s); err != nil {
						t.Fatal(err)
					}
					if len(s.Raised) != 1 || s.Raised[0].Input == nil || !s.Raised[0].Input.Equal(at(2)) {
						t.Fatalf("successor lost raise input: %+v", s.Raised)
					}

				})
			}
		}
	}
}

func TestDeltaRateSecondSameInstantCycle(t *testing.T) {
	rules := []Rule{{ID: "r", Kind: DeltaRate, Op: GT, Thresh: 30}}
	key := SeriesKey{Rule: "r", Series: "d"}
	for _, kind := range []string{"engine", "shard4", "concurrent"} {
		e := compatRestore(t, kind, rules, []byte(`{}`))
		var edges []Detection
		for i, value := range []float64{0, 100, 0, 100, 101, 201} {
			sec := i + 1
			if sec > 3 {
				sec = 3
			}
			e.ProcessEvent(Event{Seq: uint64(i + 1), Key: key, Time: at(sec), Value: value, HasValue: true, Match: true})
			edges = append(edges, e.Drain()...)
		}
		e.Advance(at(4))
		edges = append(edges, e.Drain()...)
		// A healthy reading at the same input instant clamps to its raise. Fresh
		// episodes must advance strictly beyond that fall, including cycle two.
		want := []time.Time{at(2), at(3), at(3).Add(time.Microsecond), at(3).Add(time.Microsecond), at(3).Add(2 * time.Microsecond)}
		if len(edges) != len(want) {
			t.Fatalf("%s: %+v", kind, edges)
		}
		for i, d := range edges {
			if !d.At.Equal(want[i]) || (i%2 == 0) != (d.Edge == EdgeRaised) {
				t.Fatalf("%s edge %d: %+v", kind, i, edges)
			}
		}
	}
}

// Audit every generated edge before updating the golden. The v1.7 reader differs
// only in latch stamping. Changes to payloads, edge count or ordering are refused.
func auditGoldenEdgeBumps(t *testing.T) {
	t.Helper()
	historicalDoc := loadGolden(t)
	for _, historical := range []bool{false, true} {
		raises, resolves := 0, 0
		for seed := int64(0); seed < goldenSeeds; seed++ {
			rng := rand.New(rand.NewSource(seed))
			rules := diffRules(rng)
			msgs := diffMessages(rng, rules)
			a, b := NewEngine(rules, goldenLateness), NewEngine(rules, goldenLateness)
			if historical {
				if seed >= goldenSnapshotSeeds {
					break
				}
				g := historicalDoc.Seeds[seed]
				raw := ungz(t, g.MidSnap)
				var err error
				a, err = Restore(rules, goldenLateness, raw)
				if err != nil {
					t.Fatal(err)
				}
				b, err = Restore(rules, goldenLateness, raw)
				if err != nil {
					t.Fatal(err)
				}
				msgs = msgs[g.Mid:]
			}
			a.disableEdgeBump = true
			last := map[SeriesKey]Detection{}
			lastOld := map[SeriesKey]Detection{}
			check := func() {
				x, y := a.Drain(), b.Drain()
				if len(x) != len(y) {
					t.Fatalf("seed %d: edge count changed", seed)
				}
				for i, old := range x {
					d := y[i]
					key := SeriesKey{Rule: d.RuleID, Series: d.Series}
					stamp := d.At
					d.At = old.At
					if !reflect.DeepEqual(old, d) {
						t.Fatalf("seed %d: edge payload changed: %+v %+v", seed, old, d)
					}
					d.At = stamp
					if !old.At.Equal(d.At) {
						prev, ok := last[key]
						if d.Edge == EdgeRaised {
							memo := fallMemo{at: prev.At, input: lastOld[key].At}
							if !ok || prev.Edge != EdgeResolved || !prev.At.Equal(memo.at) || !(old.At.Equal(memo.at) || (!memo.input.IsZero() && old.At.Equal(memo.input))) || !d.At.Equal(memo.at.Add(time.Microsecond)) {
								t.Fatalf("seed %d: unaudited raise: old=%+v new=%+v memo=%+v prev=%+v", seed, old, d, memo, prev)
							}
							raises++
						} else {
							if !ok || prev.Edge != EdgeRaised || !d.At.Equal(prev.At) || !old.At.Before(d.At) {
								t.Fatalf("seed %d: unaudited resolve: %+v %+v prev=%+v", seed, old, d, prev)
							}
							resolves++
						}
					}
					last[key] = d
					lastOld[key] = old
				}
			}
			normalize := func(e *Engine) []byte {
				raw := compatBytes(t, e)
				var state snapshot
				if err := json.Unmarshal(raw, &state); err != nil {
					t.Fatal(err)
				}
				state.Fell = nil
				for i := range state.Raised {
					r := &state.Raised[i]
					if r.Input != nil {
						r.At = *r.Input
						r.Input = nil
					}
				}
				raw, err := json.Marshal(state)
				if err != nil {
					t.Fatal(err)
				}
				return raw
			}
			checkState := func() {
				if !reflect.DeepEqual(normalize(a), normalize(b)) {
					t.Fatalf("seed %d historical=%v: state changed beyond edge timestamps and memos", seed, historical)
				}
			}
			for _, m := range msgs {
				for _, e := range []*Engine{a, b} {
					if !m.adv.IsZero() {
						e.Advance(m.adv)
					} else {
						e.ProcessReceived(m.seq, m.t, Receipt{}, m.evs)
					}
				}
				check()
				if m.snap {
					checkState()
				}
			}
			a.Advance(base.Add(24 * time.Hour))
			b.Advance(base.Add(24 * time.Hour))
			check()
			checkState()
		}
		t.Logf("golden differential audit historical=%v: %d Raised bumped, %d Resolved clamped", historical, raises, resolves)
	}
}

func TestGoldenEdgeBumpAudit(t *testing.T) { auditGoldenEdgeBumps(t) }
