// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/stretchr/testify/require"
)

// TestTierConfigSurvivesTheDatabaseRoundTrip pins the shape a tier setting has
// AFTER it has been through the store, which is the only shape that matters: every
// read of a tier config on the hot governance path comes back through gorm's json
// serializer, not from the map the writer built.
//
// The risk this guards is silent and total. Config is map[string]any, so a value's
// concrete type is whatever the decoder chose — and RateFor/BurstFor type-assert it.
// If a round-trip yielded json.Number, int64, or a string, the assertion would miss,
// the read would return nil, and EVERY tier setting would quietly degrade to
// "inherit the platform default" — the tier would look configured in the API and do
// nothing at all. That is fail-open in the ADR-023 sense (no error, no log, wrong
// ceiling), so it is worth proving against a real store rather than reasoning about
// encoding/json's defaults.
func TestTierConfigSurvivesTheDatabaseRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.CreateTenantTier(ctx, &TenantTier{
		Token: TierGoldToken,
		Config: map[string]any{
			"ingestMessagesPerSecond":      float64(2000),
			"ingestBurst":                  float64(4000),
			"outboundMessagesPerSecond":    float64(0.5),
			"aiInferenceRequestsPerMinute": float64(60),
		},
	}))

	got, err := s.TenantTierByToken(ctx, TierGoldToken)
	require.NoError(t, err)

	// The reads that the governance cascade actually performs.
	require.NotNil(t, got.RateFor(governance.Ingest), "a stored rate must survive the round-trip")
	require.Equal(t, float64(2000), *got.RateFor(governance.Ingest))
	require.NotNil(t, got.BurstFor(governance.Ingest), "a stored burst must survive the round-trip")
	require.Equal(t, 4000, *got.BurstFor(governance.Ingest))
	require.Equal(t, float64(0.5), *got.RateFor(governance.Outbound), "a fractional rate must not be truncated")
	require.Equal(t, float64(60), *got.RateFor(governance.AIInference), "the per-minute unit is carried, not converted")

	// The round-tripped config must still pass the write-side validator: if it did
	// not, an operator could load a tier and fail to save it back unchanged.
	require.NoError(t, ValidateTierConfig(got.Config))
}

// TestTierConfigAcceptsGoNativeIntegers pins that the validators judge a NUMBER,
// not a float64. JSON decoding (the GraphQL write path and the DB round-trip) always
// yields float64, but a seed written in Go is whatever literal the author typed:
// `"ingestBurst": 4000` is an int, and rejecting it would fail the migration at boot
// over a type nobody thinks about while reading a table of numbers.
func TestTierConfigAcceptsGoNativeIntegers(t *testing.T) {
	require.NoError(t, ValidateTierConfig(map[string]any{
		"ingestMessagesPerSecond": 2000,
		"ingestBurst":             int64(4000),
	}))

	// And those values must READ back, not merely validate — a validator that
	// accepts a type the reader drops is worse than one that rejects it.
	tier := &TenantTier{Config: map[string]any{
		"ingestMessagesPerSecond": 2000,
		"ingestBurst":             int64(4000),
	}}
	require.NotNil(t, tier.RateFor(governance.Ingest))
	require.Equal(t, float64(2000), *tier.RateFor(governance.Ingest))
	require.NotNil(t, tier.BurstFor(governance.Ingest))
	require.Equal(t, 4000, *tier.BurstFor(governance.Ingest))

	// The fail-closed direction is unchanged for genuinely unusable values.
	require.Error(t, ValidateTierConfig(map[string]any{"ingestMessagesPerSecond": 0}))
	require.Error(t, ValidateTierConfig(map[string]any{"ingestBurst": -1}))
}
