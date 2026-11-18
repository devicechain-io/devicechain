// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"math"
	"strings"
	"testing"
	"time"
)

// Check milliseconds before converting to nanoseconds: oversized values can wrap
// negative during conversion and bypass a duration-based upper bound.
func TestFetchHoldBudgetRejectsOverflowBeforeConversion(t *testing.T) {
	const window = 60 * time.Second
	for _, tc := range []struct {
		name string
		ms   int64
	}{
		{"negative", -1},
		{"above limit", 6001},
		{"first overflowing millisecond", math.MaxInt64/int64(time.Millisecond) + 1},
		{"maximum int64", math.MaxInt64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if int64(int(tc.ms)) != tc.ms {
				t.Skip("value does not fit the platform's int configuration field")
			}
			cfg := NatsFetchConfiguration{AheadHoldBudgetMillis: int(tc.ms)}
			err := cfg.ValidateAgainst(window)
			if err == nil || !strings.Contains(err.Error(), "infrastructure.nats.fetch.aheadHoldBudgetMillis") {
				t.Fatalf("%d ms: got %v, want a refusal naming the key", tc.ms, err)
			}
		})
	}
}

func TestFetchHoldBudgetAcceptsLimitAndDefault(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ms     int
		window time.Duration
		want   time.Duration
	}{
		{"limit", 6000, 60 * time.Second, 6 * time.Second},
		{"default", 0, 60 * time.Second, time.Second},
		{"capped default", 0, 3 * time.Second, 300 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := NatsFetchConfiguration{AheadHoldBudgetMillis: tc.ms}
			if err := cfg.ValidateAgainst(tc.window); err != nil {
				t.Fatalf("%d ms refused: %v", tc.ms, err)
			}
			if got := cfg.HoldBudget(tc.window); got != tc.want {
				t.Fatalf("%d ms reads as %s, want %s", tc.ms, got, tc.want)
			}
		})
	}
}
