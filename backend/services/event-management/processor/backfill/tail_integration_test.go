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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-event-management/model"
)

// rollupCount reads one device's bucketed count over [from, to] through the rollup path (60 s
// buckets), the read a chart makes.
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

// 🔴 THE GAP THIS CLOSES, shown end to end: a reading the platform accepts (eventtime.MaxAge is a
// year) but dated beyond the aggregate policy's 30 days is stored and never reaches the rollup.
// The policy job is run explicitly here so the test does not depend on its schedule.
func TestIntegrationALateReadingBeyondThePolicyReachReachesTheRollup(t *testing.T) {
	r := newRig(t, "tail")
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-75 * 24 * time.Hour).Truncate(time.Hour).Add(7 * time.Minute)
	recent := now.Add(-5 * 24 * time.Hour)
	from, to := old.Add(-2*time.Hour), now.Add(time.Hour)

	// The reading is stored, so the raw history is complete.
	r.insertReading("meter-1", recent, "recent")
	r.insertReading("meter-1", old, "old")
	var raw int64
	require.NoError(t, r.sys().Raw(`SELECT count(*) FROM "event-management".measurement_events WHERE device_token = 'meter-1'`).Scan(&raw).Error)
	require.EqualValues(t, 2, raw)

	// What the policy does each minute: refresh its trailing window. It must not be what
	// saves the old reading, or this test shows nothing.
	require.NoError(t, r.sys().Exec(`CALL refresh_continuous_aggregate('"event-management".measurement_rollups',
		now() - INTERVAL '30 days', now() - INTERVAL '1 minute')`).Error)
	require.EqualValues(t, 0, r.rollupCount("meter-1", old.Add(-time.Hour), old.Add(time.Hour)),
		"precondition: the policy window does not reach a reading 75 days old, so the rollup does not show it")

	ms := &core.Microservice{FunctionalArea: "event-management", MicroserviceId: "event-management"}
	s := NewTailSweeper(ms, r.mgr, r.cfg, r.metrics, core.NewNoOpLifecycleCallbacks())
	require.NoError(t, s.RunOnce(context.Background()))

	assert.EqualValues(t, 1, r.rollupCount("meter-1", old.Add(-time.Hour), old.Add(time.Hour)),
		"the sweep brings a reading older than the policy's reach into the rollup")
	assert.EqualValues(t, 2, r.rollupCount("meter-1", from, to))

	// A second sweep with nothing invalidated changes nothing and fails nothing.
	require.NoError(t, s.RunOnce(context.Background()))
	assert.EqualValues(t, 2, r.rollupCount("meter-1", from, to))
}

// The sweep refreshes only windows that start before the policy's reach (less its margin), and
// takes retention as its floor.
func TestIntegrationTheTailSweepStaysOutsidePolicyReachAndInsideRetention(t *testing.T) {
	r := newRig(t, "tailbounds")
	now := time.Now().UTC()
	r.insertReading("meter-1", now.Add(-3*24*time.Hour), "a")
	r.insertReading("meter-1", now.Add(-60*24*time.Hour), "b")
	r.insertReading("meter-1", now.Add(-200*24*time.Hour), "c")
	r.cfg.RetentionDays = 90

	var windows []time.Time
	ms := &core.Microservice{FunctionalArea: "event-management", MicroserviceId: "event-management"}
	s := NewTailSweeper(ms, r.mgr, r.cfg, r.metrics, core.NewNoOpLifecycleCallbacks())
	s.refresh = func(_ context.Context, _ *gorm.DB, ws, we time.Time) error {
		windows = append(windows, ws)
		return nil
	}
	require.NoError(t, s.RunOnce(context.Background()))
	require.Len(t, windows, 1, "only the 60-day-old chunk is between retention and the policy's reach: %v", windows)
	assert.True(t, windows[0].Before(now.Add(-30*24*time.Hour+24*time.Hour)))
	assert.True(t, windows[0].After(now.Add(-90*24*time.Hour)))
}

// A refresh that meets the policy's own is retried, then left for the next sweep.
func TestIntegrationATailRefreshThatMeetsThePolicyIsRetriedThenLeft(t *testing.T) {
	r := newRig(t, "tailconflict")
	r.insertReading("meter-1", time.Now().UTC().Add(-60*24*time.Hour), "b")
	collide := &pgconn.PgError{Code: "55P03"}
	var tries int
	ms := &core.Microservice{FunctionalArea: "event-management", MicroserviceId: "event-management"}
	s := NewTailSweeper(ms, r.mgr, r.cfg, r.metrics, core.NewNoOpLifecycleCallbacks())
	s.sleep = func(context.Context, time.Duration) error { return nil }
	s.refresh = func(context.Context, *gorm.DB, time.Time, time.Time) error { tries++; return collide }
	err := s.RunOnce(context.Background())
	require.Error(t, err)
	assert.Equal(t, tailRefreshTries, tries)

	// And a non-conflict error is not retried.
	tries = 0
	s.refresh = func(context.Context, *gorm.DB, time.Time, time.Time) error { tries++; return errors.New("boom") }
	require.Error(t, s.RunOnce(context.Background()))
	assert.Equal(t, 1, tries)
}

// One sweep does not hold the instance lock past its budget: the rest is left for the next, and
// the next starts over from the oldest window, so a sweep that keeps running out still finishes.
func TestIntegrationATailSweepStopsAtItsBudgetAndTheNextFinishes(t *testing.T) {
	r := newRig(t, "tailbudget")
	now := time.Now().UTC()
	for i, d := range []int{50, 60, 70} {
		r.insertReading("meter-1", now.Add(-time.Duration(d)*24*time.Hour), string(rune('a'+i)))
	}
	clock := now
	var seen []time.Time
	ms := &core.Microservice{FunctionalArea: "event-management", MicroserviceId: "event-management"}
	s := NewTailSweeper(ms, r.mgr, r.cfg, r.metrics, core.NewNoOpLifecycleCallbacks())
	s.now = func() time.Time { return clock }
	s.refresh = func(_ context.Context, _ *gorm.DB, ws, _ time.Time) error {
		seen = append(seen, ws)
		clock = clock.Add(tailBudget + time.Minute) // each refresh "takes" longer than the budget
		return nil
	}
	require.NoError(t, s.RunOnce(context.Background()))
	require.Len(t, seen, 1, "the second window is past the budget and is left alone")
	assert.True(t, seen[0].Before(now.Add(-65*24*time.Hour)), "oldest first: %v", seen[0])
}
