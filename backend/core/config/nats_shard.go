// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/streams"
)

// NatsShardGroupsConfiguration opts selected per-event streams into tenant placement.
// Group streams multiply each selected stream reservation by Count.
type NatsShardGroupsConfiguration struct {
	// Count is the total number of groups, including the existing group zero.
	Count    int      `json:"count"`
	Suffixes []string `json:"suffixes"`
	// PlacementTags accepts groups 0 through Count-1, including the existing group zero.
	PlacementTags map[int][]string `json:"placementTags,omitempty"`
	// Seed spreads the placement table over the groups when user-management FIRST creates it, and
	// never again: on an instance whose table exists it is read by nobody. Absent, every
	// vpartition starts in group zero, as before the key existed.
	//
	// 🔴 LEVEL-OWNED. dcctl renders it for a sharding level and refuses it in an operator's values
	// overlay: a release older than the key rejects the whole document, so it may reach only an
	// instance whose every pod already knows it, which is a fresh install of a level.
	Seed *NatsShardGroupsSeed `json:"seed,omitempty"`
}

// ShardSeedVPartitions is the size of the placement table a seed spreads: user-management's
// partition.DefaultP, restated because this package cannot import that one (a test holds the two
// together). A seed must place every vpartition, so its weights sum to exactly this.
const ShardSeedVPartitions = 64

// NatsShardGroupsSeed is how many vpartitions each group is seeded with, group by group.
type NatsShardGroupsSeed struct {
	Weights []int `json:"weights"`
}

// Validate rejects incomplete or unsupported placement. A nil block disables sharding.
func (c *NatsShardGroupsConfiguration) Validate() error {
	if c == nil {
		return nil
	}
	if c.Count > 16 {
		return fmt.Errorf("nats.shardGroups.count must be at most 16 (got %d)", c.Count)
	}
	if c.Count < 2 {
		return fmt.Errorf("nats.shardGroups.count must be at least 2")
	}
	seen := make(map[string]bool)
	for _, suffix := range c.Suffixes {
		if !streams.IsDeclared(suffix) {
			return fmt.Errorf("nats.shardGroups suffix %q is not declared in core/streams", suffix)
		}
		if seen[suffix] {
			return fmt.Errorf("nats.shardGroups suffix %q is repeated", suffix)
		}
		seen[suffix] = true
		if suffix != streams.InboundEvents && suffix != streams.ResolvedEvents {
			return fmt.Errorf("nats.shardGroups suffix %q is not supported in this slice; only inbound-events and resolved-events are allowed", suffix)
		}
	}
	if err := c.Seed.validate(c.Count); err != nil {
		return err
	}
	for group, tags := range c.PlacementTags {
		if group < 0 || group >= c.Count {
			return fmt.Errorf("nats.shardGroups placementTags key %d must be within 0..%d", group, c.Count-1)
		}
		for _, tag := range tags {
			if strings.TrimSpace(tag) == "" {
				return fmt.Errorf("nats.shardGroups placementTags[%d] tags must be non-empty strings", group)
			}
		}
	}
	return nil
}

// validate holds a seed to its block: one weight per group, every group seeded with at least one
// vpartition, and every vpartition of the table placed. A nil seed is the unseeded table.
func (s *NatsShardGroupsSeed) validate(count int) error {
	if s == nil {
		return nil
	}
	if len(s.Weights) != count {
		return fmt.Errorf("nats.shardGroups.seed.weights must have one weight per group (%d), got %d", count, len(s.Weights))
	}
	sum := 0
	for group, w := range s.Weights {
		if w < 1 {
			return fmt.Errorf("nats.shardGroups.seed.weights[%d] must be at least 1 (got %d): every group is seeded with a vpartition", group, w)
		}
		sum += w
	}
	if sum != ShardSeedVPartitions {
		return fmt.Errorf("nats.shardGroups.seed.weights must sum to the placement table's %d vpartitions (got %d)", ShardSeedVPartitions, sum)
	}
	return nil
}
