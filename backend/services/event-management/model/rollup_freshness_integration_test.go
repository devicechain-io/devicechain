// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegrationRollupReadSeesAMeasurementWrittenNow pins that a bucketed read served from the
// measurement_rollups continuous aggregate includes a reading recorded a moment ago, before any
// refresh has materialized it. The aggregate is real-time (materialized_only = false), so the
// view unions the raw rows above its watermark; a board's stat card reads through exactly this
// path and must not say "no data" for the first minutes of a run.
func TestIntegrationRollupReadSeesAMeasurementWrittenNow(t *testing.T) {
	inst := freshInstance(t, "itrollupfresh")
	mgr := newPostgresManager(t, inst)
	sys := systemDB(mgr)

	var materializedOnly bool
	require.NoError(t, sys.Raw(`SELECT materialized_only FROM timescaledb_information.continuous_aggregates `+
		`WHERE view_schema = 'event-management' AND view_name = 'measurement_rollups'`).Scan(&materializedOnly).Error)
	assert.False(t, materializedOnly, "the aggregate must be real-time")

	now := time.Now().UTC()
	require.NoError(t, sys.Exec(`INSERT INTO "event-management".measurement_events
  (tenant_id, event_id, payload_id, device_token, event_type, occurred_time, name, value, unit, data_type)
  VALUES ('acme', sha256('e1'), sha256('p1'), 'meter-1', 2, ?, 'kw', 41.5, 'kW', 'DOUBLE')`, now.Add(-20*time.Second)).Error)

	api := NewApi(mgr)
	ctx := core.WithTenant(context.Background(), "acme")
	dev, name := "meter-1", "kw"
	start, end := now.Add(-time.Hour), now.Add(time.Minute)
	for _, interval := range []int64{60, 3600} {
		got, err := api.BucketedMeasurements(ctx, MeasurementAggregationCriteria{
			DeviceToken: &dev, Name: &name, IntervalSeconds: interval, StartTime: &start, EndTime: &end})
		require.NoError(t, err)
		require.Lenf(t, got, 1, "interval %ds: the reading written 20s ago is visible", interval)
		assert.EqualValues(t, 1, got[0].Count)
	}
}
