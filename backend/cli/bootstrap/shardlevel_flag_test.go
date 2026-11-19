// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// scaleRun is a --sharding scale bootstrap with nothing that conflicts with the level: the state every
// refusal row starts from, and its negative control.
func scaleRun() *State {
	return &State{Instance: "prod", Profile: "default", Values: map[string]string{}, Size: SizePerf20k, Sharding: ShardingScale}
}

// withScaleAccepted stands in for the release that accepts scale, so a test can show a run gets all
// the way through the judge.
func withScaleAccepted(t *testing.T) {
	t.Helper()
	was := shardLevels[ShardingScale]
	accepted := was
	accepted.Accepted = true
	shardLevels[ShardingScale] = accepted
	t.Cleanup(func() { shardLevels[ShardingScale] = was })
}

const notYet = "--sharding scale is not yet available: it requires the r34 qualification"

// 🔑 scale is refused today, with a message saying what it waits for, and only scale: off (named or
// not) passes.
func TestScaleIsRefusedUntilItsQualification(t *testing.T) {
	err := CheckShardingAgainstCluster(scaleRun())
	require.ErrorContains(t, err, notYet)
	require.ErrorContains(t, err, "Nothing has been changed")

	for _, level := range []string{"", ShardingOff} {
		st := scaleRun()
		st.Sharding = level
		require.NoError(t, CheckShardingAgainstCluster(st), "level %q", level)
		require.Equal(t, "", st.Sharding, "off is recorded as no level")
		require.Equal(t, level != "", st.ShardingGiven)
	}

	st := scaleRun()
	st.Sharding = "heavy"
	require.ErrorContains(t, CheckShardingAgainstCluster(st), `unknown sharding level "heavy"`)
}

// 🔑 THE REFUSAL TABLE. Each row refuses its combination, and each row's control (the very same run
// without the conflicting part) gets past every combination check: it reaches the "not yet" gate
// today, and passes outright once scale is accepted.
func TestShardingRefusesEveryCombinationItCannotBeBuiltWith(t *testing.T) {
	rows := []struct {
		name     string
		conflict func(*State)
		want     string
	}{
		{"compact cluster", func(st *State) { st.Compact, st.Size = true, "" }, "this cluster was installed --compact"},
		{"ha cluster", func(st *State) { st.HA = true }, "this cluster was installed --ha"},
		{"size perf-16k", func(st *State) { st.Size = SizePerf16k }, "measured only with --size perf-20k, and this run builds size perf-16k"},
		{"size base", func(st *State) { st.Size = "" }, "measured only with --size perf-20k, and this run builds size base"},
		{"shardGroups in --values", func(st *State) { st.ValuesOverlay = shardGroupsValues(shardBlock()) }, "owns the shardGroups block"},
		{"streamReplicas 1 in --values", func(st *State) {
			st.ValuesOverlay = map[string]interface{}{"instance": map[string]interface{}{"config": map[string]interface{}{
				"infrastructure": map[string]interface{}{"nats": map[string]interface{}{"streamReplicas": 1}}}}}
		}, "owns stream replication"},
		{"mqtt fail-closed", func(st *State) { on := true; st.MqttFailClosed = &on }, "with --mqtt-fail-closed has not been measured"},
		{"detectShards past the ceiling", func(st *State) {
			st.ValuesOverlay = map[string]interface{}{"functionalAreas": map[string]interface{}{
				"event-processing": map[string]interface{}{"config": map[string]interface{}{"detectShards": 22}}}}
		}, "detectShards 22 gives 66 detection workers"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			st := scaleRun()
			row.conflict(st)
			err := CheckShardingAgainstCluster(st)
			require.ErrorContains(t, err, row.want)
			require.ErrorContains(t, err, "Nothing has been changed")
			require.NotContains(t, err.Error(), "not yet available", "the combination must be named, not only 'not yet'")

			require.ErrorContains(t, CheckShardingAgainstCluster(scaleRun()), notYet, "control")
		})
	}
	t.Run("controls pass once scale is accepted", func(t *testing.T) {
		withScaleAccepted(t)
		require.NoError(t, CheckShardingAgainstCluster(scaleRun()))
		for _, row := range rows {
			st := scaleRun()
			row.conflict(st)
			require.ErrorContains(t, CheckShardingAgainstCluster(st), row.want, row.name)
		}
	})
}

// The rows a level allows: any profile, extra areas, an explicit fail-closed off, and detectShards
// right at the ceiling. Each reaches the acceptance gate, not a combination refusal.
func TestShardingAllowsWhatALevelComposesWith(t *testing.T) {
	withScaleAccepted(t)
	for name, mutate := range map[string]func(*State){
		"profile full":      func(st *State) { st.Profile = "full" },
		"profile telemetry": func(st *State) { st.Profile = "telemetry" },
		"enable-area":       func(st *State) { st.EnableAreas = []string{"lwm2m-ingest"} },
		"fail-closed off":   func(st *State) { off := false; st.MqttFailClosed = &off },
		"detectShards 21": func(st *State) {
			st.ValuesOverlay = map[string]interface{}{"functionalAreas": map[string]interface{}{
				"event-processing": map[string]interface{}{"config": map[string]interface{}{"detectShards": 21}}}}
		},
	} {
		st := scaleRun()
		mutate(st)
		require.NoError(t, CheckShardingAgainstCluster(st), name)
	}
}

// detectShards matters only where event-processing runs: on a profile without it, a K that would
// break the ceiling is not this level's concern.
func TestShardingDetectCeilingOnlyWithEventProcessing(t *testing.T) {
	withScaleAccepted(t)
	st := scaleRun()
	st.Profile = "ingest-only"
	st.ValuesOverlay = map[string]interface{}{"functionalAreas": map[string]interface{}{
		"event-processing": map[string]interface{}{"config": map[string]interface{}{"detectShards": 64}}}}
	require.NoError(t, CheckShardingAgainstCluster(st))
	st.Profile = "default"
	require.ErrorContains(t, CheckShardingAgainstCluster(st), "detectShards 64 gives 192 detection workers")
}

// --sharding off and a raw block contradict each other; no --sharding and a raw block is custom, the
// expert escape hatch, unchanged.
func TestShardingOffRefusesARawBlockAndCustomStaysOpen(t *testing.T) {
	st := scaleRun()
	st.Sharding = ShardingOff
	st.ValuesOverlay = shardGroupsValues(shardBlock())
	require.ErrorContains(t, CheckShardingAgainstCluster(st), "--sharding off and a shardGroups block")

	custom := scaleRun()
	custom.Sharding = ""
	custom.ValuesOverlay = shardGroupsValues(shardBlock())
	require.NoError(t, CheckShardingAgainstCluster(custom), "no --sharding with a raw block is custom")

	// The same contradiction when the block is the one a re-run carries from the declaration.
	recorded := scaleRun()
	recorded.Sharding = ShardingOff
	recorded.ShardGroups = &dcv1beta1.ShardGroupsSpec{Count: 3, Suffixes: []string{"inbound-events"}}
	require.ErrorContains(t, CheckShardingAgainstCluster(recorded), "--sharding off and a shardGroups block")

	plain := scaleRun()
	plain.Sharding = ShardingOff
	require.NoError(t, CheckShardingAgainstCluster(plain), "control: --sharding off with no block")
}

// The level-owned keys are refused in an overlay at any level. capturePlacementTags is not a key of
// this release at all, so the strict reading refuses it too; the owned-key reason is what an
// operator sees once it is.
func TestOverlayCannotSetALevelOwnedKey(t *testing.T) {
	block := map[string]interface{}{"count": 3, "suffixes": []interface{}{"inbound-events"},
		"capturePlacementTags": []interface{}{"dc-nats-0"}}
	require.Error(t, ValidateValuesOverlay(shardGroupsValues(block)))
	err := refuseOverlayKeys(shardGroupsValues(map[string]interface{}{"capturePlacementTags": []interface{}{"dc-nats-0"}}), "--values f")
	require.ErrorContains(t, err, "capture stream's placement belongs to a sharding level")
	delete(block, "capturePlacementTags")
	require.NoError(t, ValidateValuesOverlay(shardGroupsValues(block)), "control")
}

// A re-run keeps the recorded level and refuses another, custom included.
func TestShardingReRunAdoptsTheRecordedLevel(t *testing.T) {
	scale := InstanceSpecFrom(func() *State { st := scaleRun(); return st }(), ClusterBinding{}, "local")
	require.Equal(t, ShardingScale, scale.Sharding)
	custom := dcv1beta1.InstanceSpec{ShardGroups: &dcv1beta1.ShardGroupsSpec{Count: 3, Suffixes: []string{"inbound-events"}}}
	off := dcv1beta1.InstanceSpec{}

	for _, tc := range []struct {
		name     string
		recorded dcv1beta1.InstanceSpec
		flag     string // "" = not given
		adopted  string
		refused  string
	}{
		{"no flag adopts scale", scale, "", ShardingScale, ""},
		{"no flag adopts off", off, "", "", ""},
		{"no flag on custom", custom, "", "", ""},
		{"scale again", scale, ShardingScale, ShardingScale, ""},
		{"off again", off, ShardingOff, "", ""},
		{"scale over off", off, ShardingScale, "", "bootstrapped with sharding off and this run names scale"},
		{"off over scale", scale, ShardingOff, "", "bootstrapped with sharding scale and this run names off"},
		{"off over custom", custom, ShardingOff, "", "bootstrapped with sharding custom and this run names off"},
		{"scale over custom", custom, ShardingScale, "", "bootstrapped with sharding custom and this run names scale"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := scaleRun()
			st.Sharding = tc.flag
			st.ShardingGiven = tc.flag != ""
			if tc.flag == ShardingOff {
				st.Sharding = ""
			}
			err := adoptRecordedSharding(st, tc.recorded)
			if tc.refused != "" {
				require.ErrorContains(t, err, tc.refused)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.adopted, st.Sharding)
		})
	}
}

// The record: off writes nothing, a level writes its name and its own block, and the read-back
// judge refuses a pruned field.
func TestShardingIsRecordedAndVerified(t *testing.T) {
	offSpec := InstanceSpecFrom(&State{Instance: "prod", Profile: "default", Values: map[string]string{}}, ClusterBinding{}, "local")
	require.Empty(t, offSpec.Sharding)
	require.Nil(t, offSpec.ShardGroups)

	spec := InstanceSpecFrom(scaleRun(), ClusterBinding{}, "local")
	spec.Provider = "local"
	require.Equal(t, ShardingScale, spec.Sharding)
	require.NotNil(t, spec.ShardGroups)
	require.NoError(t, ValidateInstanceSpec(spec), "a level's own record must read back")
	require.Equal(t, ShardingScale, ShardingOf(spec))

	require.NoError(t, verifyShardingRecorded(ShardingScale, ShardingScale))
	require.NoError(t, verifyShardingRecorded("", ""))
	require.ErrorContains(t, verifyShardingRecorded(ShardingScale, ""), "does not record sharding")
}

// A recorded level is held to this build's table: unknown is a newer dcctl's, a block that is not the
// level's own is a hand edit. off and custom are never written, only derived.
func TestARecordedLevelIsHeldToTheTable(t *testing.T) {
	good := InstanceSpecFrom(scaleRun(), ClusterBinding{}, "local")
	require.NoError(t, validateRecordedSharding(good))

	unknown := good
	unknown.Sharding = "heavy"
	require.ErrorContains(t, validateRecordedSharding(unknown), `sharding level "heavy" is unknown to this dcctl`)
	// And through the judge every read-back runs, which is what an upgrade and a re-run see.
	require.NoError(t, ValidateInstanceSpec(good))
	require.ErrorContains(t, ValidateInstanceSpec(unknown), `sharding level "heavy" is unknown to this dcctl`)

	for name, edit := range map[string]func(*dcv1beta1.ShardGroupsSpec){
		"count":    func(g *dcv1beta1.ShardGroupsSpec) { g.Count = 2; delete(g.PlacementTags, "2") },
		"suffix":   func(g *dcv1beta1.ShardGroupsSpec) { g.Suffixes = []string{"inbound-events"} },
		"tag":      func(g *dcv1beta1.ShardGroupsSpec) { g.PlacementTags["1"] = dcv1beta1.ShardPlacementTags{"dc-nats-2"} },
		"no tags":  func(g *dcv1beta1.ShardGroupsSpec) { g.PlacementTags = nil },
		"no block": nil,
	} {
		spec := good
		if edit == nil {
			spec.ShardGroups = nil
		} else {
			spec.ShardGroups = good.ShardGroups.DeepCopy()
			edit(spec.ShardGroups)
		}
		require.ErrorContains(t, validateRecordedSharding(spec), "edited by hand", name)
	}

	require.Equal(t, ShardingOff, ShardingOf(dcv1beta1.InstanceSpec{}))
	require.Equal(t, ShardingCustom, ShardingOf(dcv1beta1.InstanceSpec{ShardGroups: &dcv1beta1.ShardGroupsSpec{Count: 2}}))
}

// 🔑 THE NODE CHECK IS FED THE LEVEL'S BROKER ROW, NOT THE SIZE'S. scale runs three servers at 3 CPU /
// 4Gi each; perf-20k's single server asks 6 / 6Gi, which no 4-vCPU node holds.
func TestTheNodeCheckReadsTheLevelsBrokerRow(t *testing.T) {
	st := scaleRun()
	req, unchecked, err := brokerRequestFor(st, func(string) bool { return false })
	require.NoError(t, err)
	require.Empty(t, unchecked)
	require.Equal(t, "3", req.CPUText)
	require.Equal(t, "4Gi", req.MemText)
	ha := brokerTopology(st)
	require.Equal(t, 3, ha.ServerReplicas)

	node := func(cpu, mem string) corev1.Node {
		return corev1.Node{Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem)}}}
	}
	fits := []corev1.Node{node("3", "4Gi"), node("3920m", "13Gi"), node("4", "16Gi")}
	require.NoError(t, schedulableShortfall(ha, fits, req), "three t2d-standard-4-sized nodes hold the level's brokers")
	require.Error(t, schedulableShortfall(ha, fits[:2], req), "two nodes cannot hold three servers under the spread")
	require.Error(t, schedulableShortfall(ha, []corev1.Node{node("3", "4Gi"), node("2900m", "16Gi"), node("4", "16Gi")}, req),
		"a node short of the level's CPU request is not a broker node")

	// The control: the same state without the level asks the size's broker, and is one server.
	unsharded := scaleRun()
	unsharded.Sharding = ""
	req, _, err = brokerRequestFor(unsharded, func(string) bool { return false })
	require.NoError(t, err)
	require.Equal(t, "6", req.CPUText)
	require.Equal(t, 1, brokerTopology(unsharded).ServerReplicas)
}

// The level's broker row reaches the OpenTofu variables too, and an operator-stated one still wins.
func TestTheLevelsBrokerRowIsRendered(t *testing.T) {
	vars := infraVars(scaleRun())
	for _, want := range []string{"nats_cpu_request=3", "nats_memory_request=4Gi", "nats_memory_limit=10Gi",
		"nats_jetstream_storage=40Gi", "nats_cluster_replicas=3"} {
		require.Contains(t, vars, want)
	}
	require.NotContains(t, vars, "nats_cpu_request=6")
}

// JetStream: the level's volume holds one server's share of its pinned groups, and a reservation
// past it is still refused.
func TestTheLevelsJetStreamVolumeHoldsOneServersShare(t *testing.T) {
	st := scaleRun()
	n := &renderAuthoredConfig(t, st).Infrastructure.Nats
	require.NotNil(t, n.ShardGroups)
	ceiling, err := derivedMaxFileStore(shardLevels[ShardingScale].JetStreamStorage)
	require.NoError(t, err)
	require.NoError(t, checkJetStreamReservation(n, ceiling, "the level's volume", true))
	small, err := derivedMaxFileStore("8Gi")
	require.NoError(t, err)
	require.Error(t, checkJetStreamReservation(n, small, "an 8Gi volume", true), "control: an undersized volume is refused")
}

// declareWith runs the claim step against a stubbed declaration store, returning what it wrote (nil
// when it wrote nothing) and its error. recorded is the declaration already in the cluster (nil for a
// first bootstrap); readBack edits what the cluster hands back after the write.
func declareWith(t *testing.T, st *State, recorded *dcv1beta1.InstanceSpec, readBack func(*dcv1beta1.InstanceSpec)) (*dcv1beta1.InstanceSpec, error) {
	t.Helper()
	prevRead, prevWrite, prevNodes := readInstanceDeclaration, writeInstanceDeclaration, listNodes
	t.Cleanup(func() { readInstanceDeclaration, writeInstanceDeclaration, listNodes = prevRead, prevWrite, prevNodes })
	current := recorded
	var written *dcv1beta1.InstanceSpec
	readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
		if current == nil {
			return nil, nil
		}
		inst := aDeclaration(dcv1beta1.PhaseReady, "")
		inst.UID = "7f0d4f2e-0000-4000-8000-000000000001"
		inst.Spec = *current.DeepCopy()
		return inst, nil
	}
	writeInstanceDeclaration = func(_ context.Context, _, _ string, spec dcv1beta1.InstanceSpec, _ string, _ bool) error {
		w := spec.DeepCopy()
		written = w
		back := spec.DeepCopy()
		if readBack != nil {
			readBack(back)
		}
		current = back
		return nil
	}
	fit := corev1.Node{Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi")}}}
	listNodes = func(context.Context, string) ([]corev1.Node, error) { return []corev1.Node{fit, fit, fit}, nil }
	err := stepDeclareInstance(context.Background(), st)
	return written, err
}

func claimRun(level string) *State {
	return &State{Instance: "prod", Provider: "local", Profile: "default", Values: map[string]string{},
		Size: SizePerf20k, Sharding: level, ShardingGiven: level != ""}
}

// 🔑 THE CLAIM STEP JUDGES THE LEVEL BEFORE IT WRITES. A scale run that reached it (the command layer
// refuses it first; this is the second line, the one a re-run's adopted values are judged by) writes
// no declaration.
func TestTheClaimStepRefusesScaleBeforeWritingAnything(t *testing.T) {
	written, err := declareWith(t, claimRun(ShardingScale), nil, nil)
	require.ErrorContains(t, err, notYet)
	require.Nil(t, written, "a refused level wrote a declaration")

	// A re-run that names no level adopts the recorded one, and is judged by it.
	recorded := InstanceSpecFrom(claimRun(ShardingScale), ClusterBinding{}, "local")
	written, err = declareWith(t, claimRun(""), &recorded, nil)
	require.ErrorContains(t, err, notYet)
	require.Nil(t, written)

	// The control: off writes, and writes no level.
	written, err = declareWith(t, claimRun(""), nil, nil)
	require.NoError(t, err)
	require.NotNil(t, written)
	require.Empty(t, written.Sharding)
}

// Once a level is accepted: the claim step records it, a re-run keeps it, and a declaration that came
// back without it (a cluster whose Instance definition predates the field) is refused.
func TestTheClaimStepRecordsAndVerifiesTheLevel(t *testing.T) {
	withScaleAccepted(t)
	written, err := declareWith(t, claimRun(ShardingScale), nil, nil)
	require.NoError(t, err)
	require.Equal(t, ShardingScale, written.Sharding)
	require.NotNil(t, written.ShardGroups)

	rerun := claimRun("")
	written, err = declareWith(t, rerun, written, nil)
	require.NoError(t, err)
	require.Equal(t, ShardingScale, written.Sharding, "a flagless re-run dropped the level")

	_, err = declareWith(t, claimRun(ShardingScale), nil, func(s *dcv1beta1.InstanceSpec) { s.Sharding = "" })
	require.ErrorContains(t, err, "does not record sharding")

	// A re-run naming another level is refused with nothing written.
	recorded := InstanceSpecFrom(claimRun(ShardingScale), ClusterBinding{}, "local")
	written, err = declareWith(t, claimRun(ShardingOff), &recorded, nil)
	require.ErrorContains(t, err, "bootstrapped with sharding scale and this run names off")
	require.Nil(t, written)
}
