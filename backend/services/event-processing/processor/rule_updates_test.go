// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const validFactRule = `{"name":"n","type":"threshold","when":{"metric":"t","op":"gt","threshold":1}}`

// factMsg builds a consumed published-rule fact message on tenant's scoped subject.
func factMsg(t *testing.T, seq uint64, tenant, pvt string, ack *fakeAck, rules ...dmmodel.PublishedDetectionRule) messaging.Message {
	t.Helper()
	b, err := dmproto.MarshalDetectionRulesPublishedEvent(&dmmodel.DetectionRulesPublishedEvent{
		ProfileVersionToken: pvt, Rules: rules,
	})
	if err != nil {
		t.Fatalf("marshal fact: %v", err)
	}
	m := messaging.NewConsumedMessage("dc."+tenant+".detection-rules-published", b, 0, nil, ack)
	m.StreamSeq = seq
	return m
}

// newTestRuleStore builds a DetectRule projection over in-memory sqlite.
func newTestRuleStore(t *testing.T) *model.DetectRuleStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.DetectRule{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return model.NewDetectRuleStore(&rdb.RdbManager{Database: db})
}

// StoreRuleSource rebuilds the scoped rule set from the durable projection (NOT the fact
// stream) — two published versions of a profile both survive (retain-superseded), each with
// its composed "{tenant}/{version}/{ruleToken}" id.
func TestStoreRuleSourceRebuildsFromProjection(t *testing.T) {
	store := newTestRuleStore(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, []model.DetectRule{
		{RuleId: "acme/prof@1/r1", Tenant: "acme", ProfileVersionToken: "prof@1", RuleToken: "r1", Definition: validFactRule},
		{RuleId: "acme/prof@2/r1", Tenant: "acme", ProfileVersionToken: "prof@2", RuleToken: "r1", Definition: validFactRule},
	}); err != nil {
		t.Fatalf("seed projection: %v", err)
	}

	scoped, err := NewStoreRuleSource(store).Load(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := map[string]bool{}
	for _, sr := range scoped {
		got[sr.Compiled.ID] = true
	}
	if !got["acme/prof@1/r1"] || !got["acme/prof@2/r1"] || len(got) != 2 {
		t.Fatalf("both versions should rebuild with composed ids; got %v", got)
	}
}

// A persisted rule that no longer compiles is skipped by the rebuild (not fatal); the rest load.
func TestStoreRuleSourceSkipsUncompilable(t *testing.T) {
	store := newTestRuleStore(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, []model.DetectRule{
		{RuleId: "acme/p@1/ok", Tenant: "acme", ProfileVersionToken: "p@1", RuleToken: "ok", Definition: validFactRule},
		{RuleId: "acme/p@1/bad", Tenant: "acme", ProfileVersionToken: "p@1", RuleToken: "bad", Definition: `{"type":`},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	scoped, err := NewStoreRuleSource(store).Load(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(scoped) != 1 || scoped[0].Compiled.ID != "acme/p@1/ok" {
		t.Fatalf("only the compilable persisted rule should load; got %+v", scoped)
	}
}

// TestReconcileRegistryClosesRollingOverlap proves the post-replay registry reconcile (the Finding-1
// fix): a rule the registry MISSED — because a peer pod acked its publish fact during a rolling-update
// overlap and the registry was loaded once at Initialize — is installed from the durable projection,
// and a rule the projection no longer holds is evicted. So the dead-man armer that reconciles against
// this registry immediately after sees the true active rule set, not the stale Initialize-time one.
func TestReconcileRegistryClosesRollingOverlap(t *testing.T) {
	store := newTestRuleStore(t)
	ctx := context.Background()
	// The durable projection holds the CURRENT rule set (as if a peer pod persisted it during overlap).
	// An ABSENCE rule so the assertion below can prove the ENGINE — not just the registry — received it
	// (a threshold rule would need a driven event; an absence rule fires from SetExpected + advance).
	const absDef = `{"name":"dead","type":"absence","timeout":"10s"}`
	if err := store.Upsert(ctx, []model.DetectRule{
		{RuleId: "acme/prof@1/r1", Tenant: "acme", ProfileVersionToken: "prof@1", RuleToken: "r1", Definition: absDef},
	}); err != nil {
		t.Fatalf("seed projection: %v", err)
	}
	// A STALE registry: it MISSED prof@1/r1 and still holds a rule the projection no longer has.
	reg := runtime.NewRuleRegistry([]runtime.ScopedRule{absScopedProc("acme", "gone@1", "acme/gone@1/x")})
	eng := detectcore.NewEngine(reg.Cores(), 0)
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.engine = eng
	rp.registry = reg
	rp.RuleStore = store

	if err := rp.reconcileRegistry(ctx); err != nil {
		t.Fatalf("reconcileRegistry: %v", err)
	}
	if _, ok := reg.Lookup("acme/prof@1/r1"); !ok {
		t.Fatal("reconcile did not install the rule the registry missed during the overlap")
	}
	if _, ok := reg.Lookup("acme/gone@1/x"); ok {
		t.Fatal("reconcile did not evict a rule the projection no longer holds")
	}
	if reg.Count() != 1 {
		t.Fatalf("registry should hold exactly the projection's rules; count = %d", reg.Count())
	}
	// Engine half: prove the diff-apply reached the ENGINE, not just the registry (guards a future
	// edit that dropped the paired engine.UpsertRule / RemoveRule). fire() only emits when the rule's
	// core is present in the engine, so arming both series and advancing past the deadline fires ONLY
	// the reconciled-in rule and NOT the reconciled-out one.
	eng.SetExpected(detectcore.SeriesKey{Rule: "acme/prof@1/r1", Series: "d"}, testBase)
	eng.SetExpected(detectcore.SeriesKey{Rule: "acme/gone@1/x", Series: "d"}, testBase)
	eng.Advance(testBase.Add(11 * time.Second))
	var newFired, goneFired bool
	for _, d := range eng.Drain() {
		switch d.RuleID {
		case "acme/prof@1/r1":
			newFired = true
		case "acme/gone@1/x":
			goneFired = true
		}
	}
	if !newFired {
		t.Fatal("reconcile installed the rule in the registry but not the engine (its core did not fire)")
	}
	if goneFired {
		t.Fatal("reconcile evicted the rule from the registry but left its core running in the engine")
	}
}

// TestReconcileRegistryNoStoreIsNoOp: without a RuleStore (scaffold/test path) the reconcile is a
// no-op and leaves the registry untouched.
func TestReconcileRegistryNoStoreIsNoOp(t *testing.T) {
	reg := runtime.NewRuleRegistry([]runtime.ScopedRule{absScopedProc("acme", "p1@v1", "acme/p1@v1/r1")})
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.registry = reg
	rp.engine = detectcore.NewEngine(reg.Cores(), 0)
	rp.RuleStore = nil
	if err := rp.reconcileRegistry(context.Background()); err != nil {
		t.Fatalf("reconcileRegistry: %v", err)
	}
	if reg.Count() != 1 {
		t.Fatalf("a nil store must leave the registry untouched; count = %d", reg.Count())
	}
}

// A fact whose subject carries no tenant, or whose payload is unparseable, is dropped by the
// live consumer's decode — not fatal.
func TestDecodePublishedRuleFactDropsUnusable(t *testing.T) {
	untenanted := messaging.NewConsumedMessage("badsubject", nil, 0, nil, nil)
	if _, _, ok := decodePublishedRuleFact(context.Background(), untenanted); ok {
		t.Fatal("a fact with no parseable tenant must be dropped")
	}
	garbage := messaging.NewConsumedMessage("dc.acme.detection-rules-published", []byte("not-proto\xff"), 0, nil, nil)
	if _, _, ok := decodePublishedRuleFact(context.Background(), garbage); ok {
		t.Fatal("a fact with an unparseable payload must be dropped")
	}
	good := factMsg(t, 1, "acme", "prof@1", nil, dmmodel.PublishedDetectionRule{Token: "r1", Definition: validFactRule})
	tenant, ev, ok := decodePublishedRuleFact(context.Background(), good)
	if !ok || tenant != "acme" || ev.ProfileVersionToken != "prof@1" {
		t.Fatalf("a well-formed fact should decode: ok=%v tenant=%q ev=%+v", ok, tenant, ev)
	}
}

// scopedCoreThreshold is a compiled threshold rule with a real engine core, so an upsert
// installs a rule that actually fires.
func scopedCoreThreshold(tenant, pvt, id string) runtime.ScopedRule {
	return runtime.ScopedRule{
		Tenant:              tenant,
		ProfileVersionToken: pvt,
		Compiled:            &rules0.CompiledRule{ID: id, Core: detectcore.Rule{ID: id, Kind: detectcore.Threshold}},
	}
}

// fires reports whether feeding the engine one matching event for id produces a detection.
func fires(e *detectcore.Engine, seq uint64, id string) bool {
	e.ProcessEvent(detectcore.Event{Seq: seq, Key: detectcore.SeriesKey{Rule: id, Series: "dev"}, Time: testBase, Match: true})
	return len(e.Drain()) > 0
}

// applyRuleUpdate installs an upserted rule into BOTH the registry and the engine, and a
// removal evicts it from both — keeping the two consistent on the single writer.
func TestApplyRuleUpdateUpsertThenRemove(t *testing.T) {
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.engine = detectcore.NewEngine(rp.registry.Cores(), 0)

	id := "acme/p@1/r1"
	rp.applyRuleUpdate(ruleUpdate{upserts: []runtime.ScopedRule{scopedCoreThreshold("acme", "p@1", id)}})
	if rp.registry.Count() != 1 {
		t.Fatalf("registry count = %d, want 1 after upsert", rp.registry.Count())
	}
	if !fires(rp.engine, 1, id) {
		t.Fatal("engine did not run the upserted rule")
	}

	rp.applyRuleUpdate(ruleUpdate{removals: []string{id}})
	if rp.registry.Count() != 0 {
		t.Fatalf("registry count = %d, want 0 after removal", rp.registry.Count())
	}
	if fires(rp.engine, 2, id) {
		t.Fatal("engine still ran the removed rule")
	}
}

// durScoped is a Duration rule with a real engine core and a given raw definition, so a change
// that is invisible to the lowered core.Rule (the predicate differs, not the hold) still shows
// up in the definition.
func durScoped(id, definition string) runtime.ScopedRule {
	return runtime.ScopedRule{
		Tenant:              "acme",
		ProfileVersionToken: "p@1",
		Compiled:            &rules0.CompiledRule{ID: id, Core: detectcore.Rule{ID: id, Kind: detectcore.Duration, Hold: 10 * time.Second}},
		Definition:          definition,
	}
}

// A reused id whose DEFINITION changed (but whose lowered core.Rule is identical) must GC the
// stale rule's keyed state — the running Duration hold must not survive.
func TestApplyRuleUpdate_DefinitionChangeResetsState(t *testing.T) {
	id := "acme/p@1/r"
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.engine = detectcore.NewEngine(rp.registry.Cores(), 0)
	rp.applyRuleUpdate(ruleUpdate{upserts: []runtime.ScopedRule{durScoped(id, `{"v":"A"}`)}})
	rp.engine.ProcessEvent(detectcore.Event{Seq: 1, Key: detectcore.SeriesKey{Rule: id, Series: "d"}, Time: testBase, Match: true}) // hold armed, deadline +10s
	rp.applyRuleUpdate(ruleUpdate{upserts: []runtime.ScopedRule{durScoped(id, `{"v":"B"}`)}})                                       // changed body → reset
	rp.engine.Advance(testBase.Add(20 * time.Second))
	if dets := rp.engine.Drain(); len(dets) != 0 {
		t.Fatalf("a changed-definition upsert must GC the running hold; fired %+v", dets)
	}
}

// A RemoveRule GC (a changed-definition upsert, or a removal) marks the loop dirty so the dropped
// keyed state is checkpointed — otherwise a restart would replay it back to life (finding E). A plain
// unchanged upsert does NOT dirty (the rule set is rebuilt from the projection, not snapshotted).
func TestApplyRuleUpdate_GCMarksDirty(t *testing.T) {
	id := "acme/p@1/r"

	// Changed definition → GC → dirty.
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.engine = detectcore.NewEngine(rp.registry.Cores(), 0)
	rp.applyRuleUpdate(ruleUpdate{upserts: []runtime.ScopedRule{durScoped(id, `{"v":"A"}`)}})
	rp.dirty = false
	rp.applyRuleUpdate(ruleUpdate{upserts: []runtime.ScopedRule{durScoped(id, `{"v":"B"}`)}})
	if !rp.dirty {
		t.Fatal("a changed-definition GC must mark the loop dirty so the drop is checkpointed")
	}

	// Removal → GC → dirty.
	rp.dirty = false
	rp.applyRuleUpdate(ruleUpdate{removals: []string{id}})
	if !rp.dirty {
		t.Fatal("a removal GC must mark the loop dirty")
	}

	// Unchanged redelivery → no GC → not dirty.
	rp.applyRuleUpdate(ruleUpdate{upserts: []runtime.ScopedRule{durScoped(id, `{"v":"A"}`)}})
	rp.dirty = false
	rp.applyRuleUpdate(ruleUpdate{upserts: []runtime.ScopedRule{durScoped(id, `{"v":"A"}`)}})
	if rp.dirty {
		t.Fatal("an unchanged redelivery must not dirty the loop (rule set is not snapshotted)")
	}
}

// An unchanged redelivery (identical definition) must PRESERVE running state — the whole point
// of preserve-on-reupsert.
func TestApplyRuleUpdate_UnchangedDefinitionPreservesState(t *testing.T) {
	id := "acme/p@1/r"
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.engine = detectcore.NewEngine(rp.registry.Cores(), 0)
	rp.applyRuleUpdate(ruleUpdate{upserts: []runtime.ScopedRule{durScoped(id, `{"v":"A"}`)}})
	rp.engine.ProcessEvent(detectcore.Event{Seq: 1, Key: detectcore.SeriesKey{Rule: id, Series: "d"}, Time: testBase, Match: true})
	rp.applyRuleUpdate(ruleUpdate{upserts: []runtime.ScopedRule{durScoped(id, `{"v":"A"}`)}}) // unchanged → preserve
	rp.engine.Advance(testBase.Add(20 * time.Second))
	if dets := rp.engine.Drain(); len(dets) != 1 {
		t.Fatalf("an unchanged redelivery must preserve the running hold; fired %+v", dets)
	}
}

// runRuleConsumer PERSISTS a fact's rules to the durable projection, hands the compiled rules
// to the loop over the ruleUpdates channel, and only then acks — persist-before-ack durability.
func TestRuleConsumerPersistsThenDeliversThenAcks(t *testing.T) {
	ack := &fakeAck{}
	store := newTestRuleStore(t)
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.RuleStore = store
	rp.ruleUpdates = make(chan ruleUpdate, 1)
	rp.RuleUpdatesReader = &fakeReader{results: []readResult{
		{msg: factMsg(t, 1, "acme", "prof@1", ack, dmmodel.PublishedDetectionRule{Token: "r1", Definition: validFactRule})},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rp.procCtx = ctx
	rp.readerWG.Add(1)
	go rp.runRuleConsumer()

	select {
	case upd := <-rp.ruleUpdates:
		if len(upd.upserts) != 1 || upd.upserts[0].Compiled.ID != "acme/prof@1/r1" {
			t.Fatalf("unexpected update off the channel: %+v", upd.upserts)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rule consumer did not deliver an update")
	}
	cancel()
	rp.readerWG.Wait()
	if ack.acks != 1 {
		t.Fatalf("fact should be acked once, got %d", ack.acks)
	}
	// The rule was durably persisted (survives a restart via the projection).
	rows, err := store.LoadAll(context.Background())
	if err != nil {
		t.Fatalf("load projection: %v", err)
	}
	if len(rows) != 1 || rows[0].RuleId != "acme/prof@1/r1" {
		t.Fatalf("fact was not persisted to the durable projection: %+v", rows)
	}
}
