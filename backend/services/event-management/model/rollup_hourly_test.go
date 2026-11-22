// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/schema"
)

func TestHourlySpanOf(t *testing.T) {
	h0 := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
	at := func(h, m, s int) *time.Time {
		v := h0.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(s)*time.Second)
		return &v
	}
	cases := []struct {
		name       string
		start, end *time.Time
		interval   int64
		floor      *time.Time
		from, to   *time.Time // nil: no hour-level share
	}{
		{"aligned", at(0, 0, 0), at(5, 59, 0), 3600, nil, at(0, 0, 0), at(6, 0, 0)},
		{"end one minute short of an hour closing", at(0, 0, 0), at(5, 58, 0), 3600, nil, at(0, 0, 0), at(5, 0, 0)},
		{"end mid-minute floors to the minute", at(0, 0, 0), at(5, 59, 59), 3600, nil, at(0, 0, 0), at(6, 0, 0)},
		{"start mid-hour starts at the next hour", at(0, 17, 0), at(5, 59, 0), 3600, nil, at(1, 0, 0), at(6, 0, 0)},
		{"start in the last second of an hour", at(0, 59, 59), at(5, 59, 0), 3600, nil, at(1, 0, 0), at(6, 0, 0)},
		{"inside one hour", at(3, 5, 0), at(3, 50, 0), 3600, nil, nil, nil},
		{"one whole hour", at(3, 0, 0), at(3, 59, 0), 3600, nil, at(3, 0, 0), at(4, 0, 0)},
		{"an hour minus a minute", at(3, 0, 0), at(3, 58, 0), 3600, nil, nil, nil},
		{"retention floor moves the start", at(0, 0, 0), at(9, 59, 0), 3600, at(2, 30, 0), at(3, 0, 0), at(10, 0, 0)},
		{"retention floor older than the start changes nothing", at(4, 0, 0), at(9, 59, 0), 3600, at(2, 30, 0), at(4, 0, 0), at(10, 0, 0)},
		{"not whole hours", at(0, 0, 0), at(9, 59, 0), 5400, nil, nil, nil},
		{"minute interval stays on the minute level", at(0, 0, 0), at(9, 59, 0), 300, nil, nil, nil},
		{"two hours", at(0, 0, 0), at(9, 59, 0), 7200, nil, at(0, 0, 0), at(10, 0, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := MeasurementAggregationCriteria{IntervalSeconds: tc.interval, StartTime: tc.start, EndTime: tc.end}
			var floor time.Time
			if tc.floor != nil {
				floor = *tc.floor
			}
			span, ok := hourlySpanOf(c, floor, tc.floor != nil)
			if tc.from == nil {
				assert.False(t, ok)
				return
			}
			require.True(t, ok)
			assert.Equal(t, *tc.from, span.from)
			assert.Equal(t, *tc.to, span.to)
		})
	}
	_, ok := hourlySpanOf(MeasurementAggregationCriteria{IntervalSeconds: 3600}, time.Time{}, false)
	assert.False(t, ok, "an unbounded read has no hour span")
}

func TestMeasurementRollupHourlyTableName(t *testing.T) {
	namer := schema.NamingStrategy{TablePrefix: "event-management."}
	assert.Equal(t, "event-management.measurement_rollups", namer.TableName("MeasurementRollup"), "control: the namer prefixes the minute level")
	assert.Equal(t, "event-management.measurement_rollups_1h", MeasurementRollupHourly{}.TableName(namer))
}

func TestMergeRollupPartialsAddsSharedBuckets(t *testing.T) {
	t0 := time.Date(2026, 11, 22, 4, 0, 0, 0, time.UTC)
	nf := func(v float64) sql.NullFloat64 { return sql.NullFloat64{Float64: v, Valid: true} }
	middle := []rollupPartial{{BucketStart: t0, Name: "kw", SumValue: nf(10), MinValue: nf(2), MaxValue: nf(8), CountValue: 4}}
	edges := []rollupPartial{
		{BucketStart: t0, Name: "kw", SumValue: nf(5), MinValue: nf(1), MaxValue: nf(4), CountValue: 2},
		{BucketStart: t0.Add(2 * time.Hour), Name: "kw", MinValue: nf(7)},
	}
	got := derivePartialBuckets(mergeRollupPartials(middle, edges))
	require.Len(t, got, 2)
	assert.Equal(t, int64(6), got[0].Count)
	assert.Equal(t, nf(15), got[0].Sum)
	assert.Equal(t, nf(1), got[0].Min)
	assert.Equal(t, nf(8), got[0].Max)
	assert.Equal(t, nf(2.5), got[0].Avg)
	assert.False(t, got[1].Avg.Valid, "nothing counted: avg is NULL, as the minute query's NULLIF gives")
	assert.False(t, got[1].Sum.Valid)
}
