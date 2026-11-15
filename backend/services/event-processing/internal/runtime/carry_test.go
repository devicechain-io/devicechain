// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/detect/core"
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

// Startup has no deferred-pair records. Both edge hooks must settle all retained
// predecessors, while leaving other series, profiles, tenants and kinds alone.
func TestThresholdEdgesClearRetainedPredecessors(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(map[bool]string{false: "emitted", true: "published-in-replay"}[published], func(t *testing.T) {
			v1 := thrScoped("acme", "p@v1", "r")
			v2 := thrScoped("acme", "p@v2", "r")
			v3 := thrScoped("acme", "p@v3", "r")
			otherProfile := thrScoped("acme", "other@v1", "r")
			otherTenant := thrScoped("other", "p@v1", "r")
			changed := thrScoped("acme", "p@v1", "changed")
			changedNext := absScoped("acme", "p@v3", "changed")
			reg := NewRuleRegistry([]ScopedRule{v1, v2, v3, otherProfile, otherTenant, changed, changedNext})
			e := core.NewSharded(4, reg.Cores(), 0)
			d := NewDeadmanArmer(reg, e)
			actives := []ActiveEntry{{Tenant: "acme", ProfileToken: "p", ActiveVersionToken: "p@v3", PublishedAt: ts(20)}}
			d.LoadMembership(nil, actives)
			keys := []core.SeriesKey{
				{Rule: v1.Compiled.ID, Series: "dev"},
				{Rule: v2.Compiled.ID, Series: "dev"},
				{Rule: v1.Compiled.ID, Series: "untouched"},
				{Rule: otherProfile.Compiled.ID, Series: "dev"},
				{Rule: otherTenant.Compiled.ID, Series: "dev"},
				{Rule: changed.Compiled.ID, Series: "dev"},
			}
			for i, key := range keys {
				e.ProcessEvent(core.Event{Seq: uint64(i + 1), Key: key, Time: ts(i + 1), Match: true})
			}
			e.Drain()
			det := core.Detection{RuleID: v3.Compiled.ID, Series: "dev", Kind: core.Threshold, Edge: core.EdgeResolved}
			if published {
				d.ThresholdPublished(det, true)
			} else {
				d.ThresholdEmitted(det)
			}
			for i, key := range keys {
				if got, want := e.IsRaised(key), i >= 2; got != want {
					t.Errorf("%+v raised=%v, want %v", key, got, want)
				}
			}
			if got := e.Drain(); len(got) != 0 {
				t.Fatalf("predecessor cleanup emitted edges: %+v", got)
			}
			// Refresh on a live publish, and purge the inverse pairing on erasure.
			v4 := thrScoped("acme", "p@v4", "r")
			reg.Upsert(v4)
			e.UpsertRule(v4.Compiled.Core)
			d.ApplyActiveVersion(ActiveEntry{Tenant: "acme", ProfileToken: "p", ActiveVersionToken: "p@v4", PublishedAt: ts(30)}, func(core.SeriesKey) bool { return true })
			d.ThresholdEmitted(core.Detection{RuleID: v4.Compiled.ID, Series: "untouched", Kind: core.Threshold, Edge: core.EdgeRaised})
			if e.IsRaised(keys[2]) {
				t.Fatal("live pairing refresh did not settle the skipped-version latch")
			}
			d.RemoveTenant("acme")
			e.ProcessEvent(core.Event{Seq: 7, Key: keys[0], Time: ts(31), Match: true})
			d.ThresholdEmitted(core.Detection{RuleID: v4.Compiled.ID, Series: "dev", Kind: core.Threshold, Edge: core.EdgeResolved})
			if !e.IsRaised(keys[0]) {
				t.Fatal("erased tenant retained its predecessor pairing")
			}
		})
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
