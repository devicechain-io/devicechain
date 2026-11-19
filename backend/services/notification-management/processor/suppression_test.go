// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	dmcontract "github.com/devicechain-io/dc-device-management/contract"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type suppressionFunc func(context.Context, []string) (map[string]SuppressionStatus, error)

func (f suppressionFunc) LookupSuppressions(ctx context.Context, tokens []string) (map[string]SuppressionStatus, error) {
	return f(ctx, tokens)
}

func suppressedEvent(at, until time.Time) *dmcontract.AlarmStateChangeEvent {
	e := eventAt(dmcontract.AlarmEventRaised, "MAJOR", at)
	e.SuppressedUntil, e.SuppressionWindowToken = &until, "window-1"
	return e
}

func suppressionMetrics(n *PolicyNotifier) {
	n.suppression = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "suppression_total"}, []string{"outcome"})
}

func TestQuietHoursHoldRechecksSuppression(t *testing.T) {
	for _, remaining := range []time.Duration{30 * time.Second, 3 * time.Hour} {
		t.Run(remaining.String(), func(t *testing.T) {
			clock := insideQuiet
			n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
			suppressionMetrics(n)
			holdOne(t, n, api, ctx, clock)
			require.False(t, heldRows(t, api, ctx)[0].HeldByWindow.Valid)
			clock = quietEnds
			until := clock.Add(remaining)
			n.SuppressionLookup = suppressionFunc(func(_ context.Context, tokens []string) (map[string]SuppressionStatus, error) {
				require.Equal(t, []string{"a1"}, tokens)
				return map[string]SuppressionStatus{"a1": {WindowToken: "live-window", Until: until}}, nil
			})
			require.NoError(t, n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), clock))
			require.Zero(t, fa.calls)
			rows := heldRows(t, api, ctx)
			require.Len(t, rows, 1)
			require.True(t, rows[0].ReleaseAt.Equal(suppressionReleaseAt(until, clock)))
			require.Equal(t, "live-window", rows[0].HeldByWindow.String)
			require.Equal(t, float64(1), testutil.ToFloat64(n.suppression.WithLabelValues("rechecked")))
		})
	}
}

func TestSuppressionHoldRechecksQuietHours(t *testing.T) {
	clock := insideQuiet
	n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
	suppressionMetrics(n)
	require.NoError(t, n.Notify(ctx, suppressedEvent(clock, clock.Add(time.Hour))))
	rows := heldRows(t, api, ctx)
	require.Len(t, rows, 1)
	require.True(t, rows[0].ReleaseAt.Equal(clock.Add(time.Minute)), "suppression takes precedence over quiet hours")
	require.Equal(t, float64(0), testutil.ToFloat64(n.quiet.WithLabelValues(quietHeld)))
	n.SuppressionLookup = suppressionFunc(func(context.Context, []string) (map[string]SuppressionStatus, error) { return nil, nil })
	clock = clock.Add(time.Minute)
	require.NoError(t, n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), clock))
	require.Zero(t, fa.calls)
	rows = heldRows(t, api, ctx)
	require.Len(t, rows, 1)
	require.True(t, rows[0].ReleaseAt.Equal(quietEnds))
	require.Equal(t, "window-1", rows[0].HeldByWindow.String)
}

func TestSuppressionSkipsBeforeClaimEscalation(t *testing.T) {
	n, rec, api, ctx := pagedThenDue(t, namedAlarm())
	suppressionMetrics(n)
	policies := enabledPolicies(t, api, ctx)
	suppressed := true
	n.SuppressionLookup = suppressionFunc(func(context.Context, []string) (map[string]SuppressionStatus, error) {
		if suppressed {
			return map[string]SuppressionStatus{"a1": {WindowToken: "w", Until: n.clock().Add(time.Hour)}}, nil
		}
		return nil, nil
	})
	// Observe writes, not just delivery: a misplaced check could burn a tier silently.
	claims := 0
	require.NoError(t, stateDB(api).Callback().Update().Before("gorm:update").Register("test:suppression-claims", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "NotificationState" {
			if values, ok := tx.Statement.Dest.(map[string]interface{}); ok {
				if _, ok := values["escalation_level"]; ok {
					claims++
				}
			}
		}
	}))
	for i := 0; i < 3; i++ {
		states, err := api.OpenNotificationStates(ctx)
		require.NoError(t, err)
		require.Len(t, states, 1)
		require.NoError(t, n.Escalate(ctx, states[0], policies, n.clock(), 5))
		require.Zero(t, claims, "suppression must skip before ClaimEscalation writes")
		states, err = api.OpenNotificationStates(ctx)
		require.NoError(t, err)
		require.Zero(t, states[0].EscalationLevel)
	}
	require.Len(t, rec.sent, 1)
	require.Equal(t, float64(3), testutil.ToFloat64(n.suppression.WithLabelValues("escalation_skipped")))
	require.Zero(t, testutil.ToFloat64(n.suppression.WithLabelValues("rechecked")))
	suppressed = false
	states, err := api.OpenNotificationStates(ctx)
	require.NoError(t, err)
	require.NoError(t, n.Escalate(ctx, states[0], policies, n.clock(), 5))
	require.Equal(t, 1, claims)
	require.Len(t, rec.sent, 2)
	states, err = api.OpenNotificationStates(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, states[0].EscalationLevel)
}

func TestDeescalatedNeverHolds(t *testing.T) {
	clock := outsideQuiet
	n, api, fa, ctx := quietFixture(t, "", "", "", &clock)
	e := suppressedEvent(clock, clock.Add(time.Hour))
	e.EventType = dmcontract.AlarmEventDeescalated
	require.NoError(t, n.Notify(ctx, e))
	require.Empty(t, heldRows(t, api, ctx))
	require.Zero(t, fa.calls)
	// An existing hold must keep the event and severity of its last raise/escalation.
	e.EventType = dmcontract.AlarmEventRaised
	require.NoError(t, n.Notify(ctx, e))
	before := heldRows(t, api, ctx)[0]
	e.EventType, e.Severity, e.OccurredTime = dmcontract.AlarmEventDeescalated, "MINOR", clock.Add(time.Second)
	require.NoError(t, n.Notify(ctx, e))
	after := heldRows(t, api, ctx)[0]
	require.Equal(t, before.Event, after.Event)
	require.Equal(t, before.Severity, after.Severity)
	require.True(t, before.ReleaseAt.Equal(after.ReleaseAt))
}

func TestSuppressionLookupFailureFailsOpen(t *testing.T) {
	clock := outsideQuiet
	n, api, fa, ctx := quietFixture(t, "", "", "", &clock)
	suppressionMetrics(n)
	n.SuppressionLookup = suppressionFunc(func(context.Context, []string) (map[string]SuppressionStatus, error) {
		return nil, errors.New("old schema")
	})
	require.NoError(t, n.Notify(ctx, eventAt(dmcontract.AlarmEventRaised, "MAJOR", clock)))
	require.Equal(t, 1, fa.calls)
	require.Empty(t, heldRows(t, api, ctx))
	e := suppressedEvent(clock.Add(time.Minute), clock.Add(time.Hour))
	clock = e.OccurredTime
	require.NoError(t, n.Notify(ctx, e))
	require.Len(t, heldRows(t, api, ctx), 1)
	clock = clock.Add(time.Minute)
	require.NoError(t, n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), clock))
	require.Equal(t, 2, fa.calls)
	require.Empty(t, heldRows(t, api, ctx))
	require.Equal(t, float64(1), testutil.ToFloat64(n.suppression.WithLabelValues("lookup_failed")))
	esc, rec, escAPI, escCtx := pagedThenDue(t, namedAlarm())
	esc.SuppressionLookup, esc.suppression = n.SuppressionLookup, n.suppression
	states, err := escAPI.OpenNotificationStates(escCtx)
	require.NoError(t, err)
	require.NoError(t, esc.Escalate(escCtx, states[0], enabledPolicies(t, escAPI, escCtx), esc.clock(), 5))
	require.Len(t, rec.sent, 2)
	require.Equal(t, float64(2), testutil.ToFloat64(n.suppression.WithLabelValues("lookup_failed")))
}

func TestSuppressionThrottleFirst(t *testing.T) {
	clock := outsideQuiet
	n, api, fa, ctx := quietFixture(t, "", "", "", &clock)
	policies := enabledPolicies(t, api, ctx)
	require.NoError(t, api.RDB.DB(ctx).Model(&model.NotificationPolicy{}).Where("id = ?", policies[0].ID).Update("throttle_seconds", 3600).Error)
	require.NoError(t, n.Notify(ctx, eventAt(dmcontract.AlarmEventRaised, "MAJOR", clock)))
	clock = clock.Add(time.Minute)
	require.NoError(t, n.Notify(ctx, suppressedEvent(clock, clock.Add(time.Hour))))
	require.Equal(t, 1, fa.calls)
	require.Empty(t, heldRows(t, api, ctx), "a throttled re-raise records no hold")
}

func TestSuppressionHeldResolvedNeverPages(t *testing.T) {
	for _, kind := range []dmcontract.AlarmEventType{dmcontract.AlarmEventCleared, dmcontract.AlarmEventAcknowledged} {
		t.Run(kind.String(), func(t *testing.T) {
			clock := outsideQuiet
			n, api, fa, ctx := quietFixture(t, "", "", "", &clock)
			suppressionMetrics(n)
			policies := enabledPolicies(t, api, ctx)
			require.NoError(t, api.RDB.DB(ctx).Model(&model.NotificationPolicy{}).Where("id = ?", policies[0].ID).Update("notify_on_clear", true).Error)
			e := suppressedEvent(clock, clock.Add(time.Hour))
			require.NoError(t, n.Notify(ctx, e))
			require.Len(t, heldRows(t, api, ctx), 1)
			e.EventType, e.OccurredTime = kind, clock.Add(time.Second)
			require.NoError(t, n.Notify(ctx, e))
			require.Empty(t, heldRows(t, api, ctx))
			require.Zero(t, fa.calls)
			require.NoError(t, n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), clock.Add(2*time.Hour)))
			require.Zero(t, fa.calls)
			require.Equal(t, float64(1), testutil.ToFloat64(n.suppression.WithLabelValues("dropped_resolved")))
		})
	}
}

func TestSuppressionReleaseExplanation(t *testing.T) {
	for _, kind := range []string{"ordinary", "quiet", "window"} {
		t.Run(kind, func(t *testing.T) {
			clock := insideQuiet
			tz, slots := "", ""
			if kind == "quiet" {
				tz, slots = "Europe/Berlin", quietSlots
			}
			n, api, _, ctx := quietFixture(t, tz, slots, "", &clock)
			rec := &msgRecorder{}
			n.adapters[model.ChannelTypeSMTP] = rec
			e := eventAt(dmcontract.AlarmEventRaised, "MAJOR", clock)
			if kind == "window" {
				e = suppressedEvent(clock, clock.Add(time.Hour))
				e.SuppressionWindowToken = "<window>\n&"
			}
			require.NoError(t, n.Notify(ctx, e))
			if kind != "ordinary" {
				clock = quietEnds
				require.NoError(t, n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), clock))
			}
			require.Len(t, rec.sent, 1)
			msg := rec.sent[0]
			email := string(buildEmail("alarms@example.com", []string{"ops@example.com"}, msg))
			if kind != "window" {
				require.Equal(t, renderNotification(e), msg, "ordinary and quiet-hours-only renderings must be identical")
				require.NotContains(t, msg.TextBody, "Suppressed:")
				require.NotContains(t, msg.Payload, "suppressedBy")
				require.NotContains(t, email, "Suppressed:")
				return
			}
			require.Contains(t, msg.TextBody, "Suppressed: held by alarm suppression window <window> &; released "+formatTime(clock)+"; active since "+formatTime(e.RaisedTime))
			require.Contains(t, email, "Suppressed: held by alarm suppression window <window> &;")
			require.Equal(t, e.SuppressionWindowToken, msg.Payload["suppressedBy"])
			require.Equal(t, formatTime(clock), msg.Payload["releasedAt"])
			require.Equal(t, e.SuppressionWindowToken, msg.Alarm.SuppressedBy)
			require.True(t, clock.Equal(msg.Alarm.ReleasedAt))
			attachments := slackMessage(msg, "", "")["attachments"].([]map[string]any)
			blocks := attachments[0]["blocks"].([]map[string]any)
			fields := blocks[1]["fields"].([]map[string]any)
			var texts []string
			for _, field := range fields {
				texts = append(texts, field["text"].(string))
			}
			slack := strings.Join(texts, "\n")
			require.Contains(t, slack, "&lt;window&gt; &amp;")
			require.Contains(t, slack, "Suppressed")
			require.False(t, strings.Contains(msg.TextBody, "<window>\n"))
		})
	}
}

func TestSuppressionSchedulerBatchesOnlyDueEscalations(t *testing.T) {
	api, n := fairnessRig(t)
	ctx, _ := seedFairTenant(t, api, "tenant", 3)
	now := n.clock()
	// This open alarm has no due escalation and must not spend a status lookup.
	require.NoError(t, api.RecordNotification(ctx, "not-due", "k", "CRITICAL", now, now))
	calls := 0
	n.SuppressionLookup = suppressionFunc(func(_ context.Context, tokens []string) (map[string]SuppressionStatus, error) {
		calls++
		require.Len(t, tokens, 3)
		require.NotContains(t, tokens, "not-due")
		out := make(map[string]SuppressionStatus)
		for _, token := range tokens {
			out[token] = SuppressionStatus{WindowToken: "w", Until: now.Add(time.Hour)}
		}
		return out, nil
	})
	s := &EscalationScheduler{Api: api, Notifier: n, defaultMax: 5}
	for i := 0; i < 2; i++ {
		require.NoError(t, s.runOnce(context.Background()))
		require.Equal(t, i+1, calls, "one batch per pass, without per-alarm fallback calls")
		states, err := api.OpenNotificationStates(ctx)
		require.NoError(t, err)
		for _, state := range states {
			require.Zero(t, state.EscalationLevel)
		}
	}
}

func TestSuppressionMetricsSeededAndOldestDueAcrossTenants(t *testing.T) {
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "notification-management"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	m := NewNotifyMetrics(ms)
	families, err := reg.Gather()
	require.NoError(t, err)
	seeded := false
	for _, family := range families {
		if family.GetName() == "devicechain_notificationmanagement_suppression_total" {
			seeded = true
			require.Len(t, family.Metric, 9, "all outcomes must exist before any notification")
			for _, metric := range family.Metric {
				require.Zero(t, metric.GetCounter().GetValue())
			}
		}
	}
	require.True(t, seeded)
	for _, outcome := range []string{"held", "rechecked", "escalation_skipped", "released", "superseded", "dropped_resolved", "dropped_policy", "dropped_exhausted", "lookup_failed"} {
		require.Zero(t, testutil.ToFloat64(m.suppression.WithLabelValues(outcome)))
	}
	clock := outsideQuiet
	n, api, _, ctx := quietFixture(t, "", "", "", &clock)
	db, err := stateDB(api).DB()
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	n.oldestDue = m.deferralOldestDue
	e := suppressedEvent(clock.Add(-10*time.Minute), clock.Add(time.Hour))
	require.NoError(t, n.Notify(ctx, e))
	rows := heldRows(t, api, ctx)
	require.Len(t, rows, 1)
	require.NoError(t, api.RDB.DB(ctx).Model(rows[0]).Update("release_at", clock.Add(-5*time.Minute)).Error)
	// A visited empty tenant has state, so its zero age participates in the same pass.
	require.NoError(t, api.MarkCleared(core.WithTenant(context.Background(), "empty"), "other", "k", "MAJOR", clock, clock))
	s := &EscalationScheduler{Api: api, Notifier: n, defaultMax: 5}
	require.NoError(t, s.runOnce(context.Background()))
	require.Equal(t, float64(300), testutil.ToFloat64(m.deferralOldestDue))
	require.NoError(t, s.runOnce(context.Background()))
	require.Zero(t, testutil.ToFloat64(m.deferralOldestDue))
}

// The scheduler looks suppression up one batch at a time, when the walk reaches the batch,
// so lookups and escalations interleave under the tenant slice: the second batch is asked
// only after the first batch's alarms have been escalated.
func TestSuppressionSchedulerLooksUpLazilyPerBatch(t *testing.T) {
	api, n := fairnessRig(t)
	ctx, _ := seedFairTenant(t, api, "tenant", suppressionBatch+5)
	var sizes, escalatedBefore []int
	n.SuppressionLookup = suppressionFunc(func(_ context.Context, tokens []string) (map[string]SuppressionStatus, error) {
		states, err := api.OpenNotificationStates(ctx)
		require.NoError(t, err)
		escalated := 0
		for _, state := range states {
			if state.EscalationLevel > 0 {
				escalated++
			}
		}
		sizes, escalatedBefore = append(sizes, len(tokens)), append(escalatedBefore, escalated)
		return map[string]SuppressionStatus{}, nil
	})
	s := &EscalationScheduler{Api: api, Notifier: n, defaultMax: 5}
	require.NoError(t, s.runOnce(context.Background()))
	require.Equal(t, []int{suppressionBatch, 5}, sizes)
	require.Equal(t, 0, escalatedBefore[0])
	require.Equal(t, suppressionBatch, escalatedBefore[1], "the second batch is looked up after the first is escalated")
}

// A window token too long to record as the hold's audit cannot be honoured: the hold's write
// would fail before any send and the page would be redelivered into a dead letter. It pages.
func TestSuppressionUnstorableWindowTokenPages(t *testing.T) {
	clock := outsideQuiet
	n, api, fa, ctx := quietFixture(t, "", "", "", &clock)
	e := suppressedEvent(clock, clock.Add(time.Hour))
	e.SuppressionWindowToken = strings.Repeat("w", maxWindowTokenLength+1)
	require.NoError(t, n.Notify(ctx, e))
	require.Equal(t, 1, fa.calls)
	require.Empty(t, heldRows(t, api, ctx))
	require.True(t, validWindowToken(strings.Repeat("w", maxWindowTokenLength)))
}
