// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

func shardDocument(block string) []byte {
	if block != "" {
		block = `,"shardGroups":` + block
	}
	return []byte(`{"infrastructure":{"nats":{"hostname":"n","port":4222` + block + `},"userManagement":{"hostname":"u","port":8080}}}`)
}

func TestNatsShardGroupsAbsentRoundTrip(t *testing.T) {
	var cfg config.InstanceConfiguration
	require.NoError(t, core.LoadConfiguration(shardDocument(""), &cfg))
	require.Nil(t, cfg.Infrastructure.Nats.ShardGroups)
	encoded, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NotContains(t, strings.ToLower(string(encoded)), "shardgroups")
}

func TestNatsShardGroupsTypedValidation(t *testing.T) {
	for _, tc := range []struct{ name, block, message string }{
		{"count-zero", `{"count":0}`, "at least 2"},
		{"count-negative", `{"count":-1}`, "at least 2"},
		{"count-one", `{"count":1}`, "at least 2"},
		{"unknown-suffix", `{"count":2,"suffixes":["typo"]}`, "not declared"},
		{"duplicate-suffix", `{"count":2,"suffixes":["inbound-events","inbound-events"]}`, "repeated"},
		{"tag-negative", `{"count":2,"placementTags":{"-1":["rack"]}}`, "within 0..1"},
		{"tag-count", `{"count":2,"placementTags":{"2":["rack"]}}`, "within 0..1"},
		{"tag-nonnumeric", `{"count":2,"placementTags":{"one":["rack"]}}`, "decode failed"},
		{"tag-empty", `{"count":2,"placementTags":{"1":[""]}}`, "non-empty"},
		{"tag-whitespace", `{"count":2,"placementTags":{"1":[" "]}}`, "non-empty"},
		{"unknown-key", `{"count":2,"typo":true}`, "unknown field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg config.InstanceConfiguration
			require.ErrorContains(t, core.LoadConfiguration(shardDocument(tc.block), &cfg), tc.message)
		})
	}
	// Every declared suffix outside the two per-event ingest streams must be refused.
	for _, stream := range streams.All {
		if stream.Suffix == streams.InboundEvents || stream.Suffix == streams.ResolvedEvents {
			continue
		}
		t.Run(stream.Suffix, func(t *testing.T) {
			block, err := json.Marshal(config.NatsShardGroupsConfiguration{Count: 2, Suffixes: []string{stream.Suffix}})
			require.NoError(t, err)
			var cfg config.InstanceConfiguration
			require.ErrorContains(t, core.LoadConfiguration(shardDocument(string(block)), &cfg), "not supported in this slice")
		})
	}
}

func TestNatsShardGroupsPlacementTagsRoundTrip(t *testing.T) {
	var cfg config.InstanceConfiguration
	require.NoError(t, core.LoadConfiguration(shardDocument(`{"count":3,"suffixes":["inbound-events","resolved-events"],"placementTags":{"1":["rack-a"],"2":["rack-b","ssd"]}}`), &cfg))
	want := cfg.Infrastructure.Nats.ShardGroups
	require.Equal(t, []string{"rack-a"}, want.PlacementTags[1])
	require.Equal(t, []string{"rack-b", "ssd"}, want.PlacementTags[2])
	encoded, err := json.Marshal(cfg)
	require.NoError(t, err)
	var again config.InstanceConfiguration
	require.NoError(t, core.LoadConfiguration(encoded, &again))
	require.Equal(t, want, again.Infrastructure.Nats.ShardGroups)
}

func TestNatsShardGroupsCountBound(t *testing.T) {
	require.NoError(t, (&config.NatsShardGroupsConfiguration{Count: 16}).Validate())
	require.ErrorContains(t, (&config.NatsShardGroupsConfiguration{Count: 17}).Validate(), "at most 16")
	require.ErrorContains(t, (&config.NatsShardGroupsConfiguration{Count: 1000000}).Validate(), "at most 16")
}
