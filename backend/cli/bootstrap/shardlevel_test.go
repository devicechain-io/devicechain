// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/partition"
)

// TestOffRendersTodaysBytes pins the compat guarantee of the sharding axis: an instance at level
// off, named or not, renders byte for byte what dcctl rendered before the axis existed. It reads the
// size axis's golden, which was generated before either axis was added, and which this test has no
// flag to regenerate.
func TestOffRendersTodaysBytes(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "size_base_golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, level := range []string{"", ShardingOff} {
		out := map[string]interface{}{}
		for name, mk := range baseGoldenRows {
			st := mk()
			st.Sharding = level
			out[name] = map[string]interface{}{"helmValues": helmValues(st), "infraVars": infraVars(st)}
		}
		got, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if string(append(got, '\n')) != string(want) {
			t.Errorf("sharding %q: the render moved from testdata/size_base_golden.json; level off must render exactly what an instance that names no level rendered before the axis existed", level)
		}
	}
}

// TestOffIsInertOnEverySize holds off to "states nothing" on the paths the base golden does not
// cover: every size, with and without --ha. Named off and unnamed render the same values and the
// same OpenTofu variables, and neither carries a shardGroups block.
func TestOffIsInertOnEverySize(t *testing.T) {
	for _, size := range sizeNames {
		for _, ha := range []bool{false, true} {
			mk := func(level string) *State {
				st := persistenceState(ha, size == SizeCompact, "default")
				if size != SizeCompact {
					st.Size = size
				}
				st.Sharding = level
				return st
			}
			unnamed, named := mk(""), mk(ShardingOff)
			if !reflect.DeepEqual(helmValues(unnamed), helmValues(named)) {
				t.Errorf("size %s ha=%v: --sharding off moved the chart values", size, ha)
			}
			if !reflect.DeepEqual(infraVars(unnamed), infraVars(named)) {
				t.Errorf("size %s ha=%v: --sharding off moved the OpenTofu variables", size, ha)
			}
			if _, found := lookupPath(helmValues(named), splitPath(shardGroupsValuesPath)); found {
				t.Errorf("size %s ha=%v: --sharding off rendered a shardGroups block", size, ha)
			}
		}
	}
}

// TestScaleShapeIsFrozen holds scale's shard shape (count, suffixes, placement, seed) to a fixture.
//
// 🔴 THERE IS NO FLAG TO REGENERATE THE FIXTURE, AND THAT IS THE TEST. A level's shape is frozen per
// name: an instance's tenants were placed into its groups at install and nothing moves them, so a
// release that renders a different shape for the same name would be judged against the deployed
// block by the upgrade and refused, or, worse, accepted by a judge that had been changed with it.
// While scale is provisional (not accepted), the fixture may be edited by hand along with the table;
// once scale is accepted, a different shape is a new level name.
func TestScaleShapeIsFrozen(t *testing.T) {
	l := shardLevels[ShardingScale]
	got, err := json.MarshalIndent(l.shardGroupsBlock(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "shard_level_scale.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(append(got, '\n')) != string(want) {
		t.Errorf("scale's shard shape moved from testdata/shard_level_scale.json.\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestScaleSeedCoversEveryVPartition holds the seed to the placement table it seeds: one weight per
// group, each group seeded with at least one vpartition, and every vpartition of user-management's
// table assigned. A seed that summed short would leave holes, which user-management refuses.
func TestScaleSeedCoversEveryVPartition(t *testing.T) {
	if vpartitions != partition.DefaultP {
		t.Fatalf("the level table seeds %d vpartitions; user-management's table has %d", vpartitions, partition.DefaultP)
	}
	for name, l := range shardLevels {
		if len(l.SeedWeights) != l.Count {
			t.Errorf("%s: %d seed weights for %d groups", name, len(l.SeedWeights), l.Count)
		}
		sum := 0
		for g, w := range l.SeedWeights {
			if w < 1 {
				t.Errorf("%s: group %d is seeded with %d vpartitions", name, g, w)
			}
			sum += w
		}
		if sum != vpartitions {
			t.Errorf("%s: the seed covers %d vpartitions, not %d", name, sum, vpartitions)
		}
	}
}

// TestScaleIsKnownButNotAccepted: until scale's qualification run passes, the name is known (so a
// declaration recording it reads as scale, not as unknown) and is not accepted.
func TestScaleIsKnownButNotAccepted(t *testing.T) {
	if err := ValidateSharding(ShardingScale); err != nil {
		t.Fatalf("scale is not a known level: %v", err)
	}
	if shardLevels[ShardingScale].Accepted {
		t.Fatal("scale is accepted; it may be accepted only once its qualification run has passed")
	}
	for _, ok := range []string{"", ShardingOff} {
		if err := ValidateSharding(ok); err != nil {
			t.Errorf("ValidateSharding(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{ShardingCustom, "heavy", "Scale", "off "} {
		err := ValidateSharding(bad)
		if err == nil || !strings.Contains(err.Error(), "unknown sharding level") {
			t.Errorf("ValidateSharding(%q) = %v, want an unknown-level refusal", bad, err)
		}
	}
}

// TestScaleRendersItsBlock: a scale instance's chart values carry the level's block at the path the
// services read, and nothing for an off instance on the same state.
func TestScaleRendersItsBlock(t *testing.T) {
	st := persistenceState(false, false, "default")
	st.Size = SizePerf20k
	st.Sharding = ShardingScale
	got, found := lookupPath(helmValues(st), splitPath(shardGroupsValuesPath))
	if !found {
		t.Fatal("a scale instance rendered no shardGroups block")
	}
	l := shardLevels[ShardingScale]
	if !reflect.DeepEqual(got, l.shardGroupsBlock()) {
		t.Errorf("rendered %v, want the level's block %v", got, l.shardGroupsBlock())
	}
}
