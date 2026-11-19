// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// suppressionReleaseFixture uses the existing SQLite rig and status fake. Release
// passes below run after the window ends, so the live service returns no holds.
func suppressionReleaseFixture(t *testing.T, tz, slots, bypass string, clock *time.Time) (*PolicyNotifier, *model.Api, *fakeAdapter, context.Context) {
	n, api, fa, ctx := quietFixture(t, tz, slots, bypass, clock)
	n.SuppressionLookup = suppressionFunc(func(context.Context, []string) (map[string]SuppressionStatus, error) {
		return map[string]SuppressionStatus{"a1": {}}, nil
	})
	return n, api, fa, ctx
}

func TestSuppressionReleaseRereadsCurrentSeverity(t *testing.T) {
	for _, window := range []bool{true, false} {
		for _, kind := range []dmmodel.AlarmEventType{dmmodel.AlarmEventRaised, dmmodel.AlarmEventEscalated} {
			name := "window/" + kind.String()
			if !window {
				name = "quiet/" + kind.String()
			}
			t.Run(name, func(t *testing.T) {
				clock := insideQuiet.Add(123 * time.Nanosecond)
				n, api, _, ctx := suppressionReleaseFixture(t, "Europe/Berlin", quietSlots, "", &clock)
				rec := &msgRecorder{}
				n.adapters[model.ChannelTypeSMTP] = rec
				// An existing state from this cycle, with database-normalized RaisedTime.
				// TouchSeverity deliberately does not create a state for an unpaged alarm.
				require.NoError(t, api.RecordNotification(ctx, "a1", "k", "MAJOR", clock, clock))
				p := enabledPolicies(t, api, ctx)[0]
				require.NoError(t, api.RDB.DB(ctx).Model(&model.NotificationRule{}).
					Where("policy_id = ?", p.ID).Update("severity", "MAJOR").Error)
				e := eventAt(kind, "MAJOR", clock)
				e.PreviousSeverity = "WARNING"
				if window {
					until := clock.Add(time.Hour)
					e.SuppressedUntil, e.SuppressionWindowToken = &until, "window-1"
				}
				require.NoError(t, n.Notify(ctx, e))
				before := heldRows(t, api, ctx)
				require.Len(t, before, 1)
				down := eventAt(dmmodel.AlarmEventDeescalated, "MINOR", clock.Add(time.Second))
				down.RaisedTime, down.PreviousSeverity = e.RaisedTime, "MAJOR"
				require.NoError(t, n.Notify(ctx, down))
				states, err := api.NotificationStatesByAlarmToken(ctx, []string{"a1"})
				require.NoError(t, err)
				require.Len(t, states, 1)
				require.Equal(t, "MINOR", states[0].Severity)
				after := heldRows(t, api, ctx)
				require.Len(t, after, 1)
				require.Equal(t, before[0].Event, after[0].Event)
				require.Equal(t, "MAJOR", after[0].Severity)
				require.Empty(t, rec.sent)
				clock = quietEnds
				require.NoError(t, n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), clock))
				// Only a MAJOR rule exists, so delivery proves routing used the held severity.
				require.Len(t, rec.sent, 1)
				msg := rec.sent[0]
				if window {
					require.Equal(t, "MINOR", msg.Alarm.Severity)
					require.Contains(t, msg.Subject, "[MINOR]")
					require.Regexp(t, `(?m)^Severity:\s+MINOR$`, msg.TextBody)
					require.Equal(t, "MINOR", msg.Payload["severity"])
					require.Empty(t, msg.Alarm.PreviousSeverity)
					require.NotContains(t, msg.Payload, "previousSeverity")
					require.NotContains(t, msg.TextBody, "Previous severity")
					require.Equal(t, "RAISED", msg.Alarm.EventType)
				} else {
					require.Equal(t, renderNotification(e), msg, "quiet-hours-only rendering retains its held event")
				}
				require.Empty(t, heldRows(t, api, ctx))
			})
		}
	}
}

func TestSuppressionReleaseSeverityFallback(t *testing.T) {
	for _, mode := range []string{"no-state", "unknown-tier", "wildcard-tier", "no-state-cycle", "older-state-cycle", "no-event-cycle"} {
		t.Run(mode, func(t *testing.T) {
			clock := outsideQuiet
			n, api, _, ctx := suppressionReleaseFixture(t, "", "", "", &clock)
			rec := &msgRecorder{}
			n.adapters[model.ChannelTypeSMTP] = rec
			e := suppressedEvent(clock, clock.Add(time.Hour))
			if mode == "no-event-cycle" {
				e.RaisedTime = time.Time{}
			}
			require.NoError(t, n.Notify(ctx, e))
			if mode != "no-state" {
				raised := clock
				if mode == "older-state-cycle" {
					raised = clock.Add(-time.Hour)
				}
				require.NoError(t, api.RecordNotification(ctx, "a1", "k", "MINOR", raised, clock))
				if mode == "no-state-cycle" {
					require.NoError(t, api.RDB.DB(ctx).Model(&model.NotificationState{}).
						Where("alarm_token = ?", "a1").Update("cycle_raised_at", nil).Error)
				}
				if mode == "unknown-tier" {
					require.NoError(t, api.TouchSeverity(ctx, "a1", "UNKNOWN"))
				}
				if mode == "wildcard-tier" {
					require.NoError(t, api.TouchSeverity(ctx, "a1", model.SeverityAny))
				}
			}
			clock = clock.Add(time.Hour)
			require.NoError(t, n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), clock))
			require.Len(t, rec.sent, 1)
			require.Equal(t, "MAJOR", rec.sent[0].Alarm.Severity)
		})
	}
}

func TestSuppressionReleaseLeaseRace(t *testing.T) {
	clock := outsideQuiet
	n, api, loser, ctx := suppressionReleaseFixture(t, "", "", "", &clock)
	suppressionMetrics(n)
	require.NoError(t, n.Notify(ctx, suppressedEvent(clock, clock.Add(time.Minute))))
	clock = clock.Add(time.Minute)
	winner := &fakeAdapter{}
	replica := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: winner})
	replica.api, replica.store, replica.now = api, &fakeSecretStore{}, n.now
	replica.SuppressionLookup = n.SuppressionLookup
	suppressionMetrics(replica)
	policies := enabledPolicies(t, api, ctx)
	interleaved := false
	// Deterministically overlap the two ReleaseHeld calls: replica 1 has read the due
	// row but has not leased it. Replica 2 reads that same row and completes its send
	// before replica 1 attempts its stale CAS. This exercises won, unlike a second
	// pass started after leasing (which merely finds no due rows).
	db := stateDB(api)
	require.NoError(t, db.Callback().Query().After("gorm:query").Register("test:suppression-lease-race", func(tx *gorm.DB) {
		if interleaved || tx.Statement.Schema == nil || tx.Statement.Schema.Name != "NotificationDeferral" {
			return
		}
		rows, ok := tx.Statement.Dest.(*[]*model.NotificationDeferral)
		if !ok || len(*rows) == 0 {
			return
		}
		interleaved = true
		require.NoError(t, replica.ReleaseHeld(ctx, policies, clock))
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove("test:suppression-lease-race") })
	require.NoError(t, n.ReleaseHeld(ctx, policies, clock))
	require.True(t, interleaved, "both replicas must read the due row before the stale lease attempt")
	require.Equal(t, 1, winner.calls)
	require.Zero(t, loser.calls, "the replica that loses the lease sends nothing")
	require.Equal(t, float64(1), testutil.ToFloat64(replica.suppression.WithLabelValues("released")))
	require.Zero(t, testutil.ToFloat64(n.suppression.WithLabelValues("released")))
	require.Empty(t, heldRows(t, api, ctx))
}

func TestSuppressionReleaseCrashAfterLease(t *testing.T) {
	clock := outsideQuiet
	n, api, fa, ctx := suppressionReleaseFixture(t, "", "", "", &clock)
	suppressionMetrics(n)
	require.NoError(t, n.Notify(ctx, suppressedEvent(clock, clock.Add(time.Minute))))
	clock = clock.Add(time.Minute)
	due, err := api.DueNotificationDeferrals(ctx, clock, releaseBatch)
	require.NoError(t, err)
	require.Len(t, due, 1)
	until := clock.Add(n.releaseLease())
	won, err := api.LeaseNotificationDeferral(ctx, due[0], until)
	require.NoError(t, err)
	require.True(t, won)
	// The owner dies here, before delivery or FinishNotificationDeferral.
	policies := enabledPolicies(t, api, ctx)
	clock = until.Add(-time.Nanosecond)
	require.NoError(t, n.ReleaseHeld(ctx, policies, clock))
	require.Zero(t, fa.calls)
	require.Len(t, heldRows(t, api, ctx), 1)
	clock = until.Add(time.Second)
	require.NoError(t, n.ReleaseHeld(ctx, policies, clock))
	require.Equal(t, 1, fa.calls)
	require.Empty(t, heldRows(t, api, ctx))
	require.NoError(t, n.ReleaseHeld(ctx, policies, clock.Add(time.Hour)))
	require.Equal(t, 1, fa.calls, "a recovered release is completed exactly once")
	require.Equal(t, float64(1), testutil.ToFloat64(n.suppression.WithLabelValues("released")))
}

func TestReRaiseInsideWindowHoldsNewCycleDropsOld(t *testing.T) {
	for _, survivingOldRow := range []bool{false, true} {
		name := "replacement"
		if survivingOldRow {
			name = "surviving-earlier-cycle"
		}
		t.Run(name, func(t *testing.T) {
			clock := outsideQuiet
			n, api, _, ctx := suppressionReleaseFixture(t, "", "", "", &clock)
			suppressionMetrics(n)
			rec := &msgRecorder{}
			n.adapters[model.ChannelTypeSMTP] = rec
			end := clock.Add(time.Hour)
			first := suppressedEvent(clock, end)
			require.NoError(t, n.Notify(ctx, first))
			old := heldRows(t, api, ctx)
			require.Len(t, old, 1)
			clear := eventAt(dmmodel.AlarmEventCleared, "MAJOR", clock.Add(time.Second))
			clear.RaisedTime, clear.State = first.RaisedTime, "CLEARED"
			require.NoError(t, n.Notify(ctx, clear))
			require.Empty(t, heldRows(t, api, ctx))
			require.Equal(t, float64(1), testutil.ToFloat64(n.suppression.WithLabelValues("dropped_resolved")))
			// A delayed redelivery can put cycle 1 back after its clear. Cycle 2 must
			// replace this row, not add a second row for the same alarm and policy.
			old[0].ID = 0
			require.NoError(t, api.DeferNotifications(ctx, old))
			priorID := heldRows(t, api, ctx)[0].ID
			clock = clock.Add(2 * time.Second)
			second := suppressedEvent(clock, end)
			require.NoError(t, n.Notify(ctx, second))
			held := heldRows(t, api, ctx)
			require.Len(t, held, 1)
			require.Equal(t, priorID, held[0].ID)
			var stored dmmodel.AlarmStateChangeEvent
			require.NoError(t, json.Unmarshal(held[0].Event, &stored))
			require.True(t, second.RaisedTime.Equal(stored.RaisedTime))
			require.Equal(t, "window-1", held[0].HeldByWindow.String)
			require.Empty(t, rec.sent)
			if survivingOldRow {
				// Model a release worker retaining an earlier-cycle row while another
				// policy has already recorded cycle 2. Clear stamps were reset, so only
				// heldForEarlierCycle can reject it; severity must not leak from cycle 2.
				require.NoError(t, api.RecordNotification(ctx, "a1", "k", "MINOR", second.RaisedTime, clock))
				oldPolicy, err := api.CreateNotificationPolicy(ctx, &model.NotificationPolicyCreateRequest{
					Token: "old-policy", Enabled: true,
					Rules: []*model.NotificationRuleCreateRequest{{Severity: model.SeverityAny, ChannelToken: "smtp-1", Recipients: strPointer(`["old@x.com"]`)}},
				})
				require.NoError(t, err)
				old[0].ID, old[0].PolicyId = 0, oldPolicy.ID
				require.NoError(t, api.DeferNotifications(ctx, old))
				require.Len(t, heldRows(t, api, ctx), 2)
			}
			clock = end
			require.NoError(t, n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), clock))
			require.Len(t, rec.sent, 1, "only cycle 2 is paged")
			require.True(t, second.RaisedTime.Equal(rec.sent[0].Alarm.RaisedTime))
			require.Empty(t, heldRows(t, api, ctx))
			dropped := float64(1)
			if survivingOldRow {
				dropped++
			}
			require.Equal(t, dropped, testutil.ToFloat64(n.suppression.WithLabelValues("dropped_resolved")))
			require.Equal(t, float64(1), testutil.ToFloat64(n.suppression.WithLabelValues("released")))
		})
	}
}

func TestSuppressionReleaseRendersLiveSeverityWithoutStateRow(t *testing.T) {
	clock := outsideQuiet
	n, api, _, ctx := suppressionReleaseFixture(t, "", "", "", &clock)
	rec := &msgRecorder{}
	n.adapters[model.ChannelTypeSMTP] = rec
	p := enabledPolicies(t, api, ctx)[0]
	require.NoError(t, api.RDB.DB(ctx).Model(&model.NotificationRule{}).Where("policy_id = ?", p.ID).Update("severity", "MAJOR").Error)
	e := suppressedEvent(clock, clock.Add(time.Minute))
	require.NoError(t, n.Notify(ctx, e))
	down := eventAt(dmmodel.AlarmEventDeescalated, "MINOR", clock.Add(time.Second))
	down.RaisedTime = e.RaisedTime
	require.NoError(t, n.Notify(ctx, down))
	states, err := api.NotificationStatesByAlarmToken(ctx, []string{"a1"})
	require.NoError(t, err)
	require.Empty(t, states)
	client, url := suppressionWire(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"alarmSuppressionStatus":[{"alarmToken":"a1","severity":"MINOR","state":"ACTIVE","suppression":null}]}}`))
	})
	n.SuppressionLookup = NewSuppressionLookup(client, url)
	clock = clock.Add(time.Minute)
	require.NoError(t, n.ReleaseHeld(ctx, enabledPolicies(t, api, ctx), clock))
	require.Len(t, rec.sent, 1, "MAJOR-only routing must still deliver")
	require.Equal(t, "MINOR", rec.sent[0].Alarm.Severity)
	require.Contains(t, rec.sent[0].Subject, "[MINOR]")
	require.Equal(t, "MINOR", rec.sent[0].Payload["severity"])
	require.Empty(t, heldRows(t, api, ctx))
}
