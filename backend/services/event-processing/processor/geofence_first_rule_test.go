// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	dccore "github.com/devicechain-io/dc-microservice/core"
)

// These cover the one case the startup fence reconcile structurally cannot: a tenant whose FIRST
// fence rule is published after the process started. The reconcile seeds the tenants that already
// held a fence rule, and this tenant's rule did not exist when it ran; the only other feed — the
// geofence-set fact — is minted by a fence WRITE, so a tenant whose fences were drawn last week
// and whose rule is published today is fed by neither.
//
// They use the same real seams as geofence_wiring_test.go: device-management's own Api mints the
// fence-set version, and this service's own projection and fan-out evaluate against it.

// fenceScopedRules compiles one containment rule through the production compiler and returns it in
// the shape the published-rule fact handler hands to the seed.
func fenceScopedRules(t *testing.T, tenant, profileVersion, fenceToken string) []runtime.ScopedRule {
	t.Helper()
	cr, err := rules0.Compile(rules0.Rule{
		ID:   runtime.ComposeRuleID(tenant, "inyard"),
		Name: "in the yard",
		Type: rules0.TypeThreshold,
		When: rules0.Condition{CEL: `geo.inFence("` + fenceToken + `")`},
	}, rules0.Limits{})
	if err != nil {
		t.Fatalf("compile fence rule: %v", err)
	}
	if !cr.RequiresPosition {
		t.Fatal("fixture: a containment rule that is not position-scoped would never reach the seed")
	}
	return []runtime.ScopedRule{{Tenant: tenant, ProfileVersionToken: profileVersion, Compiled: cr}}
}

// A tenant's FIRST fence rule works from its first event, without anyone editing a fence.
//
// Before this, the rule evaluated nothing at all — a counted eval error per event — until someone
// re-saved a fence they did not want to change, which is not a repair an author would guess at.
//
// The before/after on the SAME registry is the control: the rule is installed first and fires
// nothing, so the firing afterwards is attributable to the seed rather than to a projection that
// was already populated or a rule that was not yet loaded.
func TestPublishingTheFirstFenceRuleSeedsTheProjection(t *testing.T) {
	ctx := context.Background()
	api := newFenceDmApi(t)
	dmCtx := dccore.WithTenant(ctx, "acme")
	if _, err := api.CreateGeoFence(dmCtx, &dmmodel.GeoFenceCreateRequest{
		Token: "yard", Geometry: fenceBox(0, 0, 1, 1)}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// The engine starts with NO rules at all — the state a tenant is in before it authors one.
	src := &schemaFenceSource{t: t, api: api}
	w := &captureWriter{}
	rp := newFenceProcessor(t, runtime.NewRuleRegistry(nil), w, src)
	if err := rp.startFenceView(ctx); err != nil {
		t.Fatalf("startFenceView: %v", err)
	}
	if held := rp.fenceView.RetainedVersions("acme"); len(held) != 0 {
		t.Fatalf("the reconcile seeded %v for a tenant with no fence rule; the premise of this test "+
			"is that it does not, so either the reconcile changed or the fixture is wrong", held)
	}

	// The rule arrives. applyRuleUpdate is the loop's own installer — registry AND engine — so
	// what goes live here is what a real published-rule fact installs, not a registry entry the
	// engine has never heard of.
	published := fenceScopedRules(t, "acme", "p@1", "yard")
	rp.applyRuleUpdate(ruleUpdate{upserts: published})

	// CONTROL, before the seed: the rule is live and the device is inside the fence, yet nothing
	// fires — because the projection holds nothing for this tenant.
	rp.handle(locatedMsg(t, 1, "acme", "d1", "p@1", 1, 0.5, 0.5, &fakeAck{}))
	rp.checkpoint(ctx)
	if w.writes != 0 {
		t.Fatalf("control: an unseeded fence rule published %d derived events, want 0", w.writes)
	}

	// The seed the rule fact now performs.
	if !rp.seedFencesForPublishedRules(published) {
		t.Fatal("seedFencesForPublishedRules reported shutdown")
	}
	if n := drainFenceUpdates(rp); n != 1 {
		t.Fatalf("the publish marshalled %d fence updates onto the loop, want 1", n)
	}
	if held := rp.fenceView.RetainedVersions("acme"); len(held) != 1 || held[0] != 1 {
		t.Fatalf("after the publish the projection holds %v, want [1]", held)
	}

	// And the same event now fires.
	rp.handle(locatedMsg(t, 2, "acme", "d1", "p@1", 1, 0.5, 0.5, &fakeAck{}))
	rp.checkpoint(ctx)
	if w.writes != 1 {
		t.Fatalf("after seeding, the fence rule published %d derived events, want 1", w.writes)
	}
}

// THE WIRING. The test above proves the seed WORKS; this one proves it is CALLED — by the real
// published-rule fact handler, from a real fact, with nothing hand-invoked in between.
//
// The distinction is not academic: with the helper correct and the call site missing, a fence rule
// is exactly as dead as it was before, and every assertion above still passes.
func TestARuleFactSeedsTheProjectionForItsTenant(t *testing.T) {
	ctx := context.Background()
	api := newFenceDmApi(t)
	dmCtx := dccore.WithTenant(ctx, "acme")
	if _, err := api.CreateGeoFence(dmCtx, &dmmodel.GeoFenceCreateRequest{
		Token: "yard", Geometry: fenceBox(0, 0, 1, 1)}); err != nil {
		t.Fatalf("create: %v", err)
	}

	rp := newFenceProcessor(t, runtime.NewRuleRegistry(nil), &captureWriter{},
		&schemaFenceSource{t: t, api: api})
	rp.ruleUpdates = make(chan ruleUpdate, 4)
	if err := rp.startFenceView(ctx); err != nil {
		t.Fatalf("startFenceView: %v", err)
	}
	if held := rp.fenceView.RetainedVersions("acme"); len(held) != 0 {
		t.Fatalf("fixture: the reconcile seeded %v before any rule existed", held)
	}

	// A real fact carrying a real containment rule, through the real handler.
	ack := &fakeAck{}
	fact := factMsg(t, 1, "acme", "p@1", ack, dmmodel.PublishedDetectionRule{
		Token:      "inyard",
		Definition: `{"name":"in the yard","type":"threshold","when":{"cel":"geo.inFence(\"yard\")"}}`,
	})
	if !rp.handleRuleFact(fact, true) {
		t.Fatal("handleRuleFact reported shutdown")
	}
	if ack.acks != 1 {
		t.Errorf("the fact was acked %d times, want 1", ack.acks)
	}

	if n := drainFenceUpdates(rp); n != 1 {
		t.Fatalf("the rule fact marshalled %d fence updates onto the loop, want 1 — the seed is "+
			"not wired into handleRuleFact", n)
	}
	if held := rp.fenceView.RetainedVersions("acme"); len(held) != 1 || held[0] != 1 {
		t.Fatalf("after the rule fact the projection holds %v, want [1]", held)
	}

	// Both were handed to the loop by that ONE handler call — the rules as well as the fences.
	// This asserts that the seed did not somehow displace the rule update; it deliberately does
	// NOT assert which the loop applies first, because they travel on separate channels the loop
	// selects over and no such order exists to assert (see seedFencesForPublishedRules).
	if len(rp.ruleUpdates) != 1 {
		t.Fatalf("the handler queued %d rule updates, want 1", len(rp.ruleUpdates))
	}
}

// NEGATIVE CONTROL. Publishing a rule that does NOT test containment seeds nothing.
//
// Without this the test above passes just as well against an implementation that reads
// device-management on EVERY published-rule fact — a cross-service round trip per publish for
// every tenant in the instance, most of which have no geofence at all.
func TestPublishingANonFenceRuleSeedsNothing(t *testing.T) {
	ctx := context.Background()
	api := newFenceDmApi(t)
	dmCtx := dccore.WithTenant(ctx, "acme")
	if _, err := api.CreateGeoFence(dmCtx, &dmmodel.GeoFenceCreateRequest{
		Token: "yard", Geometry: fenceBox(0, 0, 1, 1)}); err != nil {
		t.Fatalf("create: %v", err)
	}

	rp := newFenceProcessor(t, runtime.NewRuleRegistry(nil), &captureWriter{},
		&schemaFenceSource{t: t, api: api})
	if err := rp.startFenceView(ctx); err != nil {
		t.Fatalf("startFenceView: %v", err)
	}

	thr := 80.0
	plain, err := rules0.Compile(rules0.Rule{
		ID:   runtime.ComposeRuleID("acme", "hot"),
		Name: "hot",
		Type: rules0.TypeThreshold,
		When: rules0.Condition{Metric: "temperature", Op: rules0.OpGt, Threshold: &thr},
	}, rules0.Limits{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	if !rp.seedFencesForPublishedRules([]runtime.ScopedRule{
		{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: plain},
	}) {
		t.Fatal("seedFencesForPublishedRules reported shutdown")
	}
	if n := drainFenceUpdates(rp); n != 0 {
		t.Errorf("a rule that tests no fence marshalled %d fence updates, want 0", n)
	}
	if held := rp.fenceView.RetainedVersions("acme"); len(held) != 0 {
		t.Errorf("a rule that tests no fence seeded %v, want nothing", held)
	}
}

// The scaffold path — no fence source wired, so no projection — must not be reached into. A
// published fence rule there is a no-op rather than a nil dereference.
func TestSeedingWithNoFenceSourceIsANoOp(t *testing.T) {
	rp := newFenceProcessor(t, runtime.NewRuleRegistry(nil), &captureWriter{}, nil)
	if err := rp.startFenceView(context.Background()); err != nil {
		t.Fatalf("startFenceView: %v", err)
	}
	if rp.fenceView != nil {
		t.Fatal("fixture: no source must leave the projection nil")
	}
	if !rp.seedFencesForPublishedRules(fenceScopedRules(t, "acme", "p@1", "yard")) {
		t.Fatal("seedFencesForPublishedRules reported shutdown")
	}
	if n := drainFenceUpdates(rp); n != 0 {
		t.Errorf("a disabled projection marshalled %d fence updates, want 0", n)
	}
}
