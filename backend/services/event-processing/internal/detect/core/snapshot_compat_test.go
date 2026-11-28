// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func compatSnapshot(t *testing.T) ([]Rule, SeriesKey, time.Time, []byte) {
	t.Helper()
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	key := SeriesKey{Rule: "r", Series: "device"}
	// Literal future-writer JSON: do not derive it from the reader's snapshot structs.
	raw := fmt.Sprintf(`{"watermark":%q,"raised":[{"rule":"r","series":"device","at":%q,"input":%q}],"fell":[{"rule":"r","series":"device","at":%q,"input":%q,"recordedFrontier":%q}]}`,
		now.Format(time.RFC3339Nano), now.Add(time.Microsecond).Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		now.Add(-time.Microsecond).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	return []Rule{{ID: "r", Kind: Threshold}}, key, now, []byte(raw)
}

func compatRestore(t *testing.T, kind string, rules []Rule, raw []byte) Detector {
	t.Helper()
	var d Detector
	var err error
	switch kind {
	case "engine":
		d, err = Restore(rules, 0, raw)
	case "shard1":
		d, err = RestoreSharded(1, rules, 0, raw)
	case "shard4":
		d, err = RestoreSharded(4, rules, 0, raw)
	case "concurrent":
		var c *ConcurrentSharded
		c, err = RestoreConcurrentSharded(4, rules, 0, raw)
		if err == nil {
			t.Cleanup(c.Close)
			d = c
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func compatBytes(t *testing.T, d Detector) []byte {
	t.Helper()
	b, err := d.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSnapshotCompatFutureResolve(t *testing.T) {
	for _, kind := range []string{"engine", "shard4", "concurrent"} {
		for _, delay := range []time.Duration{0, 500 * time.Nanosecond} {
			t.Run(fmt.Sprintf("%s/%s", kind, delay), func(t *testing.T) {
				rules, key, now, raw := compatSnapshot(t)
				d := compatRestore(t, kind, rules, raw)
				d.ProcessEvent(Event{Seq: 1, Key: key, Time: now.Add(delay), Match: false})
				got := d.Drain()
				if len(got) != 1 || got[0].Edge != EdgeResolved || !got[0].At.Equal(now.Add(time.Microsecond)) {
					t.Fatalf("healthy input must resolve at emitted raise: %+v", got)
				}
				var s snapshot
				if err := json.Unmarshal(compatBytes(t, d), &s); err != nil {
					t.Fatal(err)
				}
				if len(s.Raised) != 0 || len(s.Fell) != 1 || s.Fell[0].Input == nil || !s.Fell[0].Input.Equal(now.Add(delay)) {
					t.Fatalf("latch/memo after resolve: %+v", s)
				}
			})
		}
	}
}

func TestSnapshotCompatRoundTrip(t *testing.T) {
	rules, _, _, raw := compatSnapshot(t)
	// Several owners and reverse insertion order exercise canonical split/merge sorting.
	e := compatRestore(t, "engine", rules, raw).(*Engine)
	var seed snapshot
	if err := json.Unmarshal(raw, &seed); err != nil {
		t.Fatal(err)
	}
	for i := 15; i >= 0; i-- {
		key := SeriesKey{Rule: "r", Series: fmt.Sprintf("device-%02d", i)}
		e.raised[key] = seed.Raised[0].At
		e.raiseInputs[key] = *seed.Raised[0].Input
		e.fell[key] = fallMemo{at: seed.Fell[0].At, input: *seed.Fell[0].Input, frontier: *seed.Fell[0].RecordedFrontier}
	}
	canonical := compatBytes(t, e)
	for _, source := range []string{"engine", "shard1", "shard4", "concurrent"} {
		b := compatBytes(t, compatRestore(t, source, rules, canonical))
		for _, target := range []string{"engine", "shard1", "shard4", "concurrent"} {
			got := compatBytes(t, compatRestore(t, target, rules, b))
			if !bytes.Equal(canonical, got) {
				t.Fatalf("%s -> %s changed snapshot", source, target)
			}
		}
	}
}

func TestSnapshotCompatMemoLifecycle(t *testing.T) {
	rules, key, now, raw := compatSnapshot(t)
	fresh := func() *Engine { return compatRestore(t, "engine", rules, raw).(*Engine) }
	for _, sweep := range []string{"matching", "rule", "series", "expected"} {
		t.Run(sweep, func(t *testing.T) {
			e := fresh()
			switch sweep {
			case "matching":
				e.RemoveMatching(func(string) bool { return true })
			case "rule":
				e.RemoveRule(key.Rule)
			case "series":
				e.DropSeries(key.Series, func(string, time.Time, bool) bool { return true })
			case "expected":
				e.RemoveExpected(key)
			}
			if len(e.fell) != 0 || len(e.raiseInputs) != 0 {
				t.Fatalf("unswept memo/inputs: %v %v", e.fell, e.raiseInputs)
			}
		})
	}
	t.Run("clear", func(t *testing.T) {
		e := fresh()
		e.ClearRaised(key)
		if len(e.raiseInputs) != 0 || len(e.raised) != 0 || len(e.fell) != 1 {
			t.Fatal("clear must keep prior fall only")
		}
		if n := e.LiveKeyCounts()["r"]; n != 1 {
			t.Fatalf("memo budget count = %d", n)
		}
		if keys := e.SeriesKeys(); len(keys) != 1 || keys[0] != key {
			t.Fatalf("memo enumeration: %v", keys)
		}
	})
	t.Run("descope", func(t *testing.T) {
		e := fresh()
		e.Descope(key.Rule, key.Series, now)
		m, ok := e.fell[key]
		if !ok || !m.at.Equal(now.Add(time.Microsecond)) || !m.input.Equal(now) || !m.frontier.Equal(e.Watermark()) {
			t.Fatalf("descope memo: %+v", m)
		}
	})
	for _, lazy := range []bool{false, true} {
		t.Run(fmt.Sprintf("expiry/lazy=%v", lazy), func(t *testing.T) {
			e := fresh()
			e.ClearRaised(key)
			e.Advance(now.Add(thresholdCursorHorizon))
			if lazy {
				e.ProcessEvent(Event{Seq: e.LastSeq() + 1, Key: key, Time: e.Watermark(), Match: false})
			} else {
				compatBytes(t, e)
			}
			if len(e.fell) != 1 {
				t.Fatal("expired at inclusive boundary")
			}
			e.Advance(now.Add(thresholdCursorHorizon + time.Nanosecond))
			if lazy {
				e.ProcessEvent(Event{Seq: e.LastSeq() + 1, Key: key, Time: e.Watermark(), Match: false})
			} else {
				compatBytes(t, e)
			}
			if len(e.fell) != 0 {
				t.Fatal("not expired strictly past boundary")
			}
		})
	}
	t.Run("ordinary-fall-then-same-instant-raise", func(t *testing.T) {
		e := NewEngine(rules, 0)
		for i, match := range []bool{true, false, true} {
			at := now
			if i == 0 {
				at = now.Add(-time.Second)
			}
			e.ProcessEvent(Event{Seq: uint64(i + 1), Key: key, Time: at, Match: match})
		}
		got := e.Drain()
		if len(got) != 3 || got[1].Edge != EdgeResolved || !got[1].At.Equal(now) || !got[2].At.Equal(now.Add(time.Microsecond)) {
			t.Fatalf("a same-instant raise must follow the fall by 1us: %+v", got)
		}
		m := e.fell[key]
		if !m.at.Equal(now) || !m.input.IsZero() || !m.frontier.Equal(e.Watermark()) || len(e.raiseInputs) != 1 || !e.raiseInputs[key].Equal(now) {
			t.Fatalf("fresh state: %+v, inputs %+v", m, e.raiseInputs)
		}
	})
}

// Explicit newest instants from a future writer override even a later inferred sample floor.
func TestSnapshotCompatExplicitNewest(t *testing.T) {
	rules, key, now, raw := compatSnapshot(t)
	rules[0].Kind = SlidingAgg
	rules[0].Window = time.Minute
	e := compatRestore(t, "engine", rules, raw).(*Engine)
	if floor, ok := e.newestFloor(key); !ok || !floor.Equal(now) {
		t.Fatalf("raise input floor = %v, %v", floor, ok)
	}
	e.restoreNewest([]snapNewest{{Rule: key.Rule, Series: key.Series, At: now.Add(-time.Second)}})
	canonical := compatBytes(t, e)
	for _, kind := range []string{"engine", "shard1", "shard4", "concurrent"} {
		d := compatRestore(t, kind, rules, canonical)
		if got := compatBytes(t, d); !bytes.Equal(got, canonical) {
			t.Fatalf("%s changed explicit instant below floor", kind)
		}
	}
}

// Retention starts at the recording watermark, not at a lagging device's decision time.
func TestSnapshotCompatFallRecordingFrontier(t *testing.T) {
	rules, key, now, _ := compatSnapshot(t)
	e := NewEngine(rules, 0)
	e.ProcessEvent(Event{Seq: 1, Key: key, Time: now.Add(-time.Second), Match: true})
	frontier := now.Add(time.Minute)
	e.ProcessResolved(2, frontier, []Event{{Key: key, Time: now, Match: false}})
	m := e.fell[key]
	if !m.at.Equal(now) || !m.input.IsZero() || !m.frontier.Equal(frontier) {
		t.Fatalf("lagging fall memo: %+v", m)
	}
	e.Advance(now.Add(thresholdCursorHorizon + time.Nanosecond))
	compatBytes(t, e)
	if len(e.fell) != 1 {
		t.Fatal("memo aged from device time instead of recording frontier")
	}
	e.Advance(frontier.Add(thresholdCursorHorizon + time.Nanosecond))
	e.ClearRaised(key)
	if len(e.fell) != 0 {
		t.Fatal("ClearRaised retained expired memo")
	}
}
