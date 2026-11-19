// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	dmcontract "github.com/devicechain-io/dc-device-management/contract"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

type fakeAlarmLookup struct {
	found map[string]string
	err   error
	calls [][]string
}

func (f *fakeAlarmLookup) LookupAlarms(_ context.Context, tokens []string) (map[string]string, error) {
	f.calls = append(f.calls, append([]string(nil), tokens...))
	return f.found, f.err
}

func TestEscalationAlarmBackstop(t *testing.T) {
	for _, mode := range []string{"missing", "cleared", "present", "error", "unconfigured"} {
		t.Run(mode, func(t *testing.T) {
			n, rec, api, ctx := pagedThenDue(t, namedAlarm())
			states, err := api.OpenNotificationStates(ctx)
			require.NoError(t, err)
			require.Len(t, states, 1)
			state := states[0]
			lookup := &fakeAlarmLookup{found: map[string]string{}}
			switch mode {
			case "cleared":
				lookup.found[state.AlarmToken] = "CLEARED"
			case "present":
				lookup.found[state.AlarmToken] = "ACTIVE"
			case "error":
				lookup.err = errors.New("owner unavailable")
			}
			if mode != "unconfigured" {
				n.AlarmLookup = lookup
			}
			n.lookupErrors = prometheus.NewCounter(prometheus.CounterOpts{Name: "lookup_errors_test"})
			policies, err := api.EnabledNotificationPolicies(ctx)
			require.NoError(t, err)
			require.NoError(t, n.Escalate(ctx, state, policies, n.clock(), 5))
			var stored model.NotificationState
			require.NoError(t, api.RDB.DB(ctx).Where("alarm_token = ?", state.AlarmToken).First(&stored).Error)
			terminal := mode == "missing" || mode == "cleared"
			require.Equal(t, terminal, stored.ClearedAt.Valid)
			if terminal {
				require.Len(t, rec.sent, 1)
				require.Zero(t, stored.EscalationLevel)
			} else {
				require.Len(t, rec.sent, 2)
				require.Equal(t, 1, stored.EscalationLevel)
			}
			wantErrors := float64(0)
			if mode == "error" {
				wantErrors = 1
			}
			require.Equal(t, wantErrors, testutil.ToFloat64(n.lookupErrors))
		})
	}
}

func TestOrphanAlarmSweep(t *testing.T) {
	for _, mode := range []string{"missing", "present", "error", "recent"} {
		t.Run(mode, func(t *testing.T) {
			n, _, api, ctx := pagedThenDue(t, namedAlarm())
			// Match the store's cycleKey precision when writing the fixture directly.
			now := time.Now().UTC().Truncate(time.Microsecond)
			old := now.Add(-2 * OrphanAlarmHorizon)
			require.NoError(t, api.RDB.DB(ctx).Model(&model.NotificationState{}).Where("1 = 1").Updates(map[string]any{"created_at": old, "cycle_raised_at": old}).Error)
			states, err := api.OpenNotificationStates(ctx)
			require.NoError(t, err)
			require.Len(t, states, 1)
			lookup := &fakeAlarmLookup{found: map[string]string{}}
			if mode == "present" {
				lookup.found[states[0].AlarmToken] = "ACTIVE"
			}
			if mode == "error" {
				lookup.err = errors.New("owner unavailable")
			}
			if mode == "recent" {
				require.NoError(t, api.RDB.DB(ctx).Model(states[0]).Update("cycle_raised_at", now).Error)
			}
			n.AlarmLookup = lookup
			err = n.SweepOrphans(ctx, now)
			if mode == "error" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			var stored model.NotificationState
			require.NoError(t, api.RDB.DB(ctx).First(&stored, states[0].ID).Error)
			require.Equal(t, mode == "missing", stored.ClearedAt.Valid)
			if mode == "recent" {
				require.Empty(t, lookup.calls)
			} else {
				require.Equal(t, [][]string{{stored.AlarmToken}}, lookup.calls)
			}
		})
	}
}

func TestDeletedOriginatorClearRendering(t *testing.T) {
	e := namedAlarm()
	e.EventType, e.State, e.ClearReason = dmcontract.AlarmEventCleared, "CLEARED", dmcontract.AlarmClearReasonOriginatorDeleted
	m := renderNotification(e)
	for name, value := range map[string]any{
		"smtp subject": m.Subject, "smtp body": m.TextBody,
		"slack": slackMessage(m, "", ""), "teams": teamsMessage(m, "", ""),
		"pagerduty": pagerDutyEvent("secret", pdResolve, &model.PagerDutyConfig{}, m),
		"webhook":   webhookBody(nil, m, ""),
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), "was deleted") {
				t.Fatalf("missing deletion wording: %s", encoded)
			}
		})
	}
	require.Equal(t, "ORIGINATOR_DELETED", m.Payload["clearReason"])
	e.ClearReason = ""
	ordinary := renderNotification(e)
	require.NotContains(t, ordinary.Subject, "was deleted")
	require.NotContains(t, ordinary.Payload, "clearReason")
}

func TestRepeatedDeletedOriginatorClearIsIdempotent(t *testing.T) {
	api := fencedApi(t)
	ctx := tenantScoped()
	seedRoute(t, api, ctx, "p", true, "CRITICAL", "paging", model.ChannelTypeSMTP)
	n, paging, _ := twoChannelNotifier(t, api)
	raised := time.Now().UTC().Add(-time.Hour)
	require.NoError(t, n.Notify(ctx, cycleEvent(dmcontract.AlarmEventRaised, raised, raised)))
	clear := cycleEvent(dmcontract.AlarmEventCleared, raised, raised.Add(time.Minute))
	clear.ClearReason = dmcontract.AlarmClearReasonOriginatorDeleted
	require.NoError(t, n.Notify(ctx, clear))
	require.NoError(t, n.Notify(ctx, clear))
	require.Equal(t, []string{"RAISED", "CLEARED"}, paging.types())
	open, err := api.OpenNotificationStates(ctx)
	require.NoError(t, err)
	require.Empty(t, open)
}
