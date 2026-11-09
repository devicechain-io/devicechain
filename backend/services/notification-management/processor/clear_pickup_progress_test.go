// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-notification-management/model"
	"gorm.io/gorm"
)

// pickupRig pages the named alarms through one escalating, notify-on-clear policy, oldest
// first, then stamps each cleared with nobody announcing it. recipientsFor lets a test page an
// alarm to a target the policy no longer routes (a row the pickup can claim but has nothing to
// send for).
func pickupRig(t *testing.T, hang string, tokens []string, oldRecipient map[string]bool) (*model.Api, *PolicyNotifier, *[]string) {
	t.Helper()
	api := fencedApi(t)
	ctx := tenantScoped()
	db := stateDB(api).WithContext(ctx)
	channel := enabledChannel("smtp-1", model.ChannelTypeSMTP)
	if err := db.Create(channel).Error; err != nil {
		t.Fatal(err)
	}
	p := escalatingPolicy("p", 300, 0)
	p.NotifyOnClear = true
	if err := db.Create(p).Error; err != nil {
		t.Fatal(err)
	}
	r := rule("CRITICAL", nil, "old@x.com")
	r.PolicyId, r.ChannelId = p.ID, channel.ID
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	var sent []string
	ad := adapterFunc(func(c context.Context, msg *RenderedNotification) error {
		if msg.Alarm.EventType == "CLEARED" {
			if msg.Alarm.AlarmToken == hang {
				<-c.Done()
				return c.Err()
			}
			sent = append(sent, msg.Alarm.AlarmToken)
		}
		return nil
	})
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: ad})
	n.api = api
	n.store = &fakeSecretStore{}
	n.budget = 400 * time.Millisecond
	n.timeout = 5 * time.Second
	n.now = func() time.Time { return time.Now().Add(10 * time.Minute) }
	for _, tok := range tokens {
		if !oldRecipient[tok] {
			// the policy now routes to a different recipient than the one the alarm was paged to
			if err := stateDB(api).WithContext(ctx).Model(&model.NotificationRule{}).
				Where("policy_id = ?", p.ID).Update("recipients", `["ops@x.com"]`).Error; err != nil {
				t.Fatal(err)
			}
		}
		raised := time.Now().UTC().Add(time.Second)
		ev := cycleEvent(dmmodel.AlarmEventRaised, raised, raised)
		ev.AlarmToken = tok
		if err := n.Notify(ctx, ev); err != nil {
			t.Fatal(err)
		}
		if err := api.MarkCleared(ctx, tok, "k", "CRITICAL", raised, raised.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// every alarm is routed to ops@x.com now, so a row paged to old@x.com has nothing to send
	if err := stateDB(api).WithContext(ctx).Model(&model.NotificationRule{}).
		Where("policy_id = ?", p.ID).Update("recipients", `["ops@x.com"]`).Error; err != nil {
		t.Fatal(err)
	}
	return api, n, &sent
}

// A row the pickup claims but has nothing to send for (its paged target is no longer routed) is
// handled, not progress: behind it a hanging channel must not grade the pass complete.
func TestPickupBudgetSpentAfterNoDeliveryRowIsNotComplete(t *testing.T) {
	api, n, _ := pickupRig(t, "hang", []string{"mismatch", "hang"}, map[string]bool{"mismatch": true})
	ctx := tenantScoped()
	policies, err := api.EnabledNotificationPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.AnnouncePendingClears(ctx, policies, n.clock()); err == nil {
		t.Fatal("a pass that delivered nothing and ran out of budget on a hanging channel was graded complete")
	}
}

// A pass that did deliver something and then ran out of budget with rows left is PARTIAL.
func TestPickupBudgetSpentAfterDeliveryIsPartial(t *testing.T) {
	api, n, sent := pickupRig(t, "hang", []string{"ok", "hang", "after"}, map[string]bool{})
	ctx := tenantScoped()
	policies, err := api.EnabledNotificationPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = n.AnnouncePendingClears(ctx, policies, n.clock())
	if len(*sent) == 0 {
		t.Fatal("the healthy row was not announced")
	}
	if !errors.Is(err, core.ErrPassPartial) {
		t.Fatalf("want a partial pass (something delivered, rows skipped), got %v", err)
	}
}

// A tenant whose open-states read fails AND whose clear pickup fails is ONE failed tenant.
func TestATenantFailingBothReadsIsCountedOnce(t *testing.T) {
	api, n, _ := pickupRig(t, "", []string{"a"}, map[string]bool{"a": true})
	err := stateDB(api).Callback().Query().Before("gorm:query").Register("fail-tenant-state-reads", func(d *gorm.DB) {
		if _, ok := core.TenantFromContext(d.Statement.Context); ok && d.Statement.Table == "notification_states" {
			_ = d.AddError(errors.New("state read refused"))
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &EscalationScheduler{Api: api, Notifier: n, defaultMax: 5}
	rerr := s.runOnce(context.Background())
	if rerr == nil || !strings.Contains(rerr.Error(), "1 of 1 not completed") {
		t.Fatalf("want one failed tenant of one visited, got %v", rerr)
	}
}
