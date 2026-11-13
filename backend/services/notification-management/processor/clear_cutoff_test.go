// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-notification-management/model"
	"gorm.io/gorm"
)

func boolOpt(v bool) dcgraphql.OptionalBool { return dcgraphql.OptionalBool{Set: true, Value: &v} }

// Editing a policy after it opted in must not drop a clear that is still waiting: the pickup's
// cut-off is when the option turned ON, not the policy's last save.
func TestEditingAPolicyDoesNotDropAPendingClear(t *testing.T) {
	api := fencedApi(t)
	ctx := tenantScoped()
	seedClearPolicy(t, api, ctx, false)
	if _, err := api.UpdateNotificationPolicy(ctx, "p",
		&model.NotificationPolicyUpdateRequest{NotifyOnClear: boolOpt(true)}, nil); err != nil {
		t.Fatal(err)
	}
	fa := &fakeAdapter{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa})
	n.api = api
	n.store = &fakeSecretStore{}

	time.Sleep(30 * time.Millisecond)
	raised := time.Now().UTC()
	if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, raised, raised)); err != nil {
		t.Fatal(err)
	}
	cleared := raised.Add(time.Millisecond)
	if err := api.MarkCleared(ctx, "a1", "k", "CRITICAL", raised, cleared); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	// An unrelated edit moves updated_at past the clear.
	name := "renamed"
	if _, err := api.UpdateNotificationPolicy(ctx, "p", &model.NotificationPolicyUpdateRequest{
		Name: dcgraphql.OptionalString{Set: true, Value: &name}}, nil); err != nil {
		t.Fatal(err)
	}
	policies, err := api.EnabledNotificationPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fa.calls = 0
	if err := n.AnnouncePendingClears(ctx, policies, time.Now().Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if fa.calls != 1 {
		t.Fatalf("the pending clear was sent %d times after an unrelated edit, want 1", fa.calls)
	}
}

// A destination that is healthy but slow, with more clears pending than one pass's budget can
// announce, is not a failed pass, and not a complete one either: it is PARTIAL (the rest waits
// for the next tick).
func TestABudgetSpentWithProgressIsAPartialPass(t *testing.T) {
	api := fencedApi(t)
	ctx := tenantScoped()
	seedClearPolicy(t, api, ctx, true)
	var delivered atomic.Int32
	ad := adapterFunc(func(c context.Context, _ *RenderedNotification) error {
		select {
		case <-time.After(120 * time.Millisecond):
			delivered.Add(1)
			return nil
		case <-c.Done():
			return c.Err()
		}
	})
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: ad})
	n.api = api
	n.store = &fakeSecretStore{}
	n.budget = 500 * time.Millisecond
	n.timeout = 5 * time.Second
	for i := 0; i < 12; i++ {
		raised := time.Now().UTC().Add(time.Second)
		ev := cycleEvent(dmmodel.AlarmEventRaised, raised, raised)
		ev.AlarmToken = "c" + string(rune('a'+i))
		if err := n.Notify(ctx, ev); err != nil {
			t.Fatal(err)
		}
		if err := api.MarkCleared(ctx, ev.AlarmToken, "k", "CRITICAL", raised, raised.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	policies, _ := api.EnabledNotificationPolicies(ctx)
	delivered.Store(0)
	err := n.AnnouncePendingClears(ctx, policies, time.Now().Add(10*time.Minute))
	if delivered.Load() == 0 || delivered.Load() >= 12 {
		t.Fatalf("harness: %d of 12 clears delivered; the budget should cut the pass part-way", delivered.Load())
	}
	if !errors.Is(err, core.ErrPassPartial) {
		t.Fatalf("a pass that announced %d clears and ran out of budget must be partial, got: %v", delivered.Load(), err)
	}
}

// A failed read of a tenant's open states must not also skip the pickup of its pending clears.
func TestAFailedOpenStatesReadStillPicksUpClears(t *testing.T) {
	api := fencedApi(t)
	ctx := tenantScoped()
	seedEscalatingPolicy(t, api, ctx)
	if err := stateDB(api).WithContext(ctx).Model(&model.NotificationPolicy{}).
		Where("token = ?", "p").Update("notify_on_clear", true).Error; err != nil {
		t.Fatal(err)
	}
	fa := &fakeAdapter{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa})
	n.api = api
	n.store = &fakeSecretStore{}
	n.now = func() time.Time { return time.Now().Add(10 * time.Minute) }
	raised := time.Now().UTC().Add(time.Second)
	if err := n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, raised, raised)); err != nil {
		t.Fatal(err)
	}
	if err := api.MarkCleared(ctx, "a1", "k", "CRITICAL", raised, raised.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	// A second alarm that is still open, so the tenant has states to enumerate and escalate.
	open := cycleEvent(dmmodel.AlarmEventRaised, raised, raised)
	open.AlarmToken = "open"
	if err := n.Notify(ctx, open); err != nil {
		t.Fatal(err)
	}
	// Only the OPEN-states read fails.
	if err := stateDB(api).Callback().Query().After("gorm:query").Register("fail-open-states", func(tx *gorm.DB) {
		if tx.Statement.SQL.Len() > 0 && strings.Contains(tx.Statement.SQL.String(), "acknowledged_at IS NULL") {
			tx.AddError(errors.New("open states unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	fa.calls = 0
	s := &EscalationScheduler{Api: api, Notifier: n, defaultMax: 5}
	if err := s.runOnce(context.Background()); err == nil {
		t.Fatal("a failed open-states read must still be reported as a failed pass")
	}
	if fa.calls != 1 {
		t.Fatalf("the pending clear was sent %d times though the open-states read failed, want 1", fa.calls)
	}
}
