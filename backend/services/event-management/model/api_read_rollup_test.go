// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// rollupBucketSeconds is the single source of truth for the base bucket width — the
// migration derives its time_bucket interval from this same constant, so the routing
// math and the aggregate can no longer drift. This pins the value the routing logic
// (whole-multiple-of-60 eligibility) is built around.
func TestRollupBucketSeconds(t *testing.T) {
	assert.Equal(t, int64(60), int64(rollupBucketSeconds))
}

func TestUseMeasurementRollup(t *testing.T) {
	anchorType := "customer"
	anchorToken := "acme"

	cases := []struct {
		name     string
		criteria MeasurementAggregationCriteria
		want     bool
	}{
		{"exactly one base bucket", MeasurementAggregationCriteria{IntervalSeconds: 60}, true},
		{"whole multiple (5 min)", MeasurementAggregationCriteria{IntervalSeconds: 300}, true},
		{"whole multiple (1 hour)", MeasurementAggregationCriteria{IntervalSeconds: 3600}, true},
		{"sub-minute falls back to raw", MeasurementAggregationCriteria{IntervalSeconds: 30}, false},
		{"non-multiple falls back to raw", MeasurementAggregationCriteria{IntervalSeconds: 90}, false},
		{"just under a base bucket", MeasurementAggregationCriteria{IntervalSeconds: 59}, false},
		{
			"anchor filter forces raw even at a whole multiple",
			MeasurementAggregationCriteria{IntervalSeconds: 300, AnchorType: &anchorType, AnchorToken: &anchorToken},
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, useMeasurementRollup(tc.criteria))
		})
	}
}
