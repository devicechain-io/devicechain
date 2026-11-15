// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func rollupRetentionJobs(t *testing.T, sys *gorm.DB) []struct {
	JobID     int
	DropAfter string
} {
	t.Helper()
	var jobs []struct {
		JobID     int
		DropAfter string
	}
	require.NoError(t, sys.Raw(`SELECT job_id, config->>'drop_after' AS drop_after
 FROM timescaledb_information.jobs WHERE proc_name = 'policy_retention'
 AND hypertable_schema = 'event-management' AND hypertable_name = 'measurement_rollups'`).Scan(&jobs).Error)
	return jobs
}

func rollupMaterializationTable(t *testing.T, sys *gorm.DB) string {
	t.Helper()
	var relation struct {
		Schema string
		Name   string
	}
	require.NoError(t, sys.Raw(`SELECT materialization_hypertable_schema AS schema,
 materialization_hypertable_name AS name FROM timescaledb_information.continuous_aggregates
 WHERE view_schema = 'event-management' AND view_name = 'measurement_rollups'`).Scan(&relation).Error)
	require.NotEmpty(t, relation.Schema)
	require.NotEmpty(t, relation.Name)
	return pgx.Identifier{relation.Schema, relation.Name}.Sanitize()
}

func seedRollupRetentionReading(t *testing.T, sys *gorm.DB, id string, occurred time.Time) {
	t.Helper()
	require.NoError(t, sys.Exec(`INSERT INTO "event-management".measurement_events
 (tenant_id, event_id, payload_id, device_token, event_type, occurred_time, name, value, unit, data_type)
 VALUES ('acme', sha256(CAST(? AS text)::bytea), sha256(CAST(? AS text)::bytea),
 'meter-1', 2, ?, 'kw', 41.5, 'kW', 'DOUBLE')`, id, id+"payload", occurred).Error)
}

func refreshRetentionRollup(t *testing.T, sys *gorm.DB) {
	t.Helper()
	for attempt := 0; attempt < 5; attempt++ {
		err := sys.Exec(`CALL refresh_continuous_aggregate('event-management.measurement_rollups', NULL, NULL)`).Error
		if err == nil {
			return
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55P03" || attempt == 4 {
			require.NoError(t, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestIntegrationRetentionReachesTheRollup(t *testing.T) {
	mgr := newPostgresManager(t, freshInstance(t, "itrollupretention"))
	sys := systemDB(mgr)
	require.NoError(t, ApplyDataLifecyclePolicies(context.Background(), mgr, DataLifecyclePolicy{
		ChunkIntervalHours: 24, CompressAfterDays: 0, RetentionDays: 30, FailedEventRetentionDays: 7,
	}))
	jobs := rollupRetentionJobs(t, sys)
	require.Len(t, jobs, 1)
	assert.Equal(t, "31 days", jobs[0].DropAfter)
	now := time.Now().UTC().Truncate(time.Minute)
	seedRollupRetentionReading(t, sys, "old", now.Add(-80*24*time.Hour))
	seedRollupRetentionReading(t, sys, "recent", now.Add(-time.Hour))
	refreshRetentionRollup(t, sys)
	table := rollupMaterializationTable(t, sys)
	// Inspect storage directly: the real-time view can union raw rows above its watermark.
	var oldBefore int64
	require.NoError(t, sys.Table(table).Where("bucket < ?", now.Add(-41*24*time.Hour)).Count(&oldBefore).Error)
	require.Positive(t, oldBefore, "refresh must materialize old data before retention runs")
	require.NoError(t, sys.Exec(`CALL run_job(?)`, jobs[0].JobID).Error)
	var old, recent int64
	require.NoError(t, sys.Table(table).Where("bucket < ?", now.Add(-41*24*time.Hour)).Count(&old).Error)
	assert.Zero(t, old, "31-day window plus at most one 10-day materialization chunk")
	require.NoError(t, sys.Table(table).Where("bucket = ?", now.Add(-time.Hour)).Count(&recent).Error)
	assert.EqualValues(t, 1, recent)
}

func TestIntegrationRetentionOffLeavesTheRollup(t *testing.T) {
	mgr := newPostgresManager(t, freshInstance(t, "itrollupretentionoff"))
	sys := systemDB(mgr)
	p := DataLifecyclePolicy{ChunkIntervalHours: 24, RetentionDays: 30, FailedEventRetentionDays: 7}
	require.NoError(t, ApplyDataLifecyclePolicies(context.Background(), mgr, p))
	require.Len(t, rollupRetentionJobs(t, sys), 1)
	p.RetentionDays = 0
	require.NoError(t, ApplyDataLifecyclePolicies(context.Background(), mgr, p))
	require.Empty(t, rollupRetentionJobs(t, sys))
	occurred := time.Now().UTC().Truncate(time.Minute).Add(-80 * 24 * time.Hour)
	seedRollupRetentionReading(t, sys, "old", occurred)
	refreshRetentionRollup(t, sys)
	var count int64
	require.NoError(t, sys.Table(rollupMaterializationTable(t, sys)).Where("bucket = ?", occurred).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestIntegrationRollupReadFloorHidesExpiredBuckets(t *testing.T) {
	mgr := newPostgresManager(t, freshInstance(t, "itrollupreadfloor"))
	sys := systemDB(mgr)
	require.Empty(t, rollupRetentionJobs(t, sys))
	now := time.Now().UTC().Truncate(time.Minute)
	seedRollupRetentionReading(t, sys, "expired", now.Add(-40*24*time.Hour))
	refreshRetentionRollup(t, sys)
	var count int64
	require.NoError(t, sys.Table(rollupMaterializationTable(t, sys)).Where("bucket = ?", now.Add(-40*24*time.Hour)).Count(&count).Error)
	require.EqualValues(t, 1, count)
	api := NewApi(mgr)
	api.now = func() time.Time { return now }
	dev, name := "meter-1", "kw"
	start := now.Add(-45 * 24 * time.Hour)
	criteria := MeasurementAggregationCriteria{DeviceToken: &dev, Name: &name,
		IntervalSeconds: 3600, StartTime: &start, EndTime: &now}
	ctx := core.WithTenant(context.Background(), "acme")
	// No anchor filter and a multiple-of-60 interval select the rollup path; an hourly
	// interval keeps 45 days under the bucket ceiling.
	api.RollupRetentionDays = 31
	buckets, err := api.BucketedMeasurements(ctx, criteria)
	require.NoError(t, err)
	assert.Empty(t, buckets)
	api.RollupRetentionDays = 0
	buckets, err = api.BucketedMeasurements(ctx, criteria)
	require.NoError(t, err)
	require.Len(t, buckets, 1)
}
