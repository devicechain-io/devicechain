// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
)

func fptr(f float64) *float64 { return &f }

// planEv fans an event out using its own occurred time as the (already-clamped) event time.
func planEv(reg *RuleRegistry, seq uint64, tenant string, ev *dmmodel.ResolvedEvent) PlanResult {
	return reg.Plan(seq, tenant, ev, ev.OccurredTime)
}

// measured builds a resolved measurement event for a device under a profile version,
// carrying the given metric→value readings.
func measured(tenant, device, profileVersion string, occurred time.Time, m map[string]string) *dmmodel.ResolvedEvent {
	entries := make([]dmmodel.ResolvedMeasurementEntry, 0, len(m))
	for name, val := range m {
		entries = append(entries, dmmodel.ResolvedMeasurementEntry{Name: name, Value: val})
	}
	return &dmmodel.ResolvedEvent{
		SourceDeviceToken:   device,
		ProfileVersionToken: profileVersion,
		OccurredTime:        occurred,
		Payload:             &dmmodel.ResolvedMeasurementsPayload{Entries: []dmmodel.ResolvedMeasurementsEntry{{Entries: entries}}},
	}
}

// compileScoped compiles a rule and wraps it as a ScopedRule for the given tenant/version.
func compileScoped(t *testing.T, tenant, profileVersion string, r rules.Rule) ScopedRule {
	t.Helper()
	cr, err := rules.Compile(r, rules.Limits{})
	if err != nil {
		t.Fatalf("compile rule %q: %v", r.ID, err)
	}
	return ScopedRule{Tenant: tenant, ProfileVersionToken: profileVersion, Compiled: cr}
}

// A duration rule keyed on "temperature" must NOT be fed an event that carries only another
// metric — the metric-scoped feed contract. Feeding it would evaluate the leaf false and (for
// duration alone) cancel the running hold; the fix is that Plan skips the rule entirely.
func TestPlanMetricScopedFeedSkipsUnrelatedMetric(t *testing.T) {
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	dur := rules.Rule{
		ID:   ComposeRuleID("acme", "dur1"),
		Name: "hot too long",
		Type: rules.TypeDuration,
		When: rules.Condition{Metric: "temperature", Op: rules.OpGt, Threshold: fptr(80)},
		Hold: rules.Duration(2 * time.Minute),
	}
	reg := NewRuleRegistry([]ScopedRule{compileScoped(t, "acme", "p@1", dur)})

	// An event carrying temperature IS in scope and fed.
	hot := planEv(reg, 1, "acme", measured("acme", "d1", "p@1", base, map[string]string{"temperature": "90"}))
	if len(hot.Events) != 1 {
		t.Fatalf("temperature event: got %d fanned events, want 1", len(hot.Events))
	}
	if !hot.Events[0].Match {
		t.Fatalf("temperature 90 > 80 should match the leaf")
	}

	// An event carrying only battery is OUT of scope for the temperature rule: no event.
	batt := planEv(reg, 2, "acme", measured("acme", "d1", "p@1", base.Add(time.Minute), map[string]string{"battery": "50"}))
	if len(batt.Events) != 0 {
		t.Fatalf("battery-only event must not feed the temperature duration rule; got %d events", len(batt.Events))
	}
}

// A batched measurement message fans out one event per sample, so a threshold rule sees an
// early breach even when a later reading in the same batch drops below — the missed-detection
// the last-value-wins collapse would have hidden.
func TestPlanFansEverySampleInABatch(t *testing.T) {
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	thr := rules.Rule{
		ID:   ComposeRuleID("acme", "thr1"),
		Name: "over",
		Type: rules.TypeThreshold,
		When: rules.Condition{Metric: "temperature", Op: rules.OpGt, Threshold: fptr(100)},
	}
	reg := NewRuleRegistry([]ScopedRule{compileScoped(t, "acme", "p@1", thr)})

	ev := &dmmodel.ResolvedEvent{
		SourceDeviceToken:   "d1",
		ProfileVersionToken: "p@1",
		OccurredTime:        base,
		Payload: &dmmodel.ResolvedMeasurementsPayload{Entries: []dmmodel.ResolvedMeasurementsEntry{
			{Entries: []dmmodel.ResolvedMeasurementEntry{{Name: "temperature", Value: "120"}}},
			{Entries: []dmmodel.ResolvedMeasurementEntry{{Name: "temperature", Value: "80"}}},
		}},
	}
	res := planEv(reg, 1, "acme", ev)
	if len(res.Events) != 2 {
		t.Fatalf("both samples should fan out; got %d", len(res.Events))
	}
	if !res.Events[0].Match || res.Events[1].Match {
		t.Fatalf("120 should match and 80 should not; got %v and %v", res.Events[0].Match, res.Events[1].Match)
	}
}

// A threshold rule fans out one matching event keyed by the device token, and correctly
// evaluates the structured leaf.
func TestPlanThresholdFansByDevice(t *testing.T) {
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	thr := rules.Rule{
		ID:   ComposeRuleID("acme", "thr1"),
		Name: "over",
		Type: rules.TypeThreshold,
		When: rules.Condition{Metric: "temperature", Op: rules.OpGt, Threshold: fptr(80)},
	}
	reg := NewRuleRegistry([]ScopedRule{compileScoped(t, "acme", "p@1", thr)})

	res := planEv(reg, 1, "acme", measured("acme", "d1", "p@1", base, map[string]string{"temperature": "70"}))
	if len(res.Events) != 1 || res.Events[0].Match {
		t.Fatalf("temperature 70 should fan one non-matching event; got %+v", res.Events)
	}
	if res.Events[0].Key.Series != "d1" {
		t.Fatalf("threshold series should be the device token; got %q", res.Events[0].Key.Series)
	}
}

// A rule is only fed events whose profile-version token matches its scope; a different
// version (or the empty token) selects nothing.
func TestPlanScopedByProfileVersion(t *testing.T) {
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	thr := rules.Rule{
		ID:   ComposeRuleID("acme", "thr1"),
		Name: "over",
		Type: rules.TypeThreshold,
		When: rules.Condition{Metric: "temperature", Op: rules.OpGt, Threshold: fptr(80)},
	}
	reg := NewRuleRegistry([]ScopedRule{compileScoped(t, "acme", "p@1", thr)})

	same := planEv(reg, 1, "acme", measured("acme", "d1", "p@1", base, map[string]string{"temperature": "90"}))
	if len(same.Events) != 1 {
		t.Fatalf("matching profile version should select the rule; got %d", len(same.Events))
	}
	other := planEv(reg, 2, "acme", measured("acme", "d1", "p@2", base, map[string]string{"temperature": "90"}))
	if len(other.Events) != 0 {
		t.Fatalf("a different profile version must select no rules; got %d", len(other.Events))
	}
	wrongTenant := planEv(reg, 3, "beta", measured("beta", "d1", "p@1", base, map[string]string{"temperature": "90"}))
	if len(wrongTenant.Events) != 0 {
		t.Fatalf("another tenant must select no rules; got %d", len(wrongTenant.Events))
	}
}

// A correlation rule fans one event per matching anchor on the resolved event, keyed by the
// anchor token with the source device as the distinct member; an event with no anchor of the
// rule's type contributes nothing.
func TestPlanCorrelationFansPerAnchor(t *testing.T) {
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	corr := rules.Rule{
		ID:         ComposeRuleID("acme", "corr1"),
		Name:       "many in area",
		Type:       rules.TypeCorrelation,
		AnchorType: "area",
		Count:      3,
		Window:     rules.Duration(5 * time.Minute),
	}
	reg := NewRuleRegistry([]ScopedRule{compileScoped(t, "acme", "p@1", corr)})

	ev := measured("acme", "d1", "p@1", base, map[string]string{"temperature": "90"})
	ev.Anchors = []dmmodel.ResolvedAnchor{
		{AnchorType: "area", AnchorToken: "zone-a"},
		{AnchorType: "area", AnchorToken: "zone-b"},
		{AnchorType: "gateway", AnchorToken: "gw-1"},
	}
	res := planEv(reg, 1, "acme", ev)
	if len(res.Events) != 2 {
		t.Fatalf("correlation should fan one event per matching anchor (2); got %d", len(res.Events))
	}
	got := map[string]string{}
	for _, e := range res.Events {
		got[e.Key.Series] = e.Member
	}
	if got["zone-a"] != "d1" || got["zone-b"] != "d1" {
		t.Fatalf("each anchor series should carry the device as member; got %+v", got)
	}

	// An event with no matching anchor contributes nothing.
	noArea := measured("acme", "d2", "p@1", base, map[string]string{"temperature": "90"})
	noArea.Anchors = []dmmodel.ResolvedAnchor{{AnchorType: "gateway", AnchorToken: "gw-1"}}
	if res := planEv(reg, 2, "acme", noArea); len(res.Events) != 0 {
		t.Fatalf("no matching anchor should contribute no events; got %d", len(res.Events))
	}
}
