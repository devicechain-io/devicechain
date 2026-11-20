// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/stretchr/testify/require"
)

func scaleState() *State {
	st := &State{Instance: "prod", Profile: "default", Values: map[string]string{}, Size: SizePerf20k}
	st.Sharding = ShardingScale
	return st
}

// TestScaleRenderIsAnInstanceDocumentTheServicesAccept: the level's block, seed included, passes
// dcctl's own reading, the chart's schema and the services' strict loader, and widens the broker to
// one server per group.
func TestScaleRenderIsAnInstanceDocumentTheServicesAccept(t *testing.T) {
	st := scaleState()
	cfg, err := shardGroupsFromValues(authoringValues(st))
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Equal(t, 3, cfg.Count)
	require.Equal(t, &config.NatsShardGroupsSeed{Weights: []int{22, 21, 21}}, cfg.Seed)
	require.Equal(t, 3, brokerTopology(st).ServerReplicas)

	ch, err := loadEmbeddedChart()
	require.NoError(t, err)
	raw, err := renderInstanceConfigDocument(t.Context(), ch, renderOnlyAuthoring(st))
	require.NoError(t, err)
	var doc config.InstanceConfiguration
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.NotNil(t, doc.Infrastructure.Nats.ShardGroups)
	require.Equal(t, []int{22, 21, 21}, doc.Infrastructure.Nats.ShardGroups.Seed.Weights)
	require.NoError(t, doc.Infrastructure.Nats.ShardGroups.Validate())
}

// The chart's schema refuses a malformed seed on its own, whoever renders it: a raw `helm upgrade`
// never reaches dcctl's checks.
func TestChartSchemaRefusesAMalformedSeed(t *testing.T) {
	ch, err := loadEmbeddedChart()
	require.NoError(t, err)
	for _, seed := range []interface{}{
		map[string]interface{}{"weights": []interface{}{0, 32, 32}},
		map[string]interface{}{"weights": []interface{}{64}},
		map[string]interface{}{"weights": []interface{}{22, 21, 21}, "typo": 1},
		map[string]interface{}{},
		[]interface{}{22, 21, 21},
		// Shapes the schema alone cannot see: the template holds them to the block.
		map[string]interface{}{"weights": []interface{}{32, 32}},
		map[string]interface{}{"weights": []interface{}{21, 21, 21}},
		map[string]interface{}{"weights": []interface{}{22, 22, 21}},
	} {
		st := scaleState()
		vals := renderOnlyAuthoring(st)
		block, _ := lookupPath(vals, splitPath(shardGroupsValuesPath))
		block.(map[string]interface{})["seed"] = seed
		_, err := renderInstanceConfigDocument(t.Context(), ch, vals)
		require.Error(t, err, "seed %v", seed)
	}
}

// 🔑 The seed is level-owned: an operator overlay that sets it is refused, alone or inside a whole
// shardGroups block, so it can never reach pods older than the key. The control: the same block
// without the seed is accepted.
func TestOverlayCannotSetTheSeed(t *testing.T) {
	block := func(withSeed bool) map[string]interface{} {
		b := map[string]interface{}{"count": 3, "suffixes": []interface{}{"inbound-events"}}
		if withSeed {
			b["seed"] = map[string]interface{}{"weights": []interface{}{22, 21, 21}}
		}
		return b
	}
	require.NoError(t, ValidateValuesOverlay(shardGroupsValues(block(false))))
	err := ValidateValuesOverlay(shardGroupsValues(block(true)))
	require.ErrorContains(t, err, "shardGroups.seed")
	require.ErrorContains(t, err, "belongs to a sharding level")
	err = refuseOverlayKeys(shardGroupsValues(map[string]interface{}{"seed": map[string]interface{}{"weights": []interface{}{64}}}), "--values f")
	require.ErrorContains(t, err, "belongs to a sharding level")
}
