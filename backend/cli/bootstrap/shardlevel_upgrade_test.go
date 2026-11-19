// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"errors"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
)

// upgradeOf is the state an upgrade of a level instance renders from: the declaration bootstrap
// wrote, read back through applyDeclaration exactly as hydrateUpgradeState does.
func upgradeOf(t *testing.T, level string) *State {
	t.Helper()
	spec := InstanceSpecFrom(scaleRunAt(level), ClusterBinding{}, "local")
	st := &State{Instance: "prod", Values: map[string]string{}}
	applyDeclaration(st, spec)
	return st
}

func scaleRunAt(level string) *State {
	st := scaleRun()
	st.Sharding = level
	return st
}

// deployedAs stubs the deployed configuration the judge reads.
func deployedAs(t *testing.T, cfg *config.NatsShardGroupsConfiguration, err error) *int {
	t.Helper()
	prev := readDeployedShardGroups
	t.Cleanup(func() { readDeployedShardGroups = prev })
	calls := 0
	readDeployedShardGroups = func(context.Context, string, string) (*config.NatsShardGroupsConfiguration, error) {
		calls++
		return cfg, err
	}
	return &calls
}

// theDeployedScaleBlock is what a scale instance's services run: the block as the chart rendered it.
func theDeployedScaleBlock(t *testing.T) *config.NatsShardGroupsConfiguration {
	t.Helper()
	cfg := renderAuthoredConfig(t, scaleRun()).Infrastructure.Nats.ShardGroups
	require.NotNil(t, cfg)
	return cfg
}

// 🔑 THE JUDGE, ON ITS OWN, WITH NO CONFLICTING OVERLAY (the overlay guard would mask it). The
// declaration and the frozen table are intact; the deployed block drifted. Every drift is refused,
// and the control (deployed == rendered) passes, so the refusal reads the comparison and not a
// bystander.
func TestUpgradeRefusesLevelChange(t *testing.T) {
	st := upgradeOf(t, ShardingScale)
	require.Equal(t, ShardingScale, st.Sharding, "the upgrade did not read the level from the declaration")

	good := theDeployedScaleBlock(t)
	deployedAs(t, good, nil)
	require.NoError(t, judgeShardingUpgrade(t.Context(), st), "control: the deployed block is the level's own")

	drifts := map[string]func(*config.NatsShardGroupsConfiguration){
		"count 2": func(c *config.NatsShardGroupsConfiguration) {
			c.Count = 2
			delete(c.PlacementTags, 2)
			c.Seed.Weights = []int{32, 32}
		},
		"inbound only": func(c *config.NatsShardGroupsConfiguration) { c.Suffixes = []string{"inbound-events"} },
		"tag moved":    func(c *config.NatsShardGroupsConfiguration) { c.PlacementTags[1] = []string{"dc-nats-2"} },
		"seed missing": func(c *config.NatsShardGroupsConfiguration) { c.Seed = nil },
		"other seed":   func(c *config.NatsShardGroupsConfiguration) { c.Seed.Weights = []int{21, 22, 21} },
	}
	for name, drift := range drifts {
		t.Run(name, func(t *testing.T) {
			d := cloneShardConfig(good)
			drift(d)
			deployedAs(t, d, nil)
			err := judgeShardingUpgrade(t.Context(), st)
			require.ErrorContains(t, err, "the scale level's shard shape is fixed at install")
			require.ErrorContains(t, err, "Nothing has been changed")
		})
	}
	t.Run("nothing deployed", func(t *testing.T) {
		deployedAs(t, nil, nil)
		require.ErrorContains(t, judgeShardingUpgrade(t.Context(), st), "runs no shardGroups block")
	})
	t.Run("deployed unreadable", func(t *testing.T) {
		deployedAs(t, nil, errors.New("forbidden"))
		require.ErrorContains(t, judgeShardingUpgrade(t.Context(), st), "forbidden")
	})
	t.Run("suffix order is not a difference", func(t *testing.T) {
		d := cloneShardConfig(good)
		d.Suffixes = []string{d.Suffixes[1], d.Suffixes[0]}
		deployedAs(t, d, nil)
		require.NoError(t, judgeShardingUpgrade(t.Context(), st))
	})
}

// Rule 3, and the rest of the bootstrap table: an overlay that would take the level's block or its
// replication, or a fail-closed switch the level was refused with, is refused on upgrade too, carried
// or freshly typed. The control is the same upgrade with no such values.
func TestUpgradeRefusesShardGroupsOverlayOnScale(t *testing.T) {
	deployedAs(t, theDeployedScaleBlock(t), nil)
	for name, tc := range map[string]struct {
		mutate func(*State)
		want   string
	}{
		"shardGroups count 2": {func(st *State) {
			st.ValuesOverlay = shardGroupsValues(map[string]interface{}{"count": 2, "suffixes": []interface{}{"inbound-events", "resolved-events"}})
		}, "owns the shardGroups block"},
		"shardGroups same block": {func(st *State) {
			st.ValuesOverlay = shardGroupsValues(map[string]interface{}{"count": 3, "suffixes": []interface{}{"inbound-events", "resolved-events"}})
		}, "owns the shardGroups block"},
		"streamReplicas": {func(st *State) {
			st.ValuesOverlay = map[string]interface{}{"instance": map[string]interface{}{"config": map[string]interface{}{
				"infrastructure": map[string]interface{}{"nats": map[string]interface{}{"streamReplicas": 1}}}}}
		}, "owns stream replication"},
		"fail-closed on": {func(st *State) { on := true; st.MqttFailClosed = &on }, "with --mqtt-fail-closed has not been measured"},
	} {
		t.Run(name, func(t *testing.T) {
			st := upgradeOf(t, ShardingScale)
			tc.mutate(st)
			require.ErrorContains(t, judgeShardingUpgrade(t.Context(), st), tc.want)
		})
	}
	require.NoError(t, judgeShardingUpgrade(t.Context(), upgradeOf(t, ShardingScale)), "control")
}

// custom and off are not judged here at all: the deployed configuration is not even read, so every
// rule they have today runs after this exactly as it did. custom is reported as custom.
func TestCustomUpgradeRulesUnchanged(t *testing.T) {
	calls := deployedAs(t, nil, errors.New("the level judge must not read the deployed block for custom or off"))

	custom := &State{Instance: "prod", Profile: "default", Values: map[string]string{},
		ShardGroups: &dcv1beta1.ShardGroupsSpec{Count: 3, Suffixes: []string{"inbound-events"}}}
	word, err := shardingOfRun(custom)
	require.NoError(t, err)
	require.Equal(t, ShardingCustom, word)
	require.NoError(t, judgeShardingUpgrade(t.Context(), custom))

	viaOverlay := &State{Instance: "prod", Profile: "default", Values: map[string]string{},
		ValuesOverlay: shardGroupsValues(shardBlock())}
	word, err = shardingOfRun(viaOverlay)
	require.NoError(t, err)
	require.Equal(t, ShardingCustom, word)

	off := upgradeOf(t, "")
	word, err = shardingOfRun(off)
	require.NoError(t, err)
	require.Equal(t, ShardingOff, word)
	require.NoError(t, judgeShardingUpgrade(t.Context(), off))
	require.Zero(t, *calls)

	scale := upgradeOf(t, ShardingScale)
	word, err = shardingOfRun(scale)
	require.NoError(t, err)
	require.Equal(t, ShardingScale, word)
}

// The judge's call site: an upgrade's shardGroups precheck runs it, so a drifted level instance is
// refused there, before the custom rules (which a same-count drift would pass) are reached.
func TestShardGroupsUpgradePrecheckRunsTheLevelJudge(t *testing.T) {
	prevInfra := readLiveInfra
	t.Cleanup(func() { readLiveInfra = prevInfra })
	three := int32(3)
	readLiveInfra = func(context.Context, string, string) (liveInfra, error) {
		return liveInfra{Broker: &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{Replicas: &three}}}, nil
	}
	good := theDeployedScaleBlock(t)
	deployedAs(t, good, nil)
	require.NoError(t, precheckShardGroupsUpgrade(t.Context(), upgradeOf(t, ShardingScale)))

	d := cloneShardConfig(good)
	d.Seed = nil
	deployedAs(t, d, nil)
	require.ErrorContains(t, precheckShardGroupsUpgrade(t.Context(), upgradeOf(t, ShardingScale)), "shard shape is fixed at install")
}

// An upgrade reads a recorded level it does not know as a refusal, never as off or custom.
func TestUpgradeRefusesAnUnknownRecordedLevel(t *testing.T) {
	inst := &dcv1beta1.Instance{Spec: InstanceSpecFrom(scaleRun(), ClusterBinding{}, "local")}
	inst.Spec.Sharding = "heavy"
	st := &State{Instance: "prod", Values: map[string]string{}}
	require.ErrorContains(t, applyUpgradeDeclaration(st, inst, UpgradeOptions{Instance: "prod"}), `sharding level "heavy" is unknown`)

	edited := &dcv1beta1.Instance{Spec: InstanceSpecFrom(scaleRun(), ClusterBinding{}, "local")}
	edited.Spec.ShardGroups.Count = 2
	require.ErrorContains(t, applyUpgradeDeclaration(st, edited, UpgradeOptions{Instance: "prod"}), "edited by hand")
}

func cloneShardConfig(c *config.NatsShardGroupsConfiguration) *config.NatsShardGroupsConfiguration {
	out := *c
	out.Suffixes = append([]string(nil), c.Suffixes...)
	out.PlacementTags = map[int][]string{}
	for g, tags := range c.PlacementTags {
		out.PlacementTags[g] = append([]string(nil), tags...)
	}
	if c.Seed != nil {
		out.Seed = &config.NatsShardGroupsSeed{Weights: append([]int(nil), c.Seed.Weights...)}
	}
	return &out
}
