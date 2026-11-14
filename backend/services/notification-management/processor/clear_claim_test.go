// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-notification-management/model"
)

// The claim is judged by cycle, not by comparing clocks. A clear whose event time runs ahead of
// this pod's clock (a skewed device, another pod) must still be announced exactly once.
func TestAClearWithAFutureEventTimeIsAnnouncedOnce(t *testing.T) {
	n, ra, ctx := clearHarness(t, true)
	raised := time.Now().UTC().Add(-time.Hour)
	if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, raised, raised)); err != nil {
		t.Fatal(err)
	}
	clr := cycleEvent(dmmodel.AlarmEventCleared, raised, time.Now().UTC().Add(300*time.Second))
	for i := 0; i < 3; i++ {
		if err := n.Notify(ctx, clr); err != nil {
			t.Fatal(err)
		}
	}
	// A late-path dispatch of the same cycle must not announce it again either.
	if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, raised, raised)); err != nil {
		t.Fatal(err)
	}
	clears := 0
	for _, g := range ra.types() {
		if g == "CLEARED" {
			clears++
		}
	}
	if clears != 1 {
		t.Fatalf("channel heard %v, want exactly one CLEARED", ra.types())
	}
}

// Two claimants racing on the same cycle: exactly one wins.
func TestExactlyOneConcurrentClaimantWinsTheClearAnnouncement(t *testing.T) {
	n, _, ctx := clearHarness(t, true)
	// :memory: belongs to one SQLite connection. An unrestricted pool opens
	// empty databases under contention; real multi-connection claims are tested
	// against Postgres in model/claims_integration_test.go.
	pool, err := stateDB(n.api).DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(1)
	raised := time.Now().UTC().Add(-time.Hour)
	if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, raised, raised)); err != nil {
		t.Fatal(err)
	}
	// Stamp the clear without announcing it, so the claim is what is contended.
	if err := n.api.MarkCleared(ctx, "a1", "k", "CRITICAL", raised, time.Now().UTC().Add(300*time.Second)); err != nil {
		t.Fatal(err)
	}
	states, err := n.api.NotificationStatesByAlarmToken(ctx, []string{"a1"})
	if err != nil || len(states) != 1 {
		t.Fatalf("state: %v %d", err, len(states))
	}
	cycle := states[0].CycleRaisedAt
	var wins atomic.Int32
	var losses atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			won, err := n.api.ClaimClearAnnouncement(ctx, "a1", cycle, time.Now().UTC().Add(time.Duration(i)*time.Nanosecond))
			if err != nil {
				t.Errorf("claim %d: %v", i, err)
				return
			}
			if won {
				wins.Add(1)
			} else {
				losses.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load()+losses.Load() != 8 {
		t.Errorf("%d wins + %d losses, want 8 completed claims", wins.Load(), losses.Load())
	}
	if wins.Load() != 1 {
		t.Fatalf("%d claimants won, want exactly 1", wins.Load())
	}
}

// A claim left by the previous cycle (an older pod advanced the row without knowing the column)
// must not suppress the new cycle's clear.
func TestAStalePreviousCycleClaimDoesNotSuppressTheNewCycle(t *testing.T) {
	n, ra, ctx := clearHarness(t, true)
	c1 := time.Now().UTC().Add(-3 * time.Hour)
	c2 := time.Now().UTC().Add(-time.Hour)
	_ = n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, c1, c1))
	_ = n.Notify(ctx, cycleEvent(dmmodel.AlarmEventCleared, c1, c1.Add(time.Minute)))
	_ = n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, c2, c2))
	// An older pod moved the row to cycle two and cleared what it knew, leaving cycle one's claim.
	db := stateDB(n.api).WithContext(ctx)
	if err := db.Model(&model.NotificationState{}).Where("alarm_token = ?", "a1").
		Update("clear_notified_at", time.Now().UTC().Add(time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventCleared, c2, c2.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	got := ra.types()
	if len(got) != 4 || got[3] != "CLEARED" {
		t.Fatalf("channel heard %v, want both cycles raised and cleared", got)
	}
}

// A channel renamed between the page and the clear still gets the clear: the paged set is keyed
// by the channel's id, not its token.
func TestARenamedChannelStillGetsTheClear(t *testing.T) {
	n, ra, ctx := clearHarness(t, true)
	raised := time.Now().UTC().Add(-time.Hour)
	if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, raised, raised)); err != nil {
		t.Fatal(err)
	}
	db := stateDB(n.api).WithContext(ctx)
	if err := db.Model(&model.NotificationChannel{}).Where("token = ?", "smtp-1").
		Update("token", "smtp-renamed").Error; err != nil {
		t.Fatal(err)
	}
	if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventCleared, raised, raised.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if got := ra.types(); len(got) != 2 || got[1] != "CLEARED" {
		t.Fatalf("channel heard %v, want [RAISED CLEARED]", got)
	}
}

// A clear that is stamped but never announced (it overtook its page, or its first send failed
// after both messages were acknowledged) is not lost: the scheduler's pickup sends it, once, and
// a pickup that cannot send gives the claim back for the next tick.
func TestTheSchedulerAnnouncesAPendingClearOnce(t *testing.T) {
	api := fencedApi(t)
	ctx := tenantScoped()
	seedClearPolicy(t, api, ctx, true)
	fa := &fakeAdapter{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa})
	n.api = api
	n.store = &fakeSecretStore{}
	// Raised and cleared AFTER the policy opted in (see the opt-in cut-off), and picked up on a
	// later tick, past the backoff.
	raised := time.Now().UTC().Add(time.Second)
	later := time.Now().Add(10 * time.Minute)
	if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, raised, raised)); err != nil {
		t.Fatal(err)
	}
	// Stamped cleared with nobody announcing it.
	if err := api.MarkCleared(ctx, "a1", "k", "CRITICAL", raised, raised.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	policies, err := api.EnabledNotificationPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fa.calls, fa.failTimes = 0, 1<<20
	if err := n.AnnouncePendingClears(ctx, policies, later); err == nil {
		t.Fatal("a pickup that could not send must report it")
	}
	fa.calls, fa.failTimes = 0, 0
	for i := 0; i < 3; i++ {
		if err := n.AnnouncePendingClears(ctx, policies, later); err != nil {
			t.Fatal(err)
		}
	}
	if fa.calls != 1 {
		t.Fatalf("the pending clear was sent %d times, want 1", fa.calls)
	}
}
