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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A STRING reading is read back as text, and a bucketed read treats it as what it is: a named
// request for it is refused, an every-name request leaves it out, and the numeric measurement
// beside it aggregates as before. Both the rollup (60 s) and the raw (30 s) paths, because the
// rule lives above the choice between them.
func TestIntegrationTextReadingsAreReadBackButNeverAggregated(t *testing.T) {
	inst := freshInstance(t, "ittextread")
	mgr := newPostgresManager(t, inst)
	sys := systemDB(mgr)

	now := time.Now().UTC()
	at := now.Add(-20 * time.Second)
	require.NoError(t, sys.Exec(`INSERT INTO "event-management".measurement_events
  (tenant_id, event_id, payload_id, device_token, event_type, occurred_time, name, value, unit, data_type, text_value)
  VALUES
  ('acme', sha256('e1'), sha256('p1'), 'meter-1', 2, ?, 'kw', 41.5, 'kW', 'DOUBLE', NULL),
  ('acme', sha256('e1'), sha256('p2'), 'meter-1', 2, ?, 'mode', NULL, NULL, 'STRING', 'AUTO')`, at, at).Error)

	api := NewApi(mgr)
	ctx := core.WithTenant(context.Background(), "acme")
	dev := "meter-1"
	start, end := now.Add(-time.Hour), now.Add(time.Minute)

	rows, err := api.MeasurementEvents(ctx, EventSearchCriteria{DeviceToken: &dev})
	require.NoError(t, err)
	byName := map[string]MeasurementEvent{}
	for _, r := range rows.Results {
		byName[r.Name] = r
	}
	require.Len(t, byName, 2)
	require.NotNil(t, byName["mode"].TextValue)
	assert.Equal(t, "AUTO", *byName["mode"].TextValue)
	assert.False(t, byName["mode"].Value.Valid)
	assert.Nil(t, byName["kw"].TextValue)
	assert.True(t, byName["kw"].Value.Valid)

	for _, interval := range []int64{60, 30} {
		mode, kw := "mode", "kw"
		_, err := api.BucketedMeasurements(ctx, MeasurementAggregationCriteria{
			DeviceToken: &dev, Name: &mode, IntervalSeconds: interval, StartTime: &start, EndTime: &end})
		var na *NotAggregatableError
		require.Truef(t, errors.As(err, &na), "interval %ds: named text measurement: %v", interval, err)
		assert.Equal(t, "mode", na.Name)

		all, err := api.BucketedMeasurements(ctx, MeasurementAggregationCriteria{
			DeviceToken: &dev, IntervalSeconds: interval, StartTime: &start, EndTime: &end})
		require.NoError(t, err)
		require.Lenf(t, all, 1, "interval %ds: only the numeric measurement aggregates", interval)
		assert.Equal(t, "kw", all[0].Name)
		assert.EqualValues(t, 1, all[0].Count)

		one, err := api.BucketedMeasurements(ctx, MeasurementAggregationCriteria{
			DeviceToken: &dev, Name: &kw, IntervalSeconds: interval, StartTime: &start, EndTime: &end})
		require.NoError(t, err)
		require.Len(t, one, 1)
	}
}
