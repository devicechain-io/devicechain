// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package v1beta1_test

import (
	"encoding/json"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/stretchr/testify/require"
)

func TestInstanceShardGroupsOptionalAndDeepCopied(t *testing.T) {
	var spec dcv1beta1.InstanceSpec
	off, err := json.Marshal(spec)
	require.NoError(t, err)
	require.NotContains(t, string(off), "shardGroups")
	raw := `{"provider":"local","managed":false,"shardGroups":{"count":2,"suffixes":["inbound-events"],"placementTags":{"1":["dc-nats-1"]}}}`
	require.NoError(t, json.Unmarshal([]byte(raw), &spec))
	require.NotNil(t, spec.ShardGroups)
	copy := spec.DeepCopy()
	copy.ShardGroups.Suffixes[0] = "changed"
	copy.ShardGroups.PlacementTags["1"][0] = "changed"
	require.Equal(t, "inbound-events", spec.ShardGroups.Suffixes[0])
	require.Equal(t, "dc-nats-1", spec.ShardGroups.PlacementTags["1"][0])
	props := specSchema(t, loadInstanceCRD(t))["properties"].(map[string]interface{})
	block := props["shardGroups"].(map[string]interface{})
	require.NotContains(t, block, "default")
	require.Equal(t, "object", block["type"])
	fields := block["properties"].(map[string]interface{})
	count := fields["count"].(map[string]interface{})
	require.EqualValues(t, 2, count["minimum"])
	require.EqualValues(t, 5, count["maximum"])
}
