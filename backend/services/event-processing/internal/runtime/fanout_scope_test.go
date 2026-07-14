// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
)

// compileScopedGroup compiles a rule and wraps it as a ScopedRule pinned to a group@version.
func compileScopedGroup(t *testing.T, tenant, profileVersion string, r rules.Rule, groupToken string, groupVersion int32) ScopedRule {
	t.Helper()
	sr := compileScoped(t, tenant, profileVersion, r)
	sr.GroupToken = groupToken
	sr.GroupVersion = groupVersion
	return sr
}

// withMemberships stamps a resolved event's scope memberships (as the resolver would, S3).
func withMemberships(ev *dmmodel.ResolvedEvent, refs ...dmmodel.GroupRef) *dmmodel.ResolvedEvent {
	ev.ScopeMemberships = refs
	return ev
}

func scopedThresholdRule(id string) rules.Rule {
	return rules.Rule{
		ID:   ComposeRuleID("acme", id),
		Name: "over",
		Type: rules.TypeThreshold,
		When: rules.Condition{Metric: "temperature", Op: rules.OpGt, Threshold: fptr(80)},
	}
}

// A group-scoped rule fires for an event whose ScopeMemberships include its pinned group@v.
func TestPlanScopedRuleFiresForMember(t *testing.T) {
	base := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	reg := NewRuleRegistry([]ScopedRule{
		compileScopedGroup(t, "acme", "p@1", scopedThresholdRule("hot"), "arid-areas", 3),
	})
	ev := withMemberships(
		measured("acme", "d1", "p@1", base, map[string]string{"temperature": "90"}),
		dmmodel.GroupRef{GroupToken: "arid-areas", Version: 3},
	)
	res := planEv(reg, 1, "acme", ev)
	if len(res.Events) != 1 || !res.Events[0].Match {
		t.Fatalf("a member device must feed the scoped rule; got %+v", res.Events)
	}
	if len(res.Descopes) != 0 {
		t.Fatalf("a member device must not descope; got %+v", res.Descopes)
	}
}

// A group-scoped rule does NOT fire for an event lacking its group@v — instead it emits a
// descope op (so the engine drops the series' state + resolves any raised alarm).
func TestPlanScopedRuleDescopesNonMember(t *testing.T) {
	base := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	rule := scopedThresholdRule("hot")
	reg := NewRuleRegistry([]ScopedRule{
		compileScopedGroup(t, "acme", "p@1", rule, "arid-areas", 3),
	})
	// Event carries a DIFFERENT group membership (or none for this rule's group).
	ev := withMemberships(
		measured("acme", "d1", "p@1", base, map[string]string{"temperature": "90"}),
		dmmodel.GroupRef{GroupToken: "humid-areas", Version: 1},
	)
	res := planEv(reg, 1, "acme", ev)
	if len(res.Events) != 0 {
		t.Fatalf("a non-member device must not feed the scoped rule; got %+v", res.Events)
	}
	if len(res.Descopes) != 1 {
		t.Fatalf("a non-member device must produce one descope; got %+v", res.Descopes)
	}
	d := res.Descopes[0]
	if d.RuleID != ComposeRuleID("acme", "hot") || d.Series != "d1" || !d.At.Equal(base) {
		t.Fatalf("descope must name the rule, the device series, and the event time; got %+v", d)
	}
}

// A scoped rule whose group is stamped at a DIFFERENT version is out of scope (the pin is
// version-exact — a group re-publish does not silently re-scope a rule).
func TestPlanScopedRuleVersionExact(t *testing.T) {
	base := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	reg := NewRuleRegistry([]ScopedRule{
		compileScopedGroup(t, "acme", "p@1", scopedThresholdRule("hot"), "arid-areas", 3),
	})
	ev := withMemberships(
		measured("acme", "d1", "p@1", base, map[string]string{"temperature": "90"}),
		dmmodel.GroupRef{GroupToken: "arid-areas", Version: 2}, // wrong version
	)
	res := planEv(reg, 1, "acme", ev)
	if len(res.Events) != 0 || len(res.Descopes) != 1 {
		t.Fatalf("a mismatched group VERSION must be out of scope; events=%+v descopes=%+v", res.Events, res.Descopes)
	}
}

// An UNSCOPED rule fires regardless of memberships (the profile-wide default) and never
// descopes — and a profile of only unscoped rules skips the membership machinery entirely.
func TestPlanUnscopedRuleUnaffectedByMemberships(t *testing.T) {
	base := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	reg := NewRuleRegistry([]ScopedRule{compileScoped(t, "acme", "p@1", scopedThresholdRule("hot"))})
	// Even with an unrelated membership stamped, the unscoped rule fires.
	ev := withMemberships(
		measured("acme", "d1", "p@1", base, map[string]string{"temperature": "90"}),
		dmmodel.GroupRef{GroupToken: "arid-areas", Version: 3},
	)
	res := planEv(reg, 1, "acme", ev)
	if len(res.Events) != 1 || !res.Events[0].Match {
		t.Fatalf("an unscoped rule must fire regardless of memberships; got %+v", res.Events)
	}
	if len(res.Descopes) != 0 {
		t.Fatalf("an unscoped rule never descopes; got %+v", res.Descopes)
	}
}

// A mixed profile (one scoped + one unscoped rule): the member gets both, and the unscoped one
// still fires while the scoped one descopes when out of scope.
func TestPlanMixedScopedAndUnscoped(t *testing.T) {
	base := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	reg := NewRuleRegistry([]ScopedRule{
		compileScoped(t, "acme", "p@1", scopedThresholdRule("wide")),
		compileScopedGroup(t, "acme", "p@1", scopedThresholdRule("narrow"), "arid-areas", 3),
	})
	// Out of scope for "narrow": only "wide" fires, "narrow" descopes.
	ev := measured("acme", "d1", "p@1", base, map[string]string{"temperature": "90"})
	res := planEv(reg, 1, "acme", ev)
	if len(res.Events) != 1 || res.Events[0].Key.Rule != ComposeRuleID("acme", "wide") {
		t.Fatalf("only the unscoped rule should fire for a non-member; got %+v", res.Events)
	}
	if len(res.Descopes) != 1 || res.Descopes[0].RuleID != ComposeRuleID("acme", "narrow") {
		t.Fatalf("the scoped rule should descope for a non-member; got %+v", res.Descopes)
	}
}
