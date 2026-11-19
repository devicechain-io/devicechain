// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-microservice/streams"
)

// The --sharding flag, the spec.sharding record, and the combinations a level is never built with.
// See shardlevel.go for the levels themselves.

// ShardingNames is every name --sharding knows, for the command layer's help.
func ShardingNames() []string { return append([]string(nil), shardingNames...) }

// recordedSharding is what a declaration records for a level: the name, or nothing for off.
func recordedSharding(st *State) string {
	if l := shardLevelFor(st); l != nil {
		return l.Name
	}
	return ""
}

// ShardingOf is the word dcctl reports for a declaration: its recorded level, "custom" for one that
// names no level and carries a raw shardGroups block, and off otherwise. custom is derived here and
// never written.
func ShardingOf(spec dcv1beta1.InstanceSpec) string {
	switch {
	case spec.Sharding != "":
		return spec.Sharding
	case spec.ShardGroups != nil:
		return ShardingCustom
	default:
		return ShardingOff
	}
}

// CheckShardingAgainstCluster judges this run's --sharding once the cluster's shape (compact, HA)
// and the size are settled, and normalises it: off becomes the absence of a level, remembered as
// given. It runs again after a re-run has adopted the declaration (checkSharding from the claim
// step), so what is judged is what will be written.
func CheckShardingAgainstCluster(st *State) error {
	if err := ValidateSharding(st.Sharding); err != nil {
		return fmt.Errorf("--sharding: %w", err)
	}
	if st.Sharding != "" {
		st.ShardingGiven = true
	}
	if st.Sharding == ShardingOff {
		st.Sharding = ""
	}
	return checkSharding(st)
}

// checkSharding refuses every combination a level cannot be built with, before anything is written,
// and then a level this build knows but does not accept. The combinations are judged first so that
// a refused combination says what is wrong with it rather than only "not yet".
func checkSharding(st *State) error {
	if st.Sharding == "" {
		if st.ShardingGiven {
			return refuseOffWithRawShardGroups(st)
		}
		return nil
	}
	l := shardLevelFor(st)
	if l == nil {
		return ValidateSharding(st.Sharding)
	}
	if err := refuseShardingCombination(st, l); err != nil {
		return err
	}
	if !l.Accepted {
		return fmt.Errorf("--sharding %s is not yet available: it requires the r34 qualification (a measured run at "+
			"its rated throughput, within the latency target, on the exact shape it renders), which it has not passed. "+
			"Nothing has been changed. Build the instance with --sharding off (the default)", l.Name)
	}
	return nil
}

// refuseOffWithRawShardGroups: --sharding off and a shardGroups block are a contradiction. Leaving
// --sharding out is how a raw block (custom) is installed, and the only way.
func refuseOffWithRawShardGroups(st *State) error {
	if _, found := lookupPath(authoringValues(st), splitPath(shardGroupsValuesPath)); found {
		return fmt.Errorf("--sharding off and a shardGroups block (from --values, or recorded on the instance) contradict each " +
			"other. Omit --sharding to install the block as a custom configuration, or drop the block. Nothing has been changed")
	}
	return nil
}

// refuseShardingCombination is the table of things a level is never built with. Each is refused with
// nothing written; each row has a test with a negative control (the same run without the conflicting
// part gets past this function).
func refuseShardingCombination(st *State, l *shardLevel) error {
	if st.Compact {
		return fmt.Errorf("--sharding %s needs %d broker servers, one per node under a hard spread, and about twice "+
			"the broker CPU of one server; this cluster was installed --compact, which publishes no figure for that. "+
			"Build it on a cluster installed without --compact. Nothing has been changed", l.Name, l.Count)
	}
	if st.HA {
		return fmt.Errorf("--sharding %s places each group on one broker server as an R1 stream; this cluster was "+
			"installed --ha, which replicates every stream. The two cannot be combined. Nothing has been changed", l.Name)
	}
	if size := effectiveSize(st); size != l.RequiredSize {
		return fmt.Errorf("--sharding %s is measured only with --size %s, and this run builds size %s. "+
			"Nothing has been changed", l.Name, l.RequiredSize, size)
	}
	overlay := effectiveOverlay(st)
	if _, found := lookupPath(overlay, splitPath(shardGroupsValuesPath)); found {
		return fmt.Errorf("--sharding %s owns the shardGroups block; drop instance.config.infrastructure.nats.shardGroups "+
			"from --values, or omit --sharding to install a raw block as a custom configuration. Nothing has been changed", l.Name)
	}
	if _, found := lookupPath(overlay, splitPath("instance.config.infrastructure.nats.streamReplicas")); found {
		return fmt.Errorf("--sharding %s owns stream replication (every group is one R1 stream on its own server); drop "+
			"instance.config.infrastructure.nats.streamReplicas from --values. Nothing has been changed", l.Name)
	}
	if on, _ := lookupPath(authoringValues(st), splitPath("instance.config.infrastructure.nats.mqttFailClosed")); on == true {
		return fmt.Errorf("--sharding %s with --mqtt-fail-closed has not been measured: every QoS 1 acknowledgement would "+
			"wait on one unsharded message store whose replication depends on how it was created. Build it without "+
			"--mqtt-fail-closed. Nothing has been changed", l.Name)
	}
	return refuseDetectWorkerCeiling(st, l)
}

// maxDetectWorkers is event-processing's process ceiling on groups x detectShards (its
// config.MaxDetectWorkers), refused here before anything is written instead of at pod start.
const maxDetectWorkers = 64

const detectShardsValuesPath = "functionalAreas.event-processing.config.detectShards"

// refuseDetectWorkerCeiling: with event-processing deployed and resolved-events sharded, each group
// runs its own detection workers, detectShards of them, and the process refuses to start past the
// ceiling.
func refuseDetectWorkerCeiling(st *State, l *shardLevel) error {
	vals := authoringValues(st)
	areas, err := shardGroupValuesAreas(vals)
	if err != nil {
		return err
	}
	if !slices.Contains(areas, "event-processing") || !slices.Contains(l.Suffixes, streams.ResolvedEvents) {
		return nil
	}
	shards := 1
	if raw, found := lookupPath(vals, splitPath(detectShardsValuesPath)); found {
		b, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		var k int
		if err := json.Unmarshal(b, &k); err != nil {
			return fmt.Errorf("%s must be a whole number, got %s", detectShardsValuesPath, b)
		}
		shards = max(k, 1)
	}
	if l.Count*shards > maxDetectWorkers {
		return fmt.Errorf("--sharding %s runs %d detection groups, and detectShards %d gives %d detection workers; "+
			"event-processing refuses more than %d. Lower detectShards to at most %d. Nothing has been changed",
			l.Name, l.Count, shards, l.Count*shards, maxDetectWorkers, maxDetectWorkers/l.Count)
	}
	return nil
}

// adoptRecordedSharding settles a re-run's level against the declaration already in the cluster: a
// run that names no level keeps the recorded one, and one that names a different level (custom
// counted as one) is refused: an instance keeps the level it was bootstrapped with.
func adoptRecordedSharding(st *State, recorded dcv1beta1.InstanceSpec) error {
	was := ShardingOf(recorded)
	if !st.ShardingGiven {
		st.Sharding = recorded.Sharding
		return nil
	}
	is := ShardingOff
	if st.Sharding != "" {
		is = st.Sharding
	}
	if is != was {
		return fmt.Errorf("instance %q was bootstrapped with sharding %s and this run names %s: an instance keeps "+
			"the sharding it was bootstrapped with, and nothing moves its tenants between groups. Re-run without "+
			"--sharding. Nothing has been changed", st.Instance, was, is)
	}
	return nil
}

// verifyShardingRecorded refuses a declaration that came back without the level this run asked it
// to keep. A cluster whose Instance definition predates spec.sharding accepts the write and prunes
// the field, so a later upgrade would read the instance as custom.
func verifyShardingRecorded(want, got string) error {
	if want == got {
		return nil
	}
	return fmt.Errorf("this cluster's Instance definition does not record sharding (the declaration came back "+
		"with %q, not the %q this run asked it to keep), so a later upgrade would not know the instance's level. "+
		"Run `dcctl install` to move the cluster's definitions onto this release, then run this again", got, want)
}

// validateRecordedSharding holds a declaration's level to this build's table: an unknown name was
// recorded by a newer dcctl, and rendering it as off or custom would move the instance's tenants. A
// known level's recorded shardGroups block must be the level's own: anything else is a hand edit.
func validateRecordedSharding(spec dcv1beta1.InstanceSpec) error {
	if spec.Sharding == "" {
		return nil
	}
	l, ok := shardLevels[spec.Sharding]
	if !ok {
		return fmt.Errorf("the instance declaration's sharding level %q is unknown to this dcctl: a newer dcctl may "+
			"have recorded it. Upgrade dcctl before touching this instance", spec.Sharding)
	}
	if spec.ShardGroups == nil || !l.matchesSpec(spec.ShardGroups) {
		return fmt.Errorf("the instance declaration records sharding %s but its shardGroups block is not that level's "+
			"(%d groups of %s, each on its own dc-nats server); the declaration has been edited by hand", l.Name, l.Count,
			strings.Join(l.Suffixes, ", "))
	}
	return nil
}

// matchesSpec says whether a recorded block is exactly this level's shape (seed aside: the
// declaration does not carry it).
func (l *shardLevel) matchesSpec(g *dcv1beta1.ShardGroupsSpec) bool {
	if g.Count != l.Count || len(g.Suffixes) != len(l.Suffixes) || len(g.PlacementTags) != l.Count {
		return false
	}
	for _, s := range l.Suffixes {
		if !slices.Contains(g.Suffixes, s) {
			return false
		}
	}
	for group := 0; group < l.Count; group++ {
		tags := g.PlacementTags[fmt.Sprint(group)]
		if len(tags) != 1 || tags[0] != fmt.Sprintf("dc-nats-%d", group) {
			return false
		}
	}
	return true
}

// overBroker is a size preset with this level's broker row in place of the size's (precedence layer
// 2b), so every reader of the broker (the OpenTofu variables, the node check, the volume, the
// summary) sees one answer.
func (l *shardLevel) overBroker(p sizePreset) sizePreset {
	p.NatsCPURequest = l.NatsCPURequest
	p.NatsMemoryRequest = l.NatsMemoryRequest
	p.NatsMemoryLimit = l.NatsMemoryLimit
	p.JetStreamStorage = l.JetStreamStorage
	return p
}

// summary is the preflight line printed by bootstrap for a level.
func (l *shardLevel) summary() string {
	return fmt.Sprintf("sharding %s: %d NATS servers (one per node), %s in %d groups pinned one per server, "+
		"%d vpartitions seeded %s (fixed at install; cannot be changed later); needs %d schedulable nodes with at least "+
		"%s CPU / %s allocatable each for the brokers; JetStream %s per server",
		l.Name, l.Count, strings.Join(l.Suffixes, " and "), l.Count, vpartitions, joinInts(l.SeedWeights, "/"),
		l.Count, l.NatsCPURequest, l.NatsMemoryRequest, l.JetStreamStorage)
}

func joinInts(xs []int, sep string) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}
	return strings.Join(parts, sep)
}

// ShardingSummary is the line a bootstrap prints for the level it applies, or "" for off.
func ShardingSummary(st *State) string {
	if l := shardLevelFor(st); l != nil {
		return l.summary()
	}
	return ""
}
