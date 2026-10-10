// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var boundNow = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func ptr(t time.Time) *time.Time { return &t }

// A one-second interval over a day is 86,400 buckets: refused with LIMIT_EXCEEDED,
// before the database is touched (the Api here has no database at all).
func TestBucketedMeasurementsRefusesAnOverBroadRead(t *testing.T) {
	api := &Api{}
	_, err := api.BucketedMeasurements(context.Background(), MeasurementAggregationCriteria{
		IntervalSeconds: 1,
		StartTime:       ptr(time.Now().Add(-24 * time.Hour)),
	})
	le, ok := limit.As(err)
	require.True(t, ok, "want a limit refusal, got %v", err)
	assert.Equal(t, MaxMeasurementBuckets, le.Max)
	assert.InDelta(t, 86400, le.Got, 5)
}

func TestBucketedMeasurementsRequiresAStartTime(t *testing.T) {
	api := &Api{}
	_, err := api.BucketedMeasurements(context.Background(), MeasurementAggregationCriteria{IntervalSeconds: 3600})
	class, ok := integrity.Refused(err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, integrity.ClassInvalid, class)
}

func TestBoundBucketedRange(t *testing.T) {
	cases := []struct {
		name     string
		start    time.Time
		end      *time.Time
		interval int64
		wantErr  string // "", "limit", "invalid"
	}{
		{"exactly the cap", boundNow.Add(-MaxMeasurementBuckets * time.Second), ptr(boundNow), 1, ""},
		{"one second over the cap", boundNow.Add(-(MaxMeasurementBuckets + 1) * time.Second), ptr(boundNow), 1, "limit"},
		{"a partial bucket counts as a bucket", boundNow.Add(-(MaxMeasurementBuckets*60 + 1) * time.Second), ptr(boundNow), 60, "limit"},
		{"a year hourly", boundNow.Add(-365 * 24 * time.Hour), ptr(boundNow), 3600, ""},
		{"a year per minute", boundNow.Add(-365 * 24 * time.Hour), ptr(boundNow), 60, "limit"},
		{"end defaults to now", boundNow.Add(-time.Hour), nil, 60, ""},
		{"end before start", boundNow, ptr(boundNow.Add(-time.Hour)), 60, "invalid"},
		{"interval below 1", boundNow.Add(-time.Hour), nil, 0, "invalid"},
		{"huge interval", boundNow.Add(-time.Hour), nil, 1 << 62, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := boundBucketedRange(MeasurementAggregationCriteria{
				IntervalSeconds: tc.interval, StartTime: ptr(tc.start), EndTime: tc.end,
			}, boundNow)
			switch tc.wantErr {
			case "":
				require.NoError(t, err)
				require.NotNil(t, got.EndTime)
				if tc.end == nil {
					assert.Equal(t, boundNow, *got.EndTime)
				}
			case "limit":
				_, ok := limit.As(err)
				assert.True(t, ok, "got %v", err)
			case "invalid":
				_, ok := integrity.Refused(err)
				assert.True(t, ok, "got %v", err)
			}
		})
	}
}
