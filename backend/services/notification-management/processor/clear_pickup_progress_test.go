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

// Spending the budget after a delivery must not hide an earlier hard failure.
func TestPickupBudgetSpentAfterHardFailureStaysHard(t *testing.T) {
	api, n, _ := pickupRig(t, "hang", []string{"fail", "ok", "hang", "after"}, map[string]bool{})
	n.attempts = 1
	delivered, timedOut := 0, false
	n.adapters[model.ChannelTypeSMTP] = adapterFunc(func(c context.Context, msg *RenderedNotification) error {
		if msg.Alarm.EventType != string(dmmodel.AlarmEventCleared) {
			return nil
		}
		switch msg.Alarm.AlarmToken {
		case "fail":
			return errors.New("clear delivery failed outright")
		case "hang":
			<-c.Done()
			timedOut = true
			return c.Err()
		default:
			delivered++
			return nil
		}
	})
	ctx := tenantScoped()
	policies, err := api.EnabledNotificationPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = n.AnnouncePendingClears(ctx, policies, n.clock())
	if delivered != 1 || !timedOut {
		t.Fatalf("want one delivery followed by a spent budget, got delivered=%d, timedOut=%v", delivered, timedOut)
	}
	if errors.Is(err, core.ErrPassPartial) {
		t.Fatalf("budget exhaustion downgraded a hard failure to partial: %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), `all 1 clear deliveries failed for alarm "fail"`) {
		t.Fatalf("want the earlier clear's hard failure, got %v", err)
	}
}

// A tenant whose open-states read fails AND whose clear pickup fails is ONE failed tenant.
func TestATenantFailingBothReadsIsCountedOnce(t *testing.T) {
	api, n, _ := pickupRig(t, "", []string{"a"}, map[string]bool{"a": true})
	reads := 0
	err := stateDB(api).Callback().Query().Before("gorm:query").Register("fail-tenant-state-reads", func(d *gorm.DB) {
		if _, ok := core.TenantFromContext(d.Statement.Context); ok && d.Statement.Table == "notification_states" {
			reads++
			_ = d.AddError(errors.New("state read refused"))
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &EscalationScheduler{Api: api, Notifier: n, defaultMax: 5}
	rerr := s.runOnce(context.Background())
	if reads != 2 {
		t.Fatalf("want both open-states and pending-clear reads to fail, got %d reads", reads)
	}
	if !errors.Is(rerr, core.ErrPassPartial) {
		t.Fatalf("want a partial pass for one failed tenant, got %v", rerr)
	}
	if rerr == nil || !strings.Contains(rerr.Error(), "1 of 1 not completed") {
		t.Fatalf("want one failed tenant of one visited, got %v", rerr)
	}
}

func TestSchedulerWithDeliveredAndTimedOutChannelsIsPartial(t *testing.T) {
	for _, tc := range []struct {
		name      string
		hangFirst bool
		timeout   time.Duration
		escalate  bool
	}{
		{"clear channel deadline after delivery", false, 50 * time.Millisecond, false},
		{"clear channel deadline before delivery", true, 50 * time.Millisecond, false},
		{"clear pass deadline after delivery", false, 5 * time.Second, false},
		{"escalation channel deadline before delivery", true, 50 * time.Millisecond, true},
		{"escalation pass deadline after delivery", false, 5 * time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := fencedApi(t)
			ctx := tenantScoped()
			seedClearPolicy(t, api, ctx, true)
			db := stateDB(api).WithContext(ctx)
			if tc.escalate {
				if err := db.Model(&model.NotificationPolicy{}).Where("token = ?", "p").
					Update("escalate_after_seconds", 300).Error; err != nil {
					t.Fatal(err)
				}
			}
			channel := enabledChannel("webhook-1", model.ChannelTypeWebhook)
			if err := db.Create(channel).Error; err != nil {
				t.Fatal(err)
			}
			policies, err := api.EnabledNotificationPolicies(ctx)
			if err != nil {
				t.Fatal(err)
			}
			r := rule(model.SeverityAny, nil, "ops@x.com")
			r.PolicyId, r.ChannelId = policies[0].ID, channel.ID
			if err := db.Create(&r).Error; err != nil {
				t.Fatal(err)
			}
			delivered := 0
			timedOut := make(chan struct{})
			healthy := adapterFunc(func(_ context.Context, msg *RenderedNotification) error {
				if msg.Alarm.EventType != string(dmmodel.AlarmEventRaised) {
					delivered++
				}
				return nil
			})
			hanging := adapterFunc(func(c context.Context, msg *RenderedNotification) error {
				if msg.Alarm.EventType != string(dmmodel.AlarmEventRaised) {
					<-c.Done()
					close(timedOut)
					return c.Err()
				}
				return nil
			})
			adapters := map[string]ChannelAdapter{model.ChannelTypeSMTP: healthy, model.ChannelTypeWebhook: hanging}
			if tc.hangFirst {
				adapters[model.ChannelTypeSMTP], adapters[model.ChannelTypeWebhook] = hanging, healthy
			}
			n := testNotifier(adapters)
			n.api, n.store = api, &fakeSecretStore{}
			n.attempts, n.timeout, n.budget = 1, tc.timeout, 400*time.Millisecond
			n.now = func() time.Time { return time.Now().Add(10 * time.Minute) }
			raised := time.Now().UTC().Add(time.Second)
			if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, raised, raised)); err != nil {
				t.Fatal(err)
			}
			if !tc.escalate {
				if err := api.MarkCleared(ctx, "a1", "k", "CRITICAL", raised, raised.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			s := &EscalationScheduler{Api: api, Notifier: n, defaultMax: 5}
			if err := s.runOnce(context.Background()); !errors.Is(err, core.ErrPassPartial) {
				t.Fatalf("want partial after a channel timed out alongside a delivery, got %v", err)
			}
			if delivered != 1 {
				t.Fatalf("want one actual delivery, got %d", delivered)
			}
			select {
			case <-timedOut:
			case <-time.After(time.Second):
				t.Fatal("the hanging channel did not reach its deadline")
			}
			if err := s.runOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			if delivered != 1 {
				t.Fatal("partial reporting caused the delivered notification to be repeated")
			}
		})
	}
}
