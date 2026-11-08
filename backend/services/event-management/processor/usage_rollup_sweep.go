// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// UsageRollupInterval is how often the usage rollup looks for hours that have become final.
const UsageRollupInterval = 5 * time.Minute

// usageMaxHoursPerPass caps one pass. A healthy rollup finalizes one hour per hour; after an
// outage it owes many, and the cap keeps each pass (and the lock it holds) short while it catches
// up at this many hours per interval.
const usageMaxHoursPerPass = 6

// usageWorkingRetention is how long the hourly rows and device sets are kept behind the record.
// Every hour is final soon after it closes, so this is a wide margin; it exists so an operator
// repairing an hour by hand still has its working rows.
const usageWorkingRetention = 72 * time.Hour

// UsageRollupSweep keeps the per-tenant daily usage record current.
//
// It runs on every replica but only one does the work at a time: the pass holds a SESSION
// advisory lock and does all its work on the lock's own connection, so the work ends if the lock
// does (a terminated backend cannot leave a second worker running), and a replica that finds the
// lock held declines (core.ErrPassSkipped). Where to resume is not remembered in memory: the next
// owed hour is persisted (tenant_usage_state) and read under the lock, so whichever replica gets
// the lock carries on from the same place, and each hour is computed once.
type UsageRollupSweep struct {
	*core.PeriodicTask

	lock usageLocker
	api  usageRoller
	now  func() time.Time
}

// usageRoller is what the sweep needs from the API.
type usageRoller interface {
	RollupUsagePass(ctx context.Context, db *gorm.DB, now time.Time, maxHours int) (int, error)
	PruneUsageWorkingTables(ctx context.Context, cutoff time.Time) error
}

// usageLocker is the session lock the pass runs under (rdb.RdbManager.TryAdvisoryLockSession).
type usageLocker interface {
	TryAdvisoryLockSession(ctx context.Context, key int64, fn func(db *gorm.DB) error) (bool, error)
}

// usageRollupLock names the database lock that keeps a pass to one replica.
const usageRollupLock = "event-management/usage-rollup"

// NewUsageRollupSweep builds the sweep.
func NewUsageRollupSweep(ms *core.Microservice, rdbm *rdb.RdbManager, api usageRoller,
	interval time.Duration, callbacks core.LifecycleCallbacks) *UsageRollupSweep {
	s := &UsageRollupSweep{lock: rdbm, api: api, now: time.Now}
	s.PeriodicTask = core.NewPeriodicTask(ms.FunctionalArea, "usage-rollup", interval, s.RunOnce, callbacks)
	return s
}

// RunOnce finalizes the hours the rollup owes, under the lock.
func (s *UsageRollupSweep) RunOnce(ctx context.Context) error {
	ran, err := s.lock.TryAdvisoryLockSession(ctx, rdb.AdvisoryLockKey(usageRollupLock), func(db *gorm.DB) error {
		return s.rollup(ctx, db)
	})
	if err != nil {
		return err
	}
	if !ran {
		return core.ErrPassSkipped
	}
	return nil
}

// rollup finalizes owed hours, then prunes the working rows of days long final. A failure is
// reported so it is visible rather than a quietly stale record; the next pass resumes at the hour
// that failed.
func (s *UsageRollupSweep) rollup(ctx context.Context, db *gorm.DB) error {
	now := s.now().UTC()
	n, err := s.api.RollupUsagePass(ctx, db, now, usageMaxHoursPerPass)
	if err != nil {
		log.Error().Err(err).Int("finalized", n).Msg("Usage rollup failed; it resumes at the failed hour on the next pass.")
		return fmt.Errorf("usage rollup: %w", err)
	}
	log.Debug().Int("finalized", n).Msg("Usage rolled up.")
	if err := s.api.PruneUsageWorkingTables(ctx, now.Add(-usageWorkingRetention)); err != nil {
		return fmt.Errorf("usage rollup: %w", err)
	}
	return nil
}
