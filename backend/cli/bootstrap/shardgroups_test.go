// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/stretchr/testify/require"
)

func shardBlock() map[string]interface{} {
	return map[string]interface{}{"count": 3, "suffixes": []string{"inbound-events"}}
}

func TestShardGroupsValuesFailClosed(t *testing.T) {
	require.NoError(t, ValidateValuesOverlay(shardGroupsValues(shardBlock())))
	for _, raw := range []interface{}{
		nil, true,
		map[string]interface{}{"count": 1, "suffixes": []string{"inbound-events"}},
		map[string]interface{}{"count": 6, "suffixes": []string{"inbound-events"}},
		map[string]interface{}{"count": 2.5, "suffixes": []string{"inbound-events"}},
		map[string]interface{}{"count": 2, "suffixes": []string{}},
		map[string]interface{}{"count": 2, "suffixes": []string{"inbound-events", "inbound-events"}},
		map[string]interface{}{"count": 2, "suffixes": []string{"command-delivery"}},
		map[string]interface{}{"count": 2, "suffixes": []string{"inbound-events"}, "typo": true},
		map[string]interface{}{"count": 2, "suffixes": []string{"inbound-events"}, "placementTags": map[string]interface{}{"1": []string{"dc-nats-0"}}},
	} {
		require.Error(t, ValidateValuesOverlay(shardGroupsValues(raw)), "%v", raw)
	}
}

func TestShardGroupsChartPlacementAndOffBytes(t *testing.T) {
	ch, err := loadEmbeddedChart()
	require.NoError(t, err)
	st := &State{Instance: "prod", Profile: "default", Values: map[string]string{}}
	vals := authoringValues(st)
	// Give the chart its required root key through its normal test helper's values.
	infra := vals["instance"].(map[string]interface{})["config"].(map[string]interface{})["infrastructure"].(map[string]interface{})
	infra["secrets"] = map[string]interface{}{"rootKey": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
	before, err := renderInstanceConfigDocument(t.Context(), ch, vals)
	require.NoError(t, err)
	require.NotContains(t, string(before), "shardGroups")
	cfg, err := shardGroupsFromValues(vals)
	require.NoError(t, err)
	require.Nil(t, cfg)
	after, err := renderInstanceConfigDocument(t.Context(), ch, vals)
	require.NoError(t, err)
	require.True(t, bytes.Equal(before, after), "off path changed after resolving shard groups")
	mergeOverlay(vals, shardGroupsValues(shardBlock()))
	raw, err := renderInstanceConfigDocument(t.Context(), ch, vals)
	require.NoError(t, err)
	var doc config.InstanceConfiguration
	require.NoError(t, json.Unmarshal(raw, &doc))
	require.Equal(t, []string{"dc-nats-0"}, doc.Infrastructure.Nats.ShardGroups.PlacementTags[0])
	require.Equal(t, []string{"dc-nats-1"}, doc.Infrastructure.Nats.ShardGroups.PlacementTags[1])
	require.Equal(t, []string{"dc-nats-2"}, doc.Infrastructure.Nats.ShardGroups.PlacementTags[2])
	infra["nats"].(map[string]interface{})["streamReplicas"] = 3
	_, err = renderInstanceConfigDocument(t.Context(), ch, vals)
	require.ErrorContains(t, err, "streamReplicas=1")
}

func TestShardGroupsReservationScalesOnlySelectedStreams(t *testing.T) {
	cfg := renderInstanceConfig(t, nil)
	n := &cfg.Infrastructure.Nats
	off := jetStreamReservation(n)
	selected := n.StreamMaxBytesFor("inbound-events") + n.StreamMaxBytesFor("resolved-events")
	n.ShardGroups = &config.NatsShardGroupsConfiguration{Count: 3, Suffixes: []string{"inbound-events", "resolved-events"}}
	require.Equal(t, off+2*selected, jetStreamReservation(n))
	require.Error(t, checkJetStreamReservation(n, off, "legacy ceiling", false))
	require.NoError(t, checkJetStreamReservation(n, off+2*selected, "grown ceiling", false))
	n.ShardGroups.Count = -1
	require.Error(t, checkJetStreamReservation(n, off, "invalid", false))
}

func TestShardGroupsBrokerVarFileAndPermissions(t *testing.T) {
	dir := t.TempDir()
	st := &State{Instance: "prod", Profile: "default", Values: map[string]string{}}
	require.NoError(t, writeShardGroupsVarFile(dir, st))
	_, err := os.Stat(filepath.Join(dir, "dcctl-shard-groups.auto.tfvars.json"))
	require.True(t, os.IsNotExist(err))
	legacy, err := BrokerPermissionsJSON("prod", false)
	require.NoError(t, err)
	off, err := brokerPermissionsJSON("prod", false, nil)
	require.NoError(t, err)
	require.Equal(t, legacy, off)
	st.ValuesOverlay = shardGroupsValues(shardBlock())
	require.NoError(t, writeShardGroupsVarFile(dir, st))
	raw, err := os.ReadFile(filepath.Join(dir, "dcctl-shard-groups.auto.tfvars.json"))
	require.NoError(t, err)
	require.JSONEq(t, `{"nats_shard_groups":3}`, string(raw))
	spec := InstanceSpecFrom(st, ClusterBinding{}, "local")
	require.NotNil(t, spec.ShardGroups)
	require.Equal(t, 3, spec.ShardGroups.Count)
	require.NoError(t, ValidateInstanceSpec(spec))
	spec.ShardGroups.Count = 1
	require.Error(t, ValidateInstanceSpec(spec))
	cfg, err := shardGroupsFromValues(authoringValues(st))
	require.NoError(t, err)
	on, err := brokerPermissionsJSON("prod", false, cfg)
	require.NoError(t, err)
	require.Contains(t, on, "inbound-events-g2")
	require.NotContains(t, on, "inbound-events-g3")
	require.False(t, strings.Contains(on, "inbound-events*"))
	st.ValuesOverlay = nil
	require.NoError(t, writeShardGroupsVarFile(dir, st))
	_, err = os.Stat(filepath.Join(dir, "dcctl-shard-groups.auto.tfvars.json"))
	require.True(t, os.IsNotExist(err))
}

// This digest is from the pre-N3 chart at HEAD, rendered with the literal values
// below. It pins the exact old-pod document, including whitespace and omission.
func TestShardGroupsOffDocumentBytes(t *testing.T) {
	ch, err := loadEmbeddedChart()
	require.NoError(t, err)
	vals := map[string]interface{}{
		"instance": map[string]interface{}{"config": map[string]interface{}{"infrastructure": map[string]interface{}{"secrets": map[string]interface{}{"rootKey": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}}}},
		"image":    map[string]interface{}{"tag": "test"},
		"ingress":  map[string]interface{}{"host": "test.invalid"},
	}
	raw, err := renderInstanceConfigDocument(t.Context(), ch, vals)
	require.NoError(t, err)
	require.Equal(t, "165eca2a0946ed91a9fc7375df426b21d166d9eef8a304c4ee7d9c2a44491066", fmt.Sprintf("%x", sha256.Sum256(raw)))
}

func TestShardGroupsDeclarationRoundTripAndBrokerTopology(t *testing.T) {
	st := &State{Instance: "prod", Profile: "default", Values: map[string]string{}, ValuesOverlay: shardGroupsValues(shardBlock())}
	spec := InstanceSpecFrom(st, ClusterBinding{}, "local")
	next := &State{Instance: "prod", Values: map[string]string{}}
	applyDeclaration(next, spec)
	cfg, err := shardGroupsFromValues(authoringValues(next))
	require.NoError(t, err)
	require.Equal(t, 3, cfg.Count)
	vars := infraVars(next)
	require.Contains(t, vars, "nats_cluster_replicas=3")
	require.Contains(t, vars, "ha=false")
	next.ValuesOverlayIntent = true
	cfg, err = shardGroupsFromValues(authoringValues(next))
	require.NoError(t, err)
	require.Nil(t, cfg)
	spec.HA = true
	require.Error(t, ValidateInstanceSpec(spec))
}

func TestShardGroupsRefusesUnwiredResolvedReaders(t *testing.T) {
	block := map[string]interface{}{"count": 2, "suffixes": []string{"resolved-events"}}
	st := &State{Instance: "prod", Profile: "default", Values: map[string]string{}, ValuesOverlay: shardGroupsValues(block)}
	_, err := shardGroupsFromValues(authoringValues(st))
	require.ErrorContains(t, err, "device-state")
	require.ErrorContains(t, ValidateInstanceSpec(InstanceSpecFrom(st, ClusterBinding{}, "local")), "device-state")
	ch, err := loadEmbeddedChart()
	require.NoError(t, err)
	vals := renderOnlyAuthoring(st)
	_, err = renderInstanceConfigDocument(t.Context(), ch, vals)
	require.ErrorContains(t, err, "group readers")
	st.Profile = "ingest-only"
	st.EnableAreas = []string{"event-management"}
	st.EnabledAreas, err = ResolveEnabledAreas(st.Profile, st.EnableAreas)
	require.NoError(t, err)
	cfg, err := shardGroupsFromValues(authoringValues(st))
	require.NoError(t, err)
	require.Equal(t, []string{"resolved-events"}, cfg.Suffixes)
	require.NoError(t, ValidateInstanceSpec(InstanceSpecFrom(st, ClusterBinding{}, "local")))
	_, err = renderInstanceConfigDocument(t.Context(), ch, renderOnlyAuthoring(st))
	require.NoError(t, err)
}
