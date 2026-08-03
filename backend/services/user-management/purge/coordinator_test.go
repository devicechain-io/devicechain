// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package purge

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
)

// settleWindow is the window every test uses. Short enough to step over, long enough that
// a bug which restarts it on every pass cannot be mistaken for one that does not.
const settleWindow = 5 * time.Minute

// fakeStore is a store whose outcome the test dictates.
//
// It exists so the coordinator's DECISIONS can be tested — when a purge completes, when
// the settle window holds it back, what the ledger records — without a real database on
// the far side of every one of them. The real relational store is exercised against real
// Postgres by the migration harness's purge drill, which is where a classification and a
// delete order can actually be wrong.
type fakeStore struct {
	name     string
	calls    int
	rows     int64
	deferred []string
	err      error
	// failFor makes the store fail for one tenant only, so a test can have two tenants
	// in the same pass reach different outcomes through the same registered store.
	failFor string
}

func (f *fakeStore) Name() string { return f.name }

func (f *fakeStore) Erase(_ context.Context, tenant string, _ time.Time) (Outcome, error) {
	f.calls++
	if f.failFor != "" && f.failFor == tenant {
		return Outcome{}, errors.New("this store is stuck for " + tenant)
	}
	return Outcome{Rows: f.rows, Deferred: f.deferred}, f.err
}

// harness is a coordinator over an in-memory database, with a clock the test moves.
type harness struct {
	t     *testing.T
	iam   *iam.Store
	coord *Coordinator
	clock time.Time
}

func newHarness(t *testing.T, stores ...Store) *harness {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, db.AutoMigrate(&iam.Tenant{}, &iam.TenantPurge{}, &iam.TenantPurgeStore{}))

	mgr := &rdb.RdbManager{Database: db}
	store := iam.NewStore(mgr)
	h := &harness{t: t, iam: store, clock: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)}
	h.coord = NewCoordinator(&core.Microservice{FunctionalArea: "user-management"}, store, mgr,
		stores, time.Minute, settleWindow, core.NewNoOpLifecycleCallbacks())
	h.coord.now = func() time.Time { return h.clock }
	return h
}

// deleteTenant creates a tenant and walks it through the real delete door.
func (h *harness) deleteTenant(token string) *iam.Tenant {
	h.t.Helper()
	ctx := context.Background()
	tenant := &iam.Tenant{Token: token}
	require.NoError(h.t, h.iam.CreateTenant(ctx, tenant))
	require.NoError(h.t, h.iam.BeginTenantPurge(ctx, tenant, h.clock))
	reloaded, err := h.iam.TenantByToken(ctx, token)
	require.NoError(h.t, err)
	return reloaded
}

// pass runs one coordinator pass over every purging tenant — the real loop, reached the
// way the ticker reaches it, minus the Postgres advisory lock the in-memory database has
// no equivalent for.
func (h *harness) pass() {
	h.t.Helper()
	require.NoError(h.t, h.coord.pass(context.Background()))
}

// exists reports whether the tenant row is still there, which is the same question as
// "is the token still reserved".
func (h *harness) exists(token string) bool {
	h.t.Helper()
	var n int64
	require.NoError(h.t, h.coord.db.Database.Unscoped().Model(&iam.Tenant{}).
		Where("token = ?", token).Count(&n).Error)
	return n > 0
}

// ledger returns a tenant's ledger lines by store name.
func (h *harness) ledger(token string, epoch time.Time) map[string]iam.TenantPurgeStore {
	h.t.Helper()
	ctx := context.Background()
	rec, err := h.iam.EnsurePurgeRecord(ctx, token, epoch)
	require.NoError(h.t, err)
	lines, err := h.iam.PurgeStores(ctx, rec.ID)
	require.NoError(h.t, err)
	out := make(map[string]iam.TenantPurgeStore, len(lines))
	for _, l := range lines {
		out[l.Store] = l
	}
	return out
}

// TestTheInterlockRefusesATenantThatIsNotPurging is the guard core/tenantpurge.Sweep
// documents and deliberately cannot enforce.
//
// The assertion that matters is not the error — it is that the store was never ASKED.
// The sweep commits in one transaction, so by the time it has been called on a live
// tenant that tenant is completely and atomically gone, and an error afterwards is a
// report, not a defence.
//
// 🔴 THE TENANT HERE CARRIES AN EPOCH, AND THAT DETAIL IS THE TEST. Written the obvious
// way — a plain live tenant, assert an error — it passes with the lifecycle check
// DELETED, because a live tenant also has no epoch and the next guard refuses it for a
// different reason. Verified by mutation: removing the lifecycle check left the whole
// suite green. Giving this tenant an epoch leaves the lifecycle check as the only thing
// that can refuse it, and asserting on the lifecycle wording keeps it that way.
func TestTheInterlockRefusesATenantThatIsNotPurging(t *testing.T) {
	store := &fakeStore{name: "rdb"}
	h := newHarness(t, store)
	ctx := context.Background()

	live := &iam.Tenant{Token: "still-in-business"}
	require.NoError(t, h.iam.CreateTenant(ctx, live))
	epoch := h.clock
	live.PurgeEpoch = &epoch
	require.Equal(t, iam.PurgeActive, live.PurgeState)

	err := h.coord.PurgeTenant(ctx, live)
	require.Error(t, err)
	require.Contains(t, err.Error(), "still-in-business")
	require.Contains(t, err.Error(), "its lifecycle is",
		"the refusal must come from the lifecycle check, not from a later guard")
	require.Zero(t, store.calls, "an erasure must not be attempted for a tenant that is not purging")
	require.True(t, h.exists("still-in-business"))
}

// TestTheInterlockRefusesAPurgingTenantWithNoEpoch covers the other half. Without an
// epoch the deletion record has no identity — (token, epoch) is its key — so a purge that
// ran anyway would erase the data and have nowhere to record that it had.
func TestTheInterlockRefusesAPurgingTenantWithNoEpoch(t *testing.T) {
	store := &fakeStore{name: "rdb"}
	h := newHarness(t, store)
	ctx := context.Background()

	tenant := h.deleteTenant("acme")
	tenant.PurgeEpoch = nil

	err := h.coord.PurgeTenant(ctx, tenant)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no epoch")
	require.Zero(t, store.calls)
}

// TestAPendingStoreHoldsTheTokenForever is the honest-accounting property, and it is what
// stops this slice from claiming more than it does.
//
// A store that is registered but not yet built reports what it still holds on every pass.
// The purge therefore never completes and the token is never released — which is correct,
// because the tenant's data really is still in that store. The failure this prevents is
// the one where the store is simply not registered: then nothing reports anything, every
// registered store is clean, and the coordinator releases the token over data it never
// looked at.
func TestAPendingStoreHoldsTheTokenForever(t *testing.T) {
	clean := &fakeStore{name: "rdb", rows: 12}
	held := Pending{StoreName: "tsdb", Holds: "the event store still holds this tenant's telemetry"}
	h := newHarness(t, clean, held)

	tenant := h.deleteTenant("acme")
	for i := 0; i < 5; i++ {
		h.pass()
		h.clock = h.clock.Add(time.Hour)
	}

	require.True(t, h.exists("acme"), "a purge with an unerased store must never complete")

	lines := h.ledger("acme", *tenant.PurgeEpoch)
	require.True(t, lines["rdb"].Complete)
	require.False(t, lines["tsdb"].Complete)
	require.Contains(t, lines["tsdb"].Deferred, "the event store still holds",
		"the ledger must say what is retained, in words, on every purge")
	require.Empty(t, lines["tsdb"].Failure, "a pending store is a stated gap, not a transient failure")
}

// TestCompletionWaitsOutTheSettleWindow pins the difference between "the delete returned
// no error" and "nothing came back".
//
// A residual scan run immediately after a sweep can only see writes that were already in
// flight; a straggler admitted just before the ingest fence noticed the deletion lands
// afterwards. So one clean pass is not enough, and the token is not released until every
// store has been clean across the window.
func TestCompletionWaitsOutTheSettleWindow(t *testing.T) {
	h := newHarness(t, &fakeStore{name: "rdb", rows: 9})
	h.deleteTenant("acme")

	h.pass()
	require.True(t, h.exists("acme"), "one clean pass must not release the token")

	h.clock = h.clock.Add(settleWindow - time.Second)
	h.pass()
	require.True(t, h.exists("acme"), "a second short of the window is still short of it")

	h.clock = h.clock.Add(2 * time.Second)
	h.pass()
	require.False(t, h.exists("acme"), "once the window has elapsed the token is released")
}

// TestTheSettleWindowRunsFromTheFirstCleanPass pins the timestamp's carry-forward, which
// is the difference between a window that can elapse and one that cannot.
//
// If each pass restamped clean-since, "clean for five minutes" would be measured from the
// most recent pass and never reach the window — the purge would run forever while every
// store reported clean, which is a stall that looks exactly like healthy operation.
func TestTheSettleWindowRunsFromTheFirstCleanPass(t *testing.T) {
	h := newHarness(t, &fakeStore{name: "rdb"})
	tenant := h.deleteTenant("acme")
	first := h.clock

	// Several passes inside the window, none of which may move the timestamp.
	for i := 0; i < 3; i++ {
		h.pass()
		h.clock = h.clock.Add(time.Minute)
	}
	line := h.ledger("acme", *tenant.PurgeEpoch)["rdb"]
	require.NotNil(t, line.CleanSince)
	require.WithinDuration(t, first, *line.CleanSince, time.Second,
		"clean-since must date the FIRST clean pass, not the latest one")
}

// TestAStoreGoingDirtyRestartsTheSettleWindow is the other side of the same property.
// Carrying the timestamp forward is only safe while the store keeps reporting clean; a
// store that finds rows again has proved a writer is still live, and the window has to
// start over from that point rather than counting the quiet time before it.
func TestAStoreGoingDirtyRestartsTheSettleWindow(t *testing.T) {
	store := &fakeStore{name: "rdb"}
	h := newHarness(t, store)
	tenant := h.deleteTenant("acme")

	h.pass()
	firstClean := h.ledger("acme", *tenant.PurgeEpoch)["rdb"].CleanSince
	require.NotNil(t, firstClean)

	// A resurrection vector fires: something wrote for the tenant again.
	h.clock = h.clock.Add(time.Minute)
	store.err = errors.New("3 row(s) still identify as \"acme\" after the sweep")
	h.pass()
	dirty := h.ledger("acme", *tenant.PurgeEpoch)["rdb"]
	require.False(t, dirty.Complete)
	require.Nil(t, dirty.CleanSince, "a store that reports residue loses its clean-since")
	require.Contains(t, dirty.Failure, "still identify")

	// It settles again — and the window now runs from HERE, not from the first pass.
	h.clock = h.clock.Add(time.Minute)
	restart := h.clock
	store.err = nil
	h.pass()
	again := h.ledger("acme", *tenant.PurgeEpoch)["rdb"].CleanSince
	require.NotNil(t, again)
	require.WithinDuration(t, restart, *again, time.Second)

	// The elapsed time from the FIRST clean pass is now past the window, so a coordinator
	// that had kept the original timestamp would complete here. It must not.
	h.clock = h.clock.Add(settleWindow - time.Minute)
	h.pass()
	require.True(t, h.exists("acme"),
		"the window must be measured from the restart, or a resurrection buys nothing")
}

// TestEveryStoreIsAskedEvenWhenOneFails guards a plausible refactor: aborting the tenant
// on the first error. A store that is down would then hide every other store's state, so
// the ledger would say nothing about them and an operator diagnosing a stuck purge would
// be reading a report shaped by the order the stores happen to be registered in.
func TestEveryStoreIsAskedEvenWhenOneFails(t *testing.T) {
	first := &fakeStore{name: "rdb", err: errors.New("connection refused")}
	second := &fakeStore{name: "broker", rows: 4}
	h := newHarness(t, first, second)
	tenant := h.deleteTenant("acme")

	h.pass()

	require.Equal(t, 1, first.calls)
	require.Equal(t, 1, second.calls, "a failing store must not stop the ones after it")
	lines := h.ledger("acme", *tenant.PurgeEpoch)
	require.Contains(t, lines["rdb"].Failure, "connection refused")
	require.True(t, lines["broker"].Complete)
	require.True(t, h.exists("acme"))
}

// TestCompletionSumsWhatEveryStoreErased checks the record carries the total rather than
// the last store's figure, which is the number an operator reads as "how much was erased".
func TestCompletionSumsWhatEveryStoreErased(t *testing.T) {
	h := newHarness(t, &fakeStore{name: "rdb", rows: 100}, &fakeStore{name: "broker", rows: 5})
	tenant := h.deleteTenant("acme")
	epoch := *tenant.PurgeEpoch

	h.pass()
	h.clock = h.clock.Add(settleWindow + time.Second)
	h.pass()

	require.False(t, h.exists("acme"))

	ctx := context.Background()
	rec, err := h.iam.EnsurePurgeRecord(ctx, "acme", epoch)
	require.NoError(t, err)
	require.True(t, rec.Complete())
	// 105 per pass, and the pass that completes is the second one.
	require.EqualValues(t, 105, rec.Rows)
}

// TestAStuckTenantDoesNotHoldUpTheQueue covers the pass loop: a purge that cannot finish
// must not stop one that can.
//
// The stuck tenant is FIRST in the queue, which is the arrangement that catches the
// plausible mistake — returning the tenant's error out of the pass instead of logging it
// would leave every tenant behind it unpurged, and on a queue drained oldest-first that
// means one wedged purge freezes every later deletion indefinitely.
func TestAStuckTenantDoesNotHoldUpTheQueue(t *testing.T) {
	h := newHarness(t, &fakeStore{name: "rdb", failFor: "wedged"})
	h.deleteTenant("wedged")
	h.clock = h.clock.Add(time.Minute) // a later cut, so "wedged" sorts first
	h.deleteTenant("healthy")

	h.pass()
	h.clock = h.clock.Add(settleWindow + time.Second)
	h.pass()

	require.True(t, h.exists("wedged"), "the stuck tenant's token stays reserved")
	require.False(t, h.exists("healthy"), "a tenant behind a stuck one must still complete")
}

// TestATenantThePassCannotEvenStartDoesNotAbortIt is the isolation property at the level
// where it can actually break.
//
// A stuck STORE does not make PurgeTenant return an error — the failure is recorded in
// the ledger and the pass moves on — so the test above cannot see whether the pass
// tolerates an error at all. Verified by mutation: turning the pass's log-and-continue
// into a `return err` left that test green. What DOES error out of PurgeTenant is a
// tenant the coordinator refuses to touch, and the refusal is permanent, so a pass that
// aborted on it would freeze every later deletion on the instance forever.
func TestATenantThePassCannotEvenStartDoesNotAbortIt(t *testing.T) {
	h := newHarness(t, &fakeStore{name: "rdb"})
	h.deleteTenant("corrupt")
	h.clock = h.clock.Add(time.Minute) // a later cut, so "corrupt" sorts first
	h.deleteTenant("healthy")

	// A purging row whose epoch is missing: the coordinator refuses it outright, because
	// its deletion record would have no identity.
	require.NoError(t, h.coord.db.Database.Model(&iam.Tenant{}).
		Where("token = ?", "corrupt").Update("purge_epoch", nil).Error)

	h.pass()
	h.clock = h.clock.Add(settleWindow + time.Second)
	h.pass()

	require.True(t, h.exists("corrupt"), "a tenant the coordinator refuses keeps its token")
	require.False(t, h.exists("healthy"), "a refusal must not stop the rest of the queue")
}

// TestACoordinatorWithNoStoresRefusesToCompleteAnything is the failure this whole design
// exists to make impossible, arriving through the coordinator rather than through a store.
//
// With no stores registered, every completeness check passes vacuously: nothing reports
// anything unclean, so `allClean` stays true, and the settle window is measured against a
// zero-value timestamp that is always long elapsed. Without an explicit refusal the purge
// completes on its FIRST pass, releasing the token and recording an erasure having asked
// nobody — the most reassuring possible route to the exact outcome ADR-077 exists to
// prevent. Confirmed by removing the guard: this tenant's token was released immediately.
func TestACoordinatorWithNoStoresRefusesToCompleteAnything(t *testing.T) {
	h := newHarness(t)
	h.deleteTenant("acme")

	h.pass()
	h.clock = h.clock.Add(settleWindow + time.Hour)
	h.pass()

	require.True(t, h.exists("acme"),
		"a coordinator that asks nobody must never conclude a tenant's data is gone")
}
