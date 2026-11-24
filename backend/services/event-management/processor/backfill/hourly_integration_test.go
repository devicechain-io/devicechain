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
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestIntegrationHourlyBackfillEqualsMinuteAggregate(t *testing.T) {
	r := newRig(t, "hourbackfill")
	require.NoError(t, r.sys().Exec(`SELECT alter_job(job_id, scheduled => false)
 FROM timescaledb_information.jobs WHERE proc_name = 'policy_refresh_continuous_aggregate'
 AND hypertable_schema = 'event-management'`).Error)
	end := time.Now().UTC().Truncate(time.Hour)
	start := end.Add(-8 * time.Hour)
	for i := 0; i < 8*60; i++ {
		at := start.Add(time.Duration(i)*time.Minute + 20*time.Second)
		r.insertReading("meter-1", at, at.String())
		if i%3 == 0 {
			r.insertReading("meter-1", at.Add(time.Second), at.String()+"extra")
		}
	}
	require.NoError(t, r.sys().Exec(`UPDATE "event-management".measurement_events
 SET value = (extract(minute FROM occurred_time)::int % 17 - 8) * 0.5`).Error)
	require.NoError(t, r.sys().Exec(`CALL refresh_continuous_aggregate('event-management.measurement_rollups', NULL, NULL)`).Error)
	// Put the hour watermark above history without filling it. Real-time aggregation must
	// not conceal a missing historical refresh in the equality below.
	require.NoError(t, r.sys().Exec(`CALL refresh_continuous_aggregate('event-management.measurement_rollups_1h', ?::timestamptz, ?::timestamptz)`, end.Add(-time.Hour), end).Error)
	require.NoError(t, r.sys().Exec(`UPDATE "event-management".rollup_levels SET complete_from = ? WHERE level = '1h'`, end).Error)
	type partial struct {
		TenantID, DeviceToken, Name  string
		EventType                    int
		Bucket                       time.Time
		SumValue, MinValue, MaxValue float64
		CountValue                   int64
	}
	var want, got []partial
	reference := `SELECT tenant_id, device_token, event_type, name,
 time_bucket('1 hour', bucket) AS bucket, sum(sum_value) AS sum_value,
 min(min_value) AS min_value, max(max_value) AS max_value, sum(count_value)::bigint AS count_value
 FROM "event-management".measurement_rollups WHERE bucket >= ? AND bucket < ?
 GROUP BY tenant_id, device_token, event_type, name, time_bucket('1 hour', bucket)
 ORDER BY tenant_id, device_token, event_type, name, bucket`
	hourly := `SELECT * FROM "event-management".measurement_rollups_1h WHERE bucket >= ? AND bucket < ?
 ORDER BY tenant_id, device_token, event_type, name, bucket`
	require.NoError(t, r.sys().Raw(reference, start, end).Scan(&want).Error)
	require.Len(t, want, 8)
	require.NoError(t, r.sys().Raw(hourly, start, end).Scan(&got).Error)
	require.NotEqual(t, want, got, "precondition: migration-era history is missing")
	ms := &core.Microservice{FunctionalArea: "event-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	s := NewHourlyBackfiller(ms, r.mgr, 0, core.NewNoOpLifecycleCallbacks())
	// The same lock used by the calculated backfill and tail sweep excludes this worker.
	ran, err := r.mgr.TryAdvisoryLockSession(context.Background(), rdb.AdvisoryLockKey(lockName), func(*gorm.DB) error {
		return s.RunOnce(context.Background())
	})
	require.True(t, ran)
	require.ErrorIs(t, err, core.ErrPassSkipped)
	// Interrupt the second window after the minute refresh, before the hour refresh.
	// Only the first fully refreshed window may be published, even though work on
	// the next one already reached the database.
	err = r.mgr.SingletonPassSession(context.Background(), lockName, func(db *gorm.DB) error {
		defer func() { _ = db.Exec(`RESET statement_timeout`).Error }()
		p := &interruptedHourlyProgress{hourlyDBProgress: &hourlyDBProgress{db: db, now: end}}
		return s.pass(context.Background(), p)
	})
	require.ErrorIs(t, err, errHourlyInterrupted)
	var afterInterruption time.Time
	require.NoError(t, r.sys().Raw(`SELECT complete_from FROM "event-management".rollup_levels WHERE level = '1h'`).Scan(&afterInterruption).Error)
	require.True(t, end.Add(-time.Hour).Equal(afterInterruption), "the incomplete second window stays owed")
	for i := 0; i < 3; i++ {
		require.NoError(t, s.RunOnce(context.Background()))
	}
	var marker time.Time
	require.NoError(t, r.sys().Raw(`SELECT complete_from FROM "event-management".rollup_levels WHERE level = '1h'`).Scan(&marker).Error)
	require.True(t, start.Equal(marker), "stop at the earliest source hour")
	got = nil
	require.NoError(t, r.sys().Raw(hourly, start, end).Scan(&got).Error)
	require.Equal(t, want, got, "hour partials must equal the minute aggregate after backfill")
}

var errHourlyInterrupted = errors.New("interrupted between minute and hour refresh")

type interruptedHourlyProgress struct {
	*hourlyDBProgress
	calls int
}

func (p *interruptedHourlyProgress) refresh(ctx context.Context, start, end time.Time, budget time.Duration) error {
	p.calls++
	if p.calls == 2 {
		if err := p.scoped(ctx).Exec(`CALL refresh_continuous_aggregate(?::regclass, ?::timestamptz, ?::timestamptz)`,
			rollupView, start, end).Error; err != nil {
			return err
		}
		return errHourlyInterrupted
	}
	return p.hourlyDBProgress.refresh(ctx, start, end, budget)
}
