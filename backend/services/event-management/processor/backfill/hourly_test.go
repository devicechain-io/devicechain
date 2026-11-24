// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backfill

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type memoryHourlyProgress struct {
	marker, floor time.Time
	databaseNow   time.Time
	starts        []time.Time
	completed     map[time.Time]bool
	failAt        int
	clock         *time.Time
	cost          time.Duration
	failPublish   bool
}

func (p *memoryHourlyProgress) bounds(context.Context) (time.Time, time.Time, time.Time, error) {
	now := p.databaseNow
	if now.IsZero() {
		now = p.marker
	}
	return p.marker, p.floor, now, nil
}

func (p *memoryHourlyProgress) refresh(ctx context.Context, from, to time.Time, budget time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.starts = append(p.starts, from)
	if p.clock != nil {
		*p.clock = p.clock.Add(p.cost)
	}
	if len(p.starts) == p.failAt {
		return errors.New("interrupted mid-window")
	}
	p.completed[from] = true
	return nil
}

func (p *memoryHourlyProgress) advance(_ context.Context, from, to time.Time) error {
	if !p.completed[to] {
		return errors.New("marker passed an incomplete window")
	}
	if p.failPublish {
		return errors.New("interrupted before marker publication")
	}
	if !p.marker.Equal(from) {
		return errors.New("stale marker")
	}
	p.marker = to
	return nil
}

func TestHourlyBackfillIsBoundedAndNewestFirst(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	p := &memoryHourlyProgress{marker: now, floor: now.Add(-20 * time.Hour), completed: map[time.Time]bool{}}
	s := &HourlyBackfiller{now: func() time.Time { return now }}
	require.NoError(t, s.pass(context.Background(), p))
	require.Len(t, p.starts, hourlyWindowsPerPass)
	for i, at := range p.starts {
		require.Equal(t, now.Add(-time.Duration(i+1)*time.Hour), at)
	}
	require.Equal(t, now.Add(-hourlyWindowsPerPass*time.Hour), p.marker)
}

func TestHourlyMarkerStaysAtLastCompletedWindowAndResumes(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	p := &memoryHourlyProgress{marker: now, floor: now.Add(-3 * time.Hour), completed: map[time.Time]bool{}, failAt: 2}
	s := &HourlyBackfiller{now: func() time.Time { return now }}
	require.Error(t, s.pass(context.Background(), p))
	require.Equal(t, now.Add(-time.Hour), p.marker)
	p.failAt = 0
	require.NoError(t, s.pass(context.Background(), p))
	require.Equal(t, p.starts[1], p.starts[2], "retry the interrupted window first")
	require.Equal(t, p.floor, p.marker)
	before := len(p.starts)
	require.NoError(t, s.pass(context.Background(), p))
	require.Len(t, p.starts, before, "stop at earliest data or retention")
}

func TestHourlyBackfillStopsAtWallBudget(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	p := &memoryHourlyProgress{marker: now, floor: now.Add(-20 * time.Hour), completed: map[time.Time]bool{}, clock: &now, cost: hourlyPassBudget}
	s := &HourlyBackfiller{now: func() time.Time { return now }}
	require.NoError(t, s.pass(context.Background(), p))
	require.Len(t, p.starts, 1)
}

func TestHourlyInterruptedPublicationRepeatsRefreshedWindow(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	p := &memoryHourlyProgress{marker: now, floor: now.Add(-2 * time.Hour), completed: map[time.Time]bool{}, failPublish: true}
	s := &HourlyBackfiller{now: func() time.Time { return now }}
	require.Error(t, s.pass(context.Background(), p))
	require.Equal(t, now, p.marker, "refresh alone cannot publish completeness")
	require.True(t, p.completed[now.Add(-time.Hour)], "the first hour was actually refreshed")
	require.False(t, p.completed[now.Add(-2*time.Hour)], "the next hour still lacks its refresh")
	p.failPublish = false
	require.NoError(t, s.pass(context.Background(), p))
	require.Equal(t, []time.Time{now.Add(-time.Hour), now.Add(-time.Hour), now.Add(-2 * time.Hour)}, p.starts)
	require.Equal(t, p.floor, p.marker)
}

func TestHourlyBackfillWaitsForTheMigrationHourToClose(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 17, 0, 0, time.UTC)
	marker := now.Truncate(time.Hour).Add(time.Hour)
	p := &memoryHourlyProgress{marker: marker, databaseNow: now, floor: marker.Add(-20 * time.Hour), completed: map[time.Time]bool{}}
	s := &HourlyBackfiller{now: func() time.Time { return now }}
	require.NoError(t, s.pass(context.Background(), p))
	require.Empty(t, p.starts, "a refresh must not move the hour watermark beyond the present")
	require.Equal(t, marker, p.marker)
	now = marker
	p.databaseNow = now
	require.NoError(t, s.pass(context.Background(), p))
	require.Len(t, p.starts, hourlyWindowsPerPass, "work begins once the newest owed hour has closed")
}
