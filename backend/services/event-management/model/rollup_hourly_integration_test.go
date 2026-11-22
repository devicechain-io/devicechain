// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// seedHourlyReadings writes one reading per name every minute (20s past it, so no row sits on a
// bucket edge) for [from, to] under one tenant. Values are multiples of 0.5 so every sum is exact
// in float64 and the two levels can be compared for equality, not within a tolerance. The names
// differ in case on purpose: the database's collation, not Go's byte order, decides their order.
func seedHourlyReadings(t *testing.T, sys *gorm.DB, tenant string, from, to time.Time) {
	t.Helper()
	require.NoError(t, sys.Exec(`INSERT INTO "event-management".measurement_events
 (tenant_id, event_id, payload_id, device_token, event_type, occurred_time, name, value, unit, data_type)
 SELECT ?::text, sha256(convert_to(?::text || n || ts::text, 'UTF8')),
        sha256(convert_to('p' || ?::text || n || ts::text, 'UTF8')),
        'meter-1', 2, ts, n, ((extract(epoch FROM ts)::bigint / 60) % 97) + idx * 0.5, 'u', 'DOUBLE'
 FROM generate_series(?::timestamptz + interval '20 seconds', ?::timestamptz, interval '1 minute') ts,
      unnest(ARRAY['kw', 'Kw', 'amps', '_x']) WITH ORDINALITY AS u(n, idx)`,
		tenant, tenant, tenant, from, to).Error)
}

// refreshRollupsNow brings both levels up to date over the whole range, the minute level first,
// retrying while the aggregates' own policy jobs hold the refresh (55P03).
func refreshRollupsNow(t *testing.T, sys *gorm.DB, views ...string) {
	t.Helper()
	for _, view := range views {
		for attempt := 0; ; attempt++ {
			err := sys.Exec(fmt.Sprintf(`CALL refresh_continuous_aggregate('event-management.%s', NULL, NULL)`, view)).Error
			if err == nil {
				break
			}
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "55P03" || attempt == 20 {
				require.NoError(t, err)
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
}

func setHourlyMarker(t *testing.T, sys *gorm.DB, at time.Time) {
	t.Helper()
	require.NoError(t, sys.Exec(`UPDATE "event-management".rollup_levels SET complete_from = ? WHERE level = '1h'`, at).Error)
}

func hourlyMaterialization(t *testing.T, sys *gorm.DB) string {
	t.Helper()
	var name string
	require.NoError(t, sys.Raw(`SELECT format('%I.%I', materialization_hypertable_schema, materialization_hypertable_name)
 FROM timescaledb_information.continuous_aggregates
 WHERE view_schema = 'event-management' AND view_name = 'measurement_rollups_1h'`).Scan(&name).Error)
	require.NotEmpty(t, name, "the hour aggregate does not exist")
	return name
}

func hourlyRefreshPolicies(t *testing.T, sys *gorm.DB) []string {
	t.Helper()
	var configs []string
	require.NoError(t, sys.Raw(`SELECT config::text FROM timescaledb_information.jobs
 WHERE hypertable_schema = 'event-management' AND hypertable_name = 'measurement_rollups_1h'
 AND proc_name = 'policy_refresh_continuous_aggregate'`).Scan(&configs).Error)
	return configs
}

// TestIntegrationHourlyReadEqualsTheMinuteLevel is the equivalence proof: for reads of whole
// hours, the answer served through the hour level is the answer re-bucketing the minute level
// gives, at every edge the two levels can disagree on. It reads through the real chain, with
// the current hour still filling.
func TestIntegrationHourlyReadEqualsTheMinuteLevel(t *testing.T) {
	mgr := newPostgresManager(t, freshInstance(t, "ithourly"))
	sys := systemDB(mgr)
	now := time.Now().UTC().Truncate(time.Minute).Add(30 * time.Second)
	hour0 := now.Truncate(time.Hour).Add(-9 * time.Hour)
	seedHourlyReadings(t, sys, "acme", hour0, now)
	seedHourlyReadings(t, sys, "other", hour0, now) // a second tenant that must never leak in
	refreshRollupsNow(t, sys, "measurement_rollups", "measurement_rollups_1h")
	// The migration records the next hour boundary as the hour the aggregate is complete from, so a
	// store just created reads everything from the minute level. Move the marker back, as it stands
	// on a store that has run for a while, so the hour level serves the range under test.
	setHourlyMarker(t, sys, hour0)

	var materialized int64
	require.NoError(t, sys.Table(hourlyMaterialization(t, sys)).Count(&materialized).Error)
	require.Positive(t, materialized, "the hour level holds nothing, so the comparison below would be vacuous")

	api := NewApi(mgr)
	api.now = func() time.Time { return now }
	ctx := core.WithTenant(context.Background(), "acme")
	dev, name := "meter-1", "kw"
	at := func(h int, m int) *time.Time {
		v := hour0.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
		return &v
	}
	nowPtr := func() *time.Time { v := now; return &v }

	cases := []struct {
		name            string
		start, end      *time.Time
		interval        int64
		metric          *string
		retentionDays   int
		wantHourlyShare bool
	}{
		{"aligned start, ends now (partial current hour)", at(0, 0), nowPtr(), 3600, nil, 0, true},
		{"aligned start, ends now, one metric", at(0, 0), nowPtr(), 3600, &name, 0, true},
		{"mid-hour start", at(0, 17), nowPtr(), 3600, nil, 0, true},
		{"mid-hour start and mid-hour end", at(1, 17), at(6, 41), 3600, nil, 0, true},
		{"end on an hour boundary", at(0, 0), at(5, 0), 3600, nil, 0, true},
		{"end on the last minute of an hour", at(0, 0), at(5, 59), 3600, nil, 0, true},
		{"end on the second-to-last minute of an hour", at(0, 0), at(5, 58), 3600, nil, 0, true},
		{"two-hour buckets, ragged edges", at(1, 30), at(8, 20), 7200, nil, 0, true},
		{"three-hour buckets, ends now", at(0, 0), nowPtr(), 10800, nil, 0, true},
		{"inside a single hour", at(3, 5), at(3, 50), 3600, nil, 0, false},
		{"exactly one whole hour", at(3, 0), at(3, 59), 3600, nil, 0, true},
		{"retention floor cutting an hour", at(0, 0), nowPtr(), 3600, nil, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api.RollupRetentionDays = tc.retentionDays
			if tc.retentionDays > 0 {
				// A one-day window whose floor falls mid-hour: 7h30m before now.
				api.now = func() time.Time { return now.Add(24*time.Hour - 7*time.Hour - 30*time.Minute) }
				defer func() { api.now = func() time.Time { return now } }()
			}
			criteria := MeasurementAggregationCriteria{DeviceToken: &dev, Name: tc.metric,
				IntervalSeconds: tc.interval, StartTime: tc.start, EndTime: tc.end}
			_, span, err := api.hourlySpan(ctx, criteria)
			require.NoError(t, err)
			require.Equal(t, tc.wantHourlyShare, span, "the read's hour-level share")

			want, err := api.bucketedMeasurementsFromMinuteRollup(ctx, criteria)
			require.NoError(t, err)
			require.NotEmpty(t, want, "the reference read is empty, so the comparison would be vacuous")
			got, err := api.BucketedMeasurements(ctx, criteria)
			require.NoError(t, err)
			assertSameBuckets(t, want, got)
		})
	}

	// Negative control: the comparison reads the hour level, and fails when the hour level is
	// stale. A late reading lands in an old hour and only the minute level is refreshed; the
	// hour read must now DIFFER from the minute reference, and agree again once it is refreshed.
	// The first half is what makes the equalities above evidence rather than coincidence.
	api.RollupRetentionDays = 0
	late := hour0.Add(3*time.Hour + 10*time.Minute + 45*time.Second)
	require.NoError(t, sys.Exec(`INSERT INTO "event-management".measurement_events
 (tenant_id, event_id, payload_id, device_token, event_type, occurred_time, name, value, unit, data_type)
 VALUES ('acme', sha256('late'), sha256('plate'), 'meter-1', 2, ?, 'kw', 1000, 'u', 'DOUBLE')`, late).Error)
	refreshRollupsNow(t, sys, "measurement_rollups")
	criteria := MeasurementAggregationCriteria{DeviceToken: &dev, Name: &name, IntervalSeconds: 3600,
		StartTime: at(0, 0), EndTime: nowPtr()}
	want, err := api.bucketedMeasurementsFromMinuteRollup(ctx, criteria)
	require.NoError(t, err)
	got, err := api.BucketedMeasurements(ctx, criteria)
	require.NoError(t, err)
	require.NotEqual(t, want, got, "a stale hour level must be visible to the comparison")
	refreshRollupsNow(t, sys, "measurement_rollups_1h")
	got, err = api.BucketedMeasurements(ctx, criteria)
	require.NoError(t, err)
	assertSameBuckets(t, want, got)
}

func assertSameBuckets(t *testing.T, want, got []MeasurementBucket) {
	t.Helper()
	require.Equal(t, len(want), len(got), "bucket count")
	for i := range want {
		assert.Truef(t, want[i].BucketStart.Equal(got[i].BucketStart), "row %d bucket_start: want %v got %v", i, want[i].BucketStart, got[i].BucketStart)
		assert.Equalf(t, want[i].Name, got[i].Name, "row %d name", i)
		assert.Equalf(t, want[i].Count, got[i].Count, "row %d count (%s @ %v)", i, want[i].Name, want[i].BucketStart)
		assert.Equalf(t, want[i].Min, got[i].Min, "row %d min", i)
		assert.Equalf(t, want[i].Max, got[i].Max, "row %d max", i)
		assert.Equalf(t, want[i].Sum, got[i].Sum, "row %d sum", i)
		assert.Equalf(t, want[i].Avg, got[i].Avg, "row %d avg", i)
	}
}

type hourlyJob struct {
	JobID            int64
	ScheduleInterval string
	Config           string
	Scheduled        bool
}

func hourlyPolicyJob(t *testing.T, sys *gorm.DB) hourlyJob {
	t.Helper()
	var job hourlyJob
	require.NoError(t, sys.Raw(`SELECT job_id, schedule_interval::text AS schedule_interval, config::text AS config, scheduled
 FROM timescaledb_information.jobs
 WHERE hypertable_schema = 'event-management' AND hypertable_name = 'measurement_rollups_1h'
 AND proc_name = 'policy_refresh_continuous_aggregate'`).Scan(&job).Error)
	require.NotZero(t, job.JobID, "the hour aggregate has no refresh policy")
	return job
}

func hourlyMarker(t *testing.T, sys *gorm.DB) time.Time {
	t.Helper()
	var at time.Time
	require.NoError(t, sys.Raw(`SELECT complete_from FROM "event-management".rollup_levels WHERE level = '1h'`).Scan(&at).Error)
	return at
}

// TestIntegrationHourlyAggregateIsCreatedWithoutHistory upgrades a store that already holds
// readings, some older than the policy's window, and requires the migration to be DDL only: the
// aggregate starts empty, the marker is the next hour boundary, and reads of the old history
// still equal the minute level. A planted defect that ignores the marker shows the history
// missing. A replay over the live aggregate leaves its policy job and marker untouched.
func TestIntegrationHourlyAggregateIsCreatedWithoutHistory(t *testing.T) {
	inst := freshInstance(t, "ithourlyup")
	id := NewRollup1hSchema().ID
	before := newPostgresManagerWith(t, inst, migrationsBefore(t, id))
	sys := systemDB(before)
	var existing int64
	require.NoError(t, sys.Raw(`SELECT count(*) FROM timescaledb_information.continuous_aggregates
 WHERE view_name = 'measurement_rollups_1h'`).Scan(&existing).Error)
	require.Zero(t, existing, "the chain before the migration must not hold the aggregate")

	now := time.Now().UTC().Truncate(time.Minute).Add(30 * time.Second)
	old := now.Truncate(time.Hour).Add(-50 * 24 * time.Hour) // older than the policy's 30-day window
	seedHourlyReadings(t, sys, "acme", old, old.Add(5*time.Hour))
	seedHourlyReadings(t, sys, "acme", now.Truncate(time.Hour).Add(-3*time.Hour), now)
	refreshRollupsNow(t, sys, "measurement_rollups")

	started := time.Now()
	mgr := newPostgresManagerWith(t, inst, Migrations)
	sys = systemDB(mgr)
	t.Logf("the migration chain ran in %v", time.Since(started))
	mat := hourlyMaterialization(t, sys)

	var materialized int64
	require.NoError(t, sys.Table(mat).Count(&materialized).Error)
	assert.Zero(t, materialized, "the migration must not fill the aggregate")
	marker := hourlyMarker(t, sys)
	assert.True(t, marker.Equal(marker.Truncate(time.Hour)), "the marker is on an hour boundary: %v", marker)
	assert.True(t, marker.After(started) && !marker.After(started.Add(time.Hour+time.Minute)),
		"the marker is the next hour boundary: %v", marker)
	job := hourlyPolicyJob(t, sys)
	assert.Equal(t, "00:01:00", job.ScheduleInterval)
	var interval string
	require.NoError(t, sys.Raw(`SELECT d.time_interval::text FROM timescaledb_information.dimensions d
 WHERE d.hypertable_schema = '_timescaledb_internal' AND d.hypertable_name = ?`, mat[len("_timescaledb_internal."):]).
		Scan(&interval).Error)
	assert.Equal(t, "7 days", interval, "the hour materialization's chunk width")

	// Stand in for the policy's first passes: the watermark moves up over the trailing window, which
	// materializes nothing before it. The 50-day-old hours are now below a watermark and absent
	// from the aggregate, which is what an unfilled history looks like.
	require.NoError(t, sys.Exec(`CALL refresh_continuous_aggregate('event-management.measurement_rollups_1h', ?::timestamptz, ?::timestamptz)`,
		now.Add(-30*24*time.Hour), now.Truncate(time.Hour).Add(-time.Hour)).Error)

	ctx := core.WithTenant(context.Background(), "acme")
	dev := "meter-1"
	start := old.Add(-time.Hour)
	criteria := MeasurementAggregationCriteria{DeviceToken: &dev, IntervalSeconds: 3600, StartTime: &start, EndTime: &now}
	api := NewApi(mgr)
	want, err := api.bucketedMeasurementsFromMinuteRollup(ctx, criteria)
	require.NoError(t, err)
	require.NotEmpty(t, want)
	got, err := api.BucketedMeasurements(ctx, criteria)
	require.NoError(t, err)
	assertSameBuckets(t, want, got)

	// Planted defect: ignore the marker. The hour level has nothing for the old history, so the
	// read must come back short; the comparison above is what catches it.
	setHourlyMarker(t, sys, time.Unix(0, 0).UTC())
	defective := NewApi(mgr)
	short, err := defective.BucketedMeasurements(ctx, criteria)
	require.NoError(t, err)
	assert.Less(t, len(short), len(want), "a read that ignores the marker must lose the unfilled history")
	setHourlyMarker(t, sys, marker)

	// Replay: the migration run again over the live aggregate changes nothing.
	require.NoError(t, NewRollup1hSchema().Migrate(mgr.Database.WithContext(core.WithSystemContext(context.Background()))))
	assert.Equal(t, mat, hourlyMaterialization(t, sys), "a replay must not recreate the aggregate")
	assert.Equal(t, job, hourlyPolicyJob(t, sys), "a replay must not replace or alter the policy job")
	assert.True(t, marker.Equal(hourlyMarker(t, sys)), "a replay must keep the first marker")
}

// TestIntegrationHourlyReadWithTheMinutePolicyStalled: the hour level is built from the minute
// level's real-time view, so it is right even when the minute level has not been refreshed.
func TestIntegrationHourlyReadWithTheMinutePolicyStalled(t *testing.T) {
	mgr := newPostgresManager(t, freshInstance(t, "ithourlystall"))
	sys := systemDB(mgr)
	// Stop both policy jobs: nothing refreshes unless the test does it.
	require.NoError(t, sys.Exec(`SELECT alter_job(job_id, scheduled => false) FROM timescaledb_information.jobs
 WHERE proc_name = 'policy_refresh_continuous_aggregate' AND hypertable_schema = 'event-management'`).Error)
	now := time.Now().UTC().Truncate(time.Minute).Add(30 * time.Second)
	hour0 := now.Truncate(time.Hour).Add(-6 * time.Hour)
	seedHourlyReadings(t, sys, "acme", hour0, now)
	refreshRollupsNow(t, sys, "measurement_rollups_1h") // the minute level is never refreshed
	setHourlyMarker(t, sys, hour0)

	api := NewApi(mgr)
	ctx := core.WithTenant(context.Background(), "acme")
	dev := "meter-1"
	criteria := MeasurementAggregationCriteria{DeviceToken: &dev, IntervalSeconds: 3600, StartTime: &hour0, EndTime: &now}
	want, err := api.bucketedMeasurementsFromMinuteRollup(ctx, criteria)
	require.NoError(t, err)
	require.NotEmpty(t, want)
	got, err := api.BucketedMeasurements(ctx, criteria)
	require.NoError(t, err)
	assertSameBuckets(t, want, got)
}

// TestIntegrationRetentionReachesTheHourlyAggregate: the reconciler gives the hour aggregate the
// same retention job as the minute one, and removes it when retention is turned off.
func TestIntegrationRetentionReachesTheHourlyAggregate(t *testing.T) {
	mgr := newPostgresManager(t, freshInstance(t, "ithourlyret"))
	sys := systemDB(mgr)
	jobs := func() map[string]string {
		var rows []struct {
			Name      string
			DropAfter string
		}
		require.NoError(t, sys.Raw(`SELECT hypertable_name AS name, config->>'drop_after' AS drop_after
 FROM timescaledb_information.jobs WHERE proc_name = 'policy_retention'
 AND hypertable_schema = 'event-management' AND hypertable_name LIKE 'measurement_rollups%'`).Scan(&rows).Error)
		out := map[string]string{}
		for _, r := range rows {
			out[r.Name] = r.DropAfter
		}
		return out
	}
	p := DataLifecyclePolicy{ChunkIntervalHours: 24, RetentionDays: 30, FailedEventRetentionDays: 7}
	require.NoError(t, ApplyDataLifecyclePolicies(context.Background(), mgr, p))
	assert.Equal(t, map[string]string{"measurement_rollups": "31 days", "measurement_rollups_1h": "31 days"}, jobs())
	p.RetentionDays = 0
	require.NoError(t, ApplyDataLifecyclePolicies(context.Background(), mgr, p))
	assert.Empty(t, jobs())
}
