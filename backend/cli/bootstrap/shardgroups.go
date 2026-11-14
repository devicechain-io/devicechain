// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

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
			areas, err = ResolveEnabledAreas("", nil)
			if err != nil {
				return nil, err
			}
		}
	} else if profile, ok := vals["profile"].(string); ok {
		var err error
		areas, err = ResolveEnabledAreas(profile, nil)
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
