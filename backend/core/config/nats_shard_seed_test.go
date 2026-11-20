// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/partition"
	"github.com/stretchr/testify/require"
)

// The seed spreads user-management's table, so it must cover exactly that table.
func TestShardSeedVPartitionsIsThePlacementTableSize(t *testing.T) {
	require.Equal(t, partition.DefaultP, config.ShardSeedVPartitions)
}

func TestNatsShardGroupsSeedRoundTrip(t *testing.T) {
	var cfg config.InstanceConfiguration
	require.NoError(t, core.LoadConfiguration(shardDocument(`{"count":3,"suffixes":["inbound-events"],"seed":{"weights":[22,21,21]}}`), &cfg))
	got := cfg.Infrastructure.Nats.ShardGroups
	require.Equal(t, &config.NatsShardGroupsSeed{Weights: []int{22, 21, 21}}, got.Seed)
	encoded, err := json.Marshal(cfg)
	require.NoError(t, err)
	var again config.InstanceConfiguration
	require.NoError(t, core.LoadConfiguration(encoded, &again))
	require.Equal(t, got, again.Infrastructure.Nats.ShardGroups)
}

// An unseeded block encodes no seed key, so a document written for a custom instance is the one it
// was before the key existed (an older pod would refuse the key).
func TestNatsShardGroupsUnseededEncodesNoSeed(t *testing.T) {
	encoded, err := json.Marshal(config.NatsShardGroupsConfiguration{Count: 2, Suffixes: []string{"inbound-events"}})
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "seed")
}

func TestNatsShardGroupsSeedValidation(t *testing.T) {
	for _, tc := range []struct{ name, seed, message string }{
		{"too-few-weights", `{"weights":[32,32]}`, "one weight per group (3), got 2"},
		{"too-many-weights", `{"weights":[16,16,16,16]}`, "one weight per group (3), got 4"},
		{"no-weights", `{"weights":[]}`, "one weight per group (3), got 0"},
		{"zero-weight", `{"weights":[32,32,0]}`, "weights[2] must be at least 1"},
		{"negative-weight", `{"weights":[33,32,-1]}`, "weights[2] must be at least 1"},
		{"sum-short", `{"weights":[21,21,21]}`, "sum to the placement table's 64 vpartitions (got 63)"},
		{"sum-over", `{"weights":[22,22,21]}`, "sum to the placement table's 64 vpartitions (got 65)"},
		{"unknown-key", `{"weights":[22,21,21],"typo":1}`, "unknown field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg config.InstanceConfiguration
			block := `{"count":3,"suffixes":["inbound-events"],"seed":` + tc.seed + `}`
			require.ErrorContains(t, core.LoadConfiguration(shardDocument(block), &cfg), tc.message)
		})
	}
	// The controls: the boundaries that must pass.
	for _, weights := range [][]int{{22, 21, 21}, {1, 1, 62}, {12, 26, 26}} {
		require.NoError(t, (&config.NatsShardGroupsConfiguration{Count: 3, Suffixes: []string{"inbound-events"},
			Seed: &config.NatsShardGroupsSeed{Weights: weights}}).Validate(), "weights %v", weights)
	}
}
