// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bytes"
	"testing"
	"time"
)

func TestCarryRaisedOwnersAndRestore(t *testing.T) {
	rules := []Rule{{ID: "old", Kind: Threshold}, {ID: "new", Kind: Threshold}, {ID: "other", Kind: Threshold}}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	single := NewEngine(rules, 0)
	sharded := NewSharded(4, rules, 0)
	concurrent := NewConcurrentSharded(4, rules, 0)
	defer concurrent.Close()
	engines := []Detector{single, sharded, concurrent}
	// These keys actually change owners, so insertion into the old shard is observable.
	series := "dev"
	if shardOf("old", series, 4, 0) == shardOf("new", series, 4, 0) {
		t.Fatal("fixture must cross shards")
	}
	old := SeriesKey{"old", series}
	next := SeriesKey{"new", series}
	for _, e := range engines {
		e.ProcessEvent(Event{Seq: 1, Key: old, Time: base, Match: true})
		e.ProcessEvent(Event{Seq: 2, Key: old, Time: base.Add(10 * time.Second), Match: true})
		e.ProcessEvent(Event{Seq: 3, Key: SeriesKey{"other", series}, Time: base.Add(11 * time.Second), Match: true})
		e.Drain()
		if n := e.CarryRaised(map[string]string{"old": "new"}, nil); n != 1 {
			t.Fatalf("moved %d, want 1", n)
		}
		if n := e.CarryRaised(map[string]string{"old": "new"}, nil); n != 0 {
			t.Fatalf("copied old latch: moved %d again", n)
		}
		e.ProcessEvent(Event{Seq: 4, Key: next, Time: base.Add(5 * time.Second), Match: false})
		if got := e.Drain(); len(got) != 0 {
			t.Fatalf("lost newest cursor: %+v", got)
		}
		// Exercise the live owner before restore can repair a misplaced entry.
		e.ProcessEvent(Event{Seq: 5, Key: next, Time: base.Add(12 * time.Second), Match: true})
		if got := e.Drain(); len(got) != 0 {
			t.Fatalf("new key's live owner did not receive latch: %+v", got)
		}
	}
	want, _ := single.Snapshot()
	for _, e := range engines {
		snap, err := e.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(snap, want) {
			t.Fatalf("carry differs by shard count: %s != %s", snap, want)
		}
		restored, err := RestoreSharded(4, rules, 0, snap)
		if err != nil {
			t.Fatal(err)
		}
		restored.ProcessEvent(Event{Seq: 6, Key: next, Time: base.Add(13 * time.Second), Match: true})
		if got := restored.Drain(); len(got) != 0 {
			t.Fatalf("carried latch lost on restart: %+v", got)
		}
		restored.ProcessEvent(Event{Seq: 7, Key: next, Time: base.Add(14 * time.Second), Match: false})
		got := restored.Drain()
		if len(got) != 1 || got[0].Edge != EdgeResolved {
			t.Fatalf("new owner cannot resolve: %+v", got)
		}
		restored.ProcessEvent(Event{Seq: 8, Key: SeriesKey{"other", series}, Time: base.Add(15 * time.Second), Match: false})
		if got := restored.Drain(); len(got) != 1 || got[0].Edge != EdgeResolved {
			t.Fatalf("unrelated latch changed: %+v", got)
		}
	}
}

func TestCarryRaisedKeepsSuccessor(t *testing.T) {
	for _, k := range []int{1, 4} {
		e := NewSharded(k, []Rule{{ID: "old", Kind: Threshold}, {ID: "new", Kind: Threshold}}, 0)
		a := e.owner(SeriesKey{"old", "dev"})
		b := e.owner(SeriesKey{"new", "dev"})
		old := SeriesKey{"old", "dev"}
		next := SeriesKey{"new", "dev"}
		at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		a.raised[old] = at
		a.newest[old] = at.Add(time.Second)
		b.raised[next] = at.Add(2 * time.Second)
		b.newest[next] = at.Add(3 * time.Second)
		if n := e.CarryRaised(map[string]string{"old": "new"}, nil); n != 1 {
			t.Fatalf("moved %d", n)
		}
		if _, ok := a.raised[old]; ok {
			t.Fatal("old latch was copied")
		}
		if _, ok := a.newest[old]; ok {
			t.Fatal("old cursor left behind")
		}
		if !b.raised[next].Equal(at.Add(2*time.Second)) || !b.newest[next].Equal(at.Add(3*time.Second)) {
			t.Fatal("overwrote successor state")
		}
	}
}

// Both source and destination edges must settle before a latch can move.
func TestCarryRaisedSkipsPendingSourceAndSuccessor(t *testing.T) {
	rules := []Rule{{ID: "old", Kind: Threshold}, {ID: "new", Kind: Threshold}}
	engines := []Detector{NewEngine(rules, 0), NewSharded(4, rules, 0), NewConcurrentSharded(4, rules, 0)}
	defer engines[2].(*ConcurrentSharded).Close()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, e := range engines {
		old := SeriesKey{Rule: "old", Series: "dev"}
		next := SeriesKey{Rule: "new", Series: "dev"}
		e.ProcessEvent(Event{Seq: 1, Key: old, Time: at, Match: true})
		e.Drain()
		for _, pending := range []SeriesKey{old, next} {
			n := e.CarryRaised(map[string]string{"old": "new"}, func(k SeriesKey) bool { return k == pending })
			if n != 0 || !e.IsRaised(old) || e.IsRaised(next) {
				t.Fatalf("pending %v did not exclude carry: moved=%d", pending, n)
			}
		}
		if n := e.CarryRaised(map[string]string{"old": "new"}, nil); n != 1 || e.IsRaised(old) || !e.IsRaised(next) {
			t.Fatalf("settled carry moved=%d", n)
		}
		e.ProcessEvent(Event{Seq: 2, Key: next, Time: at.Add(time.Second), Match: false})
		if e.IsRaised(next) {
			t.Fatal("successor query returned a stale latch")
		}
	}
}
