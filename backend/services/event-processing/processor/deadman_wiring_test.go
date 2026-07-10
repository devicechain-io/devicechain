// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/entity"
)

// absScopedProc is an absence ScopedRule with a real engine core, so installing it into a real
// engine + registry lets the dead-man armer actually arm and fire it (Timeout 10s).
func absScopedProc(tenant, pvt, id string) runtime.ScopedRule {
	return runtime.ScopedRule{
		Tenant:              tenant,
		ProfileVersionToken: pvt,
		Compiled: &rules0.CompiledRule{
			ID:   id,
			Type: rules0.TypeAbsence,
			Core: detectcore.Rule{ID: id, Kind: detectcore.Absence, Timeout: 10 * time.Second},
		},
	}
}

// deadmanFires reports whether advancing the engine's watermark past the given series' dead-man
// deadline produces an absence detection — the observable proof the armer armed it in the engine.
func deadmanFires(e *detectcore.Engine, id, device string, at time.Time) bool {
	e.Advance(at)
	for _, d := range e.Drain() {
		if d.RuleID == id && d.Series == device {
			return true
		}
	}
	return false
}

// armedProcessor builds a processor over a REAL engine + registry holding one absence rule, with a
// dead-man armer and an armUpdates channel — the substrate for the live-wiring tests.
func armedProcessor(t *testing.T, id string) (*ResolvedEventsProcessor, *detectcore.Engine) {
	t.Helper()
	reg := runtime.NewRuleRegistry([]runtime.ScopedRule{absScopedProc("acme", "p1@v1", id)})
	eng := detectcore.NewEngine(reg.Cores(), 0)
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.engine = eng
	rp.registry = reg
	rp.armer = runtime.NewDeadmanArmer(reg, eng)
	rp.armUpdates = make(chan armUpdate, 8)
	return rp, eng
}

// TestStartDeadmanArmerReconcilesFromStores: startDeadmanArmer builds the armer and arms every
// rostered device from the projections, AND sweeps a stale engine key whose device is not rostered
// (deleted while down). The whole reconcile wiring — LoadAll, the model→runtime mappers, Reconcile.
func TestStartDeadmanArmerReconcilesFromStores(t *testing.T) {
	id := runtime.ComposeRuleID("acme", "p1@v1/r1")
	rp, eng := armedProcessor(t, id)
	rp.armer = nil // startDeadmanArmer builds it
	ctx := context.Background()
	rp.RosterStore = newProcRosterStore(t)
	rp.ProfileActiveStore = newProcActiveStore(t)

	// A stale dead-man in the engine snapshot for a device that is NOT rostered (deleted while down).
	eng.SetExpected(detectcore.SeriesKey{Rule: id, Series: "goneDev"}, testBase)
	// The durable projections: one live device + its profile's active version.
	if err := rp.RosterStore.Upsert(ctx, &model.DeviceRoster{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p1", ExpectedSince: testBase}); err != nil {
		t.Fatalf("seed roster: %v", err)
	}
	if err := rp.ProfileActiveStore.Upsert(ctx, &model.ProfileActive{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: testBase}); err != nil {
		t.Fatalf("seed active: %v", err)
	}

	if err := rp.startDeadmanArmer(ctx); err != nil {
		t.Fatalf("startDeadmanArmer: %v", err)
	}
	if rp.armer == nil {
		t.Fatal("armer not built")
	}
	// One advance past both deadlines (testBase+10): dev1 fires, goneDev was swept so it does not.
	eng.Advance(testBase.Add(11 * time.Second))
	var dev1Fired, goneFired bool
	for _, d := range eng.Drain() {
		switch d.Series {
		case "dev1":
			dev1Fired = true
		case "goneDev":
			goneFired = true
		}
	}
	if goneFired {
		t.Fatal("reconcile did not sweep the stale (unrostered) dead-man")
	}
	if !dev1Fired {
		t.Fatal("reconcile did not arm the rostered device's dead-man")
	}
}

// TestStartDeadmanArmerNoStoresIsNoOp: with the read-model stores unwired (scaffold path),
// startDeadmanArmer leaves the armer nil and arms nothing.
func TestStartDeadmanArmerNoStoresIsNoOp(t *testing.T) {
	rp, _ := armedProcessor(t, runtime.ComposeRuleID("acme", "p1@v1/r1"))
	rp.armer = nil
	rp.RosterStore = nil
	rp.ProfileActiveStore = nil
	if err := rp.startDeadmanArmer(context.Background()); err != nil {
		t.Fatalf("startDeadmanArmer: %v", err)
	}
	if rp.armer != nil {
		t.Fatal("armer should stay nil without the read-model stores")
	}
}

// TestRosterConsumerArmsViaLoop: a device-roster fact flows through the roster consumer
// (persist → authoritative load-back → marshal onto the loop), and applying the marshaled
// armUpdate (as the single-writer loop does) arms the device's dead-man in the real engine.
func TestRosterConsumerArmsViaLoop(t *testing.T) {
	id := runtime.ComposeRuleID("acme", "p1@v1/r1")
	rp, eng := armedProcessor(t, id)
	rp.RosterStore = newProcRosterStore(t)
	// The profile's active version is already known to the armer (its rule fact arrived earlier).
	rp.armer.ApplyActiveVersion(runtime.ActiveEntry{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: testBase})

	ack := &fakeAck{acked: make(chan struct{}, 1)}
	rp.RosterReader = &fakeReader{results: []readResult{{msg: rosterMsg(t, "acme", "dev1", "p1", testBase, ack)}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rp.procCtx = ctx
	rp.readerWG.Add(1)
	go rp.runRosterConsumer()

	waitForAck(t, ack) // persisted + marshaled + acked; the armUpdate is now buffered
	applyOneArmUpdate(t, rp)
	cancel()
	rp.readerWG.Wait()

	if !deadmanFires(eng, id, "dev1", testBase.Add(11*time.Second)) {
		t.Fatal("roster fact did not arm the device's dead-man through the loop")
	}
}

// TestEntityDeletedConsumerDisarmsViaLoop: a device already armed is disarmed when its
// entity-deleted fact flows through the consumer (persist tombstone → load-back reports gone →
// marshal present=false → loop disarms), so no dead-man fires for the deleted device.
func TestEntityDeletedConsumerDisarmsViaLoop(t *testing.T) {
	id := runtime.ComposeRuleID("acme", "p1@v1/r1")
	rp, eng := armedProcessor(t, id)
	rp.RosterStore = newProcRosterStore(t)
	rp.armer.ApplyActiveVersion(runtime.ActiveEntry{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: testBase})
	// Seed the device as live + armed (roster row + in-memory arming).
	ctx0 := context.Background()
	if err := rp.RosterStore.Upsert(ctx0, &model.DeviceRoster{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p1", ExpectedSince: testBase}); err != nil {
		t.Fatalf("seed roster: %v", err)
	}
	rp.armer.ApplyDeviceMembership(runtime.RosterEntry{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p1", ExpectedSince: testBase}, true)

	ack := &fakeAck{acked: make(chan struct{}, 1)}
	rp.EntityDeletedReader = &fakeReader{results: []readResult{{msg: entityDeletedMsg(t, "acme", entity.TypeDevice, "dev1", ack)}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rp.procCtx = ctx
	rp.readerWG.Add(1)
	go rp.runEntityDeletedConsumer()

	waitForAck(t, ack)
	applyOneArmUpdate(t, rp)
	cancel()
	rp.readerWG.Wait()

	if deadmanFires(eng, id, "dev1", testBase.Add(3*time.Hour)) {
		t.Fatal("deleted device still fired a dead-man (not disarmed through the loop)")
	}
}

// TestApplyRuleUpdateArmsEngineFirst: applyRuleUpdate must install the rule core in the engine
// BEFORE arming against it (the wiring contract SetExpected depends on). A device deferred while
// its profile's active version was unknown is armed when the rule update carries the active entry —
// which only works if the rule is installed first.
func TestApplyRuleUpdateArmsEngineFirst(t *testing.T) {
	id := runtime.ComposeRuleID("acme", "p1@v1/r1")
	reg := runtime.NewRuleRegistry(nil) // EMPTY: the rule arrives via the update
	eng := detectcore.NewEngine(nil, 0)
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.engine = eng
	rp.registry = reg
	rp.armer = runtime.NewDeadmanArmer(reg, eng)
	rp.armUpdates = make(chan armUpdate, 8)

	// A rostered device whose active version is not yet known: deferred (in byProfile, unarmed).
	rp.armer.ApplyDeviceMembership(runtime.RosterEntry{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p1", ExpectedSince: testBase}, true)

	rp.applyRuleUpdate(ruleUpdate{
		upserts: []runtime.ScopedRule{absScopedProc("acme", "p1@v1", id)},
		active:  &runtime.ActiveEntry{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: testBase},
	})
	if !deadmanFires(eng, id, "dev1", testBase.Add(11*time.Second)) {
		t.Fatal("applyRuleUpdate did not arm the deferred device engine-first")
	}
}

// TestArmRecheckConvergesToProjectionTruth proves the fix for the cross-consumer read/send
// inversion: the loop re-reads the authoritative (monotonically-converged) projection on every
// recheck, so a STALE in-memory observation applied out of lifecycle order is corrected. Both
// directions: a stale "live" against a tombstoned projection disarms; a stale "deleted" against a
// live projection re-arms.
func TestArmRecheckConvergesToProjectionTruth(t *testing.T) {
	id := runtime.ComposeRuleID("acme", "p1@v1/r1")
	rp, eng := armedProcessor(t, id)
	rp.RosterStore = newProcRosterStore(t)
	rp.armer.ApplyActiveVersion(runtime.ActiveEntry{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: testBase})
	ctx := context.Background()
	rp.procCtx = ctx

	// Direction A — projection converged to a TOMBSTONE (created then deleted), but a stale "live"
	// observation armed the device in memory. The recheck re-reads the tombstone and disarms.
	if err := rp.RosterStore.Upsert(ctx, &model.DeviceRoster{Tenant: "acme", DeviceToken: "devA", ProfileToken: "p1", ExpectedSince: testBase}); err != nil {
		t.Fatalf("seed create A: %v", err)
	}
	if err := rp.RosterStore.Delete(ctx, "acme", "devA", testBase.Add(time.Hour)); err != nil {
		t.Fatalf("seed delete A: %v", err)
	}
	rp.armer.ApplyDeviceMembership(runtime.RosterEntry{Tenant: "acme", DeviceToken: "devA", ProfileToken: "p1", ExpectedSince: testBase}, true) // stale live
	rp.applyArmRecheck(armUpdate{tenant: "acme", deviceToken: "devA"})

	// Direction B — projection is LIVE, but a stale "deleted" observation disarmed it in memory
	// (which also evicted it from byProfile). The recheck re-reads live and re-arms.
	if err := rp.RosterStore.Upsert(ctx, &model.DeviceRoster{Tenant: "acme", DeviceToken: "devB", ProfileToken: "p1", ExpectedSince: testBase}); err != nil {
		t.Fatalf("seed create B: %v", err)
	}
	rp.armer.ApplyDeviceMembership(runtime.RosterEntry{Tenant: "acme", DeviceToken: "devB", ProfileToken: "p1", ExpectedSince: testBase}, true)
	rp.armer.ApplyDeviceMembership(runtime.RosterEntry{Tenant: "acme", DeviceToken: "devB"}, false) // stale disarm
	rp.applyArmRecheck(armUpdate{tenant: "acme", deviceToken: "devB"})

	eng.Advance(testBase.Add(3 * time.Hour))
	var aFired, bFired bool
	for _, d := range eng.Drain() {
		switch d.Series {
		case "devA":
			aFired = true
		case "devB":
			bFired = true
		}
	}
	if aFired {
		t.Fatal("recheck did not disarm a stale-armed device against a tombstoned projection")
	}
	if !bFired {
		t.Fatal("recheck did not re-arm a stale-disarmed device against a live projection")
	}
}

// applyOneArmUpdate receives one buffered recheck signal and applies it exactly as the single-writer
// loop's armUpdates case does — via applyArmRecheck, which re-reads the authoritative roster
// projection. The consumer sends the signal BEFORE it acks, so after waitForAck the value is already
// buffered and this receive is deterministic (no drainer race).
func applyOneArmUpdate(t *testing.T, rp *ResolvedEventsProcessor) {
	t.Helper()
	select {
	case au := <-rp.armUpdates:
		rp.applyArmRecheck(au)
	case <-time.After(2 * time.Second):
		t.Fatal("no arm recheck was marshaled onto the loop")
	}
}
