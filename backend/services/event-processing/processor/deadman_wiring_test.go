// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
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

// TestStartDeadmanArmerReconcilesFromStores: the startup pair — startDeadmanGate builds the armer +
// loads membership, reconcileDeadmanArming arms every rostered device from the projections AND sweeps
// a stale engine key whose device is not rostered (deleted while down). The whole reconcile wiring —
// LoadAll, the model→runtime mappers, Reconcile.
func TestStartDeadmanArmerReconcilesFromStores(t *testing.T) {
	id := runtime.ComposeRuleID("acme", "p1@v1/r1")
	rp, eng := armedProcessor(t, id)
	rp.armer = nil // startDeadmanGate builds it
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

	if err := rp.startDeadmanGate(ctx); err != nil {
		t.Fatalf("startDeadmanGate: %v", err)
	}
	if rp.armer == nil {
		t.Fatal("armer not built")
	}
	if err := rp.reconcileDeadmanArming(ctx); err != nil {
		t.Fatalf("reconcileDeadmanArming: %v", err)
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
// startDeadmanGate leaves the armer nil and reconcileDeadmanArming arms nothing.
func TestStartDeadmanArmerNoStoresIsNoOp(t *testing.T) {
	rp, _ := armedProcessor(t, runtime.ComposeRuleID("acme", "p1@v1/r1"))
	rp.armer = nil
	rp.RosterStore = nil
	rp.ProfileActiveStore = nil
	if err := rp.startDeadmanGate(context.Background()); err != nil {
		t.Fatalf("startDeadmanGate: %v", err)
	}
	if rp.armer != nil {
		t.Fatal("armer should stay nil without the read-model stores")
	}
	if err := rp.reconcileDeadmanArming(context.Background()); err != nil {
		t.Fatalf("reconcileDeadmanArming: %v", err)
	}
}

// TestDropStaleAbsences: the publish-time membership gate drops a buffered ABSENCE detection whose
// device left the rule's scope (unrostered) while keeping one for a live rostered device; non-absence
// detections always pass through.
func TestDropStaleAbsences(t *testing.T) {
	id := runtime.ComposeRuleID("acme", "p1@v1/r1")
	rp, _ := armedProcessor(t, id)
	rp.armer.LoadMembership(
		[]runtime.RosterEntry{{Tenant: "acme", DeviceToken: "live", ProfileToken: "p1", ExpectedSince: testBase}},
		[]runtime.ActiveEntry{{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: testBase}},
	)
	rp.pendingDets = []detectcore.Detection{
		{RuleID: id, Series: "live", Kind: detectcore.Absence},
		{RuleID: id, Series: "gone", Kind: detectcore.Absence},
		{RuleID: id, Series: "gone", Kind: detectcore.Threshold}, // non-absence: always kept
	}
	rp.dropSupersededDetections()

	var liveAbs, thr, goneAbs bool
	for _, d := range rp.pendingDets {
		switch {
		case d.Kind == detectcore.Absence && d.Series == "live":
			liveAbs = true
		case d.Kind == detectcore.Absence && d.Series == "gone":
			goneAbs = true
		case d.Kind == detectcore.Threshold:
			thr = true
		}
	}
	if goneAbs {
		t.Fatal("stale absence for a departed device was not dropped")
	}
	if !liveAbs || !thr {
		t.Fatalf("gate dropped a detection it should keep: %+v", rp.pendingDets)
	}
}

// TestDropSupersededFrontierDetections is the ADR-057 D6 gate: a superseded profile version's retained
// rule still fires its FRONTIER-triggered detections (Duration/Session/Aggregate timers + pane closes)
// off the shared watermark even though the fan-out starves it of events, so those must be dropped at
// publish lest they contribute a false edge to the alarm object under the stable contributor id. It
// drops ONLY on a CONFIRMED version mismatch: the active version's detection, an unrostered device's,
// and a superseded EVENT-driven kind all pass through.
func TestDropSupersededFrontierDetections(t *testing.T) {
	v1 := runtime.ComposeRuleID("acme", "p1@v1/rDur")
	v2 := runtime.ComposeRuleID("acme", "p1@v2/rDur")
	durScoped := func(pvt, id string) runtime.ScopedRule {
		return runtime.ScopedRule{Tenant: "acme", ProfileVersionToken: pvt,
			Compiled: &rules0.CompiledRule{ID: id, Type: rules0.TypeDuration,
				Core: detectcore.Rule{ID: id, Kind: detectcore.Duration, Hold: 5 * time.Second}}}
	}
	reg := runtime.NewRuleRegistry([]runtime.ScopedRule{durScoped("p1@v1", v1), durScoped("p1@v2", v2)})
	eng := detectcore.NewEngine(reg.Cores(), 0)
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.engine = eng
	rp.registry = reg
	rp.armer = runtime.NewDeadmanArmer(reg, eng)
	// Device dev is on p1 whose ACTIVE version is p1@v2 → p1@v1 is superseded.
	rp.armer.LoadMembership(
		[]runtime.RosterEntry{{Tenant: "acme", DeviceToken: "dev", ProfileToken: "p1", ExpectedSince: testBase}},
		[]runtime.ActiveEntry{{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v2", PublishedAt: testBase}},
	)
	rp.pendingDets = []detectcore.Detection{
		{RuleID: v1, Series: "dev", Kind: detectcore.Duration},        // superseded frontier → DROP
		{RuleID: v2, Series: "dev", Kind: detectcore.Duration},        // active version → keep
		{RuleID: v1, Series: "dev", Kind: detectcore.Threshold},       // superseded but event-driven → keep
		{RuleID: v1, Series: "unrostered", Kind: detectcore.Duration}, // unrostered → keep (fail toward not-dropping)
	}
	rp.dropSupersededDetections()

	key := func(kind detectcore.RuleKind, rule, series string) string {
		return fmt.Sprintf("%d|%s|%s", kind, rule, series)
	}
	kept := map[string]bool{}
	for _, d := range rp.pendingDets {
		kept[key(d.Kind, d.RuleID, d.Series)] = true
	}
	if kept[key(detectcore.Duration, v1, "dev")] {
		t.Fatal("a superseded version's Duration (frontier) detection must be dropped")
	}
	for _, want := range []string{
		key(detectcore.Duration, v2, "dev"),        // active version kept
		key(detectcore.Threshold, v1, "dev"),       // event-driven superseded kept
		key(detectcore.Duration, v1, "unrostered"), // uncertain membership kept
	} {
		if !kept[want] {
			t.Fatalf("the gate dropped a detection it should keep: %s (kept=%v)", want, kept)
		}
	}
}

// TestCatchUpDrainsRosterFactsToHead: catchUpFactProjections persists a backlog of roster facts to the
// durable projection BEFORE replay (making the gate's membership current for a device deleted/created
// while down), acks them, and does NOT signal the loop (signal=false — the loop is not running yet).
func TestCatchUpDrainsRosterFactsToHead(t *testing.T) {
	rp, _ := armedProcessor(t, runtime.ComposeRuleID("acme", "p1@v1/r1"))
	rp.RosterStore = newProcRosterStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rp.procCtx = ctx
	ack1, ack2 := &fakeAck{}, &fakeAck{}
	rp.RosterReader = &fakeReader{drainable: true, results: []readResult{
		{msg: rosterMsg(t, "acme", "dev1", "p1", testBase, ack1)},
		{msg: rosterMsg(t, "acme", "dev2", "p1", testBase, ack2)},
	}}

	if err := rp.catchUpFactProjections(); err != nil {
		t.Fatalf("catchUpFactProjections: %v", err)
	}
	if _, live, err := rp.RosterStore.Load(ctx, "acme", "dev1"); err != nil || !live {
		t.Fatalf("dev1 not persisted by catch-up: live=%v err=%v", live, err)
	}
	if _, live, err := rp.RosterStore.Load(ctx, "acme", "dev2"); err != nil || !live {
		t.Fatalf("dev2 not persisted by catch-up: live=%v err=%v", live, err)
	}
	if ack1.acks == 0 || ack2.acks == 0 {
		t.Fatal("catch-up must ack the facts it drained")
	}
	if len(rp.armUpdates) != 0 {
		t.Fatalf("catch-up must NOT signal the loop (signal=false), got %d armUpdates", len(rp.armUpdates))
	}
}

// TestCatchUpFailsClosedOnDegradedBroker: when the broker reports UNDELIVERED facts (pending>0) but a
// bounded read cannot fetch any (a degraded broker that answers ConsumerInfo but not Fetch), catch-up
// fails startup rather than proceed with a knowingly-stale gate (MEDIUM-1 fix).
func TestCatchUpFailsClosedOnDegradedBroker(t *testing.T) {
	rp, _ := armedProcessor(t, runtime.ComposeRuleID("acme", "p1@v1/r1"))
	rp.RosterStore = newProcRosterStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rp.procCtx = ctx
	// Backlog says 3 undelivered, but ReadMessage never yields one (EOF immediately).
	rp.RosterReader = &fakeReader{readEOFImmediately: true, numPending: 3}

	if err := rp.catchUpFactProjections(); err == nil {
		t.Fatal("catch-up must fail closed when undelivered facts cannot be fetched")
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

// TestArmRecheckRetriesOnError proves the arm-side mirror of the attribute retry (attrRetries): a
// transient RosterStore.Load failure does NOT silently strand stale membership — which for a deleted
// device is a published FALSE absence and for a created device a missed one, since the driving fact
// (an entity-deleted tombstone) is frequently the device's LAST fact ever. The recheck is queued and
// the ticker retry arms it once the store recovers. A cancelled context stands in for the DB blip.
func TestArmRecheckRetriesOnError(t *testing.T) {
	id := runtime.ComposeRuleID("acme", "p1@v1/r1")
	rp, eng := armedProcessor(t, id)
	rp.RosterStore = newProcRosterStore(t)
	rp.armer.ApplyActiveVersion(runtime.ActiveEntry{Tenant: "acme", ProfileToken: "p1", ActiveVersionToken: "p1@v1", PublishedAt: testBase})
	ctx := context.Background()
	if err := rp.RosterStore.Upsert(ctx, &model.DeviceRoster{Tenant: "acme", DeviceToken: "dev1", ProfileToken: "p1", ExpectedSince: testBase}); err != nil {
		t.Fatalf("seed roster: %v", err)
	}
	au := armUpdate{tenant: "acme", deviceToken: "dev1"}

	// First recheck under a CANCELLED context: RosterStore.Load errors → queued for retry, not dropped.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	rp.procCtx = cctx
	rp.applyArmRecheck(au)
	if _, pending := rp.armRetries[au]; !pending {
		t.Fatal("a failed arm recheck must be queued for retry, not silently dropped")
	}

	// Ticker retry under a LIVE context heals it: the device arms and the retry set clears.
	rp.procCtx = ctx
	rp.retryArmRechecks(rp.clock.Now().Add(time.Hour))
	if len(rp.armRetries) != 0 {
		t.Fatalf("a healed retry must clear the set, got %d entries", len(rp.armRetries))
	}
	if !deadmanFires(eng, id, "dev1", testBase.Add(11*time.Second)) {
		t.Fatal("retry did not arm the device's dead-man after the store recovered")
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
