// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"gorm.io/datatypes"
)

// The quiet-hours fixture: Berlin, Wednesday 22:00 for eight hours, which in October 2026
// (CEST, UTC+2) is 20:00Z Wednesday 14th to 04:00Z Thursday 15th. Bypass at CRITICAL.
var (
	quietSlots   = `[{"day":"WEDNESDAY","start":"22:00","durationMinutes":480}]`
	insideQuiet  = time.Date(2026, 10, 14, 21, 30, 0, 0, time.UTC) // 23:30 Berlin
	quietEnds    = time.Date(2026, 10, 15, 4, 0, 0, 0, time.UTC)   // 06:00 Berlin
	outsideQuiet = time.Date(2026, 10, 14, 19, 0, 0, 0, time.UTC)  // 21:00 Berlin
)

// quietFixture is a real persistence Api with one SMTP channel and one enabled policy
// routing every severity to it, with the given quiet hours, and a notifier standing at
// *clock. The returned adapter counts deliveries.
func quietFixture(t *testing.T, tz, slots, bypass string, clock *time.Time) (*PolicyNotifier, *model.Api, *fakeAdapter, context.Context) {
	t.Helper()
	api := fencedApi(t)
	ctx := tenantScoped()
	if _, err := api.CreateNotificationChannel(ctx, &model.NotificationChannelCreateRequest{
		Token: "smtp-1", ChannelType: model.ChannelTypeSMTP, Enabled: true,
		Config: strPointer(`{"host":"smtp.example.com","from":"alarms@example.com"}`),
	}); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	req := &model.NotificationPolicyCreateRequest{
		Token: "p1", Enabled: true,
		Rules: []*model.NotificationRuleCreateRequest{{
			Severity: model.SeverityAny, ChannelToken: "smtp-1", Recipients: strPointer(`["ops@x.com"]`),
		}},
	}
	if tz != "" {
		req.QuietHoursTimezone, req.QuietHoursSlots = &tz, &slots
	}
	if bypass != "" {
		req.QuietHoursBypassSeverity = &bypass
	}
	if _, err := api.CreateNotificationPolicy(ctx, req); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	fa := &fakeAdapter{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa})
	n.api = api
	n.store = &fakeSecretStore{}
	n.now = func() time.Time { return *clock }
	n.quiet = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "quiet_hours_total"}, []string{"outcome"})
	return n, api, fa, ctx
}

func eventAt(eventType dmmodel.AlarmEventType, severity string, occurred time.Time) *dmmodel.AlarmStateChangeEvent {
	return &dmmodel.AlarmStateChangeEvent{
		EventType: eventType, AlarmToken: "a1", AlarmKey: "k", OriginatorType: "device", OriginatorId: 1,
		Severity: severity, State: "ACTIVE", RaisedTime: occurred, OccurredTime: occurred,
	}
}

func heldRows(t *testing.T, api *model.Api, ctx context.Context) []*model.NotificationDeferral {
	t.Helper()
	held, err := api.NotificationDeferralsByAlarmToken(ctx, []string{"a1"})
	if err != nil {
		t.Fatal(err)
	}
	return held
}

func enabledPolicies(t *testing.T, api *model.Api, ctx context.Context) []*model.NotificationPolicy {
	t.Helper()
	ps, err := api.EnabledNotificationPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

// The headline: inside quiet hours a below-bypass alarm pages NOBODY now, and is held to
// the end of the stretch. Before quiet hours existed this paged at 23:30.
func TestDispatchHoldsANotificationInsideQuietHours(t *testing.T) {
	clock := insideQuiet
	n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "CRITICAL", &clock)

	if err := n.dispatch(ctx, eventAt(dmmodel.AlarmEventRaised, "MAJOR", clock)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if fa.calls != 0 {
		t.Fatalf("paged %d time(s) inside quiet hours, want 0", fa.calls)
	}
	held := heldRows(t, api, ctx)
	if len(held) != 1 {
		t.Fatalf("held %d notification(s), want 1", len(held))
	}
	if !held[0].ReleaseAt.Equal(quietEnds) {
		t.Fatalf("held until %s, want %s (06:00 Berlin)", held[0].ReleaseAt.UTC(), quietEnds)
	}
	if got := testutil.ToFloat64(n.quiet.WithLabelValues(quietHeld)); got != 1 {
		t.Fatalf("held counter = %v, want 1", got)
	}
	// Nothing was sent, so there is no notification state for escalation to run from.
	if states, _ := api.NotificationStatesByAlarmToken(ctx, []string{"a1"}); len(states) != 0 {
		t.Fatalf("a held notification stamped notification state: %+v", states[0])
	}
}

func TestBypassSeverityPagesInsideQuietHours(t *testing.T) {
	clock := insideQuiet
	n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "CRITICAL", &clock)
	if err := n.dispatch(ctx, eventAt(dmmodel.AlarmEventRaised, "CRITICAL", clock)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if fa.calls != 1 || len(heldRows(t, api, ctx)) != 0 {
		t.Fatalf("CRITICAL at the bypass: calls=%d held=%d, want 1 and 0", fa.calls, len(heldRows(t, api, ctx)))
	}
}

func TestOutsideQuietHoursPagesNow(t *testing.T) {
	clock := outsideQuiet
	n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
	if err := n.dispatch(ctx, eventAt(dmmodel.AlarmEventRaised, "MINOR", clock)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if fa.calls != 1 || len(heldRows(t, api, ctx)) != 0 {
		t.Fatalf("outside quiet hours: calls=%d held=%d, want 1 and 0", fa.calls, len(heldRows(t, api, ctx)))
	}
}

// A policy with no quiet hours is untouched, and another policy's quiet hours do not hold
// it: each policy's are its own.
func TestQuietHoursAreThePolicysOwn(t *testing.T) {
	clock := insideQuiet
	n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
	if _, err := api.CreateNotificationChannel(ctx, &model.NotificationChannelCreateRequest{
		Token: "smtp-2", ChannelType: model.ChannelTypeSMTP, Enabled: true,
		Config: strPointer(`{"host":"smtp.example.com","from":"alarms@example.com"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := api.CreateNotificationPolicy(ctx, &model.NotificationPolicyCreateRequest{
		Token: "always", Enabled: true,
		Rules: []*model.NotificationRuleCreateRequest{{
			Severity: model.SeverityAny, ChannelToken: "smtp-2", Recipients: strPointer(`["noc@x.com"]`),
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := n.dispatch(ctx, eventAt(dmmodel.AlarmEventRaised, "MAJOR", clock)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if fa.calls != 1 {
		t.Fatalf("the policy without quiet hours paged %d time(s), want 1", fa.calls)
	}
	if held := heldRows(t, api, ctx); len(held) != 1 {
		t.Fatalf("the quiet policy held %d, want 1", len(held))
	}
}

// Throttling comes first: a notification the throttle suppresses is not held for the
// morning.
func TestAThrottledNotificationIsNotHeld(t *testing.T) {
	clock := insideQuiet
	n, api, _, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
	p := enabledPolicies(t, api, ctx)[0]
	p.ThrottleSeconds.Int64, p.ThrottleSeconds.Valid = 3600, true
	state := &model.NotificationState{AlarmToken: "a1"}
	state.LastNotifiedAt.Time, state.LastNotifiedAt.Valid = clock.Add(-time.Minute), true
	sendNow, held := n.holdQuiet(eventAt(dmmodel.AlarmEventRaised, "MAJOR", clock),
		[]*model.NotificationPolicy{p}, state, clock)
	if len(held) != 0 || len(sendNow) != 1 {
		t.Fatalf("throttled: held=%d sendNow=%d, want 0 and 1 (plan then throttles it)", len(held), len(sendNow))
	}
}

// At the end of the stretch the held notification is sent, the row is gone, and the
// alarm's notification state is stamped at the RELEASE, which is when somebody was paged.
func TestReleaseSendsAtTheEndOfQuietHours(t *testing.T) {
	clock := insideQuiet
	n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
	if err := n.dispatch(ctx, eventAt(dmmodel.AlarmEventRaised, "MAJOR", clock)); err != nil {
		t.Fatal(err)
	}

	// Still quiet a minute before the end: nothing.
	early := quietEnds.Add(-time.Minute)
	if err := n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), early); err != nil {
		t.Fatal(err)
	}
	if fa.calls != 0 || len(heldRows(t, api, ctx)) != 1 {
		t.Fatalf("before the end: calls=%d held=%d", fa.calls, len(heldRows(t, api, ctx)))
	}

	if err := n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), quietEnds); err != nil {
		t.Fatal(err)
	}
	if fa.calls != 1 {
		t.Fatalf("released %d time(s), want 1", fa.calls)
	}
	if held := heldRows(t, api, ctx); len(held) != 0 {
		t.Fatalf("%d row(s) still held after the release", len(held))
	}
	states, _ := api.NotificationStatesByAlarmToken(ctx, []string{"a1"})
	if len(states) != 1 || !states[0].LastNotifiedAt.Time.Equal(quietEnds) {
		t.Fatalf("state after release = %+v, want last notified at %s", states, quietEnds)
	}
	if got := testutil.ToFloat64(n.quiet.WithLabelValues(quietReleased)); got != 1 {
		t.Fatalf("released counter = %v", got)
	}
	// A second pass sends nothing more.
	if err := n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), quietEnds.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if fa.calls != 1 {
		t.Fatalf("a second pass re-sent: calls = %d", fa.calls)
	}
}

// An alarm acknowledged or cleared while held is not paged about in the morning. A stamp
// from an EARLIER cycle of the same alarm token does not count.
func TestReleaseDropsWhatWasResolvedWhileHeld(t *testing.T) {
	for _, tc := range []struct {
		name    string
		resolve func(api *model.Api, ctx context.Context, at time.Time) error
	}{
		{"cleared", func(api *model.Api, ctx context.Context, at time.Time) error {
			return api.MarkCleared(ctx, "a1", "k", "MAJOR", at)
		}},
		{"acknowledged", func(api *model.Api, ctx context.Context, at time.Time) error {
			return api.MarkAcknowledged(ctx, "a1", "k", "MAJOR", at)
		}},
	} {
		t.Run(tc.name+" while held", func(t *testing.T) {
			clock := insideQuiet
			n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
			if err := n.dispatch(ctx, eventAt(dmmodel.AlarmEventRaised, "MAJOR", clock)); err != nil {
				t.Fatal(err)
			}
			if err := tc.resolve(api, ctx, clock.Add(10*time.Minute)); err != nil {
				t.Fatal(err)
			}
			if err := n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), quietEnds); err != nil {
				t.Fatal(err)
			}
			if fa.calls != 0 {
				t.Fatalf("paged about an alarm %s while held", tc.name)
			}
			if len(heldRows(t, api, ctx)) != 0 {
				t.Fatal("the dropped row was left behind")
			}
			if got := testutil.ToFloat64(n.quiet.WithLabelValues(quietDroppedResolved)); got != 1 {
				t.Fatalf("dropped_resolved = %v", got)
			}
		})
		t.Run(tc.name+" in an earlier cycle", func(t *testing.T) {
			clock := insideQuiet
			n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
			if err := tc.resolve(api, ctx, clock.Add(-time.Hour)); err != nil {
				t.Fatal(err)
			}
			if err := n.dispatch(ctx, eventAt(dmmodel.AlarmEventRaised, "MAJOR", clock)); err != nil {
				t.Fatal(err)
			}
			if err := n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), quietEnds); err != nil {
				t.Fatal(err)
			}
			if fa.calls != 1 {
				t.Fatalf("a stamp from before the held event dropped it: calls = %d", fa.calls)
			}
		})
	}
}

// A flapping alarm overnight is ONE page in the morning, describing the alarm as it last
// was.
func TestOneHeldNotificationPerAlarmAndPolicy(t *testing.T) {
	clock := insideQuiet
	n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "CRITICAL", &clock)
	var rendered []*RenderedNotification
	fa2 := &recordingAdapter{onDeliver: func(r *RenderedNotification) { rendered = append(rendered, r) }}
	n.adapters[model.ChannelTypeSMTP] = fa2
	for i, sev := range []string{"MINOR", "MAJOR", "MINOR"} {
		at := clock.Add(time.Duration(i) * 10 * time.Minute)
		if err := n.dispatch(ctx, eventAt(dmmodel.AlarmEventEscalated, sev, at)); err != nil {
			t.Fatal(err)
		}
	}
	if held := heldRows(t, api, ctx); len(held) != 1 || held[0].Severity != "MINOR" ||
		!held[0].OccurredTime.Equal(clock.Add(20*time.Minute)) {
		t.Fatalf("held = %+v, want ONE row carrying the last event", held)
	}
	if err := n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), quietEnds); err != nil {
		t.Fatal(err)
	}
	if fa.calls != 0 || len(rendered) != 1 {
		t.Fatalf("morning pages = %d, want 1", len(rendered))
	}
	if want := "[MINOR]"; len(rendered[0].Subject) < len(want) || rendered[0].Subject[:len(want)] != want {
		t.Fatalf("the morning page describes %q, want the last event's severity", rendered[0].Subject)
	}
}

// Editing a policy's quiet hours re-evaluates what it holds; disabling it drops it.
func TestPolicyEditsReachHeldNotifications(t *testing.T) {
	t.Run("quiet hours removed: sent on the next pass", func(t *testing.T) {
		clock := insideQuiet
		n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
		if err := n.dispatch(ctx, eventAt(dmmodel.AlarmEventRaised, "MAJOR", clock)); err != nil {
			t.Fatal(err)
		}
		req := &model.NotificationPolicyUpdateRequest{}
		req.QuietHoursTimezone.Set, req.QuietHoursSlots.Set = true, true
		if _, err := api.UpdateNotificationPolicy(ctx, "p1", req, nil); err != nil {
			t.Fatal(err)
		}
		next := time.Now().Add(time.Minute) // the update brings it due at the wall clock
		if err := n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), next); err != nil {
			t.Fatal(err)
		}
		if fa.calls != 1 {
			t.Fatalf("quiet hours removed, but the held page was not sent: calls = %d", fa.calls)
		}
	})
	t.Run("still quiet: held again to the new end", func(t *testing.T) {
		clock := insideQuiet
		n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
		if err := n.dispatch(ctx, eventAt(dmmodel.AlarmEventRaised, "MAJOR", clock)); err != nil {
			t.Fatal(err)
		}
		// Due now, but the policy is quiet for it at the pass: it is moved, not sent.
		rows := heldRows(t, api, ctx)
		if len(rows) != 1 {
			t.Fatalf("held %d notification(s), want 1", len(rows))
		}
		if err := api.RescheduleNotificationDeferral(ctx, rows[0], clock); err != nil {
			t.Fatal(err)
		}
		if err := n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), clock.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if fa.calls != 0 {
			t.Fatalf("sent inside quiet hours: calls = %d", fa.calls)
		}
		if rows = heldRows(t, api, ctx); len(rows) != 1 || !rows[0].ReleaseAt.Equal(quietEnds) {
			t.Fatalf("held = %+v, want one row moved to %s", rows, quietEnds)
		}
	})
	t.Run("disabled: dropped", func(t *testing.T) {
		clock := insideQuiet
		n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
		if err := n.dispatch(ctx, eventAt(dmmodel.AlarmEventRaised, "MAJOR", clock)); err != nil {
			t.Fatal(err)
		}
		off := false
		req := &model.NotificationPolicyUpdateRequest{}
		req.Enabled.Set, req.Enabled.Value = true, &off
		if _, err := api.UpdateNotificationPolicy(ctx, "p1", req, nil); err != nil {
			t.Fatal(err)
		}
		if err := n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), time.Now().Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if fa.calls != 0 || len(heldRows(t, api, ctx)) != 0 {
			t.Fatalf("disabled policy: calls=%d held=%d, want 0 and 0", fa.calls, len(heldRows(t, api, ctx)))
		}
		if got := testutil.ToFloat64(n.quiet.WithLabelValues(quietDroppedPolicy)); got != 1 {
			t.Fatalf("dropped_policy = %v", got)
		}
	})
}

// A release that reaches no channel is put back and retried, up to the cap.
func TestAFailedReleaseIsRetriedThenDropped(t *testing.T) {
	clock := insideQuiet
	n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
	fa.failTimes = 1 << 30
	n.attempts = 1
	if err := n.dispatch(ctx, eventAt(dmmodel.AlarmEventRaised, "MAJOR", clock)); err != nil {
		t.Fatal(err)
	}
	at := quietEnds
	for pass := 1; pass <= maxReleaseAttempts; pass++ {
		err := n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), at)
		held := heldRows(t, api, ctx)
		if pass < maxReleaseAttempts {
			if err == nil {
				t.Fatalf("pass %d: a release that reached nobody reported success", pass)
			}
			if len(held) != 1 || held[0].Attempts != pass {
				t.Fatalf("pass %d: held = %+v, want one row with attempts %d", pass, held, pass)
			}
		} else if len(held) != 0 {
			t.Fatalf("after %d failed releases the row is still held", pass)
		}
		at = at.Add(time.Minute)
	}
	if fa.calls != maxReleaseAttempts {
		t.Fatalf("adapter called %d times, want %d", fa.calls, maxReleaseAttempts)
	}
	if got := testutil.ToFloat64(n.quiet.WithLabelValues(quietDroppedExhausted)); got != 1 {
		t.Fatalf("dropped_exhausted = %v", got)
	}
}

// Escalation waits out quiet hours and resumes after them, without spending a tier.
func TestEscalationWaitsForQuietHoursToEnd(t *testing.T) {
	smtp := enabledChannel("smtp-1", model.ChannelTypeSMTP)
	p := escalatingPolicy("p", 300, 0, rule(model.SeverityAny, smtp, "ops@x.com"))
	p.QuietHoursTimezone.String, p.QuietHoursTimezone.Valid = "Europe/Berlin", true
	js := datatypes.JSON(quietSlots)
	p.QuietHoursSlots = &js
	p.QuietHoursBypassSeverity.String, p.QuietHoursBypassSeverity.Valid = "CRITICAL", true
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: &fakeAdapter{}})

	state := openState("MAJOR", insideQuiet.Add(-time.Hour), 0)
	if got := n.planEscalation(state, []*model.NotificationPolicy{p}, insideQuiet, 5); len(got) != 0 {
		t.Fatalf("escalated inside quiet hours: %d deliveries", len(got))
	}
	if got := n.planEscalation(state, []*model.NotificationPolicy{p}, quietEnds, 5); len(got) != 1 {
		t.Fatalf("did not escalate once quiet hours ended: %d deliveries", len(got))
	}
	critical := openState("CRITICAL", insideQuiet.Add(-time.Hour), 0)
	if got := n.planEscalation(critical, []*model.NotificationPolicy{p}, insideQuiet, 5); len(got) != 1 {
		t.Fatalf("a bypass-severity alarm did not escalate inside quiet hours: %d", len(got))
	}
}

// The scheduler visits a tenant whose ONLY alarm was held: it has no notification state,
// so the state-tenant list alone would never release it.
func TestSchedulerReleasesForATenantWithNoNotificationState(t *testing.T) {
	clock := insideQuiet
	n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
	if err := n.dispatch(ctx, eventAt(dmmodel.AlarmEventRaised, "MAJOR", clock)); err != nil {
		t.Fatal(err)
	}
	if states, _ := api.NotificationStatesByAlarmToken(ctx, []string{"a1"}); len(states) != 0 {
		t.Fatal("fixture: the tenant must have no notification state")
	}
	clock = quietEnds.Add(time.Minute) // the scheduler reads the notifier's clock
	s := &EscalationScheduler{Api: api, Notifier: n, defaultMax: 5}
	if err := s.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if fa.calls != 1 {
		t.Fatalf("the scheduler did not release the held notification: calls = %d", fa.calls)
	}
}

func TestUnionSorted(t *testing.T) {
	got := unionSorted([]string{"a", "c", "e"}, []string{"b", "c", "f"})
	want := []string{"a", "b", "c", "e", "f"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if got := unionSorted(nil, []string{"x"}); len(got) != 1 || got[0] != "x" {
		t.Fatalf("got %v", got)
	}
}

// recordingAdapter succeeds and hands each rendering to onDeliver.
type recordingAdapter struct {
	onDeliver func(*RenderedNotification)
}

func (r *recordingAdapter) Deliver(_ context.Context, _ *model.NotificationChannel, _ string, _ []string, rn *RenderedNotification) error {
	r.onDeliver(rn)
	return nil
}

// A policy whose rules route this severity nowhere has nothing to hold: it must not leave
// a row behind that the morning pass then drops.
func TestAPolicyThatWouldSendNothingHoldsNothing(t *testing.T) {
	smtp := enabledChannel("smtp-1", model.ChannelTypeSMTP)
	p := policy("p", rule("CRITICAL", smtp, "ops@x.com"))
	p.QuietHoursTimezone.String, p.QuietHoursTimezone.Valid = "Europe/Berlin", true
	js := datatypes.JSON(quietSlots)
	p.QuietHoursSlots = &js
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: &fakeAdapter{}})
	_, held := n.holdQuiet(eventAt(dmmodel.AlarmEventRaised, "MINOR", insideQuiet),
		[]*model.NotificationPolicy{p}, nil, insideQuiet)
	if len(held) != 0 {
		t.Fatalf("held %d notification(s) for a policy with no rule for MINOR", len(held))
	}
}

// Stored quiet hours that no longer validate are IGNORED, and the alarm pages: the other
// reading — always quiet — is a silent outage.
func TestStoredQuietHoursThatDoNotValidatePage(t *testing.T) {
	smtp := enabledChannel("smtp-1", model.ChannelTypeSMTP)
	p := policy("p", rule(model.SeverityAny, smtp, "ops@x.com"))
	p.QuietHoursTimezone.String, p.QuietHoursTimezone.Valid = "Local", true
	js := datatypes.JSON(quietSlots)
	p.QuietHoursSlots = &js
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: &fakeAdapter{}})
	sendNow, held := n.holdQuiet(eventAt(dmmodel.AlarmEventRaised, "MINOR", insideQuiet),
		[]*model.NotificationPolicy{p}, nil, insideQuiet)
	if len(held) != 0 || len(sendNow) != 1 {
		t.Fatalf("unvalidatable quiet hours: held=%d sendNow=%d, want 0 and 1", len(held), len(sendNow))
	}
	state := openState("MINOR", insideQuiet.Add(-time.Hour), 0)
	p.EscalateAfterSeconds.Int64, p.EscalateAfterSeconds.Valid = 300, true
	if got := n.planEscalation(state, []*model.NotificationPolicy{p}, insideQuiet, 5); len(got) != 1 {
		t.Fatalf("unvalidatable quiet hours suppressed escalation: %d", len(got))
	}
}
