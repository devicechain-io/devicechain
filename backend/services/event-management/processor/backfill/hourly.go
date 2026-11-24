// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backfill

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
)

const (
	hourlyWindowsPerPass = 4
	hourlyPassBudget     = 30 * time.Second
	hourlyPassInterval   = time.Minute
)

// hourlyProgress is operated only on the existing backfill lock's pinned session.
type hourlyProgress interface {
	bounds(context.Context) (marker, floor, databaseNow time.Time, err error)
	refresh(context.Context, time.Time, time.Time, time.Duration) error
	advance(context.Context, time.Time, time.Time) error
}

type hourlySessionLocker interface {
	SingletonPassSession(context.Context, string, func(*gorm.DB) error) error
}

// HourlyBackfiller fills the hour level's migration-era gap, newest hour first.
// It shares calc-backfill's pinned-session lock with the calculated worker and tail
// sweep. All reads, refreshes and marker writes live and die with that session.
// A policy conflict, timeout or crash leaves the current hour owed; a successful
// refresh followed by a crash before the marker write is safely repeated.
type HourlyBackfiller struct {
	*core.PeriodicTask
	rdb           hourlySessionLocker
	retentionDays int
	now           func() time.Time
	completeFrom  prometheus.Gauge
	remaining     prometheus.Gauge
	windows       prometheus.Counter
	initProgress  func()
	// Once migration history is covered, policies and the tail sweep maintain it.
	// Never rescan source bounds on subsequent passes in this process.
	floorReached bool
}

// NewHourlyBackfiller uses the same retention window as the rollup read path.
func NewHourlyBackfiller(ms *core.Microservice, mgr *rdb.RdbManager, retentionDays int,
	callbacks core.LifecycleCallbacks) *HourlyBackfiller {
	s := &HourlyBackfiller{
		rdb: mgr, retentionDays: retentionDays, now: time.Now,
		windows: ms.NewCounter("rollup_hour_backfill_windows_total",
			"Hour windows refreshed and recorded complete by this replica."),
	}
	// A replica that never wins the lock has no observed progress to export.
	s.initProgress = func() {
		s.completeFrom = ms.NewGauge("rollup_hour_backfill_complete_from_timestamp_seconds",
			"Earliest fully refreshed hour boundary, as a Unix timestamp.")
		s.remaining = ms.NewGauge("rollup_hour_backfill_remaining_windows",
			"Hour windows still owed before earliest data or the rollup retention horizon.")
	}
	s.PeriodicTask = core.NewPeriodicTask(ms.FunctionalArea, "rollup-hour-backfill", hourlyPassInterval,
		s.RunOnce, callbacks, ms.NewPeriodicTaskMetrics("rollup_hour_backfill"))
	return s
}

// RunOnce bounds the lock acquisition, catalog reads and all refreshes together.
func (s *HourlyBackfiller) RunOnce(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, hourlyPassBudget)
	defer cancel()
	return s.rdb.SingletonPassSession(ctx, lockName, func(db *gorm.DB) error {
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = db.WithContext(cleanup).Exec(`RESET statement_timeout`).Error
		}()
		// Cross-tenant by design: only time bounds and aggregate progress leave the DB.
		// Both rollup levels group by tenant, and refreshes never return tenant rows.
		p := &hourlyDBProgress{db: db, retentionDays: s.retentionDays, floorReached: s.floorReached}
		return s.pass(ctx, p)
	})
}

func (s *HourlyBackfiller) pass(ctx context.Context, progress hourlyProgress) error {
	deadline := s.now().Add(hourlyPassBudget)
	marker, floor, databaseNow, err := progress.bounds(ctx)
	if err != nil {
		return err
	}
	s.report(marker, floor)
	if !marker.After(floor) {
		s.floorReached = true
	}
	// A migration sets the marker to NEXT hour. Refreshing that still-open hour
	// would push the materialization watermark into the future and hide subsequent
	// readings below it. Wait for it to close before publishing any completeness.
	if marker.After(databaseNow.UTC().Truncate(time.Hour)) {
		return nil
	}
	for n := 0; n < hourlyWindowsPerPass && marker.After(floor); n++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		left := deadline.Sub(s.now())
		if left <= 0 {
			break
		}
		start := marker.Add(-time.Hour)
		if err := progress.refresh(ctx, start, marker, left); err != nil {
			// The hour policy also refreshes every minute, independently of our
			// advisory lock. A 55P03 collision leaves this hour owed for next pass.
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
				return core.ErrPassSkipped
			}
			return err
		}
		// Never publish completeness before the hour refresh returns successfully.
		// No transaction surrounds CALL (the existing refresh mechanism is idempotent).
		if err := progress.advance(ctx, marker, start); err != nil {
			return err
		}
		marker = start
		if s.windows != nil {
			s.windows.Inc()
		}
		s.report(marker, floor)
	}
	if !marker.After(floor) {
		s.floorReached = true
	}
	return nil
}

func (s *HourlyBackfiller) report(marker, floor time.Time) {
	if s.initProgress != nil {
		s.initProgress()
		s.initProgress = nil
	}
	if s.completeFrom != nil {
		s.completeFrom.Set(float64(marker.Unix()))
		s.remaining.Set(max(0, marker.Sub(floor).Hours()))
	}
}

type hourlyDBProgress struct {
	db            *gorm.DB
	retentionDays int
	floorReached  bool
}

func (p *hourlyDBProgress) scoped(ctx context.Context) *gorm.DB {
	return p.db.WithContext(core.WithSystemContext(ctx))
}

func (p *hourlyDBProgress) bounds(ctx context.Context) (marker, floor, databaseNow time.Time, err error) {
	db := p.scoped(ctx)
	if err = db.Raw(`SELECT complete_from, now() FROM "event-management".rollup_levels WHERE level = '1h'`).Row().Scan(&marker, &databaseNow); err != nil {
		return marker, floor, databaseNow, err
	}
	if marker.IsZero() || !marker.Equal(marker.UTC().Truncate(time.Hour)) {
		return marker, floor, databaseNow, fmt.Errorf("hour rollup completeness marker is missing or off the hour")
	}
	if p.floorReached {
		return marker, marker, databaseNow, nil
	}
	// The minute level may retain data already dropped from raw; conversely, raw may
	// hold old readings the minute policy never reached. Cover both, not just raw.
	var earliest sql.NullTime
	if err = db.Raw(`SELECT min(at) FROM (
 SELECT min(bucket) AS at FROM "event-management".measurement_rollups
 UNION ALL SELECT min(range_start) AS at FROM timescaledb_information.chunks
 WHERE hypertable_schema = 'event-management' AND hypertable_name = 'measurement_events'
) source_bounds`).Row().Scan(&earliest); err != nil {
		return marker, floor, databaseNow, err
	}
	floor = marker // an empty store has no historical work
	if earliest.Valid {
		floor = earliest.Time
		if p.retentionDays > 0 {
			floor = laterTime(floor, databaseNow.Add(-time.Duration(p.retentionDays)*24*time.Hour))
		}
		// Refresh the whole boundary hour. Reads enforce the precise retention floor.
		floor = floor.UTC().Truncate(time.Hour)
	}
	return marker, floor, databaseNow, nil
}

func laterTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func (p *hourlyDBProgress) refresh(ctx context.Context, start, end time.Time, budget time.Duration) error {
	// Raw retention may already have dropped chunks still represented by retained
	// minute rows. Refreshing minutes here would DELETE those rows and copy the
	// hole into hours. The minute policy and tail sweep own minute maintenance.
	if budget <= 0 {
		return &pgconn.PgError{Code: "57014", Message: "the hour refresh budget was spent"}
	}
	db := p.scoped(ctx)
	if err := db.Exec(`SELECT set_config('statement_timeout', ?, false)`,
		fmt.Sprintf("%d", max(1, budget.Milliseconds()))).Error; err != nil {
		return err
	}
	return db.Exec(`CALL refresh_continuous_aggregate(?::regclass, ?::timestamptz, ?::timestamptz)`,
		rollupViewHourly, start, end).Error
}

func (p *hourlyDBProgress) advance(ctx context.Context, from, to time.Time) error {
	row := p.scoped(ctx).Exec(`UPDATE "event-management".rollup_levels SET complete_from = ?
 WHERE level = '1h' AND complete_from = ?`, to, from)
	if row.Error != nil {
		return row.Error
	}
	if row.RowsAffected != 1 {
		return fmt.Errorf("hour rollup completeness marker changed during a locked pass")
	}
	return nil
}
