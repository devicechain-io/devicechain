// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"strconv"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
)

// geoBase is the shared occurred time of the geofence fan-out tests.
var geoBase = time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

// located builds a resolved LOCATION event stamped with a fence-set version, carrying one fix per
// (lon, lat) pair.
func located(device, profileVersion string, fenceSetVersion int32, occurred time.Time, fixes ...[2]float64) *dmmodel.ResolvedEvent {
	entries := make([]dmmodel.ResolvedLocationEntry, 0, len(fixes))
	for _, f := range fixes {
		lon := strconv.FormatFloat(f[0], 'f', -1, 64)
		lat := strconv.FormatFloat(f[1], 'f', -1, 64)
		entries = append(entries, dmmodel.ResolvedLocationEntry{Latitude: &lat, Longitude: &lon})
	}
	return &dmmodel.ResolvedEvent{
		SourceDeviceToken:   device,
		ProfileVersionToken: profileVersion,
		OccurredTime:        occurred,
		EventType:           esmodel.Location,
		FenceSetVersion:     fenceSetVersion,
		Payload:             &dmmodel.ResolvedLocationsPayload{Entries: entries},
	}
}

// fenceRuleReg is a registry with one threshold rule whose raw-CEL leaf is pure containment.
func fenceRuleReg(t *testing.T, tenant, profileVersion, fenceToken string) *RuleRegistry {
	t.Helper()
	r := rules.Rule{
		ID:   ComposeRuleID(tenant, "inyard"),
		Name: "in the yard",
		Type: rules.TypeThreshold,
		When: rules.Condition{CEL: `geo.inFence("` + fenceToken + `")`},
	}
	return NewRuleRegistry([]ScopedRule{compileScoped(t, tenant, profileVersion, r)})
}

// planThroughView is the exact resolution the live processor performs: the fence set is looked up
// from the loop-owned view using the event's OWN tenant and the version stamped on the event.
func planThroughView(reg *RuleRegistry, v *FenceSetView, seq uint64, tenant string, ev *dmmodel.ResolvedEvent) PlanResult {
	return reg.Plan(seq, tenant, ev, ev.OccurredTime, nil, v.ForEvent(tenant, ev))
}

// TestPlanEvaluatesAgainstTheSTAMPEDFenceSet is the determinism claim of the whole arc, and the
// only test that holds it.
//
// The yard is moved: version 7 has it at [0,1]², version 9 has it at [10,11]². A device standing
// still at (0.5, 0.5) sends one event BEFORE the move (stamped 7) and one AFTER (stamped 9). The
// first must still be INSIDE — evaluated against the geometry that was live when it happened —
// and the second must be OUTSIDE. Both are planned after version 9 exists, which is what makes
// this a replay-correctness test rather than a sequencing accident.
//
// Without it, an implementation that reads the tenant's CURRENT fence set passes every other test
// in this file.
func TestPlanEvaluatesAgainstTheSTAMPEDFenceSet(t *testing.T) {
	v := NewFenceSetView()
	v.Put("acme", boxFence(7, "yard", 0, 0, 1, 1))
	v.Put("acme", boxFence(9, "yard", 10, 10, 11, 11))
	reg := fenceRuleReg(t, "acme", "p@1", "yard")

	before := located("d1", "p@1", 7, geoBase, [2]float64{0.5, 0.5})
	after := located("d1", "p@1", 9, geoBase.Add(time.Minute), [2]float64{0.5, 0.5})

	oldRes := planThroughView(reg, v, 1, "acme", before)
	if len(oldRes.Events) != 1 {
		t.Fatalf("the pre-move event fed %d rule events, want 1 (eval errors: %d)", len(oldRes.Events), oldRes.EvalErrors)
	}
	if !oldRes.Events[0].Match {
		t.Error("an event stamped with version 7 did not match the version-7 geometry; it was evaluated " +
			"against the CURRENT fence set, which is the determinism break this arc exists to prevent")
	}

	newRes := planThroughView(reg, v, 2, "acme", after)
	if len(newRes.Events) != 1 {
		t.Fatalf("the post-move event fed %d rule events, want 1 (eval errors: %d)", len(newRes.Events), newRes.EvalErrors)
	}
	if newRes.Events[0].Match {
		t.Error("an event stamped with version 9 matched the version-9 geometry at a position outside it; " +
			"the stamp is being ignored in the other direction")
	}
}

// TestPlanPositionScopedFeedSkipsPositionlessEvents: a fence rule is fed ONLY events that report a
// position. A measurement event must feed it nothing — not a false leaf (which for a Duration rule
// cancels an in-flight hold) and not an eval error (which would bury the "a rule is broken" signal
// under routine telemetry).
//
// The location event in the same test is the positive control: without it, "no events" would pass
// against a registry that never matched anything.
func TestPlanPositionScopedFeedSkipsPositionlessEvents(t *testing.T) {
	v := NewFenceSetView()
	v.Put("acme", boxFence(3, "yard", 0, 0, 1, 1))
	reg := fenceRuleReg(t, "acme", "p@1", "yard")

	loc := planThroughView(reg, v, 1, "acme", located("d1", "p@1", 3, geoBase, [2]float64{0.5, 0.5}))
	if len(loc.Events) != 1 || !loc.Events[0].Match {
		t.Fatalf("control: a location event inside the fence did not feed a matching event (%d events, errors %d)",
			len(loc.Events), loc.EvalErrors)
	}

	// A measurement event carries no position, and is stamped with fence-set version 0 by design.
	meas := measured("acme", "d1", "p@1", geoBase.Add(time.Minute), map[string]string{"battery": "50"})
	res := planThroughView(reg, v, 2, "acme", meas)
	if len(res.Events) != 0 {
		t.Errorf("a measurement event fed the fence rule %d events; a positionless event must be skipped", len(res.Events))
	}
	if res.EvalErrors != 0 {
		t.Errorf("a measurement event produced %d eval errors on the fence rule; it must be skipped BEFORE "+
			"evaluation, not error on every reading the fleet sends", res.EvalErrors)
	}
}

// TestPlanRequiresPositionIsSetOnlyForFenceRules is the counterweight: the position scope must not
// leak onto rules that have nothing to do with fences, or one new gate would silently starve the
// existing rule set.
func TestPlanRequiresPositionIsSetOnlyForFenceRules(t *testing.T) {
	thr := rules.Rule{
		ID:   ComposeRuleID("acme", "hot"),
		Name: "hot",
		Type: rules.TypeThreshold,
		When: rules.Condition{Metric: "temperature", Op: rules.OpGt, Threshold: fptr(80)},
	}
	reg := NewRuleRegistry([]ScopedRule{compileScoped(t, "acme", "p@1", thr)})
	v := NewFenceSetView()

	res := planThroughView(reg, v, 1, "acme",
		measured("acme", "d1", "p@1", geoBase, map[string]string{"temperature": "90"}))
	if len(res.Events) != 1 || !res.Events[0].Match {
		t.Fatalf("a non-fence rule stopped being fed positionless events: %d events, errors %d",
			len(res.Events), res.EvalErrors)
	}
}

// TestPlanFansEveryLocationFixInABatch: a store-and-forward tracker uploads a run of buffered fixes
// as one message. Each fix is its own sample, so a device that entered and left the fence between
// two uploads still trips it — the same reason a measurement batch fans per sample.
func TestPlanFansEveryLocationFixInABatch(t *testing.T) {
	v := NewFenceSetView()
	v.Put("acme", boxFence(2, "yard", 0, 0, 1, 1))
	reg := fenceRuleReg(t, "acme", "p@1", "yard")

	ev := located("d1", "p@1", 2, geoBase,
		[2]float64{5, 5},     // outside
		[2]float64{0.5, 0.5}, // inside — would be lost by a last-value-wins fold
		[2]float64{9, 9},     // outside again
	)
	res := planThroughView(reg, v, 1, "acme", ev)
	if len(res.Events) != 3 {
		t.Fatalf("a 3-fix batch fanned %d events, want 3", len(res.Events))
	}
	if res.Events[0].Match || !res.Events[1].Match || res.Events[2].Match {
		t.Errorf("fix-by-fix matches were %v/%v/%v, want false/true/false",
			res.Events[0].Match, res.Events[1].Match, res.Events[2].Match)
	}
}

// TestPlanWithNoFencesInScope: a tenant that has never authored a fence stamps version 0, whose
// fence set is KNOWN and empty. A rule naming a fence then gets "no such fence" — an eval error,
// so the sample is skipped and counted, and the rule is visibly broken rather than quietly quiet.
//
// The second half is the positive control: the same rule, same device, same position, against a
// version whose set holds the fence, fires. So the first half is a statement about the fence set,
// not about a rule that never ran.
func TestPlanWithNoFencesInScope(t *testing.T) {
	v := NewFenceSetView()
	reg := fenceRuleReg(t, "acme", "p@1", "yard")

	none := planThroughView(reg, v, 1, "acme", located("d1", "p@1", 0, geoBase, [2]float64{0.5, 0.5}))
	if len(none.Events) != 0 {
		t.Errorf("a rule naming a fence that never existed fed %d events", len(none.Events))
	}
	if none.EvalErrors != 1 {
		t.Errorf("eval errors = %d, want 1: naming a fence that did not exist must be visible, not silent",
			none.EvalErrors)
	}

	v.Put("acme", boxFence(1, "yard", 0, 0, 1, 1))
	some := planThroughView(reg, v, 2, "acme", located("d1", "p@1", 1, geoBase, [2]float64{0.5, 0.5}))
	if len(some.Events) != 1 || !some.Events[0].Match || some.EvalErrors != 0 {
		t.Fatalf("control: with the fence present the rule did not fire (%d events, errors %d)",
			len(some.Events), some.EvalErrors)
	}
}

// TestPlanUnresolvableFenceSetIsAnErrorNotAMiss: the view has evicted (or never held) the stamped
// version. The sample is skipped and COUNTED — the operator-visible signal that the retention
// bound bit — rather than answered "outside", which would be a wrong answer wearing a normal face.
func TestPlanUnresolvableFenceSetIsAnErrorNotAMiss(t *testing.T) {
	v := NewFenceSetViewRetaining(1)
	v.Put("acme", boxFence(1, "yard", 0, 0, 1, 1))
	v.Put("acme", boxFence(2, "yard", 0, 0, 1, 1)) // evicts version 1
	reg := fenceRuleReg(t, "acme", "p@1", "yard")

	res := planThroughView(reg, v, 1, "acme", located("d1", "p@1", 1, geoBase, [2]float64{0.5, 0.5}))
	if len(res.Events) != 0 {
		t.Errorf("an unresolvable fence set fed %d events", len(res.Events))
	}
	if res.EvalErrors != 1 {
		t.Errorf("eval errors = %d, want 1", res.EvalErrors)
	}
	// Control: the retained version still answers.
	ok := planThroughView(reg, v, 2, "acme", located("d1", "p@1", 2, geoBase, [2]float64{0.5, 0.5}))
	if len(ok.Events) != 1 || !ok.Events[0].Match {
		t.Fatalf("control: the retained version did not answer (%d events, errors %d)", len(ok.Events), ok.EvalErrors)
	}
}

// TestPlanFencesAreTenantIsolated: two tenants each own a fence token "yard", at opposite ends of
// the world, at the SAME version number (versions are per-tenant and collide by construction). A
// device of one tenant standing inside its own yard must match, and a device of the other tenant
// standing at the same coordinates must not — which is only possible if the fence set was resolved
// from the event's own tenant.
func TestPlanFencesAreTenantIsolated(t *testing.T) {
	v := NewFenceSetView()
	v.Put("acme", boxFence(4, "yard", 0, 0, 1, 1))
	v.Put("globex", boxFence(4, "yard", 100, 40, 101, 41))

	acmeReg := fenceRuleReg(t, "acme", "p@1", "yard")
	globexReg := fenceRuleReg(t, "globex", "p@1", "yard")

	// Both devices sit at (0.5, 0.5): inside ACME's yard, nowhere near globex's.
	acmeRes := planThroughView(acmeReg, v, 1, "acme", located("d1", "p@1", 4, geoBase, [2]float64{0.5, 0.5}))
	if len(acmeRes.Events) != 1 || !acmeRes.Events[0].Match {
		t.Fatalf("acme's device inside acme's yard did not match (%d events, errors %d)",
			len(acmeRes.Events), acmeRes.EvalErrors)
	}
	globexRes := planThroughView(globexReg, v, 2, "globex", located("d9", "p@1", 4, geoBase, [2]float64{0.5, 0.5}))
	if len(globexRes.Events) != 1 {
		t.Fatalf("globex's event fed %d rule events, want 1 (errors %d)", len(globexRes.Events), globexRes.EvalErrors)
	}
	if globexRes.Events[0].Match {
		t.Error("globex's device matched at coordinates that are inside ACME's yard; one tenant's fence " +
			"geometry was reachable from another tenant's evaluation")
	}
	// And the mirror, so neither direction is asserted by accident.
	globexHome := planThroughView(globexReg, v, 3, "globex", located("d9", "p@1", 4, geoBase, [2]float64{100.5, 40.5}))
	if len(globexHome.Events) != 1 || !globexHome.Events[0].Match {
		t.Fatalf("globex's device inside globex's own yard did not match (%d events, errors %d)",
			len(globexHome.Events), globexHome.EvalErrors)
	}
}

// TestLocationEventWithUnparseablePositionIsSkipped: a fix missing a coordinate names no point.
// It must not become a position at (0, 0) — which is in the Gulf of Guinea and inside any fence
// authored around the origin — and it must not stop the fixes beside it in the same batch.
func TestLocationEventWithUnparseablePositionIsSkipped(t *testing.T) {
	v := NewFenceSetView()
	v.Put("acme", boxFence(1, "origin", -1, -1, 1, 1))
	reg := fenceRuleReg(t, "acme", "p@1", "origin")

	lat := "0.5"
	ev := &dmmodel.ResolvedEvent{
		SourceDeviceToken:   "d1",
		ProfileVersionToken: "p@1",
		OccurredTime:        geoBase,
		EventType:           esmodel.Location,
		FenceSetVersion:     1,
		Payload: &dmmodel.ResolvedLocationsPayload{Entries: []dmmodel.ResolvedLocationEntry{
			{Latitude: &lat}, // no longitude: no point
			{Latitude: &lat, Longitude: strptr("0.5")}, // a real fix, inside
		}},
	}
	res := planThroughView(reg, v, 1, "acme", ev)
	if len(res.Events) != 1 {
		t.Fatalf("fanned %d events, want 1 (the coordinate-less fix is skipped, the real one is not)", len(res.Events))
	}
	if !res.Events[0].Match {
		t.Error("the real fix did not match")
	}
	if res.EvalErrors != 0 {
		t.Errorf("eval errors = %d; a fix with no coordinates is not an error, it is not a position", res.EvalErrors)
	}
}

// TestLocationInputsStayHeartbeats: a location sample carries a position and NO measurement, so it
// still matches no metric-gated rule and still counts as a heartbeat for an absence rule. Giving
// location events a payload a rule can read must not turn them into metric-bearing samples.
//
// It also pins the one behaviour change this fan-out makes to a pre-existing path: a location event
// with N fixes now yields N inputs where it previously yielded one heartbeat. That is deliberate
// (each fix is a sample, exactly as each measurement entry is), and it is stated here because it
// changes what a count aggregate over location events counts — fixes, not messages.
func TestLocationInputsStayHeartbeats(t *testing.T) {
	one := BuildInputs(located("d1", "p@1", 1, geoBase, [2]float64{0.5, 0.5}), geoBase)
	if len(one) != 1 {
		t.Fatalf("a single-fix location event yielded %d inputs, want 1", len(one))
	}
	if one[0].M != nil {
		t.Errorf("a location input carries measurements: %v", one[0].M)
	}
	if one[0].Position == nil {
		t.Fatal("a location input carries no position")
	}

	three := BuildInputs(located("d1", "p@1", 1, geoBase,
		[2]float64{0, 0}, [2]float64{1, 1}, [2]float64{2, 2}), geoBase)
	if len(three) != 3 {
		t.Fatalf("a 3-fix location event yielded %d inputs, want 3", len(three))
	}
	for i, in := range three {
		if in.M != nil {
			t.Errorf("fix %d carries measurements: %v", i, in.M)
		}
		if !in.Occurred.Equal(geoBase) {
			t.Errorf("fix %d is stamped %v, want the message's clamped time %v", i, in.Occurred, geoBase)
		}
	}
}

func strptr(s string) *string { return &s }
