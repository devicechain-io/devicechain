// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/stretchr/testify/require"
)

// A sharded stream is counted once per group that can share a server. Only a placement of
// exactly one server-name tag pins a group; anything else counts on every server.
func TestShardCopiesPerServerFollowsPlacement(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		tags map[int][]string
		want int
	}{
		{"no placement: every group may share one server", nil, 3},
		{"one group per server", map[int][]string{0: {"dc-nats-0"}, 1: {"dc-nats-1"}, 2: {"dc-nats-2"}}, 1},
		{"two groups on one server", map[int][]string{0: {"dc-nats-0"}, 1: {"dc-nats-1"}, 2: {"dc-nats-1"}}, 2},
		{"one group unpinned joins the busiest", map[int][]string{0: {"dc-nats-0"}, 1: {"dc-nats-1"}}, 2},
		{"a tag that is not a server name pins nothing", map[int][]string{0: {"dc-nats-0"}, 1: {"zone-a"}, 2: {"zone-b"}}, 3},
		{"two tags pin nothing", map[int][]string{0: {"dc-nats-0", "zone-a"}, 1: {"dc-nats-1"}, 2: {"dc-nats-2"}}, 2},
	} {
		g := &config.NatsShardGroupsConfiguration{Count: 3, Suffixes: []string{"inbound-events"}, PlacementTags: tc.tags}
		if got := shardCopiesPerServer(g); got != tc.want {
			t.Errorf("%s: %d copies per server, want %d", tc.name, got, tc.want)
		}
	}
}

// r33a: a three-group perf-20k install was refused on the preset's own 40Gi volume, because the
// three pinned group copies were summed against one server. The chart pins one group per server,
// so the install fits. The NEGATIVE CONTROL is the same document with its placement removed,
// which is the old reading and must still be refused: the relaxation is the placement's doing.
func TestAThreeGroupPerf20kInstallFitsThePresetVolume(t *testing.T) {
	t.Parallel()
	st := perfStateNamed(SizePerf20k, false)
	st.ValuesOverlay = shardGroupsValues(shardBlock())
	n := &renderAuthoredConfig(t, st).Infrastructure.Nats
	require.NotNil(t, n.ShardGroups)
	require.Len(t, n.ShardGroups.PlacementTags, 3, "the chart pins every group")

	ceiling, err := derivedMaxFileStore(sizePresets[SizePerf20k].JetStreamStorage)
	require.NoError(t, err)
	require.NoError(t, checkJetStreamReservation(n, ceiling, "the 40Gi volume", true))

	// The whole precheck, on the volume the planned apply provisions.
	planned := *st
	planned.LiveVolumes.JetStream = sizePresets[SizePerf20k].JetStreamStorage
	require.NoError(t, precheckJetStreamReservation(t.Context(), &planned))

	unpinned := *n
	unpinned.ShardGroups = &config.NatsShardGroupsConfiguration{Count: 3, Suffixes: n.ShardGroups.Suffixes}
	require.Error(t, checkJetStreamReservation(&unpinned, ceiling, "the 40Gi volume", true),
		"with nothing proving the groups apart, the three copies must still be counted on one server")
}

// renderAuthoredConfig renders the document precheckJetStreamReservation judges.
func renderAuthoredConfig(t *testing.T, st *State) *config.InstanceConfiguration {
	t.Helper()
	ch, err := loadEmbeddedChart()
	require.NoError(t, err)
	raw, err := renderInstanceConfigDocument(t.Context(), ch, renderOnlyAuthoring(st))
	require.NoError(t, err)
	require.NotNil(t, raw)
	doc, err := loadInstanceConfigDocument(raw)
	require.NoError(t, err)
	return doc
}

// Still fail closed: a volume too small for ONE server's share is refused, and the refusal
// says the number is per server.
func TestAnUndersizedShardGroupVolumeIsStillRefused(t *testing.T) {
	t.Parallel()
	st := perfStateNamed(SizePerf20k, false)
	st.ValuesOverlay = shardGroupsValues(shardBlock())
	st.LiveVolumes.JetStream = "30Gi"
	err := precheckJetStreamReservation(t.Context(), st)
	require.Error(t, err)
	for _, want := range []string{"90% of its 30Gi", "per broker server", "one server's share", "Nothing has been changed"} {
		require.Contains(t, err.Error(), want)
	}
}

// The planned volume is the one the apply provisions: the live one, compact's own -var, the
// operator's stated size, then the preset.
func TestPlannedJetStreamVolumeHonoursTheOperator(t *testing.T) {
	orig := operatorStatedValue
	t.Cleanup(func() { operatorStatedValue = orig })
	stated := func(v string, readable bool) {
		operatorStatedValue = func(_ *State, name string) (string, bool, bool) {
			if name != "nats_jetstream_storage" {
				return "", false, false
			}
			return v, true, readable
		}
	}

	perf := perfStateNamed(SizePerf20k, false)
	stated("48Gi", true)
	got, unread := plannedJetStreamVolume(perf)
	require.Equal(t, "48Gi", got)
	require.Empty(t, unread)

	live := *perf
	live.LiveVolumes.JetStream = "36Gi"
	got, _ = plannedJetStreamVolume(&live)
	require.Equal(t, "36Gi", got, "an apply never resizes a live volume")

	got, _ = plannedJetStreamVolume(compactState(true))
	require.Equal(t, compact.JetStreamStorage, got, "compact's -var wins over a stated size")

	stated("", false)
	got, unread = plannedJetStreamVolume(perf)
	require.Empty(t, got)
	require.NotEmpty(t, unread)

	operatorStatedValue = func(*State, string) (string, bool, bool) { return "", false, false }
	got, _ = plannedJetStreamVolume(perf)
	require.Equal(t, sizePresets[SizePerf20k].JetStreamStorage, got)
}

// End to end through the step: the stated size is the one judged. A stated 1Gi is refused on
// its own number, which no preset or default could have produced.
func TestTheInfraStepJudgesTheStatedVolume(t *testing.T) {
	orig := operatorStatedValue
	t.Cleanup(func() { operatorStatedValue = orig })
	operatorStatedValue = func(_ *State, name string) (string, bool, bool) {
		if name == "nats_jetstream_storage" {
			return "1Gi", true, true
		}
		return "", false, false
	}
	st := &State{Instance: "prod", Profile: "default", Values: map[string]string{},
		ValuesOverlay: shardGroupsValues(shardBlock())}
	err := stepInfraApply(t.Context(), st)
	require.ErrorContains(t, err, "90% of its 1Gi JetStream volume")
}

// statedValueIn reads the declaration OpenTofu would use: later files win, JSON is read as JSON,
// and anything but a plain string is stated but unreadable.
func TestStatedValueInFollowsTofuPrecedence(t *testing.T) {
	t.Setenv("TF_VAR_nats_jetstream_storage", "")
	const name = "nats_jetstream_storage"
	write := func(t *testing.T, dir, file, body string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte(body), 0o600))
	}

	dir := t.TempDir()
	_, stated, _ := statedValueIn(dir, name)
	require.False(t, stated)

	write(t, dir, "terraform.tfvars", "other = \"x\"\nnats_jetstream_storage = \"44Gi\" # grown\n")
	v, stated, readable := statedValueIn(dir, name)
	require.True(t, stated && readable)
	require.Equal(t, "44Gi", v)

	write(t, dir, "terraform.tfvars.json", `{"nats_jetstream_storage": "46Gi"}`)
	write(t, dir, "b.auto.tfvars.json", `{"nats_jetstream_storage": "50Gi"}`)
	write(t, dir, "a.auto.tfvars", "nats_jetstream_storage = \"48Gi\"\n")
	v, _, _ = statedValueIn(dir, name)
	require.Equal(t, "50Gi", v, "the last auto file by name wins")

	write(t, dir, "c.auto.tfvars", "nats_jetstream_storage = local.size\n")
	_, stated, readable = statedValueIn(dir, name)
	require.True(t, stated)
	require.False(t, readable, "an expression is not evaluated")

	env := t.TempDir()
	t.Setenv("TF_VAR_nats_jetstream_storage", "52Gi")
	v, stated, readable = statedValueIn(env, name)
	require.True(t, stated && readable)
	require.Equal(t, "52Gi", v)
	// A file overrides the environment, as in OpenTofu.
	write(t, env, "terraform.tfvars", "nats_jetstream_storage = \"54Gi\"\n")
	v, _, _ = statedValueIn(env, name)
	require.Equal(t, "54Gi", v)
	require.False(t, strings.Contains(v, "52"))
}
