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
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/core"
)

type fakeRoller struct {
	passes  int
	maxes   []int
	cutoffs []time.Time
	fail    bool
}

func (f *fakeRoller) RollupUsagePass(_ context.Context, _ *gorm.DB, _ time.Time, maxHours int) (int, error) {
	f.passes++
	f.maxes = append(f.maxes, maxHours)
	if f.fail {
		return 0, errors.New("store down")
	}
	return 1, nil
}

func (f *fakeRoller) PruneUsageWorkingTables(_ context.Context, cutoff time.Time) error {
	f.cutoffs = append(f.cutoffs, cutoff)
	return nil
}

type fakeLock struct{ held bool }

func (l *fakeLock) TryAdvisoryLockSession(_ context.Context, _ int64, fn func(db *gorm.DB) error) (bool, error) {
	if l.held {
		return false, nil
	}
	return true, fn(nil)
}

func sweepAt(r usageRoller, l usageLocker, now time.Time) *UsageRollupSweep {
	return &UsageRollupSweep{api: r, lock: l, now: func() time.Time { return now }}
}

// A pass runs under the session lock, capped, and prunes behind the record.
func TestUsageRollupRunsACappedPassUnderTheLock(t *testing.T) {
	f := &fakeRoller{}
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	require.NoError(t, sweepAt(f, &fakeLock{}, now).RunOnce(context.Background()))
	assert.Equal(t, 1, f.passes)
	assert.Equal(t, []int{usageMaxHoursPerPass}, f.maxes)
	assert.Equal(t, []time.Time{now.Add(-usageWorkingRetention)}, f.cutoffs)
}

// A replica that finds the lock held does nothing and says so.
func TestUsageRollupDeclinesWhenTheLockIsHeld(t *testing.T) {
	f := &fakeRoller{}
	err := sweepAt(f, &fakeLock{held: true}, time.Now()).RunOnce(context.Background())
	assert.ErrorIs(t, err, core.ErrPassSkipped)
	assert.Zero(t, f.passes)
}

// A failed pass is reported and prunes nothing.
func TestUsageRollupReportsAFailure(t *testing.T) {
	f := &fakeRoller{fail: true}
	require.Error(t, sweepAt(f, &fakeLock{}, time.Now()).RunOnce(context.Background()))
	assert.Empty(t, f.cutoffs)
}
