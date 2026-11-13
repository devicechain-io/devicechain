// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-notification-management/model"
)

// msgRecorder keeps every rendering it is asked to deliver, so a test can say WHICH
// transitions reached a channel and not only how many.
type msgRecorder struct{ sent []*RenderedNotification }

func (r *msgRecorder) Deliver(_ context.Context, _ *model.NotificationChannel, _ string,
	_ []string, msg *RenderedNotification) error {
	r.sent = append(r.sent, msg)
	return nil
}

func (r *msgRecorder) types() []string {
	out := make([]string, 0, len(r.sent))
	for _, m := range r.sent {
		out = append(out, m.Alarm.EventType)
	}
	return out
}

// seedClearPolicy stores an enabled SMTP channel and a tenant-wide policy routing every
// severity to it, with notifyOnClear as given.
func seedClearPolicy(t *testing.T, api *model.Api, ctx context.Context, notifyOnClear bool) {
	t.Helper()
	db := stateDB(api).WithContext(ctx)
	channel := enabledChannel("smtp-1", model.ChannelTypeSMTP)
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	p := policy("p")
	p.NotifyOnClear = notifyOnClear
	if err := db.Create(p).Error; err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	r := rule(model.SeverityAny, nil, "ops@x.com")
	r.PolicyId, r.ChannelId = p.ID, channel.ID
	if err := db.Create(&r).Error; err != nil {
		t.Fatalf("seed rule: %v", err)
	}
}

func clearHarness(t *testing.T, notifyOnClear bool) (*PolicyNotifier, *msgRecorder, context.Context) {
	t.Helper()
	api := fencedApi(t)
	ctx := tenantScoped()
	seedClearPolicy(t, api, ctx, notifyOnClear)
	ra := &msgRecorder{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: ra})
	n.api = api
	n.store = &fakeSecretStore{}
	return n, ra, ctx
}

// TestAPolicyThatOptedInIsToldWhenAnAlarmClears is the feature: raise, then clear, and the
// channel hears both. The clear carries CLEARED, which is what a webhook keys on.
func TestAPolicyThatOptedInIsToldWhenAnAlarmClears(t *testing.T) {
	n, ra, ctx := clearHarness(t, true)
	raised := time.Now().UTC().Add(-time.Hour)
	if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, raised, raised)); err != nil {
		t.Fatalf("raise: %v", err)
	}
	if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventCleared, raised, raised.Add(10*time.Minute))); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := ra.types(); len(got) != 2 || got[0] != "RAISED" || got[1] != "CLEARED" {
		t.Fatalf("channel heard %v, want [RAISED CLEARED]", got)
	}
}

// TestAPolicyThatDidNotOptInStaysSilentOnClear is the counterweight: with the option off —
// every policy that existed before it — the clear reaches nobody, exactly as before.
func TestAPolicyThatDidNotOptInStaysSilentOnClear(t *testing.T) {
	n, ra, ctx := clearHarness(t, false)
	raised := time.Now().UTC().Add(-time.Hour)
	_ = n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, raised, raised))
	if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventCleared, raised, raised.Add(10*time.Minute))); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := ra.types(); len(got) != 1 || got[0] != "RAISED" {
		t.Fatalf("channel heard %v, want only [RAISED]", got)
	}
}

// TestAClearIsNotAnnouncedTwice: a redelivered CLEARED (the stream is at-least-once) finds
// the state already stamped and sends nothing.
func TestAClearIsNotAnnouncedTwice(t *testing.T) {
	n, ra, ctx := clearHarness(t, true)
	raised := time.Now().UTC().Add(-time.Hour)
	_ = n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, raised, raised))
	clear := cycleEvent(dmmodel.AlarmEventCleared, raised, raised.Add(10*time.Minute))
	for i := 0; i < 3; i++ {
		if err := n.Notify(ctx, clear); err != nil {
			t.Fatalf("clear %d: %v", i, err)
		}
	}
	if got := ra.types(); len(got) != 2 {
		t.Fatalf("channel heard %v, want exactly one RAISED and one CLEARED", got)
	}
}

// TestAClearForAnAlarmNobodyWasPagedAboutIsSilent: no raise reached a human, so there is
// nothing to pair the clear with.
func TestAClearForAnAlarmNobodyWasPagedAboutIsSilent(t *testing.T) {
	n, ra, ctx := clearHarness(t, true)
	raised := time.Now().UTC().Add(-time.Hour)
	if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventCleared, raised, raised.Add(time.Minute))); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if len(ra.sent) != 0 {
		t.Fatalf("channel heard %v about an alarm it was never paged for", ra.types())
	}
}

// TestAClearDuringQuietHoursIsDroppedNotHeld: a held "cleared" released hours later would
// report an old fact as news.
func TestAClearDuringQuietHoursIsDroppedNotHeld(t *testing.T) {
	n, ra, ctx := clearHarness(t, true)
	raised := time.Now().UTC().Add(-time.Hour)
	_ = n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, raised, raised))
	n.now = func() time.Time { return insideQuiet }
	db := stateDB(n.api).WithContext(ctx)
	if err := db.Model(&model.NotificationPolicy{}).Where("token = ?", "p").Updates(map[string]any{
		"quiet_hours_timezone": "Europe/Berlin", "quiet_hours_slots": quietSlots,
	}).Error; err != nil {
		t.Fatalf("set quiet hours: %v", err)
	}
	if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventCleared, raised, raised.Add(10*time.Minute))); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := ra.types(); len(got) != 1 {
		t.Fatalf("channel heard %v, want only the raise", got)
	}
}
