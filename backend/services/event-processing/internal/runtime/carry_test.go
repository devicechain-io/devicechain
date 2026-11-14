// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"testing"
)

func TestCarryPairsAllRetainedVersionsByKind(t *testing.T) {
	v1 := thrScoped("acme", "p@v1", "r")
	v2 := thrScoped("acme", "p@v2", "r")
	v3 := thrScoped("acme", "p@v3", "r")
	v3.Definition = `{"predicate":"edited", "actions":["different"], "severity":"critical"}`
	other := thrScoped("acme", "other@v1", "r")
	changed := absScoped("acme", "p@v1", "changed")
	changedNew := thrScoped("acme", "p@v3", "changed")
	malformed := thrScoped("acme", "p@v1", "bad")
	malformed.Compiled.ID = "unparseable"
	malformed.Compiled.Core.ID = "unparseable"
	reg := NewRuleRegistry([]ScopedRule{v1, v2, v3, other, changed, changedNew, malformed})
	e := core.NewSharded(4, reg.Cores(), 0)
	d := NewDeadmanArmer(reg, e)
	for i, sr := range []ScopedRule{v1, v2, other} {
		e.ProcessEvent(core.Event{Seq: uint64(i + 1), Key: core.SeriesKey{Rule: sr.Compiled.ID, Series: []string{"a", "b", "c"}[i]}, Time: ts(i + 1), Match: true})
	}
	e.Drain()
	d.ApplyDeviceMembership(RosterEntry{Tenant: "acme", DeviceToken: "a", ProfileToken: "p", ExpectedSince: ts(0)}, true)
	if n := d.ApplyActiveVersion(ActiveEntry{Tenant: "acme", ProfileToken: "p", ActiveVersionToken: "p@v3", PublishedAt: ts(20)}, nil); n != 2 {
		t.Fatalf("skipped-version carry moved %d, want 2", n)
	}
	for _, sr := range []ScopedRule{changed, malformed, other} {
		if _, ok := d.successors("acme", "p")[sr.Compiled.ID]; ok {
			t.Fatalf("incorrect pairing: %s", sr.Compiled.ID)
		}
	}
	if !d.CarriedForward(core.SeriesKey{Rule: v1.Compiled.ID, Series: "a"}) {
		t.Fatal("event-driven carry not recognized")
	}
	if d.CarriedForward(core.SeriesKey{Rule: changed.Compiled.ID, Series: "a"}) {
		t.Fatal("kind change recognized as carry")
	}
	for i, dev := range []string{"a", "b"} {
		e.ProcessEvent(core.Event{Seq: uint64(i + 4), Key: core.SeriesKey{Rule: v3.Compiled.ID, Series: dev}, Time: ts(21 + i), Match: true})
	}
	if got := e.Drain(); len(got) != 0 {
		t.Fatalf("skipped version was not carried: %+v", got)
	}
	e.ProcessEvent(core.Event{Seq: 6, Key: core.SeriesKey{Rule: other.Compiled.ID, Series: "c"}, Time: ts(24), Match: false})
	if got := e.Drain(); len(got) != 1 || got[0].Edge != core.EdgeResolved {
		t.Fatalf("different profile latch changed: %+v", got)
	}
}

func TestCarryPairsOnlyThresholdAndAbsence(t *testing.T) {
	for _, kind := range []core.RuleKind{core.Threshold, core.Absence, core.Repeating, core.Correlation, core.Connectivity, core.Duration, core.Session, core.Aggregate, core.DeltaRate, core.CountWindow, core.SlidingAgg} {
		old := thrScoped("acme", "p@v1", "r")
		next := thrScoped("acme", "p@v2", "r")
		old.Compiled.Core.Kind = kind
		next.Compiled.Core.Kind = kind
		reg := NewRuleRegistry([]ScopedRule{old, next})
		d := NewDeadmanArmer(reg, core.NewEngine(reg.Cores(), 0))
		d.LoadMembership([]RosterEntry{{Tenant: "acme", DeviceToken: "dev", ProfileToken: "p"}}, []ActiveEntry{{Tenant: "acme", ProfileToken: "p", ActiveVersionToken: "p@v2"}})
		want := kind == core.Threshold || kind == core.Absence
		_, paired := d.successors("acme", "p")[old.Compiled.ID]
		carried := d.CarriedForward(core.SeriesKey{Rule: old.Compiled.ID, Series: "dev"})
		if paired != want || carried != want {
			t.Errorf("kind %v: paired=%v carried=%v want=%v", kind, paired, carried, want)
		}
	}
}
