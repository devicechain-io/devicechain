// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
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
			assert.Equal(t, tc.want, useMeasurementRollup(context.Background(), tc.criteria))
		})
	}
}

// A scoped caller is never served from the rollup, at any interval: the aggregate has no
// anchor dimension, so it cannot hold a member to the readings recorded while the device
// belonged to a customer in its scope. The unscoped control is the same interval, which
// the rollup does serve.
func TestAScopedCallerNeverUsesTheRollup(t *testing.T) {
	criteria := MeasurementAggregationCriteria{IntervalSeconds: 300}
	unscoped := auth.WithClaims(context.Background(), &auth.Claims{TokenType: auth.TokenTypeAccess})
	assert.True(t, useMeasurementRollup(unscoped, criteria), "control: an unscoped caller uses the rollup")

	scoped := auth.WithClaims(context.Background(), &auth.Claims{
		TokenType: auth.TokenTypeAccess, ScopeAnchors: []string{"customer:acme"}})
	assert.False(t, useMeasurementRollup(scoped, criteria), "a scoped caller must take the raw path")
}

// The same for a scoped read of a shared AREA: the anchor-aware raw path serves it, and the
// rollup stays off the table for the scoped caller at every interval.
func TestAScopedAreaAnchoredReadStaysOffTheRollup(t *testing.T) {
	area, floor := "area", "floor-shared"
	scoped := auth.WithClaims(context.Background(), &auth.Claims{
		TokenType: auth.TokenTypeAccess, ScopeAnchors: []string{"customer:acme"}})
	for _, interval := range []int64{60, 300, 3600} {
		criteria := MeasurementAggregationCriteria{IntervalSeconds: interval, AnchorType: &area, AnchorToken: &floor}
		assert.False(t, useMeasurementRollup(scoped, criteria), "interval %d", interval)
	}
}
