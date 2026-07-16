// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/stretchr/testify/require"
)

// TestTierConfigKeysCoverEveryDimension pins that the registry is DERIVED from the
// governance dimensions rather than hand-listed. A fourth dimension must get tier
// support the day it is declared — if this registry were a restated list, it would
// silently stop covering the platform and that dimension's tier setting would be
// rejected as "unknown".
func TestTierConfigKeysCoverEveryDimension(t *testing.T) {
	dims := governance.AllDimensions()
	require.NotEmpty(t, dims)

	for _, d := range dims {
		require.NoError(t, ValidateTierConfig(map[string]any{d.RateField: float64(10)}),
			"rate key for dimension %q must be registered", d.Name)
		require.NoError(t, ValidateTierConfig(map[string]any{d.BurstField: float64(10)}),
			"burst key for dimension %q must be registered", d.Name)
	}
	require.Len(t, TierConfigKeys(), len(dims)*2)
}

// TestValidateTierConfigRejectsUnknownKeys is the reason the registry exists
// (ADR-065 decision 8). An unvalidated blob fails OPEN: a typo is accepted, read by
// nobody, and does nothing — the operator believes they configured a ceiling and the
// tenant quietly keeps the platform default. Rejecting at write is what makes that
// impossible.
func TestValidateTierConfigRejectsUnknownKeys(t *testing.T) {
	// A plausible typo of a real key — the case that matters, since it is the one
	// an operator will actually produce.
	err := ValidateTierConfig(map[string]any{"ingestMessagesPerSec": float64(500)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown tier setting")
	// The error names the real keys: the operator needs the right spelling.
	require.Contains(t, err.Error(), "ingestMessagesPerSecond")

	// A key from a subsystem that has not registered one yet is equally unknown —
	// no key gets in ahead of its validator.
	require.Error(t, ValidateTierConfig(map[string]any{"shedPriority": float64(80)}))

	// An empty or nil config is valid: a tier that declares nothing inherits the
	// platform default everywhere, which is exactly what the standard tier does.
	require.NoError(t, ValidateTierConfig(nil))
	require.NoError(t, ValidateTierConfig(map[string]any{}))
}

// TestValidateTierConfigRejectsUnusableValues holds a tier setting to the same bar
// as a per-tenant override: a ceiling must be a positive number. A zero ceiling is
// not a limit, it is an outage for every tenant at the tier.
func TestValidateTierConfigRejectsUnusableValues(t *testing.T) {
	bad := []struct {
		name string
		cfg  map[string]any
	}{
		{"zero rate", map[string]any{"ingestMessagesPerSecond": float64(0)}},
		{"negative rate", map[string]any{"ingestMessagesPerSecond": float64(-1)}},
		{"string rate", map[string]any{"ingestMessagesPerSecond": "fast"}},
		{"bool rate", map[string]any{"ingestMessagesPerSecond": true}},
		{"zero burst", map[string]any{"ingestBurst": float64(0)}},
		{"negative burst", map[string]any{"ingestBurst": float64(-5)}},
		{"fractional burst", map[string]any{"ingestBurst": float64(1.5)}},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			require.Error(t, ValidateTierConfig(c.cfg))
		})
	}

	// A fractional RATE is legal — 0.5/s is one call every two seconds.
	require.NoError(t, ValidateTierConfig(map[string]any{"outboundMessagesPerSecond": float64(0.5)}))
}

// TestTierRateAndBurstReads pins the typed read path, including the defensive
// direction: an unusable value that reached the row out of band reads as "inherit",
// never as a live ceiling of zero (which would admit nothing).
func TestTierRateAndBurstReads(t *testing.T) {
	tier := &TenantTier{Config: map[string]any{
		"ingestMessagesPerSecond": float64(2000),
		"ingestBurst":             float64(4000),
	}}
	require.Equal(t, float64(2000), *tier.RateFor(governance.Ingest))
	require.Equal(t, 4000, *tier.BurstFor(governance.Ingest))

	// An undeclared dimension inherits.
	require.Nil(t, tier.RateFor(governance.Outbound))
	require.Nil(t, tier.BurstFor(governance.Outbound))

	// A nil tier and an empty config both inherit, so callers need not special-case
	// an unloaded association.
	var nilTier *TenantTier
	require.Nil(t, nilTier.RateFor(governance.Ingest))
	require.Nil(t, (&TenantTier{}).RateFor(governance.Ingest))

	// Out-of-band garbage reads as inherit, NOT as zero.
	junk := &TenantTier{Config: map[string]any{
		"ingestMessagesPerSecond": float64(0),
		"ingestBurst":             "lots",
	}}
	require.Nil(t, junk.RateFor(governance.Ingest))
	require.Nil(t, junk.BurstFor(governance.Ingest))
}
