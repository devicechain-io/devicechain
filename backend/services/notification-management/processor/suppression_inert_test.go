// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dmcontract "github.com/devicechain-io/dc-device-management/contract"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
)

func TestSuppressionPrePassHasNoSideEffects(t *testing.T) {
	for _, wired := range []bool{false, true} {
		t.Run(fmt.Sprint(wired), func(t *testing.T) {
			api, n := fairnessRig(t)
			ctx, _ := seedFairTenant(t, api, "tenant", 1)
			delete(n.adapters, model.ChannelTypeSMTP)
			n.refused = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "deliveries_refused_total"}, []string{"reason"})
			calls := 0
			if wired {
				n.SuppressionLookup = suppressionFunc(func(context.Context, []string) (map[string]SuppressionStatus, error) {
					calls++
					return nil, nil
				})
			}
			// A direct Escalate on the original path logs and refuses this rule once.
			states, err := api.OpenNotificationStates(ctx)
			require.NoError(t, err)
			policies := enabledPolicies(t, api, ctx)
			require.NoError(t, n.Escalate(ctx, states[0], policies, n.clock(), 5))
			counter := n.refused.WithLabelValues(refusalReasonNoAdapter)
			baseline := testutil.ToFloat64(counter)
			require.Equal(t, float64(1), baseline)
			logs := captureLogs(t)
			scheduler := &EscalationScheduler{Api: api, Notifier: n, defaultMax: 5}
			outcome := scheduler.serveTenant(context.Background(), "tenant", nil)
			require.False(t, outcome.failed)
			require.Equal(t, baseline, testutil.ToFloat64(counter)-baseline, "one refusal per pass, without a planning pre-pass")
			require.Equal(t, 1, strings.Count(logs.String(), "No adapter for channel type"))
			require.Zero(t, calls, "nil lookup makes no calls; an unroutable alarm is not a candidate either")
		})
	}
	// An ordinary due alarm also makes no lookup when wiring is nil, and still pages.
	api, n := fairnessRig(t)
	ctx, _ := seedFairTenant(t, api, "tenant", 1)
	require.Nil(t, n.SuppressionLookup)
	scheduler := &EscalationScheduler{Api: api, Notifier: n, defaultMax: 5}
	require.NotPanics(t, func() {
		outcome := scheduler.serveTenant(context.Background(), "tenant", nil)
		require.False(t, outcome.failed)
	}, "nil suppression wiring must never call a lookup")
	states, err := api.OpenNotificationStates(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, states[0].EscalationLevel)
}

func TestEscalationCandidateAgreesWithPlan(t *testing.T) {
	for _, mode := range []string{"due", "not-due", "scoped", "quiet-held", "invalid-quiet", "no-rule", "disabled", "missing-channel", "no-adapter", "exhausted"} {
		t.Run(mode, func(t *testing.T) {
			n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: &fakeAdapter{}})
			state := &model.NotificationState{Severity: "MAJOR", LastNotifiedAt: sql.NullTime{Time: insideQuiet.Add(-time.Hour), Valid: true}}
			p := &model.NotificationPolicy{EscalateAfterSeconds: sql.NullInt64{Int64: 60, Valid: true}, Rules: []model.NotificationRule{rule("*", enabledChannel("smtp", model.ChannelTypeSMTP))}}
			want := mode == "due" || mode == "invalid-quiet"
			switch mode {
			case "not-due":
				state.LastNotifiedAt.Time = insideQuiet
			case "scoped":
				p.DeviceTypeToken = sql.NullString{String: "device-type", Valid: true}
			case "quiet-held", "invalid-quiet":
				p.QuietHoursTimezone = sql.NullString{String: "Europe/Berlin", Valid: true}
				slots := datatypes.JSON(quietSlots)
				p.QuietHoursSlots = &slots
				if mode == "invalid-quiet" {
					p.QuietHoursTimezone.String = "invalid-zone"
				}
			case "no-rule":
				p.Rules[0].Severity = "CRITICAL"
			case "disabled":
				p.Rules[0].Channel.Enabled = false
			case "missing-channel":
				p.Rules[0].Channel = nil
			case "no-adapter":
				delete(n.adapters, model.ChannelTypeSMTP)
			case "exhausted":
				state.EscalationLevel = 5
			}
			logs := captureLogs(t)
			require.Equal(t, want, n.escalationCandidate(state, []*model.NotificationPolicy{p}, insideQuiet, 5))
			require.Empty(t, logs.String(), "candidate checks never log, including invalid stored quiet hours")
			require.Equal(t, want, len(n.planEscalation(state, []*model.NotificationPolicy{p}, insideQuiet, 5)) > 0)
		})
	}
}

func TestSuppressionUnsupportedCache(t *testing.T) {
	now := time.Now()
	requests := 0
	client, url := suppressionWire(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"errors":[{"message":"Cannot query field alarmSuppressionStatus on type Query"}]}`))
	})
	lookup := NewSuppressionLookup(client, url).(*suppressionLookup)
	lookup.now = func() time.Time { return now }
	n := &PolicyNotifier{SuppressionLookup: lookup}
	suppressionMetrics(n)
	logs := captureLogs(t)
	pass := func() { require.Empty(t, n.lookupSuppressions(tenantScoped(), []string{"a1"}, now, 5*time.Second)) }
	pass()
	require.Equal(t, 1, requests)
	require.Equal(t, float64(1), testutil.ToFloat64(n.suppression.WithLabelValues("lookup_failed")))
	require.Contains(t, logs.String(), `"level":"info"`)
	require.Contains(t, logs.String(), "does not serve suppression status yet")
	firstLogCount := strings.Count(logs.String(), "does not serve suppression status yet")
	require.Equal(t, 1, firstLogCount)
	// The cache belongs to the concrete lookup and spans tenants and notifier instances.
	other := &PolicyNotifier{SuppressionLookup: lookup, suppression: n.suppression}
	for i := 0; i < 3; i++ {
		pass()
		require.Empty(t, other.lookupSuppressions(core.WithTenant(context.Background(), "other-tenant"), []string{"other"}, now, 5*time.Second))
	}
	now = now.Add(suppressionUnsupportedFor - time.Nanosecond)
	pass()
	require.Equal(t, 1, requests, "zero requests inside the unsupported window")
	require.Equal(t, float64(1), testutil.ToFloat64(n.suppression.WithLabelValues("lookup_failed")))
	require.Equal(t, firstLogCount, strings.Count(logs.String(), "does not serve suppression status yet"), "cached unsupported status stays silent")
	now = now.Add(time.Nanosecond)
	pass()
	require.Equal(t, 2, requests, "probe again at cache expiry")
	require.Equal(t, float64(2), testutil.ToFloat64(n.suppression.WithLabelValues("lookup_failed")))
	require.Equal(t, 2, strings.Count(logs.String(), "does not serve suppression status yet"))
}

func TestSuppressionErrorsAreNotCached(t *testing.T) {
	for _, mode := range []string{"transport", "500", "graphql", "mixed", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int64
			client, url := suppressionWire(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				switch mode {
				case "transport":
					conn, _, err := w.(http.Hijacker).Hijack()
					require.NoError(t, err)
					_ = conn.Close()
				case "500":
					w.WriteHeader(http.StatusInternalServerError)
				case "graphql":
					_, _ = w.Write([]byte(`{"errors":[{"message":"permission denied"}]}`))
				case "mixed":
					_, _ = w.Write([]byte(`{"errors":[{"message":"Unknown field alarmSuppressionStatus"},{"message":"permission denied"}]}`))
				case "timeout":
					_, _ = io.Copy(io.Discard, r.Body)
					select {
					case <-r.Context().Done():
					case <-time.After(time.Second):
					}
				}
			})
			lookup := NewSuppressionLookup(client, url).(*suppressionLookup)
			n := &PolicyNotifier{SuppressionLookup: lookup}
			suppressionMetrics(n)
			timeout := 5 * time.Second
			if mode == "timeout" {
				timeout = 250 * time.Millisecond
			}
			require.Empty(t, n.lookupSuppressions(tenantScoped(), []string{"a1"}, time.Now(), timeout))
			first := requests.Load()
			require.Greater(t, first, int64(0))
			require.Empty(t, n.lookupSuppressions(tenantScoped(), []string{"a1"}, time.Now(), timeout))
			require.Greater(t, requests.Load(), first, "every non-schema failure must ask again next pass")
			require.True(t, lookup.unsupportedUntil.IsZero())
			require.Equal(t, float64(2), testutil.ToFloat64(n.suppression.WithLabelValues("lookup_failed")))
		})
	}
}

func TestUnsupportedSuppressionOnlyMatchesFieldValidation(t *testing.T) {
	for _, tc := range []struct {
		messages []string
		want     bool
	}{
		{nil, false},
		{[]string{"Cannot query field alarmSuppressionStatus on type Query"}, true},
		{[]string{"UNKNOWN FIELD AlarmSuppressionStatus"}, true},
		{[]string{"cannot query field other"}, false},
		{[]string{"alarmSuppressionStatus permission denied"}, false},
		{[]string{"unknown field alarmSuppressionStatus", "unavailable"}, false},
	} {
		require.Equal(t, tc.want, unsupportedSuppression(fmt.Errorf("wrapped: %w", &svcclient.GraphQLError{Messages: tc.messages})))
	}
	require.False(t, unsupportedSuppression(errors.New("cannot query field alarmSuppressionStatus")))
}

func TestSuppressionReleaseLookupDeadline(t *testing.T) {
	clock := outsideQuiet
	n, api, _, ctx := quietFixture(t, "", "", "", &clock)
	require.NoError(t, n.Notify(ctx, suppressedEvent(clock, clock.Add(time.Hour))))
	clock = clock.Add(time.Minute)
	calls := 0
	n.SuppressionLookup = suppressionFunc(func(ctx context.Context, tokens []string) (map[string]SuppressionStatus, error) {
		calls++
		require.Equal(t, []string{"a1"}, tokens)
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.Positive(t, time.Until(deadline))
		require.LessOrEqual(t, time.Until(deadline), 2*time.Second)
		return nil, nil
	})
	require.NoError(t, n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), clock))
	require.Equal(t, 1, calls)
	require.Empty(t, heldRows(t, api, ctx))
}

func TestHeldOutcomesPartitionCounters(t *testing.T) {
	for _, window := range []bool{false, true} {
		for _, mode := range []string{"released", "dropped_resolved", "superseded", "dropped_policy", "unroutable", "corrupt", "dropped_exhausted"} {
			t.Run(fmt.Sprintf("window=%t/%s", window, mode), func(t *testing.T) {
				clock := insideQuiet
				n, api, fa, ctx := quietFixture(t, "Europe/Berlin", quietSlots, "", &clock)
				suppressionMetrics(n)
				event := eventAt(dmcontract.AlarmEventRaised, "MAJOR", clock)
				if window {
					event = suppressedEvent(clock, clock.Add(time.Hour))
				}
				require.NoError(t, n.Notify(ctx, event))
				rows := heldRows(t, api, ctx)
				require.Len(t, rows, 1)
				require.Equal(t, window, rows[0].HeldByWindow.Valid)
				policies := enabledPolicies(t, api, ctx)
				clock = quietEnds
				outcome := mode
				switch mode {
				case "dropped_resolved":
					event.EventType, event.OccurredTime = dmcontract.AlarmEventAcknowledged, clock
					require.NoError(t, n.Notify(ctx, event))
				case "superseded":
					require.NoError(t, n.Notify(ctx, eventAt(dmcontract.AlarmEventEscalated, "CRITICAL", clock)))
				default:
					switch mode {
					case "dropped_policy":
						require.NoError(t, api.RDB.DB(ctx).Model(&model.NotificationPolicy{}).Where("id = ?", policies[0].ID).Update("enabled", false).Error)
					case "unroutable":
						require.NoError(t, api.RDB.DB(ctx).Model(&model.NotificationChannel{}).Where("token = ?", "smtp-1").Update("enabled", false).Error)
						outcome = quietDroppedPolicy
					case "corrupt":
						require.NoError(t, api.RDB.DB(ctx).Model(rows[0]).Update("event", datatypes.JSON(`{bad`)).Error)
						outcome = quietDroppedExhausted
					case "dropped_exhausted":
						fa.failTimes = 100
						n.attempts = 1
						require.NoError(t, api.RDB.DB(ctx).Model(rows[0]).Update("attempts", maxReleaseAttempts-1).Error)
					}
					err := n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), clock)
					if mode == "corrupt" || mode == "dropped_exhausted" {
						require.Error(t, err)
					} else {
						require.NoError(t, err)
					}
				}
				require.Empty(t, heldRows(t, api, ctx))
				quietWant, suppressionWant := float64(1), float64(0)
				if window {
					quietWant, suppressionWant = 0, 1
				}
				for _, label := range []string{quietHeld, quietReleased, quietDroppedResolved, quietSuperseded, quietDroppedPolicy, quietDroppedExhausted} {
					q, s := float64(0), float64(0)
					if label == quietHeld || label == outcome {
						q, s = quietWant, suppressionWant
					}
					require.Equal(t, q, testutil.ToFloat64(n.quiet.WithLabelValues(label)), "quiet outcome %s", label)
					require.Equal(t, s, testutil.ToFloat64(n.suppression.WithLabelValues(label)), "suppression outcome %s", label)
				}
			})
		}
	}
}
