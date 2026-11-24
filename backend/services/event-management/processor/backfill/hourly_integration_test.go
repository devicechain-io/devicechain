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
	// Pre-fill no hours. Disable real-time aggregation so it cannot conceal missing
	// materialized history, including the first hour this backfill must publish.
	require.NoError(t, r.sys().Exec(`ALTER MATERIALIZED VIEW "event-management".measurement_rollups_1h SET (timescaledb.materialized_only = true)`).Error)
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
	require.Empty(t, got, "precondition: every historical hour, including the first, is missing")
	ms := &core.Microservice{FunctionalArea: "event-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	s := NewHourlyBackfiller(ms, r.mgr, 0, core.NewNoOpLifecycleCallbacks())
	// The same lock used by the calculated backfill and tail sweep excludes this worker.
	ran, err := r.mgr.TryAdvisoryLockSession(context.Background(), rdb.AdvisoryLockKey(lockName), func(*gorm.DB) error {
		return s.RunOnce(context.Background())
	})
	require.True(t, ran)
	require.ErrorIs(t, err, core.ErrPassSkipped)
	// Interrupt after the first previously absent hour refresh, before publishing
	// its marker. Restart must repeat that hour; the second hour is still absent.
	err = r.mgr.SingletonPassSession(context.Background(), lockName, func(db *gorm.DB) error {
		defer func() { _ = db.Exec(`RESET statement_timeout`).Error }()
		p := &interruptedHourlyProgress{hourlyDBProgress: &hourlyDBProgress{db: db}}
		return s.pass(context.Background(), p)
	})
	require.ErrorIs(t, err, errHourlyInterrupted)
	var afterInterruption time.Time
	require.NoError(t, r.sys().Raw(`SELECT complete_from FROM "event-management".rollup_levels WHERE level = '1h'`).Scan(&afterInterruption).Error)
	require.True(t, end.Equal(afterInterruption), "the refreshed but unpublished first window stays owed")
	got = nil
	require.NoError(t, r.sys().Raw(hourly, end.Add(-time.Hour), end).Scan(&got).Error)
	require.Equal(t, want[len(want)-1:], got, "the interrupted pass really filled its first hour")
	got = nil
	require.NoError(t, r.sys().Raw(hourly, end.Add(-2*time.Hour), end.Add(-time.Hour)).Scan(&got).Error)
	require.Empty(t, got, "before the second pass, end-2h must still be absent")
	// A raw chunk can start before its first reading; the catalog floor deliberately
	// includes that empty part of the boundary chunk.
	_, floor, _, err := (&hourlyDBProgress{db: r.sys()}).bounds(context.Background())
	require.NoError(t, err)
	for i := 0; i < int(end.Sub(floor)/time.Hour)/hourlyWindowsPerPass+1; i++ {
		require.NoError(t, s.RunOnce(context.Background()))
	}
	var marker time.Time
	require.NoError(t, r.sys().Raw(`SELECT complete_from FROM "event-management".rollup_levels WHERE level = '1h'`).Scan(&marker).Error)
	require.True(t, floor.Equal(marker), "stop at the earliest source chunk/minute hour")
	got = nil
	require.NoError(t, r.sys().Raw(hourly, start, end).Scan(&got).Error)
	require.Equal(t, want, got, "hour partials must equal the minute aggregate after backfill")
}

var errHourlyInterrupted = errors.New("interrupted between hour refresh and marker publication")

type interruptedHourlyProgress struct {
	*hourlyDBProgress
}

func (p *interruptedHourlyProgress) advance(context.Context, time.Time, time.Time) error {
	return errHourlyInterrupted
}

func TestIntegrationHourlyBackfillPreservesMinutesAfterRawChunkDrop(t *testing.T) {
	r := newRig(t, "hourretention")
	require.NoError(t, r.sys().Exec(`SELECT alter_job(job_id, scheduled => false)
 FROM timescaledb_information.jobs WHERE proc_name = 'policy_refresh_continuous_aggregate'
 AND hypertable_schema = 'event-management'`).Error)
	var now time.Time
	require.NoError(t, r.sys().Raw(`SELECT now()`).Scan(&now).Error)
	// A 30-day raw policy drops this entire daily chunk below its floor, while
	// RollupRetentionDays retains its minute summaries for 31 days.
	rawFloor := now.UTC().Truncate(24 * time.Hour).Add(-30 * 24 * time.Hour)
	start, end := rawFloor.Add(-time.Hour), rawFloor
	r.insertReading("retained", start.Add(10*time.Minute), "retained-a")
	r.insertReading("retained", start.Add(11*time.Minute), "retained-b")
	r.insertReading("recent", now.Add(-2*time.Hour), "recent")
	require.NoError(t, r.sys().Exec(`CALL refresh_continuous_aggregate('event-management.measurement_rollups', NULL, NULL)`).Error)
	// Query materialized data only, so real-time aggregation cannot mask deletion
	// of the minute rows or a skipped hour refresh.
	for _, view := range []string{rollupView, rollupViewHourly} {
		require.NoError(t, r.sys().Exec(`ALTER MATERIALIZED VIEW `+view+` SET (timescaledb.materialized_only = true)`).Error)
	}
	type partial struct {
		TenantID, DeviceToken, Name  string
		EventType                    int
		Bucket                       time.Time
		SumValue, MinValue, MaxValue float64
		CountValue                   int64
	}
	minutes := `SELECT * FROM "event-management".measurement_rollups WHERE bucket >= ? AND bucket < ? ORDER BY bucket`
	var before, after, want, got []partial
	require.NoError(t, r.sys().Raw(minutes, start, end).Scan(&before).Error)
	require.Len(t, before, 2)
	require.NoError(t, r.sys().Raw(`SELECT tenant_id, device_token, event_type, name,
 time_bucket('1 hour', bucket) AS bucket, sum(sum_value) AS sum_value,
 min(min_value) AS min_value, max(max_value) AS max_value, sum(count_value)::bigint AS count_value
 FROM "event-management".measurement_rollups WHERE bucket >= ? AND bucket < ?
 GROUP BY tenant_id, device_token, event_type, name, time_bucket('1 hour', bucket)`, start, end).Scan(&want).Error)
	require.Len(t, want, 1)
	var dropped []string
	require.NoError(t, r.sys().Raw(`SELECT drop_chunks('"event-management".measurement_events', older_than => ?::timestamptz)`, rawFloor).Scan(&dropped).Error)
	require.Len(t, dropped, 1, "precondition: one raw chunk was physically dropped")
	var rawCount int64
	require.NoError(t, r.sys().Raw(`SELECT count(*) FROM "event-management".measurement_events WHERE occurred_time >= ? AND occurred_time < ?`, start, end).Scan(&rawCount).Error)
	require.Zero(t, rawCount)
	require.NoError(t, r.sys().Raw(minutes, start, end).Scan(&after).Error)
	require.Equal(t, before, after, "dropping raw preserves retained minute rows")
	require.NoError(t, r.sys().Exec(`UPDATE "event-management".rollup_levels SET complete_from = ? WHERE level = '1h'`, end).Error)
	hourly := `SELECT * FROM "event-management".measurement_rollups_1h WHERE bucket >= ? AND bucket < ?`
	require.NoError(t, r.sys().Raw(hourly, start, end).Scan(&got).Error)
	require.Empty(t, got, "precondition: the hour still needs backfill")
	ms := &core.Microservice{FunctionalArea: "event-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	s := NewHourlyBackfiller(ms, r.mgr, 31, core.NewNoOpLifecycleCallbacks())
	require.NoError(t, s.RunOnce(context.Background()))
	after = nil
	require.NoError(t, r.sys().Raw(minutes, start, end).Scan(&after).Error)
	require.Equal(t, before, after, "backfill must never refresh minutes over dropped raw")
	require.NoError(t, r.sys().Raw(hourly, start, end).Scan(&got).Error)
	require.Equal(t, want, got, "hour partials equal the surviving minute summaries")
	var marker time.Time
	require.NoError(t, r.sys().Raw(`SELECT complete_from FROM "event-management".rollup_levels WHERE level = '1h'`).Scan(&marker).Error)
	require.True(t, start.Equal(marker), "publish the retained hour only after refreshing it")
}
