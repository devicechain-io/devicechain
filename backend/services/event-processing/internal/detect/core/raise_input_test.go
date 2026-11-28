// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// A raise-only checkpoint reconstructs the run from the decision instant. The emitted
// stamp only orders alarm edges; using it invents a later run and can re-raise after a break.
func TestDurationRaiseOnlySnapshotContinuesLikeLive(t *testing.T) {
	for _, bumped := range []bool{false, true} {
		for _, kind := range []string{"engine", "shard1", "shard4", "concurrent"} {
			t.Run(fmt.Sprintf("bumped=%v/%s", bumped, kind), func(t *testing.T) {
				hold := 10 * time.Second
				rules := []Rule{{ID: "r", Kind: Duration, Hold: hold}}
				key := SeriesKey{Rule: "r", Series: "d"}
				decision := at(10)
				live := NewEngine(rules, 0)
				live.Advance(decision.Add(-hold))
				if bumped {
					// Exercise the shared edge writer with a fall at the next hold's deadline.
					live.emit(rules[0], key, decision.Add(-hold))
					live.resolve(rules[0], key, decision)
					live.Drain()
				}
				live.ProcessEvent(Event{Seq: 1, Key: key, Time: decision.Add(-hold), Match: true})
				live.Advance(decision)
				stamp := decision
				if bumped {
					stamp = stamp.Add(time.Microsecond)
				}
				if got := live.Drain(); len(got) != 1 || !got[0].At.Equal(stamp) {
					t.Fatalf("setup raise: %+v", got)
				}
				// Older writers consumed the run on raise. Keep every other checkpoint field.
				var s map[string]json.RawMessage
				if err := json.Unmarshal(compatBytes(t, live), &s); err != nil {
					t.Fatal(err)
				}
				delete(s, "active")
				raw, err := json.Marshal(s)
				if err != nil {
					t.Fatal(err)
				}
				restored := compatRestore(t, kind, rules, raw)
				// A break before the run says nothing, while one at its start counts late.
				// A break exactly at the decision must end the run, with no phantom hold.
				stream := []Event{
					{Seq: 2, Key: key, Time: decision.Add(-hold - time.Nanosecond), Match: false},
					{Seq: 3, Key: key, Time: decision.Add(-hold), Match: false},
					{Seq: 4, Key: key, Time: decision, Match: false},
					{Seq: 5, Key: key, Time: decision.Add(2 * hold), Match: true},
					{Seq: 6, Key: key, Time: decision.Add(3 * hold), Match: false},
					{Seq: 7, Key: key, Time: decision.Add(4 * hold), Match: true},
					{Seq: 8, Key: key, Time: decision.Add(5 * hold), Match: false},
				}
				var all []Detection
				for _, ev := range stream {
					live.ProcessEvent(ev)
					restored.ProcessEvent(ev)
					want, got := live.Drain(), restored.Drain()
					if !reflect.DeepEqual(got, want) {
						t.Errorf("seq %d: restored %+v, live %+v", ev.Seq, got, want)
					}
					all = append(all, want...)
					wantLate, gotLate := live.DrainLateSamples(), restored.DrainLateSamples()
					if gotLate != wantLate {
						t.Errorf("seq %d: restored late=%d, live=%d", ev.Seq, gotLate, wantLate)
					}
				}
				if len(all) != 5 || all[0].Edge != EdgeResolved || !all[0].At.Equal(stamp) {
					t.Fatalf("continued stream must resolve and complete two more episodes: %+v", all)
				}
			})
		}
	}
}

func TestDropSeriesUsesRaiseDecisionTime(t *testing.T) {
	for _, kind := range []string{"engine", "shard1", "shard4", "concurrent"} {
		for _, recorded := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/input=%v", kind, recorded), func(t *testing.T) {
				rules, key, decision, raw := compatSnapshot(t)
				stamp := decision.Add(time.Microsecond)
				want := decision
				if !recorded {
					// Literal old-format checkpoint: no optional decision time.
					raw = []byte(fmt.Sprintf(`{"watermark":%q,"raised":[{"rule":"r","series":"device","at":%q}]}`,
						decision.Format(time.RFC3339Nano), stamp.Format(time.RFC3339Nano)))
					want = stamp
				}
				d := compatRestore(t, kind, rules, raw)
				calls := 0
				stale := func(id string, input time.Time, raised bool) bool {
					calls++
					if id != key.Rule || !raised || !input.Equal(want) {
						t.Errorf("stale callback got (%s, %v, %v), want (%s, %v, true)", id, input, raised, key.Rule, want)
					}
					return input.Before(want.Add(time.Nanosecond))
				}
				if n := d.DropSeries(key.Series, stale); n == 0 || calls != 1 {
					t.Fatalf("stale incarnation must be dropped: entries=%d calls=%d", n, calls)
				}
				if got := d.SeriesKeys(); len(got) != 0 {
					t.Fatalf("stale state survived: %+v", got)
				}
				if got := d.Drain(); len(got) != 0 {
					t.Fatalf("teardown emitted detections: %+v", got)
				}
			})
		}
	}
}
