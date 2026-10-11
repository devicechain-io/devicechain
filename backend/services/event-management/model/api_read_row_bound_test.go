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

// rowRead is an aggregation over exactly `buckets` buckets of `interval` seconds, ending
// now. endTime is explicit so the bucket count is exact, not one more for the time that
// passes before the read computes it.
func rowRead(buckets, interval int64, name *string) MeasurementAggregationCriteria {
	end := time.Now()
	start := end.Add(-time.Duration(buckets*interval) * time.Second)
	dev := "d1"
	return MeasurementAggregationCriteria{DeviceToken: &dev, Name: name, StartTime: &start, EndTime: &end, IntervalSeconds: interval}
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
	assert.Equal(t, 9000*6, le.Got, "Got is buckets x distinct names")
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
	n, err := api.distinctMeasurementNames(ctx, criteria, false, 100)
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

// The cap is inclusive: 10,000 buckets x 5 names is exactly 50,000 rows and is allowed;
// 7,143 buckets x 7 names is 50,001 rows and is refused.
func TestBucketedMeasurementsRowCapIsExact(t *testing.T) {
	api := newRowBoundTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")

	seedNames(t, api, "A", time.Now().Add(-time.Minute), names(5)...)
	_, err := api.BucketedMeasurements(ctx, rowRead(MaxMeasurementBuckets, 1, nil))
	_, refused := limit.As(err)
	assert.False(t, refused, "50,000 rows is at the cap, not over it: got %v", err)

	seedNames(t, api, "A", time.Now().Add(-time.Minute), "m05", "m06")
	_, err = api.BucketedMeasurements(ctx, rowRead(7143, 1, nil))
	le, ok := limit.As(err)
	require.True(t, ok, "50,001 rows must be refused, got %v", err)
	assert.Equal(t, "rows", le.What)
	assert.Equal(t, 50_001, le.Got)
}

// The count stops one name past what the cap allows: far more names than that are
// refused all the same, and the count reads no further.
func TestDistinctMeasurementNamesStopsAtEnough(t *testing.T) {
	api := newRowBoundTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	seedNames(t, api, "A", time.Now().Add(-time.Minute), names(12)...)

	criteria, _, err := boundBucketedRange(rowRead(MaxMeasurementBuckets, 1, nil), time.Now())
	require.NoError(t, err)
	n, err := api.distinctMeasurementNames(ctx, criteria, false, 6)
	require.NoError(t, err)
	assert.Equal(t, int64(6), n)

	_, err = api.BucketedMeasurements(ctx, rowRead(MaxMeasurementBuckets, 1, nil))
	le, ok := limit.As(err)
	require.True(t, ok, "12 names at 10,000 buckets must be refused, got %v", err)
	assert.Equal(t, MaxMeasurementBuckets*6, le.Got, "the count stopped at 6 names")
}

// A zero-length range is zero buckets by arithmetic but one bucket of rows: it must not
// multiply the name count away.
func TestBoundBucketedRowsCountsAZeroLengthRangeAsOneBucket(t *testing.T) {
	_, refused := limit.As(boundBucketedRows(0, MaxMeasurementRows+1))
	assert.True(t, refused)
	assert.NoError(t, boundBucketedRows(0, MaxMeasurementRows))
}

// If the name count fails, the read fails with it: a failed count is never read as zero
// names, which would let the read through unbounded.
func TestBucketedMeasurementsFailsClosedWhenTheNameCountFails(t *testing.T) {
	api := newRowBoundTestApi(t)
	require.NoError(t, api.RDB.Database.Migrator().DropTable(&MeasurementRollup{}))
	ctx := core.WithTenant(context.Background(), "A")

	criteria, _, err := boundBucketedRange(rowRead(10, 60, nil), time.Now())
	require.NoError(t, err)
	n, err := api.distinctMeasurementNames(ctx, criteria, true, 100)
	require.Error(t, err)
	assert.Zero(t, n)

	// rollup-eligible (60 s interval), so the count reads the dropped table; the error
	// must be the count's, i.e. the aggregation was never reached.
	_, err = api.BucketedMeasurements(ctx, rowRead(10, 60, nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "counting measurement names")
}

// The rollup scope counts only names in its own range, device and tenant: a rollup
// bucket before the (floored) start is not counted.
func TestDistinctMeasurementNamesRollupScope(t *testing.T) {
	api := newRowBoundTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	inRange := time.Now().Add(-time.Hour).Truncate(time.Minute)
	seedRollupNames(t, api, "A", inRange, "r1", "r2", "r3")
	seedRollupNames(t, api, "A", time.Now().Add(-72*time.Hour).Truncate(time.Minute), "old1", "old2")
	seedRollupNames(t, api, "B", inRange, "b1", "b2")
	other := &MeasurementRollup{DeviceToken: "d2", EventType: esmodel.Measurement, Name: "other", Bucket: inRange, CountValue: 1}
	require.NoError(t, api.RDB.DB(ctx).Create(other).Error)

	criteria, _, err := boundBucketedRange(rowRead(24*60, 60, nil), time.Now())
	require.NoError(t, err)
	n, err := api.distinctMeasurementNames(ctx, criteria, true, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(3), n)
}

// The raw scope applies the eventTypes filter: names under another event type are not
// counted.
func TestDistinctMeasurementNamesRawEventTypes(t *testing.T) {
	api := newRowBoundTestApi(t)
	ctx := core.WithTenant(context.Background(), "A")
	at := time.Now().Add(-time.Minute)
	seedNames(t, api, "A", at, "m1", "m2")
	other := &MeasurementEvent{EventId: []byte("x"), PayloadId: []byte("x"), DeviceToken: "d1",
		EventType: esmodel.Alert, OccurredTime: at, Name: "elsewhere"}
	require.NoError(t, api.RDB.DB(ctx).Create(other).Error)

	criteria, _, err := boundBucketedRange(rowRead(3600, 1, nil), time.Now())
	require.NoError(t, err)
	criteria.EventTypes = []esmodel.EventType{esmodel.Measurement}
	n, err := api.distinctMeasurementNames(ctx, criteria, false, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	criteria.EventTypes = nil
	n, err = api.distinctMeasurementNames(ctx, criteria, false, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(3), n, "the control: without the filter the third name counts")
}
