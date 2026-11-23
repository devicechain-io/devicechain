// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backfill

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-event-management/model"
)

// rollupView is the continuous aggregate a refresh brings up to date, and rollupViewHourly the
// one-hour aggregate built over it.
const (
	rollupView       = `"event-management".measurement_rollups`
	rollupViewHourly = `"event-management".measurement_rollups_1h`
)

// refreshRollups refreshes the minute aggregate over a window and then the hour aggregate over
// the same window, in that order: the hour level is built from the minute level, so it only
// sees what the minute refresh has already put there. The aggregates' own policies reach only a
// trailing window, so a refresh of older history that stopped at the minute level would leave
// the hour level answering with what it had before.
//
// budget is the window's whole allowance, not each statement's: each statement runs under a
// statement_timeout of what is left of it, so two statements cannot take twice the time the
// window was given. A spent budget is reported as a statement timeout (57014), which is how the
// callers already classify a window that ran out.
func refreshRollups(db *gorm.DB, start, end time.Time, budget time.Duration) error {
	began := time.Now()
	for _, view := range []string{rollupView, rollupViewHourly} {
		left := budget - time.Since(began)
		if left <= 0 {
			return &pgconn.PgError{Code: "57014", Message: "the window's refresh budget was spent on the minute aggregate"}
		}
		if err := db.Exec(`SELECT set_config('statement_timeout', ?, false)`,
			fmt.Sprintf("%d", left.Milliseconds())).Error; err != nil {
			return err
		}
		if err := db.Exec(`CALL refresh_continuous_aggregate(?::regclass, ?::timestamptz, ?::timestamptz)`,
			view, start, end).Error; err != nil {
			return err
		}
	}
	return nil
}

// refreshNext refreshes the rollup over the next chunk window the aggregate's own policy will
// not reach, oldest first, and reports whether there is nothing left to refresh.
//
// 🔴 THE CURSOR IS THE DEBT. A window is passed (refresh_cursor moves past it) only in the same
// commit as the refresh that covered it succeeding, so a crash or a failed retry leaves the
// window still owed. It is never advanced first.
//
// One window at a time because a refresh's cost is the invalidated span across ALL tenants
// (the aggregate is instance-wide), and because the refresh holds the aggregate's invalidation
// lock for as long as it runs, which makes the policy job's own refresh fail (55P03) every
// minute meanwhile. A chunk bounds both.
func (r *jobRun) refreshNext(ctx context.Context) (bool, error) {
	w := r.w
	job := r.job
	// Nothing written, nothing to refresh: a job over compressed or empty history writes no row,
	// and an instance-wide refresh for it would be cost with no purpose.
	if job.Written == 0 {
		return true, nil
	}
	windows, err := chunkWindows(r.db, job.RangeFrom, job.RangeTo)
	if err != nil {
		return false, err
	}
	// The policy refreshes the trailing rollupPolicyReach each minute. A window that starts
	// inside it, less a margin of a day or a chunk, is its to refresh; any older is ours.
	margin := max(24*time.Hour, w.cfg.ChunkInterval)
	reach := w.now().Add(-rollupPolicyReach).Add(margin)
	var next *chunkWindow
	for i := len(windows) - 1; i >= 0; i-- { // oldest first
		win := windows[i]
		if !win.Start.Before(reach) {
			continue
		}
		if job.RefreshCursor != nil && !win.End.After(*job.RefreshCursor) {
			continue
		}
		next = &win
		break
	}
	if next == nil {
		return true, nil
	}
	end := next.End
	cursor := &end
	d := model.BackfillDelta{RefreshCursor: &cursor}
	// A chunk the fill skipped (compressed, or near retention) holds nothing this job wrote.
	if !next.Compressed && !r.beyondRetention(*next) {
		if err := r.refreshWindow(ctx, *next); err != nil {
			return false, err
		}
		d.WindowsRefreshed = 1
	}
	if err := r.commit(d); err != nil {
		return false, err
	}
	return false, nil
}

// refreshWindow runs the refresh over one chunk, retrying while it meets the policy job's own
// refresh, which errors it out with lock_not_available rather than making it wait.
func (r *jobRun) refreshWindow(ctx context.Context, win chunkWindow) error {
	w := r.w
	if err := w.timeout(r.db, refreshTimeout); err != nil {
		return err
	}
	defer func() { _ = w.timeout(r.db, statementTimeout) }()
	started := w.now()
	backoff := time.Second
	for try := 1; ; try++ {
		var err error
		if w.hooks.refresh != nil {
			err = w.hooks.refresh(ctx, r.db, win.Start, win.End)
		} else {
			err = refreshRollups(r.db, win.Start, win.End, refreshTimeout)
		}
		if err == nil {
			w.metrics.RefreshSeconds.Observe(w.now().Sub(started).Seconds())
			return nil
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
			return err
		}
		w.metrics.RefreshConflicts.Inc()
		if try >= refreshTries {
			return &failJob{msg: fmt.Sprintf("the rollup refresh over %s to %s met the aggregate policy %d times in a row",
				win.Start.Format(time.RFC3339), win.End.Format(time.RFC3339), refreshTries)}
		}
		log.Info().Str("job", r.job.Token).Time("windowStart", win.Start).Int("try", try).
			Msg("Rollup refresh met the aggregate policy; retrying.")
		sleep := w.hooks.sleep
		if sleep == nil {
			sleep = sleepCtx
		}
		if err := sleep(ctx, backoff); err != nil {
			return err
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}
