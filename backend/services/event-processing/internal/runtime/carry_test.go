// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-event-processing/internal/detect/core"
)

func TestPreActivationEvaluationBoundAndLifecycle(t *testing.T) {
	v1, v2 := thrScoped("acme", "p@v1", "r"), thrScoped("acme", "p@v2", "r")
	reg := NewRuleRegistry([]ScopedRule{v1, v2})
	e := core.NewSharded(4, reg.Cores(), 0)
	d := NewDeadmanArmer(reg, e)
	roster := []RosterEntry{{Tenant: "acme", DeviceToken: "dev", ProfileToken: "p", ExpectedSince: ts(0)}}
	active := []ActiveEntry{{Tenant: "acme", ProfileToken: "p", ActiveVersionToken: "p@v1", PublishedAt: ts(0)}}
	d.Reconcile(roster, active)
	for i := 0; i <= maxPreActivationEvaluations; i++ {
		d.SuccessorEvaluated("acme", "p@v1", fmt.Sprint(i))
		d.SuccessorEvaluated("acme", "p@unregistered", fmt.Sprint(i))
	}
	if len(d.preActivationEvaluations) != 0 || d.carryUncertain {
		t.Fatal("active and unregistered events must not allocate evaluation records")
	}
	d.SuccessorEvaluated("acme", "p@v2", "dev")
	d.SuccessorEvaluated("acme", "p@v2", "dev")
	if len(d.preActivationEvaluations) != 1 {
		t.Fatal("non-active evaluations must be deduplicated")
	}
	d.RemoveTenant("other")
	if len(d.preActivationEvaluations) != 1 {
		t.Fatal("tenant removal crossed tenant boundary")
	}
	d.RemoveTenant("acme")
	if len(d.preActivationEvaluations) != 0 {
		t.Fatal("tenant removal retained evaluations")
	}
	d.Reconcile(roster, active)
	d.SuccessorEvaluated("acme", "p@v2", "dev")
	d.ApplyActiveVersion(ActiveEntry{Tenant: "acme", ProfileToken: "p", ActiveVersionToken: "p@v2", PublishedAt: ts(2)}, nil)
	if len(d.preActivationEvaluations) != 0 {
		t.Fatal("activation must consume its version's records")
	}
	d.Reconcile(roster, active)
	for i := 0; i <= maxPreActivationEvaluations; i++ {
		d.SuccessorEvaluated("acme", "p@v2", fmt.Sprint(i))
	}
	if len(d.preActivationEvaluations) != 0 || !d.carryUncertain {
		t.Fatal("overflow must prune records and disable carry")
	}
	old := core.SeriesKey{Rule: v1.Compiled.ID, Series: "dev"}
	next := core.SeriesKey{Rule: v2.Compiled.ID, Series: "dev"}
	e.ProcessEvent(core.Event{Seq: 1, Key: old, Time: ts(1), Match: true})
	e.Drain()
	d.ApplyActiveVersion(ActiveEntry{Tenant: "acme", ProfileToken: "p", ActiveVersionToken: "p@v2", PublishedAt: ts(2)}, nil)
	if e.IsRaised(next) {
		t.Fatal("pruning lost information and allowed carry")
	}
	d.Reconcile(roster, active)
	if len(d.preActivationEvaluations) != 0 || d.carryUncertain {
		t.Fatal("Reconcile must reset transient evaluation state")
	}
}

func TestOldRecoveryCancelsOtherPredecessorsDeferredCarry(t *testing.T) {
	v0, v1, v2 := thrScoped("acme", "p@v0", "r"), thrScoped("acme", "p@v1", "r"), thrScoped("acme", "p@v2", "r")
	reg := NewRuleRegistry([]ScopedRule{v0, v1, v2})
	e := core.NewEngine(reg.Cores(), 0)
	d := NewDeadmanArmer(reg, e)
	d.LoadMembership([]RosterEntry{{Tenant: "acme", DeviceToken: "dev", ProfileToken: "p", ExpectedSince: ts(0)}},
		[]ActiveEntry{{Tenant: "acme", ProfileToken: "p", ActiveVersionToken: "p@v2", PublishedAt: ts(2)}})
	old := core.SeriesKey{Rule: v1.Compiled.ID, Series: "dev"}
	next := core.SeriesKey{Rule: v2.Compiled.ID, Series: "dev"}
	e.ProcessEvent(core.Event{Seq: 1, Key: old, Time: ts(1), Match: true})
	e.Drain()
	d.deferredThresholds = map[core.SeriesKey]core.SeriesKey{old: next}
	d.ThresholdPublished(core.Detection{RuleID: v0.Compiled.ID, Series: "dev", Kind: core.Threshold, Edge: core.EdgeResolved, At: ts(3)})
	if n := d.CarryReadyThresholds(nil); n != 0 || e.IsRaised(next) {
		t.Fatal("an acknowledged shared recovery must not be undone by another predecessor's deferred carry")
	}
}

func TestOldRecoveryClearsSuccessorOnlyWhenAcknowledged(t *testing.T) {
	v1, v2 := thrScoped("acme", "p@v1", "r"), thrScoped("acme", "p@v2", "r")
	// Cleanup must also handle partially overlapping alarm destinations; they
	// are deliberately ineligible for carry and redundant-raise suppression.
	v1.Compiled.AlarmCarryActions = []string{"alarm-a"}
	v2.Compiled.AlarmCarryActions = []string{"alarm-a", "alarm-b"}
	reg := NewRuleRegistry([]ScopedRule{v1, v2})
	e := core.NewEngine(reg.Cores(), 0)
	d := NewDeadmanArmer(reg, e)
	d.LoadMembership([]RosterEntry{{Tenant: "acme", DeviceToken: "dev", ProfileToken: "p", ExpectedSince: ts(0)}},
		[]ActiveEntry{{Tenant: "acme", ProfileToken: "p", ActiveVersionToken: "p@v2", PublishedAt: ts(1)}})
	next := core.SeriesKey{Rule: v2.Compiled.ID, Series: "dev"}
	e.ProcessEvent(core.Event{Seq: 1, Key: next, Time: ts(5), Match: true})
	e.Drain()
	det := core.Detection{RuleID: v1.Compiled.ID, Series: "dev", Kind: core.Threshold, Edge: core.EdgeResolved, At: ts(2)}
	d.ThresholdEmitted(det)
	if !e.IsRaised(next) {
		t.Fatal("an unacknowledged or dropped old recovery cleared the successor")
	}
	d.ThresholdPublished(det)
	if e.IsRaised(next) || len(e.Drain()) != 0 {
		t.Fatal("acknowledged old recovery must clear even a newer successor without emitting")
	}
}

// Retained rules and latches alone do not establish an observed transition.
func TestCarryWithoutPreviousActiveVersionAllowsFreshRaise(t *testing.T) {
	old := thrScoped("acme", "p@v1", "r")
	next := thrScoped("acme", "p@v2", "r")
	reg := NewRuleRegistry([]ScopedRule{old, next})
	e := core.NewEngine(reg.Cores(), 0)
	d := NewDeadmanArmer(reg, e)
	d.ApplyDeviceMembership(RosterEntry{Tenant: "acme", DeviceToken: "dev", ProfileToken: "p", ExpectedSince: ts(0)}, true)
	key := core.SeriesKey{Rule: old.Compiled.ID, Series: "dev"}
	e.ProcessEvent(core.Event{Seq: 1, Key: key, Time: ts(1), Match: true})
	e.Drain()
	if n := d.ApplyActiveVersion(ActiveEntry{Tenant: "acme", ProfileToken: "p", ActiveVersionToken: "p@v2", PublishedAt: ts(2)}, nil); n != 0 {
		t.Fatalf("carry without a previous active version moved %d latches", n)
	}
	e.ProcessEvent(core.Event{Seq: 2, Key: core.SeriesKey{Rule: next.Compiled.ID, Series: "dev"}, Time: ts(3), Match: true})
	if got := e.Drain(); len(got) != 1 || got[0].Edge != core.EdgeRaised || got[0].RuleID != next.Compiled.ID {
		t.Fatalf("unknown continuity must allow a fresh successor raise: %+v", got)
	}
}

func TestCarryPairsOnlyPreviousActiveVersionByKind(t *testing.T) {
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
	d.LoadMembership(nil, []ActiveEntry{{Tenant: "acme", ProfileToken: "p", ActiveVersionToken: "p@v2"}})
	for i, sr := range []ScopedRule{v1, v2, other} {
		e.ProcessEvent(core.Event{Seq: uint64(i + 1), Key: core.SeriesKey{Rule: sr.Compiled.ID, Series: []string{"a", "b", "c"}[i]}, Time: ts(i + 1), Match: true})
	}
	e.Drain()
	d.ApplyDeviceMembership(RosterEntry{Tenant: "acme", DeviceToken: "a", ProfileToken: "p", ExpectedSince: ts(0)}, true)
	d.ApplyDeviceMembership(RosterEntry{Tenant: "acme", DeviceToken: "b", ProfileToken: "p", ExpectedSince: ts(0)}, true)
	if n := d.ApplyActiveVersion(ActiveEntry{Tenant: "acme", ProfileToken: "p", ActiveVersionToken: "p@v3", PublishedAt: ts(20)}, nil); n != 1 {
		t.Fatalf("previous-version carry moved %d, want 1", n)
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
	if got := e.Drain(); len(got) != 1 || got[0].Edge != core.EdgeRaised || got[0].Series != "a" {
		t.Fatalf("only the previous version may suppress a fresh raise: %+v", got)
	}
	e.ProcessEvent(core.Event{Seq: 6, Key: core.SeriesKey{Rule: other.Compiled.ID, Series: "c"}, Time: ts(24), Match: false})
	if got := e.Drain(); len(got) != 1 || got[0].Edge != core.EdgeResolved {
		t.Fatalf("different profile latch changed: %+v", got)
	}
}

// Startup has no deferred-pair records. Emission must settle retained
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
			det := core.Detection{RuleID: v3.Compiled.ID, Series: "dev", Kind: core.Threshold, Edge: core.EdgeResolved, At: ts(20)}
			if published {
				d.ThresholdPublished(det)
			} else {
				d.ThresholdEmitted(det)
			}
			for i, key := range keys {
				if got, want := e.IsRaised(key), published || i >= 2; got != want {
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
			d.ThresholdEmitted(core.Detection{RuleID: v4.Compiled.ID, Series: "untouched", Kind: core.Threshold, Edge: core.EdgeRaised, At: ts(30)})
			if e.IsRaised(keys[2]) {
				t.Fatal("live pairing refresh did not settle the skipped-version latch")
			}
			d.RemoveTenant("acme")
			e.ProcessEvent(core.Event{Seq: 7, Key: keys[0], Time: ts(31), Match: true})
			d.ThresholdEmitted(core.Detection{RuleID: v4.Compiled.ID, Series: "dev", Kind: core.Threshold, Edge: core.EdgeResolved, At: ts(40)})
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
