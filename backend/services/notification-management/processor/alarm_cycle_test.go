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

// cycleEvent builds one transition for alarm "a1" in the cycle that began at raised.
func cycleEvent(et dmmodel.AlarmEventType, raised, occurred time.Time) *dmmodel.AlarmStateChangeEvent {
	return &dmmodel.AlarmStateChangeEvent{
		EventType: et, AlarmToken: "a1", AlarmKey: "k", Severity: "CRITICAL", State: "ACTIVE",
		RaisedTime: raised, OccurredTime: occurred,
	}
}

// seedEscalatingPolicy stores an enabled SMTP channel and an escalating CRITICAL policy
// for the tenant, so Notify's own policy load finds them.
func seedEscalatingPolicy(t *testing.T, api *model.Api, ctx context.Context) {
	t.Helper()
	db := stateDB(api).WithContext(ctx)
	channel := enabledChannel("smtp-1", model.ChannelTypeSMTP)
	if err := db.Create(channel).Error; err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	p := escalatingPolicy("p", 300, 0)
	if err := db.Create(p).Error; err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	r := rule("CRITICAL", nil, "ops@x.com")
	r.PolicyId, r.ChannelId = p.ID, channel.ID
	if err := db.Create(&r).Error; err != nil {
		t.Fatalf("seed rule: %v", err)
	}
}

// TestASecondCycleOfTheSameAlarmEscalates is the end-to-end case, every transition through
// Notify. device-management REUSES an alarm's token when it re-raises (same row, back to
// ACTIVE, acknowledgement reset), so the notification state of cycle two is the row cycle
// one left behind. Cycle one was acknowledged and cleared; cycle two is paged and nobody
// acknowledges it — so the scheduler must escalate it, and cycle one's acknowledgement,
// redelivered late by the unordered consumer, must not silence it.
func TestASecondCycleOfTheSameAlarmEscalates(t *testing.T) {
	api := fencedApi(t)
	ctx := tenantScoped()
	seedEscalatingPolicy(t, api, ctx)
	now := time.Now().UTC()

	fa := &fakeAdapter{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa})
	n.api = api
	n.store = &fakeSecretStore{}
	notify := func(e *dmmodel.AlarmStateChangeEvent) {
		t.Helper()
		if err := n.Notify(ctx, e); err != nil {
			t.Fatalf("%s: %v", e.EventType, err)
		}
	}

	// Cycle one: paged, acknowledged, cleared. Its RAISED carries nanoseconds, as
	// device-management's in-memory row does; the later events carry the microseconds
	// its database hands back.
	c1 := now.Add(-2 * time.Hour).Add(123 * time.Nanosecond)
	c1db := c1.Truncate(time.Microsecond)
	notify(cycleEvent(dmmodel.AlarmEventRaised, c1, c1))
	notify(cycleEvent(dmmodel.AlarmEventAcknowledged, c1db, c1.Add(time.Minute)))
	notify(cycleEvent(dmmodel.AlarmEventCleared, c1db, c1.Add(10*time.Minute)))
	if fa.calls != 1 {
		t.Fatalf("cycle one paged %d time(s), want 1", fa.calls)
	}

	// Cycle two: re-raised on the same token and paged twenty minutes ago.
	c2 := now.Add(-20 * time.Minute)
	notify(cycleEvent(dmmodel.AlarmEventRaised, c2, c2))
	if fa.calls != 2 {
		t.Fatalf("cycle two's RAISED paged %d time(s) in all, want 2", fa.calls)
	}
	// Cycle one's acknowledgement and clear, redelivered after cycle two began.
	notify(cycleEvent(dmmodel.AlarmEventAcknowledged, c1db, c1.Add(time.Minute)))
	notify(cycleEvent(dmmodel.AlarmEventCleared, c1db, c1.Add(10*time.Minute)))

	open, err := api.OpenNotificationStates(ctx)
	if err != nil {
		t.Fatalf("open states: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("cycle two is open and unacknowledged, so it is an escalation candidate; "+
			"got %d candidates", len(open))
	}
	policies, err := api.EnabledNotificationPolicies(ctx)
	if err != nil {
		t.Fatalf("policies: %v", err)
	}
	if err := n.Escalate(ctx, open[0], policies, now, 5); err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if fa.calls != 3 {
		t.Fatalf("cycle two's escalation: %d page(s) in all, want 3", fa.calls)
	}

	// And cycle two's own acknowledgement still stops it.
	notify(cycleEvent(dmmodel.AlarmEventAcknowledged, c2, c2.Add(25*time.Minute)))
	if open, err = api.OpenNotificationStates(ctx); err != nil || len(open) != 0 {
		t.Fatalf("cycle two acknowledged must leave no candidate; got %d (err %v)", len(open), err)
	}
}
