// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-event-processing/model"
)

// TestAttributeScopeVocabularyMatches pins that the runtime view's scope constants equal device-
// management's AttributeScope string values. The projection stores those strings verbatim, and the
// view flattens by comparing against its own constants, so a rename upstream would silently break
// SERVER-over-SHARED precedence (every dynamic threshold would fall back to shared or vanish) with no
// compile error. This guard turns that drift into a test failure.
func TestAttributeScopeVocabularyMatches(t *testing.T) {
	if runtime.AttrScopeServer != string(dmmodel.AttributeScopeServer) {
		t.Errorf("AttrScopeServer %q != device-management %q", runtime.AttrScopeServer, dmmodel.AttributeScopeServer)
	}
	if runtime.AttrScopeShared != string(dmmodel.AttributeScopeShared) {
		t.Errorf("AttrScopeShared %q != device-management %q", runtime.AttrScopeShared, dmmodel.AttributeScopeShared)
	}
}

// TestStartAttributeViewReconciles proves the loop-owned view is rebuilt from the durable projection
// at startup with SERVER-over-SHARED precedence, so the first live event after restart resolves a
// dynamic threshold from the persisted value. A nil store leaves the view nil (scaffold path).
func TestStartAttributeViewReconciles(t *testing.T) {
	store := newProcAttributeStore(t)
	ctx := context.Background()
	seed := []*model.DeviceAttribute{
		{Tenant: "acme", DeviceToken: "d1", Scope: "SHARED", AttrKey: "lim", Value: 100, LastEventAt: testBase},
		{Tenant: "acme", DeviceToken: "d1", Scope: "SERVER", AttrKey: "lim", Value: 50, LastEventAt: testBase},
		{Tenant: "acme", DeviceToken: "d2", Scope: "SHARED", AttrKey: "lim", Value: 30, LastEventAt: testBase},
	}
	for _, a := range seed {
		if err := store.Upsert(ctx, a); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.AttributeStore = store
	if err := rp.startAttributeView(ctx); err != nil {
		t.Fatalf("startAttributeView: %v", err)
	}
	if got := rp.attrView.For("acme", "d1")["lim"]; got != 50 {
		t.Errorf("d1 lim = %v, want 50 (server shadows shared)", got)
	}
	if got := rp.attrView.For("acme", "d2")["lim"]; got != 30 {
		t.Errorf("d2 lim = %v, want 30", got)
	}

	// A nil store leaves the view disabled (scaffold/test path).
	scaffold := newTestProcessor(newTestStore(t), nil, 1)
	if err := scaffold.startAttributeView(ctx); err != nil {
		t.Fatalf("startAttributeView (no store): %v", err)
	}
	if scaffold.attrView != nil {
		t.Errorf("no AttributeStore must leave attrView nil, got %v", scaffold.attrView)
	}
}

// TestApplyAttrRecheckReplacesFromStore proves a recheck re-reads the authoritative projection and
// REPLACES the device's view entry: a value change is reflected, and a device whose rows are gone
// (every attribute removed / purged) drops out of the view — never a stale carry-over.
func TestApplyAttrRecheckReplacesFromStore(t *testing.T) {
	store := newProcAttributeStore(t)
	ctx := context.Background()
	if err := store.Upsert(ctx, &model.DeviceAttribute{
		Tenant: "acme", DeviceToken: "d1", Scope: "SERVER", AttrKey: "lim", Value: 50, LastEventAt: testBase}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.AttributeStore = store
	rp.attrView = runtime.NewDeviceAttributeView()
	// Seed a STALE in-memory entry the recheck must overwrite from the store.
	rp.attrView.ReplaceDevice("acme", "d1", []runtime.AttrEntry{{Tenant: "acme", DeviceToken: "d1", Scope: "SERVER", Key: "lim", Value: 999}})

	rp.applyAttrRecheck(attrUpdate{tenant: "acme", deviceToken: "d1"})
	if got := rp.attrView.For("acme", "d1")["lim"]; got != 50 {
		t.Errorf("recheck must install the store value: got %v, want 50", got)
	}

	// Purge the device (as the entity-deleted teardown does), then recheck: the entry must drop.
	if err := store.PurgeDevice(ctx, "acme", "d1", testBase.Add(1)); err != nil {
		t.Fatalf("purge: %v", err)
	}
	rp.applyAttrRecheck(attrUpdate{tenant: "acme", deviceToken: "d1"})
	if m := rp.attrView.For("acme", "d1"); m != nil {
		t.Errorf("a purged device must drop from the view, got %v", m)
	}
}

// TestApplyAttrRecheckRetriesOnError proves a transient LoadDevice failure does NOT silently strand a
// stale bound: the recheck is queued and the ticker retry heals it once the store recovers. A
// cancelled context stands in for the transient DB error.
func TestApplyAttrRecheckRetriesOnError(t *testing.T) {
	store := newProcAttributeStore(t)
	if err := store.Upsert(context.Background(), &model.DeviceAttribute{
		Tenant: "acme", DeviceToken: "d1", Scope: "SERVER", AttrKey: "lim", Value: 50, LastEventAt: testBase}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.AttributeStore = store
	rp.attrView = runtime.NewDeviceAttributeView()
	at := attrUpdate{tenant: "acme", deviceToken: "d1"}

	// First recheck under a CANCELLED context: LoadDevice errors → deferred to the retry set, view
	// left unchanged (no spurious empty).
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	rp.procCtx = cctx
	rp.applyAttrRecheck(at)
	if _, pending := rp.attrRetries[at]; !pending {
		t.Fatal("a failed recheck must be queued for retry")
	}
	if m := rp.attrView.For("acme", "d1"); m != nil {
		t.Fatalf("view must be unchanged on a failed recheck, got %v", m)
	}

	// Ticker retry under a LIVE context heals it: the value installs and the retry set clears.
	rp.procCtx = context.Background()
	rp.retryAttrRechecks(rp.clock.Now().Add(time.Hour))
	if got := rp.attrView.For("acme", "d1")["lim"]; got != 50 {
		t.Fatalf("retry must install the value: got %v, want 50", got)
	}
	if len(rp.attrRetries) != 0 {
		t.Fatalf("a healed retry must clear the set, got %d entries", len(rp.attrRetries))
	}
}

// TestAttributeConsumerSignalsViewRecheck is the end-to-end live wiring: the consumer persists a fact
// and signals a recheck onto the loop; draining that signal into applyAttrRecheck lands the converged
// value in the view. A set lands the value; a later removal drops the key — the whole "goes live"
// path, minus the loop's select (driven here directly). The two facts run through SEPARATE consumer
// invocations against the same durable store so the assertion after each is deterministic: since a
// recheck reads the LIVE projection (not the fact), a set-recheck racing an already-applied removal
// would correctly read the removed state — the split pins each transition instead.
func TestAttributeConsumerSignalsViewRecheck(t *testing.T) {
	store := newProcAttributeStore(t)
	rp := newTestProcessor(newTestStore(t), nil, 1)
	rp.AttributeStore = store
	rp.attrView = runtime.NewDeviceAttributeView()
	rp.attrUpdates = make(chan attrUpdate, 8)

	// A set fact: persist + signal, then the driven recheck lands 50 in the view.
	feedAttrFact(t, rp, &dmmodel.DeviceAttributeEvent{DeviceToken: "d1", AttrKey: "lim", Scope: "SERVER", Value: 50, UpdatedAt: testBase})
	rp.applyAttrRecheck(drainAttrUpdate(t, rp))
	if got := rp.attrView.For("acme", "d1")["lim"]; got != 50 {
		t.Fatalf("after the set fact + recheck, view lim = %v, want 50", got)
	}

	// A removal fact (strictly newer): the recheck now reads no live rows and drops the device.
	feedAttrFact(t, rp, &dmmodel.DeviceAttributeEvent{DeviceToken: "d1", AttrKey: "lim", Scope: "SERVER", Removed: true, UpdatedAt: testBase.Add(1)})
	rp.applyAttrRecheck(drainAttrUpdate(t, rp))
	if m := rp.attrView.For("acme", "d1"); m != nil {
		t.Fatalf("after the removal fact + recheck, the device must drop from the view, got %v", m)
	}
}

// feedAttrFact runs one device-attribute fact through a fresh consumer invocation (single message,
// then it blocks until cancel), returning after the fact is persisted-and-acked and its recheck is
// queued on attrUpdates. Running one fact per invocation keeps the caller's post-fact assertion
// race-free (the consumer cannot run ahead to a later fact).
func feedAttrFact(t *testing.T, rp *ResolvedEventsProcessor, ev *dmmodel.DeviceAttributeEvent) {
	t.Helper()
	ack := &fakeAck{acked: make(chan struct{}, 1)}
	rp.AttributeReader = &fakeReader{results: []readResult{{msg: attrMsg(t, "acme", ev, ack)}}}
	ctx, cancel := context.WithCancel(context.Background())
	rp.procCtx = ctx
	rp.readerWG.Add(1)
	go rp.runAttributeConsumer()
	waitForAck(t, ack)
	cancel()
	rp.readerWG.Wait()
	// Restore a live context so the caller's applyAttrRecheck (which reads the store through
	// rp.procCtx) does not run against the just-cancelled consumer context.
	rp.procCtx = context.Background()
}

// drainAttrUpdate pulls one recheck signal the consumer marshaled onto the loop, failing if none
// arrived (the consumer must signal after every persist).
func drainAttrUpdate(t *testing.T, rp *ResolvedEventsProcessor) attrUpdate {
	t.Helper()
	select {
	case au := <-rp.attrUpdates:
		return au
	default:
		t.Fatal("expected a recheck signal on attrUpdates, got none")
		return attrUpdate{}
	}
}
