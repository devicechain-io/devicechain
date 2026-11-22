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
			_, span := api.hourlySpan(criteria)
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

// TestIntegrationHourlyAggregateMaterializesExistingHistory upgrades a store that predates the
// aggregate and already holds readings, and requires the history to be readable from the hour
// level without waiting for a policy pass; then it replays the migration over the live
// aggregate and requires the aggregate, its policy and its materialization to be left alone.
func TestIntegrationHourlyAggregateMaterializesExistingHistory(t *testing.T) {
	inst := freshInstance(t, "ithourlyup")
	id := NewRollup1hSchema().ID
	before := newPostgresManagerWith(t, inst, migrationsBefore(t, id))
	sys := systemDB(before)
	var existing int64
	require.NoError(t, sys.Raw(`SELECT count(*) FROM timescaledb_information.continuous_aggregates
 WHERE view_name = 'measurement_rollups_1h'`).Scan(&existing).Error)
	require.Zero(t, existing, "the chain before the migration must not hold the aggregate")

	now := time.Now().UTC().Truncate(time.Minute)
	start := now.Truncate(time.Hour).Add(-50 * 24 * time.Hour) // older than the policy's 30-day window
	seedHourlyReadings(t, sys, "acme", start, start.Add(5*time.Hour))
	refreshRollupsNow(t, sys, "measurement_rollups")

	mgr := newPostgresManagerWith(t, inst, Migrations)
	sys = systemDB(mgr)
	mat := hourlyMaterialization(t, sys)
	var old int64
	require.NoError(t, sys.Table(mat).Where("bucket < ?", now.Add(-40*24*time.Hour)).Count(&old).Error)
	require.EqualValues(t, 4*5, old, "five hours of four names, materialized by the migration itself")
	require.Len(t, hourlyRefreshPolicies(t, sys), 1)
	var realTime bool
	require.NoError(t, sys.Raw(`SELECT NOT materialized_only FROM timescaledb_information.continuous_aggregates
 WHERE view_name = 'measurement_rollups_1h'`).Scan(&realTime).Error)
	assert.True(t, realTime, "the hour aggregate must be real-time")

	// Replay: the migration run again over the live aggregate changes nothing.
	var matName string
	var jobID int64
	snapshot := func() {
		matName = hourlyMaterialization(t, sys)
		require.NoError(t, sys.Raw(`SELECT job_id FROM timescaledb_information.jobs
 WHERE hypertable_schema = 'event-management' AND hypertable_name = 'measurement_rollups_1h'
 AND proc_name = 'policy_refresh_continuous_aggregate'`).Scan(&jobID).Error)
	}
	snapshot()
	firstMat, firstJob := matName, jobID
	require.NoError(t, NewRollup1hSchema().Migrate(mgr.Database.WithContext(core.WithSystemContext(context.Background()))))
	snapshot()
	assert.Equal(t, firstMat, matName, "a replay must not recreate the aggregate")
	assert.Equal(t, firstJob, jobID, "a replay must not replace the policy")
	var after int64
	require.NoError(t, sys.Table(mat).Where("bucket < ?", now.Add(-40*24*time.Hour)).Count(&after).Error)
	assert.EqualValues(t, old, after)
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
