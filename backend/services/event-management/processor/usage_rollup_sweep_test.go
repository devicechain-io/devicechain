// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRoller struct {
	froms   []time.Time
	tos     []time.Time
	cutoffs []time.Time
	fail    bool
	// owed is the hour FirstUsageHourOwed reports, if set.
	owed *time.Time
}

func (f *fakeRoller) FirstUsageHourOwed(_ context.Context, from, before time.Time) (time.Time, bool, error) {
	if f.owed == nil || f.owed.Before(from) || !f.owed.Before(before) {
		return time.Time{}, false, nil
	}
	return *f.owed, true, nil
}

func (f *fakeRoller) RollupUsageHours(_ context.Context, from, to, _ time.Time) (int64, error) {
	f.froms, f.tos = append(f.froms, from), append(f.tos, to)
	if f.fail {
		return 0, errors.New("store down")
	}
	return 1, nil
}

func (f *fakeRoller) PruneUsageWorkingTables(_ context.Context, cutoff time.Time) error {
	f.cutoffs = append(f.cutoffs, cutoff)
	return nil
}

func sweepAt(r usageRoller, now time.Time) *UsageRollupSweep {
	return &UsageRollupSweep{api: r, now: func() time.Time { return now }}
}

// A pass works once per hour boundary, on closed hours only: the recent ones, always including the
// hour that just closed. The open hour is never scanned.
func TestUsageRollupRecomputesRecentClosedHoursOncePerHour(t *testing.T) {
	f := &fakeRoller{}
	now := time.Date(2026, 11, 7, 0, 5, 0, 0, time.UTC)
	midnight := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	s := sweepAt(f, now)
	require.NoError(t, s.rollup(context.Background()))
	require.Len(t, f.froms, 1)
	assert.Equal(t, midnight.Add(-time.Duration(usageRollupHours)*time.Hour), f.froms[0])
	assert.Equal(t, midnight.Add(-time.Hour), f.tos[0], "ends with the hour that just closed")

	// Later in the same hour: nothing newly closed, nothing scanned.
	s.now = func() time.Time { return now.Add(20 * time.Minute) }
	require.NoError(t, s.rollup(context.Background()))
	assert.Len(t, f.froms, 1)

	next := now.Add(time.Hour)
	s.now = func() time.Time { return next }
	require.NoError(t, s.rollup(context.Background()))
	require.Len(t, f.froms, 2)
	assert.Equal(t, midnight, f.tos[1])
	assert.Equal(t, next.Add(-usageWorkingRetention), f.cutoffs[1])
	assert.Greater(t, usageWorkingRetention, time.Duration(usageCatchUpHours+1)*time.Hour,
		"nothing is pruned while a pass can still rewrite it")
}

// An hour the ledger still owes (after failed passes, an outage, an upgrade) pulls the pass back
// to it, up to the catch-up window, so failures delay the record without losing it.
func TestUsageRollupComputesTheHoursTheLedgerStillOwes(t *testing.T) {
	now := time.Date(2026, 11, 7, 23, 5, 0, 0, time.UTC)
	owed := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC) // all of yesterday, from 23:05 today
	f := &fakeRoller{owed: &owed}
	require.NoError(t, sweepAt(f, now).rollup(context.Background()))
	assert.Equal(t, owed, f.froms[0], "the catch-up reaches the start of yesterday at any time of today")

	tooOld := now.Add(-time.Duration(usageCatchUpHours+2) * time.Hour)
	f = &fakeRoller{owed: &tooOld}
	require.NoError(t, sweepAt(f, now).rollup(context.Background()))
	assert.Equal(t, now.Truncate(time.Hour).Add(-time.Duration(usageRollupHours)*time.Hour), f.froms[0],
		"beyond the window the pass does not reach; that day stays incomplete")
}

// A failed pass is reported, prunes nothing, and is retried within the same hour.
func TestUsageRollupReportsAFailureAndRetries(t *testing.T) {
	f := &fakeRoller{fail: true}
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	s := sweepAt(f, now)
	require.Error(t, s.rollup(context.Background()))
	assert.Empty(t, f.cutoffs)
	f.fail = false
	require.NoError(t, s.rollup(context.Background()))
	require.Len(t, f.froms, 2, "the failed pass is retried in the same hour")
	assert.Len(t, f.cutoffs, 1)
}
