// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backfill

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/devicechain-io/dc-microservice/rdb"
)

const (
	// tailTick is how often the task wakes. A pass runs when one is due: an hour after the last
	// one that finished, or at the next tick after one that lost the lock, so a long calculated
	// backfill starves the sweep for minutes and not for an hour per miss.
	tailTick = 5 * time.Minute
	// tailInterval is the gap between completed passes.
	tailInterval = time.Hour
	// tailFirstDelay is how soon after start the first pass runs, plus up to tailJitter, so a
	// restart is repaired in minutes and replicas do not line up.
	tailFirstDelay = 2 * time.Minute
	tailJitter     = 3 * time.Minute
	// tailRefreshTries is how often one window's refresh is retried when it meets the aggregate
	// policy's own refresh before the window is given up for now.
	tailRefreshTries = 3
	// tailBudget is the longest one pass runs. The lock is NOT held across it (see sweep), so it
	// bounds work per pass, not how long anything else waits.
	tailBudget = 10 * time.Minute
	// tailWindowTimeout caps ONE window's refresh, well under the budget: a window that cannot
	// finish in this (merged invalidations re-aggregating a whole chunk across tenants) is
	// skipped and backed off instead of holding the sweep, and the aggregate's lock, for the
	// statement timeout the backfill uses.
	tailWindowTimeout = 3 * time.Minute
	// tailBackoffBase and tailBackoffMax bound the in-memory backoff of a window that failed.
	tailBackoffBase = 10 * time.Minute
	tailBackoffMax  = 24 * time.Hour
	// tailFloorSlack is how far below the event-time floor an instance looks. A reading is
	// checked against the floor when it ARRIVES, so one accepted at the limit can wait for a
	// sweep that runs after the floor has moved past it.
	tailFloorSlack = 90 * 24 * time.Hour
)

// Reasons a window was not refreshed, the label of Metrics.TailSkipped.
const (
	tailSkipTimeout  = "timeout"
	tailSkipConflict = "conflict"
	tailSkipError    = "error"
	tailSkipBackoff  = "backoff"
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
// chunk at a time, from where the policy's reach begins down to the floor.
//
// 🔴 HOW A PASS BEHAVES ON A BIG STORE, which is what the design is for:
//   - The instance lock (the backfill worker's, so the two never refresh at once) is taken PER
//     WINDOW and released between windows, so the policy job and a backfill interleave with a
//     sweep instead of waiting out a pass.
//   - Newest window first: the readings a person is most likely to be looking at.
//   - A window that times out or fails is skipped and BACKED OFF in memory (doubling, capped),
//     and the pass moves on, so one window that cannot finish never blocks the rest.
//   - A lost lock ends the pass and the next tick (minutes) tries again.
//
// The invalidation log Timescale keeps was considered as the thing to drive from (a pass with
// nothing to do would then be one query) and rejected: it is an internal catalog table whose
// shape and units are not a contract, and measured on 2.31 it was EMPTY for a late reading that
// was still missing from the rollup.
type TailSweeper struct {
	*core.PeriodicTask

	rdb     *rdb.RdbManager
	cfg     Config
	metrics *Metrics
	now     func() time.Time
	// refresh, sleep and jitter replace the refresh itself, the wait between retries and the
	// random delay, for tests.
	refresh func(ctx context.Context, db *gorm.DB, ws, we time.Time) error
	sleep   func(ctx context.Context, d time.Duration) error
	jitter  func() time.Duration

	// nextPass is when the next pass is due; zero until the first tick fixes it.
	nextPass time.Time
	// backoff remembers, per window start, when a failed window may be tried again. Lost on a
	// restart, which only means it is tried again.
	backoff map[time.Time]tailBackoff
	// cycleDone holds the windows refreshed in the current cycle, a cycle being the passes from
	// one that completed to the next. A pass that ran out of budget or lost the lock continues
	// from where it was, so a store too big for one budget still gets to its oldest window.
	cycleDone map[time.Time]struct{}
	// afterWindow runs after a window's lock is released, for tests.
	afterWindow func()
}

type tailBackoff struct {
	until time.Time
	delay time.Duration
}

// NewTailSweeper builds the sweep. cfg carries the chunk interval and retention window the
// backfill worker is given, so the two agree on where the policy's reach ends.
func NewTailSweeper(ms *core.Microservice, rdbm *rdb.RdbManager, cfg Config, metrics *Metrics,
	callbacks core.LifecycleCallbacks) *TailSweeper {
	return newTailSweeper(ms, rdbm, cfg, metrics, callbacks, time.Now)
}

func newTailSweeper(ms *core.Microservice, rdbm *rdb.RdbManager, cfg Config, metrics *Metrics,
	callbacks core.LifecycleCallbacks, now func() time.Time) *TailSweeper {
	s := &TailSweeper{rdb: rdbm, cfg: cfg, metrics: metrics, now: now,
		backoff: map[time.Time]tailBackoff{}, cycleDone: map[time.Time]struct{}{},
		jitter: func() time.Duration { return rand.N(tailJitter) }}
	// The first pass is due from CONSTRUCTION, not from the first tick: the task's first tick is a
	// tick after start, and counting the delay from there would double it.
	s.nextPass = now().Add(tailFirstDelay + s.jitter())
	s.PeriodicTask = core.NewPeriodicTask(ms.FunctionalArea, "rollup-tail", tailTick, s.RunOnce, callbacks)
	return s
}

// RunOnce runs a pass if one is due.
func (s *TailSweeper) RunOnce(ctx context.Context) error {
	now := s.now()
	if s.nextPass.IsZero() {
		s.nextPass = now.Add(tailFirstDelay + s.jitter())
	}
	if now.Before(s.nextPass) {
		return core.ErrPassSkipped
	}
	complete, err := s.sweep(ctx)
	if complete {
		s.nextPass = s.now().Add(tailInterval + s.jitter())
	}
	return err
}

// floor is the oldest instant worth looking at: the later of the retention floor (nothing older
// is kept) and the event-time floor with slack (nothing older is accepted).
func (s *TailSweeper) floor(now time.Time) time.Time {
	floor := now.Add(-eventtime.MaxAge).Add(-s.cfg.ChunkInterval).Add(-tailFloorSlack)
	if s.cfg.RetentionDays > 0 {
		if r := now.Add(-time.Duration(s.cfg.RetentionDays) * 24 * time.Hour); r.After(floor) {
			floor = r
		}
	}
	return floor
}

// sweep refreshes the chunk windows between the floor and the start of the policy's reach. It
// reports whether the pass reached the end of them (false when it lost the lock or ran out of
// budget, so the next tick continues).
func (s *TailSweeper) sweep(ctx context.Context) (complete bool, err error) {
	now := s.now()
	margin := max(24*time.Hour, s.cfg.ChunkInterval)
	// The policy owns a window that starts inside its reach less the margin; this owns the rest.
	reach := now.Add(-rollupPolicyReach).Add(margin)
	windows, err := chunkWindows(s.rdb.DB(core.WithSystemContext(ctx)), s.floor(now), reach)
	if err != nil {
		return false, err
	}
	deadline := now.Add(tailBudget)
	var failed int
	var lastErr error
	for _, w := range windows { // newest first
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if !w.Start.Before(reach) {
			continue
		}
		if _, done := s.cycleDone[w.Start]; done {
			continue
		}
		if b, ok := s.backoff[w.Start]; ok && s.now().Before(b.until) {
			s.metrics.TailSkipped.WithLabelValues(tailSkipBackoff).Inc()
			continue
		}
		if s.now().After(deadline) {
			log.Info().Msg("Rollup tail sweep reached its time budget; the rest is left for the next tick.")
			return false, core.PassResult(len(windows), failed, lastErr)
		}
		ran, rerr := s.rdb.TryAdvisoryLockSession(ctx, rdb.AdvisoryLockKey(lockName), func(db *gorm.DB) error {
			defer func() { _ = db.WithContext(context.Background()).Exec(`RESET statement_timeout`).Error }()
			return s.refreshWindow(ctx, db.WithContext(core.WithSystemContext(ctx)), w)
		})
		if s.afterWindow != nil {
			s.afterWindow()
		}
		if !ran {
			if rerr != nil {
				return false, fmt.Errorf("acquiring the rollup tail lock: %w", rerr)
			}
			// A backfill (or another replica's window) holds it. Left for the next tick.
			return false, nil
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			failed++
			lastErr = rerr
			s.skip(w, rerr)
			continue
		}
		delete(s.backoff, w.Start)
		s.cycleDone[w.Start] = struct{}{}
	}
	s.cycleDone = map[time.Time]struct{}{}
	return true, core.PassResult(len(windows), failed, lastErr)
}

// skip records a failed window: its reason, a doubled backoff, a log line.
func (s *TailSweeper) skip(w chunkWindow, err error) {
	reason := tailSkipError
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == "57014":
		reason = tailSkipTimeout
	case errors.As(err, &pgErr) && pgErr.Code == "55P03":
		reason = tailSkipConflict
	}
	delay := tailBackoffBase
	if b, ok := s.backoff[w.Start]; ok {
		delay = min(b.delay*2, tailBackoffMax)
	}
	s.backoff[w.Start] = tailBackoff{until: s.now().Add(delay), delay: delay}
	s.metrics.TailSkipped.WithLabelValues(reason).Inc()
	log.Warn().Err(err).Time("windowStart", w.Start).Str("reason", reason).Dur("retryIn", delay).
		Msg("Rollup tail refresh skipped a window; it is tried again after the backoff.")
}

func (s *TailSweeper) refreshWindow(ctx context.Context, db *gorm.DB, win chunkWindow) error {
	if err := db.Exec(`SELECT set_config('statement_timeout', ?, false)`,
		fmt.Sprintf("%d", tailWindowTimeout.Milliseconds())).Error; err != nil {
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
			return fmt.Errorf("the rollup refresh over %s to %s met the aggregate policy %d times in a row: %w",
				win.Start.Format(time.RFC3339), win.End.Format(time.RFC3339), tailRefreshTries, err)
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
