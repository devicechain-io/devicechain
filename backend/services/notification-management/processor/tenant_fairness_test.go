// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"gorm.io/datatypes"
)

func fairnessRig(t *testing.T) (*model.Api, *PolicyNotifier) {
	t.Helper()
	api := fencedApi(t)
	db, err := stateDB(api).DB()
	if err != nil {
		t.Fatal(err)
	}
	// The fixture uses :memory:; all workers must see the same SQLite connection.
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: adapterFunc(func(context.Context, *RenderedNotification) error { return nil })})
	n.api, n.store, n.attempts = api, &fakeSecretStore{}, 1
	n.budget, n.timeout = 2*time.Second, 5*time.Second
	n.now = func() time.Time { return time.Now().UTC().Add(10 * time.Minute) }
	return api, n
}

func seedFairTenant(t *testing.T, api *model.Api, tenant string, count int) (context.Context, *model.NotificationPolicy) {
	t.Helper()
	ctx := core.WithTenant(context.Background(), tenant)
	seedClearPolicy(t, api, ctx, true)
	db := stateDB(api).WithContext(ctx)
	if err := db.Model(&model.NotificationPolicy{}).Where("token = ?", "p").Update("escalate_after_seconds", 60).Error; err != nil {
		t.Fatal(err)
	}
	policies := enabledPolicies(t, api, ctx)
	at := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < count; i++ {
		if err := api.RecordNotification(ctx, fmt.Sprintf("alarm-%03d", i), "k", "CRITICAL", at, at); err != nil {
			t.Fatal(err)
		}
	}
	return ctx, policies[0]
}

func TestSlowTenantDoesNotBlockOtherTenantWork(t *testing.T) {
	for _, backlog := range []int{5, 50} {
		t.Run(fmt.Sprint(backlog), func(t *testing.T) {
			api, n := fairnessRig(t)
			seedFairTenant(t, api, "aaa", backlog)
			ctx, p := seedFairTenant(t, api, "bbb", 1)
			now := time.Now().UTC()
			ev := cycleEvent(dmmodel.AlarmEventRaised, now.Add(-time.Minute), now.Add(-time.Minute))
			ev.AlarmToken = "cleared"
			if err := n.Notify(ctx, ev); err != nil {
				t.Fatal(err)
			}
			if err := api.MarkCleared(ctx, ev.AlarmToken, "k", "CRITICAL", ev.RaisedTime, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			ev.AlarmToken = "held"
			d := &model.NotificationDeferral{AlarmToken: "held", PolicyId: p.ID, AlarmKey: "k", Severity: "CRITICAL",
				OccurredTime: ev.OccurredTime, ReleaseAt: now.Add(-time.Second), Event: datatypes.JSON(mustJSON(ev))}
			if err := stateDB(api).WithContext(ctx).Create(d).Error; err != nil {
				t.Fatal(err)
			}
			blocked, unblock, served := make(chan struct{}, 1), make(chan struct{}), make(chan string, 8)
			n.adapters[model.ChannelTypeSMTP] = adapterFunc(func(c context.Context, msg *RenderedNotification) error {
				tenant, _ := core.TenantFromContext(c)
				if tenant == "aaa" {
					select {
					case blocked <- struct{}{}:
					default:
					}
					select {
					case <-unblock:
						return errors.New("endpoint unavailable")
					case <-c.Done():
						return c.Err()
					}
				}
				served <- msg.Alarm.AlarmToken
				return nil
			})
			s := &EscalationScheduler{Api: api, Notifier: n, defaultMax: 5}
			done := make(chan error, 1)
			go func() { done <- s.runOnce(context.Background()) }()
			select {
			case <-blocked:
			case <-time.After(time.Second):
				t.Error("slow tenant never started")
			}
			seen := map[string]bool{}
			timer := time.NewTimer(time.Second)
			for len(seen) < 3 {
				select {
				case alarm := <-served:
					seen[alarm] = true
				case <-timer.C:
					t.Errorf("healthy tenant stalled behind %d alarms; served %v", backlog, seen)
					goto release
				}
			}
		release:
			timer.Stop()
			close(unblock)
			<-done
			for _, alarm := range []string{"alarm-000", "held", "cleared"} {
				if !seen[alarm] {
					t.Errorf("healthy tenant's %s was not served while the other tenant was blocked", alarm)
				}
			}
		})
	}
}

func TestTenantWorkerPoolIsBounded(t *testing.T) {
	api, n := fairnessRig(t)
	for i := 0; i < 6; i++ {
		seedFairTenant(t, api, fmt.Sprint(i), 1)
	}
	entered, release := make(chan struct{}, 6), make(chan struct{})
	var active, maximum atomic.Int32
	n.adapters[model.ChannelTypeSMTP] = adapterFunc(func(c context.Context, _ *RenderedNotification) error {
		a := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); a > old; old = maximum.Load() {
			if maximum.CompareAndSwap(old, a) {
				break
			}
		}
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-c.Done():
			return c.Err()
		}
	})
	s := &EscalationScheduler{Api: api, Notifier: n, defaultMax: 5}
	done := make(chan error, 1)
	go func() { done <- s.runOnce(context.Background()) }()
	for i := 0; i < 4; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Error("four tenants did not start concurrently")
			goto release
		}
	}
	select {
	case <-entered:
		t.Error("more than four tenants started")
	case <-time.After(40 * time.Millisecond):
	}
release:
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if maximum.Load() != 4 {
		t.Fatalf("maximum concurrent tenants = %d, want 4", maximum.Load())
	}
}

// slowEscalations makes every escalation page take 70ms and every clear succeed at once.
func slowEscalations(n *PolicyNotifier, clears *atomic.Int32) {
	n.adapters[model.ChannelTypeSMTP] = adapterFunc(func(c context.Context, msg *RenderedNotification) error {
		if msg.Alarm.EventType == "CLEARED" {
			clears.Add(1)
			return nil
		}
		select {
		case <-time.After(70 * time.Millisecond):
			return nil
		case <-c.Done():
			return c.Err()
		}
	})
}

func escalatedCount(t *testing.T, api *model.Api, ctx context.Context) int {
	t.Helper()
	states, err := api.OpenNotificationStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	claimed := 0
	for _, st := range states {
		claimed += st.EscalationLevel
	}
	return claimed
}

func TestTenantSliceLeavesUnclaimedWorkAndStillAnnouncesClears(t *testing.T) {
	api, n := fairnessRig(t)
	ctx, _ := seedFairTenant(t, api, "aaa", 8)
	// Four more tenants: five tenants on four workers, so one always waits for a
	// worker and the slice binds.
	others := make([]context.Context, 0, 4)
	for i := 1; i <= 4; i++ {
		octx, _ := seedFairTenant(t, api, fmt.Sprintf("b%d", i), 8)
		others = append(others, octx)
	}
	now := time.Now().UTC()
	ev := cycleEvent(dmmodel.AlarmEventRaised, now.Add(-time.Minute), now.Add(-time.Minute))
	ev.AlarmToken = "cleared"
	if err := n.Notify(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if err := api.MarkCleared(ctx, ev.AlarmToken, "k", "CRITICAL", ev.RaisedTime, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	n.budget = 100 * time.Millisecond
	n.sliceExhausted = prometheus.NewCounter(prometheus.CounterOpts{Name: "test_slice_exhausted_total"})
	var clears atomic.Int32
	slowEscalations(n, &clears)
	s := &EscalationScheduler{Api: api, Notifier: n, defaultMax: 5}
	err := s.runOnce(context.Background())
	if !errors.Is(err, core.ErrPassPartial) {
		t.Fatalf("cut-short pass = %v", err)
	}
	// The four tenants admitted first all hit their 200ms slice while the fifth
	// waited; the first of them to look left its remaining alarms due.
	short := 0
	for _, c := range append([]context.Context{ctx}, others[:3]...) {
		if got := escalatedCount(t, api, c); got < 8 {
			short++
			if got < 1 || got > 3 {
				t.Fatalf("started %d alarms in a 200ms slice at 70ms each", got)
			}
		}
	}
	if short == 0 {
		t.Fatal("no contended tenant was cut at its slice")
	}
	// The last tenant was admitted after everyone else, so nobody waited behind it.
	if got := escalatedCount(t, api, others[3]); got != 8 {
		t.Fatalf("uncontended tenant escalated %d of 8 alarms", got)
	}
	if clears.Load() != 1 {
		t.Fatalf("cut-short tenant announced %d clears", clears.Load())
	}
	if testutil.ToFloat64(n.sliceExhausted) < 1 {
		t.Fatal("slice exhaustion was not counted")
	}
	// The remaining alarms are still due, and a healthy pass drains them.
	n.adapters[model.ChannelTypeSMTP] = adapterFunc(func(context.Context, *RenderedNotification) error { return nil })
	n.budget = time.Second
	if err := s.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range append([]context.Context{ctx}, others...) {
		states, err := api.OpenNotificationStates(c)
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range states {
			if st.EscalationLevel != 1 {
				t.Fatalf("%s tier = %d", st.AlarmToken, st.EscalationLevel)
			}
		}
	}
}

// A tenant alone with its backlog is not cut at its slice: nobody is waiting for the
// worker, so stopping would only idle capacity until the next tick.
func TestTenantSliceIsWorkConservingWhenUncontended(t *testing.T) {
	api, n := fairnessRig(t)
	ctx, _ := seedFairTenant(t, api, "aaa", 8)
	n.budget = 100 * time.Millisecond
	n.sliceExhausted = prometheus.NewCounter(prometheus.CounterOpts{Name: "test_slice_uncontended_total"})
	var clears atomic.Int32
	slowEscalations(n, &clears)
	s := &EscalationScheduler{Api: api, Notifier: n, defaultMax: 5}
	if err := s.runOnce(context.Background()); err != nil {
		t.Fatalf("uncontended pass = %v", err)
	}
	if got := escalatedCount(t, api, ctx); got != 8 {
		t.Fatalf("lone tenant escalated %d of 8 alarms past its 200ms slice", got)
	}
	if testutil.ToFloat64(n.sliceExhausted) != 0 {
		t.Fatal("an uncontended tenant was counted as slice-exhausted")
	}
}

func TestAdmittedAlarmKeepsItsBudgetAndUsesClaimTime(t *testing.T) {
	api, n := fairnessRig(t)
	ctx, p := seedFairTenant(t, api, "aaa", 1)
	states, err := api.OpenNotificationStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	claimTime := time.Now().UTC().Add(time.Minute)
	n.now = func() time.Time { return claimTime }
	pass := &tenantPass{deadline: time.Now().Add(10 * time.Millisecond), timedOut: make(map[uint]bool)}
	ctx = context.WithValue(ctx, tenantPassKey{}, pass)
	n.adapters[model.ChannelTypeSMTP] = adapterFunc(func(c context.Context, _ *RenderedNotification) error {
		select {
		case <-time.After(40 * time.Millisecond):
			return nil
		case <-c.Done():
			return c.Err()
		}
	})
	if err := n.Escalate(ctx, states[0], []*model.NotificationPolicy{p}, claimTime.Add(-time.Minute), 5); err != nil {
		t.Fatal(err)
	}
	states, err = api.OpenNotificationStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !states[0].LastNotifiedAt.Time.Equal(claimTime) {
		t.Fatalf("claim stamped %v, want %v", states[0].LastNotifiedAt.Time, claimTime)
	}
}

func TestTimeoutMemoPreservesTiersAndProbesOnNextPass(t *testing.T) {
	api, n := fairnessRig(t)
	ctx, _ := seedFairTenant(t, api, "aaa", 5)
	n.timeout = 10 * time.Millisecond
	calls := 0
	n.adapters[model.ChannelTypeSMTP] = adapterFunc(func(c context.Context, _ *RenderedNotification) error { calls++; <-c.Done(); return c.Err() })
	s := &EscalationScheduler{Api: api, Notifier: n, defaultMax: 5}
	if err := s.runOnce(context.Background()); err == nil {
		t.Fatal("timeout pass reported success")
	}
	states, err := api.OpenNotificationStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	claimed := 0
	for _, st := range states {
		claimed += st.EscalationLevel
	}
	if calls != 1 || claimed != 1 {
		t.Fatalf("calls=%d claimed tiers=%d, want one probe and one tier", calls, claimed)
	}
	n.adapters[model.ChannelTypeSMTP] = adapterFunc(func(context.Context, *RenderedNotification) error { calls++; return nil })
	if err := s.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	states, err = api.OpenNotificationStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 5 {
		t.Fatalf("recovered channel got %d total calls, want 5", calls)
	}
	for _, st := range states {
		if st.EscalationLevel != 1 {
			t.Fatalf("%s lost its escalation: tier %d", st.AlarmToken, st.EscalationLevel)
		}
	}
}

func TestTimeoutMemoSkipsOnlyTheFailingChannelAndRecordsIt(t *testing.T) {
	api, n := fairnessRig(t)
	ctx, p := seedFairTenant(t, api, "aaa", 1)
	bad := enabledChannel("bad", model.ChannelTypeWebhook)
	if err := stateDB(api).WithContext(ctx).Create(bad).Error; err != nil {
		t.Fatal(err)
	}
	p.Rules = append(p.Rules, rule(model.SeverityAny, bad))
	pass := &tenantPass{deadline: time.Now().Add(time.Minute), timedOut: map[uint]bool{bad.ID: true}}
	ctx = context.WithValue(ctx, tenantPassKey{}, pass)
	rows := make(chan ledgerJob, 4)
	n.Ledger = &AttemptLedger{queue: rows, store: &memoryLedgerStore{}}
	healthy, failed := 0, 0
	n.adapters[model.ChannelTypeSMTP] = adapterFunc(func(context.Context, *RenderedNotification) error { healthy++; return nil })
	n.adapters[model.ChannelTypeWebhook] = adapterFunc(func(context.Context, *RenderedNotification) error { failed++; return nil })
	states, err := api.OpenNotificationStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = n.Escalate(ctx, states[0], []*model.NotificationPolicy{p}, time.Now(), 5)
	if !errors.Is(err, core.ErrPassPartial) || healthy != 1 || failed != 0 {
		t.Fatalf("err=%v healthy=%d failing=%d", err, healthy, failed)
	}
	found := false
	for len(rows) > 0 {
		row := (<-rows).row
		n.Ledger.done()
		if row.ChannelToken == "bad" && row.Status == model.AttemptStatusNotAttempted && row.ErrorCode.String == model.AttemptErrorTimeout {
			found = true
		}
	}
	if !found {
		t.Fatal("memoised channel has no skipped ledger row")
	}
	states, err = api.OpenNotificationStates(ctx)
	if err != nil || states[0].EscalationLevel != 1 {
		t.Fatalf("mixed alarm tier not advanced: %v %v", states, err)
	}
}

func TestTenantAdmissionRotatesAndCancellationStopsPass(t *testing.T) {
	api, n := fairnessRig(t)
	s := &EscalationScheduler{Api: api, Notifier: n}
	for i, want := range []string{"a", "b", "c", "a"} {
		order := s.tenantOrder([]string{"a", "b", "c"})
		if order[0] != want {
			t.Fatalf("pass %d order %v, want %s first", i, order, want)
		}
	}
	seedFairTenant(t, api, "aaa", 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.runOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled pass = %v", err)
	}
}
func TestHeldReleaseStopsBeforeLeasingWhenTenantSliceExpires(t *testing.T) {
	api, n := fairnessRig(t)
	ctx, p := seedFairTenant(t, api, "aaa", 0)
	now := time.Now().UTC()
	ev := cycleEvent(dmmodel.AlarmEventRaised, now.Add(-time.Minute), now.Add(-time.Minute))
	d := &model.NotificationDeferral{AlarmToken: "a1", PolicyId: p.ID, AlarmKey: "k", Severity: "CRITICAL",
		OccurredTime: ev.OccurredTime, ReleaseAt: now.Add(-time.Second), Event: datatypes.JSON(mustJSON(ev))}
	if err := stateDB(api).WithContext(ctx).Create(d).Error; err != nil {
		t.Fatal(err)
	}
	pass := &tenantPass{deadline: time.Now().Add(-time.Second), timedOut: make(map[uint]bool)}
	pctx := context.WithValue(ctx, tenantPassKey{}, pass)
	calls := 0
	n.adapters[model.ChannelTypeSMTP] = adapterFunc(func(context.Context, *RenderedNotification) error { calls++; return nil })
	if err := n.ReleaseHeld(pctx, []*model.NotificationPolicy{p}, now); !errors.Is(err, core.ErrPassPartial) {
		t.Fatalf("expired slice: %v", err)
	}
	if calls != 0 {
		t.Fatalf("started %d releases after slice expiry", calls)
	}
	rows, err := api.NotificationDeferralsByAlarmToken(ctx, []string{"a1"})
	if err != nil || len(rows) != 1 || rows[0].Attempts != 0 || !rows[0].ReleaseAt.Equal(d.ReleaseAt) {
		t.Fatalf("deferral was claimed: %v %v", rows, err)
	}
	if err := n.ReleaseHeld(ctx, []*model.NotificationPolicy{p}, now); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("next pass released %d alarms", calls)
	}
}
