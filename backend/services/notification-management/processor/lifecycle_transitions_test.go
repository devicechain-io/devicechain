// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// The three state-only alarm transitions — ACKNOWLEDGED, CLEARED, DEESCALATED — used to
// call straight through to the persistence api from Notify's switch. That made them the
// one door into this service that skipped both write guards the paging path has: the
// ADR-077 lifecycle gate, and the recognition that the erasure fence's refusal is
// PERMANENT rather than a failure worth five redeliveries.
//
// These tests use the same instrument as the dispatch gate tests one file over: a
// PolicyNotifier with a NIL api. "Did the refusal happen before the write?" is then
// answerable by whether the call survives at all — an assertion that merely checked the
// returned error would pass just as well with the gate placed after the write it is
// supposed to prevent.

// lifecycleEvent builds one state-only transition of the given type.
func lifecycleEvent(t dmmodel.AlarmEventType) *dmmodel.AlarmStateChangeEvent {
	return &dmmodel.AlarmStateChangeEvent{
		EventType: t, AlarmToken: "a1", AlarmKey: "k", Severity: "CRITICAL",
		State: "ACTIVE", OccurredTime: time.Now().UTC(),
	}
}

// lifecycleEventTypes is the set under test — every Notify branch that writes without
// paging. Enumerated rather than spot-checked because the defect was per-branch: fixing
// one and not its two siblings would look identical from any single-case test.
var lifecycleEventTypes = []dmmodel.AlarmEventType{
	dmmodel.AlarmEventAcknowledged,
	dmmodel.AlarmEventCleared,
	dmmodel.AlarmEventDeescalated,
}

// TestLifecycleTransitionsRefuseADeletedTenantBeforeWriting pins the gap: each of the
// three write-only transitions must consult the ADR-077 gate BEFORE touching the api.
//
// Writing here is not harmless just because nobody is paged. markTerminal CREATES a row
// when none exists, so a deleted tenant's late CLEARED planted a fresh row in a schema
// the purge sweep was erasing — which loses the sweep's clean-since and restarts the
// settle window it cannot complete without.
func TestLifecycleTransitionsRefuseADeletedTenantBeforeWriting(t *testing.T) {
	for _, et := range lifecycleEventTypes {
		n := gatedNotifier(map[string]ChannelAdapter{}, true)
		var err error
		if panicked(func() { err = n.Notify(deletedTenantCtx(), lifecycleEvent(et)) }) {
			t.Fatalf("%s: the refusal must come before the state write; it reached the (nil) api instead", et)
		}
		if err != nil {
			t.Fatalf("%s: a refusal must return nil so the consumer acks, got %v", et, err)
		}
	}
}

// TestLifecycleTransitionsForALiveTenantStillWrite is the counterweight and the proof the
// instrument works: with the gate answering "not deleted", every one of the same three
// calls must get past it and reach the nil api. Without this, the test above would pass
// against a Notify that returned nil for these event types unconditionally — which is to
// say, against a service that silently stopped tracking acknowledgements.
func TestLifecycleTransitionsForALiveTenantStillWrite(t *testing.T) {
	for _, et := range lifecycleEventTypes {
		n := gatedNotifier(map[string]ChannelAdapter{}, false)
		if !panicked(func() { _ = n.Notify(deletedTenantCtx(), lifecycleEvent(et)) }) {
			t.Fatalf("%s: a live tenant's transition must reach the api; it did not", et)
		}
	}
}

// TestAPurgedTenantsWriteIsPermanentNotTransient pins the classification.
//
// The ADR-077 gate above fails open by design — a 60s TTL, and it goes blind entirely
// once the purge removes the tenant row — so the erasure FENCE is what actually refuses
// the write. Its refusal used to leave applyLifecycle's caller with an ordinary error,
// which the processor reads as retryable: five redeliveries an AckWait apart, refused
// identically each time, ending in a dead letter that reports an alarm nobody was paged
// about. For a deleted tenant there is nobody to page.
//
// It drives applyLifecycle directly because the write is what has to return the sentinel,
// and the persistence api is a concrete type with no seam a fake could take.
func TestAPurgedTenantsWriteIsPermanentNotTransient(t *testing.T) {
	n := testNotifier(nil)
	called := 0
	// Wrapped, as it actually arrives: the fence adds the tenant token to the sentinel,
	// and gorm surfaces it through the statement's error. An errors.Is check survives
	// that; an equality check would not.
	err := n.applyLifecycle(tenantScoped(), "a1", "clear", func(context.Context) error {
		called++
		return fmt.Errorf("stamping cleared_at: %w (tenant %q)", rdb.ErrTenantPurged, "acme")
	})
	if called != 1 {
		t.Fatalf("the write ran %d time(s), want 1", called)
	}
	if err != nil {
		t.Fatalf("a purged tenant's refused write must be dropped, not retried; got %v", err)
	}
}

// TestAnOrdinaryWriteFailureIsStillRetried is the counterweight. Without it, an
// applyLifecycle that swallowed EVERY error would satisfy the test above while silently
// discarding the acknowledgement that stops an alarm escalating — a resolved alarm paging
// an operator forever.
func TestAnOrdinaryWriteFailureIsStillRetried(t *testing.T) {
	n := testNotifier(nil)
	boom := errors.New("connection refused")
	err := n.applyLifecycle(tenantScoped(), "a1", "clear", func(context.Context) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("an ordinary write failure must surface for redelivery, got %v", err)
	}
}

// TestALifecycleWriteIsBounded pins the deadline these transitions had none of. They run
// on the durable consumer's worker context, which is a context.Background() — so without
// their own timeout, a stalled database holds a worker on a state stamp indefinitely, and
// graceful shutdown behind it. The Notifier contract requires the implementation to bound
// itself; nothing else will.
func TestALifecycleWriteIsBounded(t *testing.T) {
	n := testNotifier(nil)
	var deadline time.Duration
	var had bool
	_ = n.applyLifecycle(tenantScoped(), "a1", "clear", func(c context.Context) error {
		if dl, ok := c.Deadline(); ok {
			had, deadline = true, time.Until(dl)
		}
		return nil
	})
	if !had {
		t.Fatal("the lifecycle write was handed a context with NO deadline: on the consumer path " +
			"that is the worker's context.Background(), which never fires")
	}
	if deadline > recordTimeout || deadline < recordTimeout-time.Second {
		t.Fatalf("lifecycle write deadline was %v away, want ~%v (recordTimeout)", deadline, recordTimeout)
	}
}

// fencedApi builds a real persistence Api over an in-memory database carrying the REAL
// erasure-fence callbacks, with a fence standing for the given tenants. Nothing is faked:
// the refusal these tests classify is the one rdb.RegisterTenantFence produces, wrapped
// exactly as it will be in production.
func fencedApi(t *testing.T, fenced ...string) *model.Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := rdb.RegisterTenantFence(db); err != nil {
		t.Fatalf("register the fence: %v", err)
	}
	if err := db.AutoMigrate(&model.NotificationChannel{}, &model.NotificationPolicy{},
		&model.NotificationRule{}, &model.NotificationState{}, &rdb.PurgedTenant{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	api := model.NewApi(&rdb.RdbManager{Database: db}, nil)
	// Fences are planted here only for the tenants the caller names up front; a test that
	// has to SEED before fencing (so the write it classifies would otherwise have
	// succeeded) plants its own with plantFence afterwards.
	for _, tenant := range fenced {
		plantFence(t, db, tenant)
	}
	return api
}

// stateDB reaches the gorm handle behind an Api so a test can plant a fence after seeding.
func stateDB(api *model.Api) *gorm.DB { return api.RDB.Database }

// plantFence stands an active fence for a token, as the purge's first pass does.
func plantFence(t *testing.T, db *gorm.DB, token string) {
	t.Helper()
	now := time.Now().UTC()
	err := db.Session(&gorm.Session{NewDB: true}).
		WithContext(core.WithSystemContext(context.Background())).
		Create(&rdb.PurgedTenant{Token: token, Epoch: now, PlantedAt: now}).Error
	if err != nil {
		t.Fatalf("planting a fence for %q: %v", token, err)
	}
}

// escalationFixture is one open alarm with an escalation-enabled policy whose window has
// elapsed, so Escalate will plan a delivery and reach ClaimEscalation.
func escalationFixture() (*model.NotificationState, []*model.NotificationPolicy, time.Time) {
	now := time.Now().UTC()
	smtp := enabledChannel("smtp-1", model.ChannelTypeSMTP)
	p := escalatingPolicy("p", 300, 0, rule("CRITICAL", smtp, "ops@x.com"))
	// Tier 0, matching the row RecordNotification seeds: ClaimEscalation is a
	// compare-and-swap on escalation_level, so a fixture at a tier the row is not at would
	// lose the claim and never deliver — which would make the terminal-classification test
	// above pass for the wrong reason.
	return openState("CRITICAL", now.Add(-10*time.Minute), 0), []*model.NotificationPolicy{p}, now
}

// TestEscalateTreatsAPurgedTenantAsTerminal is the same classification as
// applyLifecycle's, on the path that needs it most.
//
// ClaimEscalation is an UPDATE, so the fence refuses it — and this is precisely where the
// ADR-077 lifecycle gate cannot help, because the gate goes blind the moment the purge
// removes the tenant row and an unresolvable tenant reads as active. The scheduler retries
// a returned error on EVERY tick, so surfacing the refusal would log a failure once a
// minute, per open alarm, until the sweep removed the rows.
//
// The state row is seeded BEFORE the fence is planted, so the claim would genuinely have
// succeeded. Without that, "no error" would be reachable by the ordinary CAS-lost path and
// this test would prove nothing.
func TestEscalateTreatsAPurgedTenantAsTerminal(t *testing.T) {
	api := fencedApi(t)
	ctx := tenantScoped()
	state, policies, now := escalationFixture()
	if err := api.RecordNotification(ctx, state.AlarmToken, state.AlarmKey, state.Severity,
		now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	plantFence(t, stateDB(api), "acme")

	fa := &fakeAdapter{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa})
	n.api = api

	if err := n.Escalate(ctx, state, policies, now, 5); err != nil {
		t.Fatalf("a purged tenant's escalation must be terminal, not an error the scheduler "+
			"retries on every tick; got %v", err)
	}
	if fa.calls != 0 {
		t.Fatalf("a purged tenant must not be paged; the adapter was called %d time(s)", fa.calls)
	}
}

// TestEscalateStillPagesALiveTenant is the counterweight, and it is what makes the test
// above about the CLASSIFICATION rather than about escalation being broken: the identical
// fixture, with no fence planted, must claim the tier and deliver.
func TestEscalateStillPagesALiveTenant(t *testing.T) {
	api := fencedApi(t)
	ctx := tenantScoped()
	state, policies, now := escalationFixture()
	if err := api.RecordNotification(ctx, state.AlarmToken, state.AlarmKey, state.Severity,
		now.Add(-10*time.Minute)); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	fa := &fakeAdapter{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa})
	n.api = api
	n.store = &fakeSecretStore{}

	if err := n.Escalate(ctx, state, policies, now, 5); err != nil {
		t.Fatalf("a live tenant's escalation must succeed: %v", err)
	}
	if fa.calls != 1 {
		t.Fatalf("adapter called %d time(s), want 1: the fixture must actually reach a delivery, "+
			"or the terminal-classification test above is measuring an empty plan", fa.calls)
	}
}

// TestAPurgedTenantsLifecycleWriteIsDroppedEndToEnd drives the same rule through the REAL
// fence rather than a closure returning the sentinel, so it also pins that the error gorm
// surfaces from the callback is one errors.Is can still see.
func TestAPurgedTenantsLifecycleWriteIsDroppedEndToEnd(t *testing.T) {
	api := fencedApi(t, "acme")
	n := testNotifier(nil)
	n.api = api

	for _, et := range lifecycleEventTypes {
		if err := n.Notify(tenantScoped(), lifecycleEvent(et)); err != nil {
			t.Fatalf("%s: a fenced tenant's transition must be dropped, not retried; got %v", et, err)
		}
	}
}

// TestALiveTenantsLifecycleWriteLands is that test's counterweight: with no fence, the same
// three transitions must actually reach the table. Without it, an applyLifecycle that
// returned nil without writing would look identical.
func TestALiveTenantsLifecycleWriteLands(t *testing.T) {
	api := fencedApi(t)
	n := testNotifier(nil)
	n.api = api
	ctx := tenantScoped()

	if err := n.Notify(ctx, lifecycleEvent(dmmodel.AlarmEventCleared)); err != nil {
		t.Fatalf("clear: %v", err)
	}
	states, err := api.NotificationStatesByAlarmToken(ctx, []string{"a1"})
	if err != nil || len(states) != 1 {
		t.Fatalf("expected the clear to write a tombstone row, got %d (err %v)", len(states), err)
	}
	if !states[0].ClearedAt.Valid {
		t.Fatal("the tombstone row carries no cleared_at stamp")
	}
}

// TestARaisedEventStillDispatches guards the switch itself. applyLifecycle now wraps three
// of the five branches, and a mis-edit that routed RAISED through it would turn every page
// in the platform into a silent state write with no test noticing.
func TestARaisedEventStillDispatches(t *testing.T) {
	n := gatedNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: &fakeAdapter{}}, false)
	if !panicked(func() { _ = n.Notify(deletedTenantCtx(), raisedEvent("CRITICAL")) }) {
		t.Fatal("a RAISED event must reach dispatch (and thus the api); it did not")
	}
}
