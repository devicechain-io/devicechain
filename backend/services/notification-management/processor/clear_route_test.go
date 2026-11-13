// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-notification-management/model"
	"gorm.io/gorm"
)

// seedRoute stores a policy (created if absent) with one rule routing severity to a channel
// (created if absent) of the given type.
func seedRoute(t *testing.T, api *model.Api, ctx context.Context, policyToken string, notifyOnClear bool,
	severity, channelToken, channelType string) {
	t.Helper()
	db := stateDB(api).WithContext(ctx)
	var ch model.NotificationChannel
	if err := db.Where("token = ?", channelToken).First(&ch).Error; err != nil {
		ch = *enabledChannel(channelToken, channelType)
		if err := db.Create(&ch).Error; err != nil {
			t.Fatalf("seed channel: %v", err)
		}
	}
	var p model.NotificationPolicy
	if err := db.Where("token = ?", policyToken).First(&p).Error; err != nil {
		p = *policy(policyToken)
		p.NotifyOnClear = notifyOnClear
		if err := db.Create(&p).Error; err != nil {
			t.Fatalf("seed policy: %v", err)
		}
	}
	r := rule(severity, nil, "ops@x.com")
	r.PolicyId, r.ChannelId = p.ID, ch.ID
	if err := db.Create(&r).Error; err != nil {
		t.Fatalf("seed rule: %v", err)
	}
}

func twoChannelNotifier(t *testing.T, api *model.Api) (*PolicyNotifier, *msgRecorder, *msgRecorder) {
	t.Helper()
	smtp, hook := &msgRecorder{}, &msgRecorder{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: smtp, model.ChannelTypeWebhook: hook})
	n.api = api
	n.store = &fakeSecretStore{}
	return n, smtp, hook
}

// A tiered alarm raised CRITICAL (paged on the CRITICAL route), de-escalated to MAJOR (no page),
// then cleared: the clear must reach the channel that paged it (PagerDuty gets its resolve), not
// be routed by the MAJOR rule it never used.
func TestADeescalatedAlarmIsClearedOnTheChannelThatPagedIt(t *testing.T) {
	api := fencedApi(t)
	ctx := tenantScoped()
	seedRoute(t, api, ctx, "p", true, "CRITICAL", "paging", model.ChannelTypeSMTP)
	seedRoute(t, api, ctx, "p", true, "MAJOR", "chat", model.ChannelTypeWebhook)
	n, paging, chat := twoChannelNotifier(t, api)
	raised := time.Now().UTC().Add(-time.Hour)

	if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, raised, raised)); err != nil {
		t.Fatal(err)
	}
	de := cycleEvent(dmmodel.AlarmEventDeescalated, raised, raised.Add(time.Minute))
	de.Severity = "MAJOR"
	if err := n.Notify(ctx, de); err != nil {
		t.Fatal(err)
	}
	clr := cycleEvent(dmmodel.AlarmEventCleared, raised, raised.Add(2*time.Minute))
	clr.Severity = "MAJOR"
	if err := n.Notify(ctx, clr); err != nil {
		t.Fatal(err)
	}
	if got := paging.types(); len(got) != 2 || got[1] != "CLEARED" {
		t.Fatalf("the paging channel heard %v, want [RAISED CLEARED]", got)
	}
	if len(chat.sent) != 0 {
		t.Fatalf("a channel that never paged heard %v", chat.types())
	}
}

// A policy created after the raise has paged nothing, so it announces no clear.
func TestAPolicyThatNeverPagedStaysSilentOnClear(t *testing.T) {
	api := fencedApi(t)
	ctx := tenantScoped()
	seedRoute(t, api, ctx, "early", true, model.SeverityAny, "paging", model.ChannelTypeSMTP)
	n, paging, late := twoChannelNotifier(t, api)
	raised := time.Now().UTC().Add(-time.Hour)
	if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, raised, raised)); err != nil {
		t.Fatal(err)
	}
	seedRoute(t, api, ctx, "late", true, model.SeverityAny, "late-hook", model.ChannelTypeWebhook)
	if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventCleared, raised, raised.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if got := paging.types(); len(got) != 2 {
		t.Fatalf("the channel that paged heard %v", got)
	}
	if len(late.sent) != 0 {
		t.Fatalf("a policy that never paged announced a clear: %v", late.types())
	}
}

// The stamp is written before anything is sent, so a stamp that fails sends nothing, and the
// redelivery that succeeds sends the clear once.
func TestATransientStampFailureDoesNotResendTheClear(t *testing.T) {
	api := fencedApi(t)
	ctx := tenantScoped()
	seedClearPolicy(t, api, ctx, true)
	ra := &msgRecorder{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: ra})
	n.api = api
	n.store = &fakeSecretStore{}
	raised := time.Now().UTC().Add(-time.Hour)
	if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, raised, raised)); err != nil {
		t.Fatal(err)
	}

	fail := true
	if err := stateDB(api).Callback().Update().Before("gorm:update").Register("test:fail_stamp", func(tx *gorm.DB) {
		if fail && tx.Statement.Table == "notification_states" {
			fail = false
			_ = tx.AddError(errors.New("connection reset"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	clr := cycleEvent(dmmodel.AlarmEventCleared, raised, raised.Add(time.Minute))
	if err := n.Notify(ctx, clr); err == nil {
		t.Fatal("the failed stamp must be returned for redelivery")
	}
	if len(ra.sent) != 1 {
		t.Fatalf("nothing may be sent before the clear is stamped; channel heard %v", ra.types())
	}
	for i := 0; i < 2; i++ {
		if err := n.Notify(ctx, clr); err != nil {
			t.Fatal(err)
		}
	}
	if got := ra.types(); len(got) != 2 || got[1] != "CLEARED" {
		t.Fatalf("channel heard %v, want [RAISED CLEARED] exactly once", got)
	}
}

// The pool is unordered, so the CLEARED can be processed before the RAISED is recorded. The
// page's own dispatch owes the clear afterwards, once.
func TestAClearThatOvertookItsPageIsStillAnnounced(t *testing.T) {
	n, ra, ctx := clearHarness(t, true)
	raised := time.Now().UTC().Add(-time.Hour)
	if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventCleared, raised, raised.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if len(ra.sent) != 0 {
		t.Fatalf("nothing was paged yet, got %v", ra.types())
	}
	for i := 0; i < 2; i++ {
		if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, raised, raised)); err != nil {
			t.Fatal(err)
		}
	}
	got := ra.types()
	clears := 0
	for _, g := range got {
		if g == "CLEARED" {
			clears++
		}
	}
	if clears != 1 || got[0] != "RAISED" {
		t.Fatalf("channel heard %v, want the raise and exactly one clear after it", got)
	}
}

// An ESCALATED processed after the CLEARED pages no one, and the cleared alarm is no longer a
// candidate for the escalation scheduler.
func TestNoEscalationFollowsTheClear(t *testing.T) {
	n, ra, ctx := clearHarness(t, true)
	raised := time.Now().UTC().Add(-time.Hour)
	_ = n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, raised, raised))
	_ = n.Notify(ctx, cycleEvent(dmmodel.AlarmEventCleared, raised, raised.Add(time.Minute)))
	before := len(ra.sent)
	if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventEscalated, raised, raised.Add(2*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if len(ra.sent) != before {
		t.Fatalf("an escalation paged after the clear: %v", ra.types())
	}
	if open, err := n.api.OpenNotificationStates(ctx); err != nil || len(open) != 0 {
		t.Fatalf("a cleared alarm must not be open: %d (err %v)", len(open), err)
	}
}

// A destination that is down must not leave a resolved alarm open (the stamp comes first), and
// the redelivery sends the clear once the destination is back, once.
func TestAFailedClearIsStampedThenRetried(t *testing.T) {
	api := fencedApi(t)
	ctx := tenantScoped()
	seedClearPolicy(t, api, ctx, true)
	fa := &fakeAdapter{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa})
	n.api = api
	n.store = &fakeSecretStore{}
	raised := time.Now().UTC().Add(-time.Hour)
	if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, raised, raised)); err != nil {
		t.Fatalf("raise: %v", err)
	}
	fa.failTimes = 1 << 20
	clr := cycleEvent(dmmodel.AlarmEventCleared, raised, raised.Add(time.Minute))
	if err := n.Notify(ctx, clr); err == nil {
		t.Fatal("a clear no channel took must be returned for redelivery")
	}
	if open, err := api.OpenNotificationStates(ctx); err != nil || len(open) != 0 {
		t.Fatalf("the cleared alarm must not be open for escalation; got %d (err %v)", len(open), err)
	}
	fa.failTimes, fa.calls = 0, 0
	for i := 0; i < 2; i++ {
		if err := n.Notify(ctx, clr); err != nil {
			t.Fatal(err)
		}
	}
	if fa.calls != 1 {
		t.Fatalf("the retried clear was sent %d times, want 1", fa.calls)
	}
}
