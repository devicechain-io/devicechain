// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/streams"
)

// NatsShardGroupsConfiguration opts selected per-event streams into tenant placement.
// Group streams multiply the stream reservation by Count; budget accounting is a later slice.
type NatsShardGroupsConfiguration struct {
	// Count is the total number of groups, including the existing group zero.
	Count         int              `json:"count"`
	Suffixes      []string         `json:"suffixes"`
	PlacementTags map[int][]string `json:"placementTags,omitempty"`
}

// Validate rejects incomplete or unsupported placement. A nil block disables sharding.
func (c *NatsShardGroupsConfiguration) Validate() error {
	if c == nil {
		return nil
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
	for group, tags := range c.PlacementTags {
		if group < 1 || group >= c.Count {
			return fmt.Errorf("nats.shardGroups placementTags key %d must be within 1..%d", group, c.Count-1)
		}
		for _, tag := range tags {
			if strings.TrimSpace(tag) == "" {
				return fmt.Errorf("nats.shardGroups placementTags[%d] tags must be non-empty strings", group)
			}
		}
	}
	return nil
}
