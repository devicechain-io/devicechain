// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backfill

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// The floor is the LATER of the retention floor and the event-time floor (plus slack): with no
// retention it is the event-time floor, pinned here; with a retention longer than the platform
// accepts it must not reach back to retention (5 years would be ~1,800 refreshes a pass); with a
// shorter one nothing older is kept, so it is retention.
func TestTheTailFloorIsTheLaterOfRetentionAndTheEventTimeFloor(t *testing.T) {
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	chunk := 24 * time.Hour
	maxAgeFloor := now.Add(-eventtime.MaxAge).Add(-chunk).Add(-tailFloorSlack)
	for _, c := range []struct {
		name string
		days int
		want time.Time
	}{
		{"retention off", 0, maxAgeFloor},
		{"five years of retention", 5 * 365, maxAgeFloor},
		{"thirty days of retention", 30, now.Add(-30 * 24 * time.Hour)},
	} {
		s := &TailSweeper{cfg: Config{ChunkInterval: chunk, RetentionDays: c.days}}
		if got := s.floor(now); !got.Equal(c.want) {
			t.Errorf("%s: floor = %s, want %s", c.name, got, c.want)
		}
	}
	if !maxAgeFloor.Before(now.Add(-eventtime.MaxAge)) {
		t.Fatal("the slack must put the floor below the event-time limit")
	}
}

// Exercise RunOnce, including scheduling, with deterministic catalog and lock results.
func TestTailPassOutcomes(t *testing.T) {
	boom := errors.New("refresh failed")
	for _, tc := range []struct {
		name                                        string
		windows, fail, backoff, lockAt, budgetAfter int
		notDue, catalogError, lockError, cancelled  bool
		want                                        error
		calls                                       int
		complete                                    bool
	}{
		{name: "not yet due", windows: 2, notDue: true, want: core.ErrPassSkipped},
		{name: "no windows due", complete: true},
		{name: "all windows already done", windows: -2, complete: true},
		{name: "all refreshed", windows: 2, calls: 2, complete: true},
		{name: "lock declined", windows: 2, lockAt: 1, calls: 1, want: core.ErrPassSkipped},
		{name: "all backed off", windows: 2, backoff: 2, want: core.ErrPassSkipped, complete: true},
		{name: "backoff then lock decline", windows: 2, backoff: 1, lockAt: 1, calls: 1, want: core.ErrPassSkipped},
		{name: "budget before any refresh", windows: 2, budgetAfter: -1, want: core.ErrPassSkipped},
		{name: "refreshed then failed", windows: 2, fail: 1, calls: 2, want: core.ErrPassPartial, complete: true},
		{name: "refreshed then lock declined", windows: 2, lockAt: 2, calls: 2, want: core.ErrPassPartial},
		{name: "refreshed with backoff", windows: 2, backoff: 1, calls: 1, want: core.ErrPassPartial, complete: true},
		{name: "refreshed then budget exhausted", windows: 2, budgetAfter: 1, calls: 1, want: core.ErrPassPartial},
		{name: "one failed", windows: 1, fail: 1, calls: 1, want: core.ErrPassPartial, complete: true},
		{name: "all two failed", windows: 2, fail: 2, calls: 2, want: boom, complete: true},
		{name: "failure then lock declined", windows: 2, fail: 1, lockAt: 2, calls: 2, want: core.ErrPassPartial},
		{name: "two failures then lock declined", windows: 3, fail: 2, lockAt: 3, calls: 3, want: boom},
		{name: "failure then budget exhausted", windows: 2, fail: 1, budgetAfter: 1, calls: 1, want: core.ErrPassPartial},
		{name: "catalog error", catalogError: true, want: boom},
		{name: "lock acquisition error", windows: 1, lockAt: 1, lockError: true, calls: 1, want: boom},
		{name: "cancelled", windows: 1, cancelled: true, want: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
			ms := &core.Microservice{FunctionalArea: "event-management"}
			ms.UseMetricsRegistry(prometheus.NewRegistry())
			s := newTailSweeper(ms, nil, Config{ChunkInterval: 24 * time.Hour}, NewMetrics(ms), core.NewNoOpLifecycleCallbacks(), func() time.Time { return clock })
			s.jitter = func() time.Duration { return 0 }
			if !tc.notDue {
				s.nextPass = clock.Add(-time.Second)
			}
			scheduled := s.nextPass
			windows := make([]chunkWindow, max(tc.windows, -tc.windows))
			for i := range windows {
				start := clock.Add(-time.Duration(45+i) * 24 * time.Hour)
				windows[i] = chunkWindow{Start: start, End: start.Add(24 * time.Hour)}
				if tc.windows < 0 {
					s.cycleDone[start] = struct{}{}
				}
				if i < tc.backoff {
					s.backoff[start] = tailBackoff{until: clock.Add(time.Hour)}
				}
			}
			reads, calls := 0, 0
			s.readWindows = func(context.Context, time.Time, time.Time) ([]chunkWindow, error) {
				reads++
				if tc.catalogError {
					return nil, boom
				}
				if tc.budgetAfter < 0 {
					clock = clock.Add(tailBudget + time.Second)
				}
				return windows, nil
			}
			s.refreshLocked = func(_ context.Context, w chunkWindow) (bool, error) {
				calls++
				require.Equal(t, windows[tc.backoff+calls-1], w, "newest first")
				if calls == tc.lockAt {
					if tc.lockError {
						return false, boom
					}
					return false, nil
				}
				if calls == tc.budgetAfter {
					clock = clock.Add(tailBudget + time.Second)
				}
				if calls <= tc.fail {
					return true, boom
				}
				return true, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			err := s.RunOnce(ctx)
			require.True(t, errors.Is(err, tc.want), "got %v, want %v", err, tc.want)
			require.Equal(t, errors.Is(tc.want, core.ErrPassPartial), errors.Is(err, core.ErrPassPartial))
			require.Equal(t, errors.Is(tc.want, core.ErrPassSkipped), errors.Is(err, core.ErrPassSkipped))
			require.Equal(t, tc.calls, calls)
			if tc.notDue {
				require.Zero(t, reads)
			} else {
				require.Equal(t, 1, reads)
			}
			if tc.complete {
				require.Equal(t, clock.Add(tailInterval), s.nextPass)
			} else {
				require.Equal(t, scheduled, s.nextPass)
			}
		})
	}
}

// The task polls every five minutes but completes hourly, so it must declare the hourly
// cadence the generic stalled-task alert measures against.
func TestTailDeclaresItsSuccessInterval(t *testing.T) {
	reg := prometheus.NewRegistry()
	ms := &core.Microservice{FunctionalArea: "event-management"}
	ms.UseMetricsRegistry(reg)
	s := newTailSweeper(ms, nil, Config{ChunkInterval: 24 * time.Hour}, NewMetrics(ms), core.NewNoOpLifecycleCallbacks(), time.Now)
	require.NoError(t, s.Initialize(context.Background()))
	require.NoError(t, s.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, s.Stop(context.Background())) })
	families, err := reg.Gather()
	require.NoError(t, err)
	got := map[string]float64{}
	for _, f := range families {
		if strings.HasPrefix(f.GetName(), "devicechain_eventmanagement_rollup_tail_") && len(f.Metric) == 1 && f.Metric[0].Gauge != nil {
			got[f.GetName()] = f.Metric[0].GetGauge().GetValue()
		}
	}
	require.Equal(t, tailTick.Seconds(), got["devicechain_eventmanagement_rollup_tail_pass_interval_seconds"])
	require.Equal(t, (tailInterval + tailJitter).Seconds(), got["devicechain_eventmanagement_rollup_tail_expected_success_interval_seconds"])
}
