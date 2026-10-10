// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newRowBoundTestApi is an in-memory database holding both sources an aggregation reads
// from, with tenant scoping registered as in production. SQLite has no time_bucket, so a
// read that passes every bound fails at the aggregation itself; the tests below assert on
// the refusal (or its absence), which is decided before that query runs.
func newRowBoundTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&MeasurementEvent{}, &MeasurementRollup{}))
	return NewApi(&rdb.RdbManager{Database: db})
}

// seedNames writes one raw measurement per name for device d1 at the given instant.
func seedNames(t *testing.T, api *Api, tenant string, at time.Time, names ...string) {
	t.Helper()
	ctx := core.WithTenant(context.Background(), tenant)
	for i, n := range names {
		row := &MeasurementEvent{
			EventId: []byte(fmt.Sprintf("e-%s-%d-%d", n, i, at.UnixNano())), PayloadId: []byte(fmt.Sprintf("p-%s-%d-%d", n, i, at.UnixNano())),
			DeviceToken: "d1", EventType: esmodel.Measurement, OccurredTime: at, Name: n,
			Value: sql.NullFloat64{Float64: 1, Valid: true},
		}
		require.NoError(t, api.RDB.DB(ctx).Create(row).Error)
	}
}

func seedRollupNames(t *testing.T, api *Api, tenant string, at time.Time, names ...string) {
	t.Helper()
	ctx := core.WithTenant(context.Background(), tenant)
	for _, n := range names {
		row := &MeasurementRollup{DeviceToken: "d1", EventType: esmodel.Measurement, Name: n, Bucket: at, CountValue: 1}
		require.NoError(t, api.RDB.DB(ctx).Create(row).Error)
	}
}

func names(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("m%02d", i)
	}
	return out
}

// rowRead is an aggregation over the last `buckets` buckets of `interval` seconds.
func rowRead(buckets, interval int64, name *string) MeasurementAggregationCriteria {
	start := time.Now().Add(-time.Duration(buckets*interval) * time.Second)
	dev := "d1"
	return MeasurementAggregationCriteria{DeviceToken: &dev, Name: name, StartTime: &start, IntervalSeconds: interval}
}

// 9,000 one-second buckets is under the bucket cap, but with six measurement names in
// range it is 54,000 rows: refused, and the refusal reports the row count it computed.
func TestBucketedMeasurementsRefusesTooManyRowsWithNoNameFilter(t *testing.T) {
	api := newRowBoundTestApi(t)
	seedNames(t, api, "A", time.Now().Add(-time.Minute), names(6)...)
	ctx := core.WithTenant(context.Background(), "A")

	_, err := api.BucketedMeasurements(ctx, rowRead(9000, 1, nil))
	le, ok := limit.As(err)
	require.True(t, ok, "want a limit refusal, got %v", err)
	assert.Equal(t, "rows", le.What)
	assert.Equal(t, MaxMeasurementRows, le.Max)
	assert.InDelta(t, 9000*6, le.Got, 6, "Got is buckets x distinct names")
}

// Five names in range is 45,000 rows: not refused.
func TestBucketedMeasurementsAllowsRowsUnderTheCap(t *testing.T) {
	api := newRowBoundTestApi(t)
	seedNames(t, api, "A", time.Now().Add(-time.Minute), names(5)...)
	ctx := core.WithTenant(context.Background(), "A")

	_, err := api.BucketedMeasurements(ctx, rowRead(9000, 1, nil))
	_, refused := limit.As(err)
	assert.False(t, refused, "45,000 rows is under the cap, got %v", err)
}

// A name filter is one name, whatever else is stored: 9,000 buckets x 1 is allowed.
func TestBucketedMeasurementsNameFilterCountsOneName(t *testing.T) {
	api := newRowBoundTestApi(t)
	seedNames(t, api, "A", time.Now().Add(-time.Minute), names(20)...)
	ctx := core.WithTenant(context.Background(), "A")

	n := "m00"
	_, err := api.BucketedMeasurements(ctx, rowRead(9000, 1, &n))
	_, refused := limit.As(err)
	assert.False(t, refused, "a single named measurement is one name, got %v", err)
}

// Names outside the range, on another device, or in another tenant do not count.
func TestBucketedMeasurementsCountsOnlyNamesInScope(t *testing.T) {
	api := newRowBoundTestApi(t)
	seedNames(t, api, "A", time.Now().Add(-time.Minute), names(5)...)
	seedNames(t, api, "A", time.Now().Add(-48*time.Hour), "old1", "old2")
	seedNames(t, api, "B", time.Now().Add(-time.Minute), "b1", "b2")
	ctx := core.WithTenant(context.Background(), "A")
	other := &MeasurementEvent{EventId: []byte("o"), PayloadId: []byte("o"), DeviceToken: "d2",
		EventType: esmodel.Measurement, OccurredTime: time.Now().Add(-time.Minute), Name: "other"}
	require.NoError(t, api.RDB.DB(ctx).Create(other).Error)

	criteria, _, err := boundBucketedRange(rowRead(9000, 1, nil), time.Now())
	require.NoError(t, err)
	n, err := api.distinctMeasurementNames(ctx, criteria, false)
	require.NoError(t, err)
	assert.Equal(t, int64(5), n)

	_, err = api.BucketedMeasurements(ctx, rowRead(9000, 1, nil))
	_, refused := limit.As(err)
	assert.False(t, refused, "only the 5 in-scope names count, got %v", err)
}

// A rollup-eligible read counts the names in the source it will read: six names in the
// rollup and none in the raw table is refused at 9,000 one-minute buckets.
func TestBucketedMeasurementsCountsNamesInTheRollupItReads(t *testing.T) {
	api := newRowBoundTestApi(t)
	seedRollupNames(t, api, "A", time.Now().Add(-time.Hour).Truncate(time.Minute), names(6)...)
	ctx := core.WithTenant(context.Background(), "A")

	_, err := api.BucketedMeasurements(ctx, rowRead(9000, 60, nil))
	le, ok := limit.As(err)
	require.True(t, ok, "want a limit refusal, got %v", err)
	assert.Equal(t, "rows", le.What)
}
