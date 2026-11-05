// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package backfill

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-event-management/model"
)

// rollupCount reads one device's bucketed count over [from, to] through the rollup path.
func (r *rig) rollupCount(device string, from, to time.Time) int64 {
	r.t.Helper()
	name := "kw"
	got, err := r.api.BucketedMeasurements(r.ctx(), model.MeasurementAggregationCriteria{
		DeviceToken: &device, Name: &name, IntervalSeconds: 3600, StartTime: &from, EndTime: &to})
	require.NoError(r.t, err)
	var n int64
	for _, b := range got {
		n += b.Count
	}
	return n
}

func (r *rig) insertReading(device string, at time.Time, id string) {
	r.t.Helper()
	require.NoError(r.t, r.sys().Exec(`INSERT INTO "event-management".measurement_events
  (tenant_id, event_id, payload_id, device_token, event_type, occurred_time, name, value, unit, data_type)
  VALUES (?, sha256(?::bytea), sha256(?::bytea), ?, 2, ?, 'kw', 1.5, 'kW', 'DOUBLE')`,
		r.tenant, id, id+"p", device, at).Error)
}

// policyRefresh is what the aggregate's policy does each minute: refresh its trailing window.
func (r *rig) policyRefresh() {
	r.t.Helper()
	require.NoError(r.t, r.sys().Exec(`CALL refresh_continuous_aggregate('"event-management".measurement_rollups',
		now() - INTERVAL '30 days', now() - INTERVAL '1 minute')`).Error)
}

func (r *rig) sweeper() *TailSweeper {
	ms := &core.Microservice{FunctionalArea: "event-management", MicroserviceId: "event-management"}
	s := NewTailSweeper(ms, r.mgr, r.cfg, r.metrics, core.NewNoOpLifecycleCallbacks())
	s.jitter = func() time.Duration { return 0 }
	return s
}

// due makes the sweeper's next pass due now.
func due(s *TailSweeper) { s.nextPass = s.now().Add(-time.Second) }

// 🔴 THE GAP THIS CLOSES, in the order production meets it: the aggregate has materialized (the
// policy has run), THEN a reading the platform accepts (eventtime.MaxAge is a year) but dated
// beyond the policy's 30 days arrives. It is stored and never reaches the rollup.
func TestIntegrationALateReadingBeyondThePolicyReachReachesTheRollup(t *testing.T) {
	r := newRig(t, "tail")
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-75 * 24 * time.Hour).Truncate(time.Hour).Add(7 * time.Minute)
	r.insertReading("meter-1", now.Add(-2*time.Hour), "first")
	r.policyRefresh()
	r.insertReading("meter-1", old, "old")
	r.policyRefresh()

	var raw int64
	require.NoError(t, r.sys().Raw(`SELECT count(*) FROM "event-management".measurement_events WHERE device_token = 'meter-1'`).Scan(&raw).Error)
	require.EqualValues(t, 2, raw)
	require.EqualValues(t, 0, r.rollupCount("meter-1", old.Add(-time.Hour), old.Add(time.Hour)),
		"precondition: the policy window does not reach a reading 75 days old, so the rollup does not show it")

	s := r.sweeper()
	due(s)
	require.NoError(t, s.RunOnce(context.Background()))
	assert.EqualValues(t, 1, r.rollupCount("meter-1", old.Add(-time.Hour), old.Add(time.Hour)),
		"the sweep brings a reading older than the policy's reach into the rollup")

	due(s)
	require.NoError(t, s.RunOnce(context.Background()))
	assert.EqualValues(t, 2, r.rollupCount("meter-1", old.Add(-2*time.Hour), now.Add(time.Hour)))
}

// The first pass runs a couple of minutes after start, not an hour after, and not before.
func TestIntegrationTheFirstTailPassIsDueSoonAfterStart(t *testing.T) {
	r := newRig(t, "tailfirst")
	s := r.sweeper()
	clock := time.Now().UTC()
	s.now = func() time.Time { return clock }
	var calls int
	s.refresh = func(context.Context, *gorm.DB, time.Time, time.Time) error { calls++; return nil }
	r.insertReading("m", clock.Add(-60*24*time.Hour), "a")

	require.ErrorIs(t, s.RunOnce(context.Background()), core.ErrPassSkipped, "not before the delay")
	clock = clock.Add(tailFirstDelay + time.Second)
	require.NoError(t, s.RunOnce(context.Background()))
	assert.Equal(t, 1, calls)
	clock = clock.Add(tailTick)
	require.ErrorIs(t, s.RunOnce(context.Background()), core.ErrPassSkipped, "a completed pass waits the hour")
	clock = clock.Add(tailInterval)
	require.NoError(t, s.RunOnce(context.Background()))
	assert.Equal(t, 2, calls)
}

// One window that can never finish does not block the others, pass after pass; it is backed off
// and counted, and tried again once its backoff ends.
//
// Negative control: with the failure aborting the pass (or the oldest window blocking), the
// newer windows are not refreshed in the later passes.
func TestIntegrationAWindowThatKeepsFailingDoesNotBlockTheOthers(t *testing.T) {
	r := newRig(t, "tailstuck")
	now := time.Now().UTC()
	for i, d := range []int{45, 55, 65} {
		r.insertReading("m", now.Add(-time.Duration(d)*24*time.Hour), string(rune('a'+i)))
	}
	clock := now
	s := r.sweeper()
	s.now = func() time.Time { return clock }
	seen := map[time.Time]int{}
	var oldest time.Time
	s.refresh = func(_ context.Context, _ *gorm.DB, ws, _ time.Time) error {
		seen[ws]++
		if oldest.IsZero() || ws.Before(oldest) {
			oldest = ws
		}
		if ws.Equal(oldest) && ws.Before(now.Add(-60*24*time.Hour)) {
			return &pgconn.PgError{Code: "57014"} // the 65-day window: statement timeout
		}
		return nil
	}
	// Learn which window is the oldest first.
	due(s)
	_ = s.RunOnce(context.Background())
	stuck := oldest
	require.Len(t, seen, 3, "every window is tried once; the failure did not end the pass")
	assert.Equal(t, 1, seen[stuck])
	assert.EqualValues(t, 1, testutil.ToFloat64(r.metrics.TailSkipped.WithLabelValues(tailSkipTimeout)))

	// Next cycle, inside the backoff: the stuck window is skipped, the others still complete.
	clock = clock.Add(tailBackoffBase / 2)
	due(s)
	_ = s.RunOnce(context.Background())
	assert.Equal(t, 1, seen[stuck], "inside its backoff the stuck window is not tried again")
	for ws, n := range seen {
		if !ws.Equal(stuck) {
			assert.Equal(t, 2, n, "the others are refreshed on every cycle")
		}
	}
	assert.Greater(t, testutil.ToFloat64(r.metrics.TailSkipped.WithLabelValues(tailSkipBackoff)), 0.0)

	// Past the backoff it is tried again, and backs off for longer.
	clock = clock.Add(tailBackoffBase + time.Minute)
	due(s)
	_ = s.RunOnce(context.Background())
	assert.Equal(t, 2, seen[stuck])
	assert.Equal(t, 2*tailBackoffBase, s.backoff[stuck].delay)
}

// The lock is released between windows, so the policy job or a backfill can take it.
//
// Negative control: holding the lock across the loop makes the in-between attempt fail.
func TestIntegrationTheTailLockIsReleasedBetweenWindows(t *testing.T) {
	r := newRig(t, "taillock")
	now := time.Now().UTC()
	for i, d := range []int{45, 55, 65} {
		r.insertReading("m", now.Add(-time.Duration(d)*24*time.Hour), string(rune('a'+i)))
	}
	s := r.sweeper()
	s.refresh = func(context.Context, *gorm.DB, time.Time, time.Time) error { return nil }
	var between, acquired int
	s.afterWindow = func() {
		between++
		ran, err := r.mgr.TryAdvisoryLockSession(context.Background(), rdb.AdvisoryLockKey(lockName),
			func(*gorm.DB) error { return nil })
		require.NoError(t, err)
		if ran {
			acquired++
		}
	}
	due(s)
	require.NoError(t, s.RunOnce(context.Background()))
	require.Equal(t, 3, between)
	assert.Equal(t, 3, acquired, "another holder of the lock gets in between every pair of windows")
}

// A pass that loses the lock ends and is due again at the next tick, not an hour later; and a
// pass that runs out of budget continues from where it stopped, so a store too big for one
// budget reaches its oldest window.
//
// Negative control: without cycleDone the second pass starts over from the newest window and
// the oldest one is never reached.
func TestIntegrationATailPassThatRunsOutOfBudgetContinuesNextTick(t *testing.T) {
	r := newRig(t, "tailbudget")
	now := time.Now().UTC()
	for i, d := range []int{45, 55, 65} {
		r.insertReading("m", now.Add(-time.Duration(d)*24*time.Hour), string(rune('a'+i)))
	}
	clock := now
	s := r.sweeper()
	s.now = func() time.Time { return clock }
	var order []time.Time
	s.refresh = func(_ context.Context, _ *gorm.DB, ws, _ time.Time) error {
		order = append(order, ws)
		clock = clock.Add(tailBudget + time.Minute) // each refresh outlasts the budget
		return nil
	}
	due(s)
	require.NoError(t, s.RunOnce(context.Background()))
	require.Len(t, order, 1, "the budget ends the first pass after one window")
	assert.True(t, s.nextPass.Before(clock), "an unfinished pass stays due")

	require.NoError(t, s.RunOnce(context.Background()))
	require.NoError(t, s.RunOnce(context.Background()))
	require.Len(t, order, 3, "each tick takes the next window")
	assert.True(t, order[0].After(order[1]) && order[1].After(order[2]), "newest first, never repeated: %v", order)
	assert.True(t, s.nextPass.After(clock), "the cycle is complete: the next pass waits the hour")

	// And a lost lock leaves the pass due at the next tick.
	hold := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_, _ = r.mgr.TryAdvisoryLockSession(context.Background(), rdb.AdvisoryLockKey(lockName), func(*gorm.DB) error {
			close(hold)
			<-release
			return nil
		})
	}()
	<-hold
	clock = clock.Add(2 * tailInterval)
	due(s)
	order = nil
	require.NoError(t, s.RunOnce(context.Background()))
	close(release)
	assert.Empty(t, order)
	assert.True(t, !s.nextPass.After(s.now()), "lost the lock: due again at the next tick")
}

// A refresh that meets the policy's own is retried, then left (and counted) for the next pass.
func TestIntegrationATailRefreshThatMeetsThePolicyIsRetriedThenLeft(t *testing.T) {
	r := newRig(t, "tailconflict")
	r.insertReading("meter-1", time.Now().UTC().Add(-60*24*time.Hour), "b")
	var tries int
	s := r.sweeper()
	s.sleep = func(context.Context, time.Duration) error { return nil }
	s.refresh = func(context.Context, *gorm.DB, time.Time, time.Time) error {
		tries++
		return &pgconn.PgError{Code: "55P03"}
	}
	due(s)
	require.Error(t, s.RunOnce(context.Background()))
	assert.Equal(t, tailRefreshTries, tries)
	assert.EqualValues(t, 1, testutil.ToFloat64(r.metrics.TailSkipped.WithLabelValues(tailSkipConflict)))

	s2 := r.sweeper()
	tries = 0
	s2.refresh = func(context.Context, *gorm.DB, time.Time, time.Time) error { tries++; return errors.New("boom") }
	due(s2)
	require.Error(t, s2.RunOnce(context.Background()))
	assert.Equal(t, 1, tries)
}

// Only windows that start before the policy's reach (less its margin), and above the retention
// floor, are refreshed.
func TestIntegrationTheTailSweepStaysOutsidePolicyReachAndInsideRetention(t *testing.T) {
	r := newRig(t, "tailbounds")
	now := time.Now().UTC()
	r.insertReading("meter-1", now.Add(-3*24*time.Hour), "a")
	r.insertReading("meter-1", now.Add(-60*24*time.Hour), "b")
	r.insertReading("meter-1", now.Add(-200*24*time.Hour), "c")
	r.cfg.RetentionDays = 90

	var windows []time.Time
	s := r.sweeper()
	s.refresh = func(_ context.Context, _ *gorm.DB, ws, _ time.Time) error {
		windows = append(windows, ws)
		return nil
	}
	due(s)
	require.NoError(t, s.RunOnce(context.Background()))
	require.Len(t, windows, 1, "only the 60-day-old chunk is between retention and the policy's reach: %v", windows)
	assert.True(t, windows[0].Before(now.Add(-30*24*time.Hour+24*time.Hour)))
	assert.True(t, windows[0].After(now.Add(-90*24*time.Hour)))
}
