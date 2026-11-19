// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"fmt"
	"strings"

	"github.com/devicechain-io/dc-microservice/streams"
)

// The sharding axis: `--sharding off|scale`.
//
// A sharding level is a NAME recorded on the instance and rendered by dcctl into levers that already
// exist (the shardGroups block, the broker), the way a size is. It differs from a size in one rule:
//
// 🔴 A LEVEL'S SHARD SHAPE IS FROZEN PER NAME, FOREVER, once the level is accepted. A later release
// may retune a level's broker sizing (with release notes, as sizes are retuned), but never its group
// count, its suffixes, its placement tags or its vpartition seed: an instance's tenants are placed
// into groups at install, and nothing moves a tenant between groups. A different shape is a new
// name. TestScaleShapeIsFrozen holds the shape to a fixture that no flag regenerates.
//
//   - off is the default and renders nothing: no shardGroups block, today's broker. It is recorded as
//     the ABSENCE of spec.sharding, so every declaration written before the axis existed reads as off
//     (or as custom, when it carries a raw shardGroups block). TestOffRendersTodaysBytes holds it to
//     the render dcctl produced before the axis existed.
//
//   - scale is three groups, one per broker server, for inbound-events and resolved-events. It is
//     PROVISIONAL: its shape and its broker row are the proposal the qualification run measures, and
//     --sharding does not accept it until that run has passed. Until then the fixture may still be
//     edited by hand; from acceptance on it may not.
//
//   - custom is never a level and never recorded: it is the word for an instance that names no level
//     and carries a raw shardGroups block of its own.
const (
	ShardingOff    = "off"
	ShardingScale  = "scale"
	ShardingCustom = "custom"
)

// shardingNames is every level name this build knows, in the order help prints them. Knowing a name
// is not accepting it: see shardLevel.Accepted.
var shardingNames = []string{ShardingOff, ShardingScale}

// vpartitions is the size of the placement table a level seeds: user-management's partition.DefaultP.
// Restated rather than imported, because the level's seed is frozen and must not follow a constant
// that could move; TestScaleSeedCoversEveryVPartition holds the two together.
const vpartitions = 64

// shardLevel is one level's frozen shape and its (retunable) broker row, as typed data so the tests
// read the numbers back rather than parsing a rendering.
type shardLevel struct {
	Name string

	// Accepted says --sharding takes this name. A known but unaccepted level is refused with a
	// message saying what it waits for, never rendered.
	Accepted bool

	// The frozen shape.
	Count    int
	Suffixes []string
	// SeedWeights is how many of the placement table's vpartitions each group is seeded with, when
	// user-management first creates the table. Its sum is the table size.
	SeedWeights []int

	// The broker row, per server. Retunable in a later release; it wins over the size's (precedence
	// layer 2b).
	NatsCPURequest    string
	NatsMemoryRequest string
	NatsMemoryLimit   string
	JetStreamStorage  string

	// RequiredSize is the only size the level is measured with.
	RequiredSize string
}

// shardLevels is the frozen table. off is not a row: it states nothing.
//
// scale's figures are the proposal (three groups, the even seed 22/21/21, broker 3 CPU / 4Gi requests
// on a 10Gi limit and a 40Gi JetStream volume per server), not yet measured as rendered.
var shardLevels = map[string]shardLevel{
	ShardingScale: {
		Name:              ShardingScale,
		Accepted:          false,
		Count:             3,
		Suffixes:          []string{streams.InboundEvents, streams.ResolvedEvents},
		SeedWeights:       []int{22, 21, 21},
		NatsCPURequest:    "3",
		NatsMemoryRequest: "4Gi",
		NatsMemoryLimit:   "10Gi",
		JetStreamStorage:  "40Gi",
		RequiredSize:      SizePerf20k,
	},
}

// ValidateSharding judges a --sharding value: a name this build knows. "" is off.
func ValidateSharding(name string) error {
	if name == "" {
		return nil
	}
	for _, n := range shardingNames {
		if n == name {
			return nil
		}
	}
	return fmt.Errorf("unknown sharding level %q (one of %s)", name, strings.Join(shardingNames, ", "))
}

// shardLevelFor is the level an instance renders, or nil for off. An unknown name is refused before
// anything renders (ValidateSharding), never rendered as off.
func shardLevelFor(st *State) *shardLevel {
	if st.Sharding == "" || st.Sharding == ShardingOff {
		return nil
	}
	l, ok := shardLevels[st.Sharding]
	if !ok {
		return nil
	}
	return &l
}

// placementTags is group g on its own server, dc-nats-g, which is the only placement the chart and
// the Instance definition accept.
func (l *shardLevel) placementTags() map[string]interface{} {
	tags := map[string]interface{}{}
	for g := 0; g < l.Count; g++ {
		tags[fmt.Sprint(g)] = []interface{}{fmt.Sprintf("dc-nats-%d", g)}
	}
	return tags
}

// shardGroupsBlock is the level's instance.config.infrastructure.nats.shardGroups block, every key
// stated: a placement left to the chart's derivation would be a shape the level did not freeze.
// Nil for off.
func (l *shardLevel) shardGroupsBlock() map[string]interface{} {
	if l == nil {
		return nil
	}
	suffixes := make([]interface{}, 0, len(l.Suffixes))
	for _, s := range l.Suffixes {
		suffixes = append(suffixes, s)
	}
	weights := make([]interface{}, 0, len(l.SeedWeights))
	for _, w := range l.SeedWeights {
		weights = append(weights, w)
	}
	return map[string]interface{}{
		"count":         l.Count,
		"suffixes":      suffixes,
		"placementTags": l.placementTags(),
		"seed":          map[string]interface{}{"weights": weights},
	}
}
