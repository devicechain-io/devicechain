// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/devicechain-io/dc-k8s/functionalarea"
	"github.com/devicechain-io/dc-microservice/config"
)

const shardGroupsValuesPath = "instance.config.infrastructure.nats.shardGroups"

// shardGroupsFromValues validates the installation subset and resolves pod-name
// placement. Each tag selects one server, so these streams must remain R1.
func shardGroupsFromValues(vals map[string]interface{}) (*config.NatsShardGroupsConfiguration, error) {
	raw, found := lookupPath(vals, splitPath(shardGroupsValuesPath))
	if !found {
		return nil, nil
	}
	if raw == nil {
		return nil, fmt.Errorf("%s must be an object; omit it to disable", shardGroupsValuesPath)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var cfg config.NatsShardGroupsConfiguration
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("shardGroups: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Count > 5 || len(cfg.Suffixes) == 0 {
		return nil, fmt.Errorf("shardGroups requires 2..5 groups and at least one suffix")
	}
	if replicas, ok := lookupPath(vals, splitPath("instance.config.infrastructure.nats.streamReplicas")); ok {
		b, _ := json.Marshal(replicas)
		if string(b) != "1" {
			return nil, fmt.Errorf("shardGroups placement selects one server per group and requires streamReplicas=1")
		}
	}
	if cfg.PlacementTags == nil {
		cfg.PlacementTags = map[int][]string{}
	}
	for group := 0; group < cfg.Count; group++ {
		want := fmt.Sprintf("dc-nats-%d", group)
		if tags, exists := cfg.PlacementTags[group]; exists && (len(tags) != 1 || tags[0] != want) {
			return nil, fmt.Errorf("shardGroups placementTags[%d] must be [%q] for the installed broker", group, want)
		}
		cfg.PlacementTags[group] = []string{want}
	}

	// An overlay alone does not carry the deployment's area set. Authoring values
	// do, and must refuse consumers N2 has not wired to group readers yet.
	var areas []string
	if explicit, ok := vals["enabledFunctionalAreas"]; ok {
		b, err := json.Marshal(explicit)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &areas); err != nil {
			return nil, err
		}
		if len(areas) == 0 {
			areas, err = shardGroupAreas("", nil)
			if err != nil {
				return nil, err
			}
		}
	} else if profile, ok := vals["profile"].(string); ok {
		var err error
		areas, err = shardGroupAreas(profile, nil)
		if err != nil {
			return nil, err
		}
	}
	if err := validateShardGroupReaders(cfg.Suffixes, areas); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func writeShardGroupsVarFile(rootdir string, st *State) error {
	path := filepath.Join(rootdir, "dcctl-shard-groups.auto.tfvars.json")
	cfg, err := shardGroupsFromValues(authoringValues(st))
	if err != nil {
		return err
	}
	if cfg == nil {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	raw, err := json.Marshal(map[string]int{"nats_shard_groups": cfg.Count})
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func shardGroupsValues(raw interface{}) map[string]interface{} {
	return map[string]interface{}{"instance": map[string]interface{}{"config": map[string]interface{}{"infrastructure": map[string]interface{}{"nats": map[string]interface{}{"shardGroups": raw}}}}}
}

func validateShardGroupReaders(suffixes, areas []string) error {
	if !slices.Contains(suffixes, "resolved-events") {
		return nil
	}
	for _, area := range []string{"device-state", "event-processing"} {
		if slices.Contains(areas, area) {
			return fmt.Errorf("shardGroups: resolved-events cannot be sharded while %s is deployed; its group readers are not wired yet (select inbound-events only)", area)
		}
	}
	return nil
}

// ResolveEnabledAreas intentionally returns nil for a profile with no extras,
// preserving Helm's profile path. Capacity checks need its concrete reader set.
func shardGroupAreas(profile string, extra []string) ([]string, error) {
	areas, err := ResolveEnabledAreas(profile, extra)
	if err != nil || len(areas) > 0 {
		return areas, err
	}
	resolved, err := functionalarea.ResolveEnabled(profile, nil)
	if err != nil {
		return nil, err
	}
	for _, area := range resolved {
		areas = append(areas, string(area))
	}
	return areas, nil
}

// An upgrade must never convert a data-bearing standalone JetStream server.
// Read the existing broker, including for --skip-infrastructure.
func precheckShardGroupsUpgrade(ctx context.Context, st *State) error {
	cfg, err := shardGroupsFromValues(authoringValues(st))
	if err != nil || cfg == nil {
		return err
	}
	infra, err := readLiveInfra(ctx, st.KubeContext, st.Instance)
	if err != nil {
		return err
	}
	servers := 0
	if infra.Broker != nil {
		servers = 1 // Kubernetes defaults an omitted replicas field to one.
		if infra.Broker.Spec.Replicas != nil {
			servers = int(*infra.Broker.Spec.Replicas)
		}
	} else {
		// A broker whose StatefulSet was deleted with --cascade=orphan (the volume-growth
		// recovery) still has one claim per server; the upgrade recreates the set over them.
		servers = len(infra.BrokerClaims)
	}
	return refuseStandaloneShardGroups(servers, servers > 0)
}

func refuseStandaloneShardGroups(servers int, known bool) error {
	if known && servers >= 3 {
		return nil
	}
	return fmt.Errorf("dcctl refuses to enable shardGroups on an existing single-server (non-HA) or unknown broker: " +
		"converting a standalone JetStream server holding data into a cluster is not supported. " +
		"Enable shard groups when the instance is first installed; an instance installed with them keeps its cluster. Nothing has been changed")
}

func checkShardGroupsRemoval(st *State, recorded map[string]interface{}) error {
	if !st.ValuesOverlayIntent {
		return nil
	}
	_, inOverlay := lookupPath(recorded, splitPath(shardGroupsValuesPath))
	if st.ShardGroups == nil && !inOverlay {
		return nil
	}
	cfg, err := shardGroupsFromValues(authoringValues(st))
	if err != nil {
		return err
	}
	if cfg == nil {
		return fmt.Errorf("dcctl refuses to drop recorded shardGroups: existing groups may hold data. Preserve shardGroups in the replacement --values file; --reset-values cannot remove it")
	}
	// Narrowing the block abandons data just as dropping it does: a group past the new
	// count, or a stream no longer sharded, keeps whatever its streams hold while the
	// services stop reading it and the broker stops admitting its subjects.
	var prev *config.NatsShardGroupsConfiguration
	if inOverlay {
		if prev, err = shardGroupsFromValues(recorded); err != nil {
			return fmt.Errorf("reading the recorded shardGroups: %w", err)
		}
	}
	if prev == nil && st.ShardGroups != nil {
		prev = &config.NatsShardGroupsConfiguration{Count: st.ShardGroups.Count, Suffixes: st.ShardGroups.Suffixes}
	}
	return refuseShardGroupsNarrowing(prev, cfg)
}

func refuseShardGroupsNarrowing(prev, cfg *config.NatsShardGroupsConfiguration) error {
	if prev == nil {
		return nil
	}
	if cfg.Count < prev.Count {
		return fmt.Errorf("dcctl refuses to reduce shardGroups.count from %d to %d: the groups past the new count may hold data. Nothing has been changed", prev.Count, cfg.Count)
	}
	for _, suffix := range prev.Suffixes {
		if !slices.Contains(cfg.Suffixes, suffix) {
			return fmt.Errorf("dcctl refuses to stop sharding %q: its existing groups may hold data. Keep it in shardGroups.suffixes. Nothing has been changed", suffix)
		}
	}
	return nil
}

// brokerTopology is the broker the instance actually runs: the --ha topology,
// widened to an odd cluster of at least three servers when shard groups are on.
// Every consumer of the server count (the OpenTofu vars, the teardown guard, the
// node-capacity check, the per-server certificate names, the claim narrowing)
// reads it from here, so a non-HA instance with shard groups cannot get a
// three-server broker whose certificate names only one of them. Stream
// replication is left at the --ha factor: the groups are independent R1 streams.
// A malformed shardGroups block is refused by its own validation; here it falls
// back to the --ha topology rather than inventing a size.
func brokerTopology(st *State) haTopology {
	topology := haFor(st.HA)
	if cfg, err := shardGroupsFromValues(authoringValues(st)); err == nil && cfg != nil {
		topology.ServerReplicas = max(3, cfg.Count|1)
	}
	return topology
}
