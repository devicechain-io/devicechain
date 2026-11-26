// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/fatih/color"
)

// The upgrade judge for sharding levels.
//
// 🔴 A LEVEL IS JUDGED FROM THE DECLARATION AND THE DEPLOYED CONFIGURATION, never from the run's
// inputs alone (the rule precheckResolvedEventsReshape already follows). There is no level flag on
// upgrade and the Instance definition refuses a spec edit, so a level never changes; what can still
// differ is what this release renders for the recorded name and what the instance runs. Either a
// hand-edited deployment or a release that broke the frozen table makes those two disagree, and both
// are refused before anything is written.

// judgeShardingUpgrade is section 3 of the levels design, rules 1-3. A custom or off instance passes
// untouched: every rule it has today runs after this, unchanged.
func judgeShardingUpgrade(ctx context.Context, st *State) error {
	word, err := shardingOfRun(st)
	if err != nil {
		return err
	}
	out(ctx).printf("  %s %s\n", color.WhiteString("Sharding:"), color.GreenString(word))
	if st.Sharding == "" {
		return nil
	}
	l, ok := shardLevels[st.Sharding]
	if !ok {
		// Rule 1. applyUpgradeDeclaration refuses this first; held here too so the judge never
		// renders an unknown name as anything.
		return fmt.Errorf("the instance declaration's sharding level %q is unknown to this dcctl: a newer dcctl may "+
			"have recorded it. Upgrade dcctl before touching this instance", st.Sharding)
	}
	// Rule 3 and the rest of the table: an overlay may not take the level's block or its replication,
	// and nothing the level was refused with at install may be turned on now.
	if err := refuseShardingCombination(st, &l); err != nil {
		return err
	}
	// Rule 2: what this release renders for the name against what the instance runs.
	rendered, err := shardGroupsFromValues(authoringValues(st))
	if err != nil {
		return err
	}
	deployed, err := readDeployedShardGroups(ctx, st.KubeContext, st.Instance)
	if err != nil {
		return fmt.Errorf("reading the deployed shardGroups to judge this upgrade against: %w", err)
	}
	if !sameShardShape(rendered, deployed) {
		return fmt.Errorf("the %s level's shard shape is fixed at install; this dcctl renders %s, and the instance "+
			"runs %s. Nothing has been changed. If the deployed configuration was edited by hand, restore it; if this "+
			"dcctl renders a different shape for the same level, use the dcctl release the instance was installed with",
			l.Name, describeShardShape(rendered), describeShardShape(deployed))
	}
	return nil
}

// sameShardShape compares every part of a block a level freezes: the count, the suffixes as a set,
// every group's placement and the seed. A nil on either side is a difference.
func sameShardShape(a, b *config.NatsShardGroupsConfiguration) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Count != b.Count {
		return false
	}
	as, bs := slices.Clone(a.Suffixes), slices.Clone(b.Suffixes)
	slices.Sort(as)
	slices.Sort(bs)
	if !slices.Equal(as, bs) {
		return false
	}
	if !maps.EqualFunc(a.PlacementTags, b.PlacementTags, slices.Equal[[]string]) {
		return false
	}
	switch {
	case a.Seed == nil || b.Seed == nil:
		return a.Seed == nil && b.Seed == nil
	default:
		return slices.Equal(a.Seed.Weights, b.Seed.Weights)
	}
}

func describeShardShape(c *config.NatsShardGroupsConfiguration) string {
	if c == nil {
		return "no shardGroups block"
	}
	b, err := json.Marshal(c)
	if err != nil {
		return fmt.Sprintf("%+v", *c)
	}
	return string(b)
}
