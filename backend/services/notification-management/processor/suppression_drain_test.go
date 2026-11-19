// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/logger"
)

type suppressionDrainAdapter struct {
	mu    sync.Mutex
	clock *time.Time
	until time.Time
	sent  map[string]int
	early bool
}

func (a *suppressionDrainAdapter) Deliver(_ context.Context, c *model.NotificationChannel, _ string, _ []string, msg *RenderedNotification) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.clock.Before(a.until) {
		a.early = true
	}
	a.sent[msg.Alarm.AlarmToken+"/"+c.Token]++
	return nil
}

func TestSuppressionDrain(t *testing.T) {
	for _, alarms := range []int{400, 2000} {
		t.Run(fmt.Sprint(alarms), func(t *testing.T) {
			if testing.Short() && alarms == 2000 {
				t.Skip("full drain measurement; 400-alarm case always runs")
			}
			started := time.Now()
			clock := outsideQuiet
			until := clock.Add(time.Hour)
			n, api, _, ctx := quietFixture(t, "", "", "", &clock)
			api.RDB.Database.Logger = logger.Default.LogMode(logger.Silent)
			suppressionMetrics(n)
			n.oldestDue = prometheus.NewGauge(prometheus.GaugeOpts{Name: "deferral_oldest_due_seconds"})
			_, err := api.CreateNotificationChannel(ctx, &model.NotificationChannelCreateRequest{Token: "smtp-2", ChannelType: model.ChannelTypeSMTP, Enabled: true, Config: strPointer(`{"host":"smtp.example.com","from":"alarms@example.com"}`)})
			require.NoError(t, err)
			_, err = api.CreateNotificationPolicy(ctx, &model.NotificationPolicyCreateRequest{Token: "p2", Enabled: true, Rules: []*model.NotificationRuleCreateRequest{{Severity: model.SeverityAny, ChannelToken: "smtp-2", Recipients: strPointer(`["second@x.com"]`)}}})
			require.NoError(t, err)
			adapter := &suppressionDrainAdapter{clock: &clock, until: until, sent: map[string]int{}}
			n.adapters[model.ChannelTypeSMTP] = adapter
			for i := 0; i < alarms; i++ {
				event := suppressedEvent(clock, until)
				event.AlarmToken = fmt.Sprintf("drain-%04d", i)
				require.NoError(t, n.Notify(ctx, event))
			}
			count := func() int64 {
				var total int64
				require.NoError(t, api.RDB.DB(ctx).Model(&model.NotificationDeferral{}).Count(&total).Error)
				return total
			}
			require.EqualValues(t, alarms*2, count())
			require.Empty(t, adapter.sent)
			calls := 0
			n.SuppressionLookup = suppressionFunc(func(_ context.Context, tokens []string) (map[string]SuppressionStatus, error) {
				calls++
				require.NotEmpty(t, tokens)
				require.LessOrEqual(t, len(tokens), releaseBatch)
				found := map[string]SuppressionStatus{}
				for _, token := range tokens {
					status := SuppressionStatus{Severity: "MAJOR", State: "ACTIVE"}
					if clock.Before(until) {
						status.WindowToken = "window-1"
						status.Until = until
					}
					found[token] = status
				}
				return found, nil
			})
			scheduler := &EscalationScheduler{Api: api, Notifier: n, defaultMax: 5}
			clock = clock.Add(time.Minute)
			require.NoError(t, scheduler.runOnce(context.Background()))
			require.Empty(t, adapter.sent)
			require.EqualValues(t, alarms*2, count())
			require.Equal(t, 1, calls)
			clock = until.Add(time.Second)
			// ReleaseHeld reads at most releaseBatch ROWS once per tenant pass. Each
			// successful release removes those rows, and this sole tenant has no slice
			// contention or escalation policies. Thus ceil(rows/releaseBatch) send passes
			// suffice. runOnce observes due age BEFORE removal, so one extra empty pass
			// is required only to reset the gauge, not to deliver any page.
			bound := (alarms*2 + releaseBatch - 1) / releaseBatch
			for pass := 0; pass < bound; pass++ {
				before := calls
				wall := time.Now()
				require.NoError(t, scheduler.runOnce(context.Background()))
				t.Logf("alarms=%d pass=%d wall=%s remaining=%d", alarms, pass+1, time.Since(wall), count())
				require.LessOrEqual(t, calls-before, 1, "one batched lookup per release pass; no escalation is configured")
			}
			require.Zero(t, count(), "must drain within ceil(rows/releaseBatch) passes")
			require.Len(t, adapter.sent, alarms*2)
			require.False(t, adapter.early)
			for i := 0; i < alarms; i++ {
				for _, channel := range []string{"smtp-1", "smtp-2"} {
					require.Equal(t, 1, adapter.sent[fmt.Sprintf("drain-%04d/%s", i, channel)])
				}
			}
			require.Positive(t, testutil.ToFloat64(n.oldestDue))
			require.NoError(t, scheduler.runOnce(context.Background()))
			require.Zero(t, testutil.ToFloat64(n.oldestDue))
			require.Len(t, adapter.sent, alarms*2)
			require.Equal(t, float64(alarms*2), testutil.ToFloat64(n.suppression.WithLabelValues("released")))
			t.Logf("alarms=%d send_passes=%d gauge_reset_passes=1 total_wall=%s", alarms, bound, time.Since(started))
		})
	}
}
