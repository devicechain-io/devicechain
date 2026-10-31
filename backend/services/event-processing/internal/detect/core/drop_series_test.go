// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import "testing"

// A deleted device's token is reusable, and the series IS the token. Without DropSeries the old
// device's raised latch absorbs the recreated device's first matching reading: no edge, no alarm.
func TestDropSeriesLetsARecreatedSeriesRaiseAgain(t *testing.T) {
	rules := []Rule{{ID: "t/r", Kind: Threshold, Op: LT, Thresh: 20}}
	for name, mk := range map[string]func() Detector{
		"engine":  func() Detector { return NewEngine(rules, 0) },
		"sharded": func() Detector { return NewSharded(4, rules, 0) },
	} {
		t.Run(name, func(t *testing.T) {
			d := mk()
			feed := func(seq uint64, sec int, series string) []Detection {
				d.ProcessEvent(Event{Seq: seq, Key: SeriesKey{Rule: "t/r", Series: series}, Time: at(sec), Value: 12, HasValue: true, Match: true})
				return d.Drain()
			}
			if got := feed(1, 0, "cc-0002"); len(got) != 1 || got[0].Edge != EdgeRaised {
				t.Fatalf("first reading must raise: %+v", got)
			}
			if got := feed(2, 1, "cc-0002"); len(got) != 0 {
				t.Fatalf("a steady breach must not re-raise: %+v", got)
			}
			feed(3, 2, "other")

			if n := d.DropSeries("cc-0002", func(string) bool { return true }); n == 0 {
				t.Fatal("dropping a raised series must report state removed")
			}
			if got := d.Drain(); len(got) != 0 {
				t.Fatalf("a drop emits no Resolved (the alarm went with the device): %+v", got)
			}
			if got := feed(4, 3, "cc-0002"); len(got) != 1 || got[0].Edge != EdgeRaised {
				t.Fatalf("the recreated device's first matching reading must raise: %+v", got)
			}
			// Another device on the same rule keeps its latch.
			if got := feed(5, 4, "other"); len(got) != 0 {
				t.Fatalf("a drop for one series must not touch another: %+v", got)
			}
			// A predicate that selects no rule drops nothing.
			if n := d.DropSeries("cc-0002", func(string) bool { return false }); n != 0 {
				t.Fatalf("unselected rules must be left alone, dropped %d", n)
			}
		})
	}
}
