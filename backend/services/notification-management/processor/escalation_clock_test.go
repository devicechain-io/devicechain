// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/storehold/holdtest"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEscalationWindowUsesDispatchClock(t *testing.T) {
	for _, tc := range []struct {
		name  string
		skew  time.Duration
		tick  time.Duration
		level int
	}{
		{"late reading gets full window", -2 * time.Hour, time.Minute, 0},
		{"late reading eventually escalates", -2 * time.Hour, 31 * time.Minute, 1},
		{"ahead device does not delay escalation", 5 * time.Minute, 31 * time.Minute, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := fencedApi(t)
			ctx := tenantScoped()
			seedEscalatingPolicy(t, api, ctx)
			require.NoError(t, stateDB(api).WithContext(ctx).Model(&model.NotificationPolicy{}).Where("token = ?", "p").Update("escalate_after_seconds", 1800).Error)
			clock := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
			sent := clock
			fa := &fakeAdapter{}
			n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa})
			n.api, n.store = api, &fakeSecretStore{}
			n.now = func() time.Time { return clock }
			eventAt := sent.Add(tc.skew)
			require.NoError(t, n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, eventAt, eventAt)))
			require.Equal(t, 1, fa.calls)
			clock = sent.Add(tc.tick)
			s := &EscalationScheduler{Api: api, Notifier: n, defaultMax: 5}
			require.NoError(t, s.runOnce(context.Background()))
			rows, err := api.NotificationStatesByAlarmToken(ctx, []string{"a1"})
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, tc.level, rows[0].EscalationLevel)
			require.Equal(t, 1+tc.level, fa.calls)
			if tc.level == 0 {
				require.True(t, rows[0].LastNotifiedAt.Time.Equal(eventAt))
			}
			if tc.level == 1 {
				clock = sent.Add(60 * time.Minute)
				require.NoError(t, s.runOnce(context.Background()))
				require.Equal(t, 2, fa.calls, "second tier must wait a full window after first claim")
				clock = sent.Add(61 * time.Minute)
				require.NoError(t, s.runOnce(context.Background()))
				require.Equal(t, 3, fa.calls)
			}
		})
	}
}

func TestEscalationWindowRestartsAfterClear(t *testing.T) {
	api := fencedApi(t)
	ctx := tenantScoped()
	seedEscalatingPolicy(t, api, ctx)
	require.NoError(t, stateDB(api).WithContext(ctx).Model(&model.NotificationPolicy{}).Where("token = ?", "p").Update("escalate_after_seconds", 1800).Error)
	sent := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	clock := sent
	fa := &fakeAdapter{}
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: fa})
	n.api, n.store = api, &fakeSecretStore{}
	n.now = func() time.Time { return clock }
	cycle := sent.Add(-2 * time.Hour)
	require.NoError(t, n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, cycle, cycle)))
	scheduler := &EscalationScheduler{Api: api, Notifier: n, defaultMax: 5}
	clock = sent.Add(31 * time.Minute)
	require.NoError(t, scheduler.runOnce(context.Background()))
	require.Equal(t, 2, fa.calls)
	require.NoError(t, n.Notify(ctx, cycleEvent(dmmodel.AlarmEventCleared, cycle, cycle.Add(time.Minute))))
	clock = sent.Add(time.Hour)
	cycle = cycle.Add(2 * time.Minute)
	require.NoError(t, n.Notify(ctx, cycleEvent(dmmodel.AlarmEventRaised, cycle, cycle)))
	require.Equal(t, 3, fa.calls)
	clock = sent.Add(61 * time.Minute)
	require.NoError(t, scheduler.runOnce(context.Background()))
	require.Equal(t, 3, fa.calls, "new cycle gets its own full window")
	clock = sent.Add(91 * time.Minute)
	require.NoError(t, scheduler.runOnce(context.Background()))
	require.Equal(t, 4, fa.calls)
}

func TestEscalationWindowKeepsPageTimeThroughStoreHold(t *testing.T) {
	api := fencedApi(t)
	ctx := tenantScoped()
	seedEscalatingPolicy(t, api, ctx)
	require.NoError(t, stateDB(api).WithContext(ctx).Model(&model.NotificationPolicy{}).Where("token = ?", "p").Update("escalate_after_seconds", 1800).Error)
	pagedAt := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	clock := pagedAt
	store := &holdtest.Store{}
	faults := &writeFaults{err: holdtest.ConnLost, store: store, healOnProbe: 3}
	injectWriteFaults(stateDB(api), faults)

	sent := 0
	n := testNotifier(map[string]ChannelAdapter{model.ChannelTypeSMTP: &funcAdapter{fn: func(int) error {
		sent++
		if sent == 1 {
			assert.Equal(t, pagedAt, clock, "the initial page must be delivered at T")
			faults.arm()
		}
		return nil
	}}})
	n.api, n.store = api, &fakeSecretStore{}
	n.now = func() time.Time { return clock }
	np := newTestProcessor(n)
	np.Hold = holdForProbe(func(c context.Context) error {
		// Advance during the outage, before the successful RecordPagedFrom retry.
		clock = pagedAt.Add(2 * time.Hour)
		return faults.probe(c)
	})
	ack, run := alarmMessage(t, cycleEvent(dmmodel.AlarmEventRaised, pagedAt, pagedAt))
	run(np)
	require.Positive(t, faults.failed.Load(), "the state write must have failed before recovery")
	require.False(t, faults.armed.Load(), "the store must have recovered")
	require.Equal(t, pagedAt.Add(2*time.Hour), clock)
	require.Equal(t, 1, sent, "settling the write must not resend the page")
	require.EqualValues(t, 1, ack.Acked.Load())
	rows, err := api.NotificationStatesByAlarmToken(ctx, []string{"a1"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].EscalationBaseAt.Valid)
	assert.Equal(t, pagedAt, rows[0].EscalationBaseAt.Time.UTC(), "the escalation window starts at delivery, not store recovery")

	// Evaluate the original window independently of the later recovery time.
	clock = pagedAt.Add(31 * time.Minute)
	scheduler := &EscalationScheduler{Api: api, Notifier: n, defaultMax: 5}
	require.NoError(t, scheduler.runOnce(context.Background()))
	rows, err = api.NotificationStatesByAlarmToken(ctx, []string{"a1"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, 1, rows[0].EscalationLevel, "the 30-minute policy must claim the first tier at T+31min")
	assert.Equal(t, 2, sent)
}
