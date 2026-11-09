// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"sync"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-notification-management/model"
)

// A webhook that is down for clears (it blocks until its context ends) while many clears are
// pending must not delay escalation: the pickup runs after the escalations, under one shared
// budget, and the escalation for another alarm is delivered at the start of the tick.
func TestAStalledClearChannelDoesNotDelayEscalation(t *testing.T) {
	api := fencedApi(t)
	ctx := tenantScoped()
	seedEscalatingPolicy(t, api, ctx)
	if err := stateDB(api).WithContext(ctx).Model(&model.NotificationPolicy{}).
		Where("token = ?", "p").Update("notify_on_clear", true).Error; err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var escalatedAt time.Time
	start := time.Now()
	ad := adapterFunc(func(c context.Context, msg *RenderedNotification) error {
		switch msg.Alarm.EventType {
		case "CLEARED":
			<-c.Done() // the destination is down
			return c.Err()
		case "RENOTIFIED":
			mu.Lock()
			escalatedAt = time.Now()
			mu.Unlock()
		}
		return nil
	})
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: ad})
	n.api = api
	n.store = &fakeSecretStore{}
	n.budget = 400 * time.Millisecond
	n.timeout = 5 * time.Second
	n.now = func() time.Time { return time.Now().Add(10 * time.Minute) }

	// One open alarm paged an hour ago, due to escalate.
	old := time.Now().UTC().Add(-time.Hour)
	esc := cycleEvent(dmmodel.AlarmEventRaised, old, old)
	esc.AlarmToken = "esc"
	if err := n.Notify(ctx, esc); err != nil {
		t.Fatal(err)
	}
	// Five alarms paged, then stamped cleared with nobody announcing them.
	for i := 0; i < 5; i++ {
		raised := time.Now().UTC().Add(time.Second)
		ev := cycleEvent(dmmodel.AlarmEventRaised, raised, raised)
		ev.AlarmToken = "c" + string(rune('1'+i))
		if err := n.Notify(ctx, ev); err != nil {
			t.Fatal(err)
		}
		if err := api.MarkCleared(ctx, ev.AlarmToken, "k", "CRITICAL", raised, raised.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}

	s := &EscalationScheduler{Api: api, Notifier: n, defaultMax: 5}
	start = time.Now()
	if err := s.runOnce(context.Background()); err == nil {
		t.Log("the pickup of stalled clears is reported as a failed tick, as it should be")
	}
	elapsed := time.Since(start)
	mu.Lock()
	at := escalatedAt
	mu.Unlock()
	if at.IsZero() {
		t.Fatal("the open alarm was not escalated")
	}
	if first := at.Sub(start); first > 200*time.Millisecond {
		t.Fatalf("escalation waited %v behind the clear pickup", first)
	}
	// Five stalled clears at one shared budget cost about one budget, not five.
	if elapsed > 1200*time.Millisecond {
		t.Fatalf("the tick took %v with a 400ms pickup budget", elapsed)
	}
}

// adapterFunc adapts a function to a ChannelAdapter.
type adapterFunc func(ctx context.Context, msg *RenderedNotification) error

func (f adapterFunc) Deliver(ctx context.Context, _ *model.NotificationChannel, _ string, _ []string,
	msg *RenderedNotification) error {
	return f(ctx, msg)
}

// Switching notifyOnClear on must not announce the clears of the last half hour: only a clear
// after the opted-in policy was last saved is picked up.
func TestTurningOnNotifyOnClearDoesNotAnnounceOlderClears(t *testing.T) {
	api := fencedApi(t)
	ctx := tenantScoped()
	seedClearPolicy(t, api, ctx, true)
	fa := &fakeAdapter{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa})
	n.api = api
	n.store = &fakeSecretStore{}
	// Paged and cleared well before the policy was (just now) saved.
	raised := time.Now().UTC().Add(-10 * time.Minute)
	if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, raised, raised)); err != nil {
		t.Fatal(err)
	}
	if err := api.MarkCleared(ctx, "a1", "k", "CRITICAL", raised, raised.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	policies, _ := api.EnabledNotificationPolicies(ctx)
	fa.calls = 0
	if err := n.AnnouncePendingClears(ctx, policies, time.Now().Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if fa.calls != 0 {
		t.Fatalf("a clear from before the opt-in was announced (%d sends)", fa.calls)
	}
}

// A clear the scheduler rebuilds from the state row does not know its originator, and says so by
// omission rather than by sending "" and 0.
func TestAClearWithoutAnOriginatorOmitsItsKeys(t *testing.T) {
	r := renderNotification(&dmmodel.AlarmStateChangeEvent{
		EventType: dmmodel.AlarmEventCleared, AlarmToken: "a", AlarmKey: "k", Severity: "MAJOR", State: "CLEARED",
	})
	for _, k := range []string{"originatorType", "originatorId", "deviceToken", "deviceName"} {
		if _, ok := r.Payload[k]; ok {
			t.Fatalf("payload carries %s though it is unknown: %v", k, r.Payload[k])
		}
	}
	if r.Alarm.Originator != "" {
		t.Fatalf("an unknown source must not be listed: %q", r.Alarm.Originator)
	}
	// A known originator still states both.
	named := renderNotification(namedEvent())
	if named.Payload["originatorType"] != "device" || named.Payload["originatorId"] != uint(42) {
		t.Fatalf("a known originator was dropped: %v", named.Payload)
	}
}
