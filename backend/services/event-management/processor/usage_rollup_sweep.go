// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// UsageRollupInterval is how often the usage rollup checks for a newly closed hour (and retries a
// failed pass).
const UsageRollupInterval = 15 * time.Minute

// usageRollupHours is how many CLOSED UTC hours a pass recomputes, ending with the hour that just
// closed. A pass does its work once per hour boundary (the passes in between find nothing new and
// return at once), so each hour is scanned this many times in all: once when it closes, and again
// as a margin for events that arrive a little after their event time. The open hour is never
// scanned, so a day's figures lag by up to an hour until it closes, and the first pass after
// midnight closes the previous day with its final numbers.
//
// Beyond that margin, a pass also computes every closed hour of the last usageCatchUpHours that the
// ledger does not record as computed (the hours a failed pass, an outage or an upgrade left
// behind), so a run of failures delays the record but does not lose it.
//
// 🔴 AN HOUR IS ONLY EVER REWRITTEN WHILE IT IS THIS RECENT. Once it falls out of the margin its
// figures are final, and that is deliberate: the raw events behind it are removed by retention,
// and a row recomputed from a store that has since lost rows would shrink a figure someone may
// already have billed on. The price is that an event stored more than a couple of hours after its
// event time is not added to its hour.
const usageRollupHours = 3

// usageCatchUpHours is how far back a pass looks for hours the ledger says are still owed. 48 hours
// reaches the start of the previous UTC day from any time of the current one, so an outage of up
// to a day still closes yesterday. A day with an hour older than this left uncomputed stays
// incomplete, which the record reports rather than hiding.
const usageCatchUpHours = 48

// usageWorkingRetention is how long the hourly rows and device sets are kept behind the record. It
// is longer than any window the rollup rewrites, so nothing is pruned while it can still change.
const usageWorkingRetention = 72 * time.Hour

// UsageRollupSweep keeps the per-tenant daily usage record current.
//
// It runs on every replica but only one does the work at a time: the pass holds a try-lock on the
// database, and a replica that finds it held declines (core.ErrPassSkipped), so two replicas never
// compute the same hours at once. The computation is an idempotent upsert, so a replica that did get
// in after a peer finished only rewrites the same numbers.
type UsageRollupSweep struct {
	*core.PeriodicTask

	rdb *rdb.RdbManager
	api usageRoller
	now func() time.Time
	// doneHour is the start of the open hour when this replica's last successful pass ran: a pass
	// in the same hour has no newly closed hour to compute. Only the sweep's goroutine touches it.
	doneHour time.Time
}

// usageRoller is what the sweep needs from the API.
type usageRoller interface {
	RollupUsageHours(ctx context.Context, from, to, now time.Time) (int64, error)
	FirstUsageHourOwed(ctx context.Context, from, before time.Time) (time.Time, bool, error)
	PruneUsageWorkingTables(ctx context.Context, cutoff time.Time) error
}

// usageRollupLock names the database lock that keeps a pass to one replica.
const usageRollupLock = "event-management/usage-rollup"

// NewUsageRollupSweep builds the sweep.
func NewUsageRollupSweep(ms *core.Microservice, rdbm *rdb.RdbManager, api usageRoller,
	interval time.Duration, callbacks core.LifecycleCallbacks) *UsageRollupSweep {
	s := &UsageRollupSweep{rdb: rdbm, api: api, now: time.Now}
	s.PeriodicTask = core.NewPeriodicTask(ms.FunctionalArea, "usage-rollup", interval, s.RunOnce, callbacks)
	return s
}

// RunOnce recomputes the days the sweep still owns, oldest first.
func (s *UsageRollupSweep) RunOnce(ctx context.Context) error {
	ran, err := s.rdb.TryAdvisoryLock(ctx, rdb.AdvisoryLockKey(usageRollupLock), func() error {
		return s.rollup(ctx)
	})
	if err != nil {
		return err
	}
	if !ran {
		return core.ErrPassSkipped
	}
	return nil
}

// rollup recomputes the hours the sweep still owns, oldest first, then prunes the working rows of
// days long final. A failure is reported so it is visible rather than a quietly stale record; the
// next pass redoes it, since every write is an idempotent recompute.
func (s *UsageRollupSweep) rollup(ctx context.Context) error {
	now := s.now().UTC()
	open := now.Truncate(time.Hour)
	if open.Equal(s.doneHour) {
		return nil
	}
	from := open.Add(-time.Duration(usageRollupHours) * time.Hour)
	owed, ok, err := s.api.FirstUsageHourOwed(ctx, open.Add(-time.Duration(usageCatchUpHours)*time.Hour), open)
	if err != nil {
		return fmt.Errorf("usage rollup: %w", err)
	}
	if ok && owed.Before(from) {
		from = owed
	}
	to := open.Add(-time.Hour)
	rows, err := s.api.RollupUsageHours(ctx, from, to, now)
	if err != nil {
		log.Error().Err(err).Time("from", from).Msg("Usage rollup failed; it is retried on the next pass.")
		return fmt.Errorf("usage rollup: %w", err)
	}
	s.doneHour = open
	log.Debug().Time("from", from).Time("to", to).Int64("rows", rows).Msg("Usage rolled up.")
	if err := s.api.PruneUsageWorkingTables(ctx, now.Add(-usageWorkingRetention)); err != nil {
		return fmt.Errorf("usage rollup: %w", err)
	}
	return nil
}
