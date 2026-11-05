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

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/rdb"
)

const (
	// tailInterval is the gap between sweeps. A sweep over history nothing has touched is one
	// catalog read and a no-op refresh per chunk (measured at milliseconds each), so it is cheap
	// enough to run often, and often is what bounds how long a late reading stays out of a chart.
	tailInterval = time.Hour
	// tailRefreshTries is how often one window's refresh is retried when it meets the aggregate
	// policy's own refresh before the sweep leaves the window for the next one.
	tailRefreshTries = 3
)

// TailSweeper keeps the rollup's old history correct.
//
// 🔴 THE AGGREGATE'S POLICY REFRESHES ONLY THE TRAILING rollupPolicyReach, and the PLATFORM
// ACCEPTS A READING DATED UP TO eventtime.MaxAge BEFORE IT ARRIVES. A reading stamped between
// the two (a device that buffered for six weeks, a gateway replaying a month of backlog) is
// stored, and logged by Timescale as an invalidation of a range the policy never looks at, so
// the rollup never learns of it: the raw history is complete, `measurementEvents` returns the
// reading, and `bucketedMeasurements` served from the rollup does not show it.
//
// The fix is NOT to widen the policy's start_offset. The policy would then take, in one refresh
// every minute, the whole invalidated span of anything within the wider reach, including the
// spans a calculated-metric backfill writes into deliberately and refreshes ONE CHUNK AT A TIME
// (see refreshNext, which depends on the policy's reach being exactly rollupPolicyReach). The
// sweep keeps that division: the policy owns the recent window, and this walks the rest, one
// chunk at a time, oldest to newest, from the retention floor (or the event-time floor) up to
// where the policy's reach begins. A chunk with nothing invalidated costs a no-op.
//
// It holds the backfill worker's advisory lock, so a sweep and a backfill never refresh at
// once, and it is the SAME lock on purpose: one writer of old rollup history at a time.
type TailSweeper struct {
	*core.PeriodicTask

	rdb     *rdb.RdbManager
	cfg     Config
	metrics *Metrics
	now     func() time.Time
	// refresh and sleep replace the refresh itself and the wait between retries, for tests.
	refresh func(ctx context.Context, db *gorm.DB, ws, we time.Time) error
	sleep   func(ctx context.Context, d time.Duration) error
}

// NewTailSweeper builds the sweep. cfg carries the chunk interval and retention window the
// backfill worker is given, so the two agree on where the policy's reach ends.
func NewTailSweeper(ms *core.Microservice, rdbm *rdb.RdbManager, cfg Config, metrics *Metrics,
	callbacks core.LifecycleCallbacks) *TailSweeper {
	s := &TailSweeper{rdb: rdbm, cfg: cfg, metrics: metrics, now: time.Now}
	s.PeriodicTask = core.NewPeriodicTask(ms.FunctionalArea, "rollup-tail", tailInterval, s.RunOnce, callbacks)
	return s
}

// RunOnce makes one sweep under the instance lock, or reports the pass skipped when a backfill
// holds it.
func (s *TailSweeper) RunOnce(ctx context.Context) error {
	ran, err := s.rdb.TryAdvisoryLockSession(ctx, rdb.AdvisoryLockKey(lockName), func(db *gorm.DB) error {
		defer func() { _ = db.WithContext(context.Background()).Exec(`RESET statement_timeout`).Error }()
		return s.sweep(ctx, db.WithContext(core.WithSystemContext(ctx)))
	})
	if !ran {
		if err != nil {
			return fmt.Errorf("acquiring the rollup tail lock: %w", err)
		}
		return core.ErrPassSkipped
	}
	return err
}

// Sweep refreshes every chunk window between the retention (or event-time) floor and the start
// of the policy's reach, and reports how many it could not.
func (s *TailSweeper) sweep(ctx context.Context, db *gorm.DB) error {
	now := s.now()
	margin := max(24*time.Hour, s.cfg.ChunkInterval)
	// The policy owns a window that starts inside its reach less the margin; this owns the rest.
	reach := now.Add(-rollupPolicyReach).Add(margin)
	floor := now.Add(-eventtime.MaxAge).Add(-s.cfg.ChunkInterval)
	if s.cfg.RetentionDays > 0 {
		if r := now.Add(-time.Duration(s.cfg.RetentionDays) * 24 * time.Hour); r.After(floor) {
			floor = r
		}
	}
	windows, err := chunkWindows(db, floor, reach)
	if err != nil {
		return err
	}
	var failed int
	var lastErr error
	for i := len(windows) - 1; i >= 0; i-- { // oldest first
		if ctx.Err() != nil {
			return ctx.Err()
		}
		w := windows[i]
		if !w.Start.Before(reach) {
			continue
		}
		if err := s.refreshWindow(ctx, db, w); err != nil {
			failed++
			lastErr = err
			log.Warn().Err(err).Time("windowStart", w.Start).
				Msg("Rollup tail refresh failed; the window is tried again on the next sweep.")
		}
	}
	return core.PassResult(len(windows), failed, lastErr)
}

func (s *TailSweeper) refreshWindow(ctx context.Context, db *gorm.DB, win chunkWindow) error {
	if err := db.Exec(`SELECT set_config('statement_timeout', ?, false)`,
		fmt.Sprintf("%d", refreshTimeout.Milliseconds())).Error; err != nil {
		return err
	}
	started := s.now()
	backoff := time.Second
	for try := 1; ; try++ {
		var err error
		if s.refresh != nil {
			err = s.refresh(ctx, db, win.Start, win.End)
		} else {
			err = db.Exec(`CALL refresh_continuous_aggregate(?::regclass, ?::timestamptz, ?::timestamptz)`,
				rollupView, win.Start, win.End).Error
		}
		if err == nil {
			s.metrics.RefreshSeconds.Observe(s.now().Sub(started).Seconds())
			return nil
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
			return err
		}
		s.metrics.RefreshConflicts.Inc()
		if try >= tailRefreshTries {
			return fmt.Errorf("the rollup refresh over %s to %s met the aggregate policy %d times in a row",
				win.Start.Format(time.RFC3339), win.End.Format(time.RFC3339), tailRefreshTries)
		}
		sleep := s.sleep
		if sleep == nil {
			sleep = sleepCtx
		}
		if err := sleep(ctx, backoff); err != nil {
			return err
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}
