// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	assets "github.com/devicechain-io/dc-deploy"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dcctl/dcdir"
	tfjson "github.com/hashicorp/terraform-json"
	"k8s.io/apimachinery/pkg/api/resource"
)

// The JetStream reservation precheck.
//
// 🔴 EVERY STREAM'S BYTE CEILING IS RESERVED UP FRONT, AGAINST THE BROKER'S max_file_store.
// A set of ceilings that sums to more than that cannot all be created: the last services to
// start fail with "insufficient storage resources available", and a retry fails the same way.
// Nothing about a setting that raises a ceiling (fail-closed gives the MQTT message store the
// 1 GiB hot ceiling instead of 256 MiB) says whether THIS instance's volume has the room,
// because an upgrade keeps the volume the instance was built with, and an operator may have
// set the ceiling by hand. So the question is asked of the document about to be written, for
// every upgrade, and a future change of any default inherits the answer.

// jetStreamUnits are the binary suffixes the volume sizes are spelled in.
var jetStreamUnits = map[string]int64{"Ki": 1 << 10, "Mi": 1 << 20, "Gi": 1 << 30, "Ti": 1 << 40}

var jetStreamSizeRe = regexp.MustCompile(`^([0-9]+)(Ki|Mi|Gi|Ti)$`)

// derivedMaxFileStore is the ceiling the broker derives from a volume size: 90% of the
// MAGNITUDE, floored, with the unit reattached (16Gi yields 14Gi, not 14.4Gi), exactly as
// modules/nats computes it. A size spelled any other way is taken at 90% of its bytes.
func derivedMaxFileStore(volume string) (int64, error) {
	if m := jetStreamSizeRe.FindStringSubmatch(volume); m != nil {
		magnitude, _ := strconv.ParseInt(m[1], 10, 64)
		return (magnitude * 9 / 10) * jetStreamUnits[m[2]], nil
	}
	q, err := resource.ParseQuantity(volume)
	if err != nil {
		return 0, fmt.Errorf("the JetStream volume size %q cannot be read: %w", volume, err)
	}
	return q.Value() * 9 / 10, nil
}

// maxStoreAfterGrowth drops the recorded file-store ceiling when the apply is about to
// re-derive it, which is when the JetStream volume the upgrade passes is not the one the
// release recorded and the recorded ceiling is the one that size derived (not a hand-set
// nats_jetstream_max_file_store, which stays and is what the precheck must honour).
//
// 🔴 WITHOUT THIS A GROWN VOLUME IS CHECKED AGAINST ITS OWN OLD CEILING. The release records
// the ceiling it was applied with, a 16Gi volume's 14Gi, and the reservation check prefers a
// recorded ceiling. The volume-growth recipe leaves the claims at the new size, the apply
// derives the new ceiling from it, and the check would still refuse the reservation the
// growth was made to hold.
func maxStoreAfterGrowth(state *tfjson.State, rootdir, liveVolume, recordedMax string) string {
	if recordedMax == "" || liveVolume == "" {
		return recordedMax
	}
	vals, found, err := releaseValuesInState(state, natsReleaseAddress)
	if err != nil || !found {
		return recordedMax
	}
	size, ok := nestedString(vals, "config", "jetstream", "fileStore", "pvc", "size")
	if !ok || size == "" {
		return recordedMax
	}
	rq, rerr := resource.ParseQuantity(size)
	lq, lerr := resource.ParseQuantity(liveVolume)
	if rerr != nil || lerr != nil || rq.Cmp(lq) == 0 {
		return recordedMax // the volume did not move: the recorded ceiling is still the broker's
	}
	if statedByOperator(rootdir, "nats_jetstream_max_file_store") {
		return recordedMax // the operator stated it (tfvars or TF_VAR), so the apply keeps it
	}
	return ""
}

// liveMaxFileStore reads the file-store ceiling the broker was last applied with out of the
// instance root's state, or "" when the state does not carry one. It is the ceiling an
// operator's hand-set nats_jetstream_max_file_store arrives as.
func liveMaxFileStore(state *tfjson.State) string {
	vals, found, err := releaseValuesInState(state, natsReleaseAddress)
	if err != nil || !found {
		return ""
	}
	v, _ := nestedString(vals, "config", "jetstream", "fileStore", "maxSize")
	return v
}

// jetStreamReservation is the most the services can ask ONE broker server to hold, by the
// same reading TestRenderedReservationFitsTheJetStreamStore uses. It is judged against one
// server's ceiling because that is what each server has: every server gets its own volume of
// the stated size, and a stream's reservation lands on the servers that hold its replicas.
//
// Every unsharded stream, the MQTT store and the KV buckets are counted whole: a stream never
// puts two replicas on one server, and an untagged stream may be placed on any server, so all of
// them can land on the same one (r33a saw exactly that).
//
// 🔴 A SHARDED STREAM IS COUNTED ONCE PER GROUP THAT CAN SHARE A SERVER, NOT ONCE PER GROUP. Each
// group is a separate R1 stream, and a group pinned to its own server reserves nothing on the
// others. Summing every group's copy against one server's volume refused a three-group install on
// the very volume each of its three servers had. See shardCopiesPerServer for when a group counts
// as pinned; anything else is counted as if it could join the busiest server.
func jetStreamReservation(n *config.NatsConfiguration) int64 {
	var reserved int64
	for _, s := range streams.Suffixes() {
		copies := 1
		if n.ShardGroups != nil && slices.Contains(n.ShardGroups.Suffixes, s) {
			copies = shardCopiesPerServer(n.ShardGroups)
		}
		reserved += int64(copies) * n.StreamMaxBytesFor(s)
	}
	return reserved + n.MqttStoreReservation() + n.KvReservation()
}

// natsServerTagRe is the one placement tag that selects exactly one broker server: the
// server's own name, which modules/nats gives every server as a tag when shard groups are on
// (server_tags = [$SERVER_NAME]) and which the chart writes into placementTags for each group.
var natsServerTagRe = regexp.MustCompile(`^` + regexp.QuoteMeta(natsStatefulSetName) + `-[0-9]+$`)

// shardCopiesPerServer is how many groups of one sharded stream a single server can end up
// holding. A group is PINNED when its placement is exactly one server-name tag, and two groups
// pinned to different names are on different servers. A group with any other placement (no
// tags, several, or a tag that is not a server name, which can match more than one server) is
// counted on every server, because nothing stops JetStream from putting it beside the others.
// With no pinned group at all this is Count, the old reading: the check only relaxes where the
// placement proves the copies are apart.
func shardCopiesPerServer(g *config.NatsShardGroupsConfiguration) int {
	pinned := map[string]int{}
	unpinned := 0
	for group := 0; group < g.Count; group++ {
		tags := g.PlacementTags[group]
		if len(tags) == 1 && natsServerTagRe.MatchString(tags[0]) {
			pinned[tags[0]]++
			continue
		}
		unpinned++
	}
	busiest := 0
	for _, c := range pinned {
		busiest = max(busiest, c)
	}
	return unpinned + busiest
}

// requiredVolumeGi is the smallest whole-GiB JetStream volume whose derived max_file_store
// (floor of 90% of the magnitude, as derivedMaxFileStore computes it) holds reserved bytes.
func requiredVolumeGi(reserved int64) int64 {
	gi := (reserved + (1 << 30) - 1) >> 30
	return (gi*10 + 8) / 9 // smallest N with floor(9N/10) >= gi
}

// checkJetStreamReservation judges a reservation against a ceiling and says what to do.
// derived is true when the ceiling is 90% of a volume (so the volume that would hold the
// reservation can be named), false when an operator set max_file_store by hand.
func checkJetStreamReservation(n *config.NatsConfiguration, ceiling int64, source string, derived bool) error {
	if err := n.ShardGroups.Validate(); err != nil {
		return err
	}
	reserved := jetStreamReservation(n)
	if reserved <= ceiling {
		return nil
	}
	grow := "Grow the instance's JetStream volume (see the upgrade guide), or lower the stream ceilings."
	if derived {
		grow = fmt.Sprintf("Grow the instance's JetStream volume to at least %dGi per broker server, which is what holds this "+
			"reservation at 90%% of the volume (see the upgrade guide), or lower the stream ceilings.",
			requiredVolumeGi(reserved))
	}
	remedy := grow
	// Name the runway only when it is what tipped the sum over: without it the reservation
	// would have fit, so the time the operator asked for is the part that is missing.
	if extra := n.RunwayReservation(); extra > 0 && reserved-extra <= ceiling {
		remedy = fmt.Sprintf("The outage runway asks for %d MiB of it: %d events a second held for %d minutes "+
			"on the event streams (infrastructure.nats.streamDesignRate and streamRunwayMinutes; "+
			"`dcctl upgrade <provider> <instance> --values <file>` sets them under instance.config.infrastructure.nats). %s "+
			"Or lower the design rate or the runway.",
			extra>>20, n.StreamDesignRate, n.StreamRunwayMinutes, grow)
	}
	if n.MqttFailClosed {
		remedy += " `--mqtt-fail-closed=false` is the way back: it is the setting that raises the " +
			"reservation by giving the MQTT message store the hot stream ceiling."
	}
	if n.ShardGroups != nil {
		remedy = "With shard groups this is one server's share: every unsharded stream, and each sharded " +
			"stream's groups that can be placed on the same server. " + remedy
	}
	return fmt.Errorf("the services would reserve %d MiB of JetStream storage up front, and this instance's "+
		"broker holds at most %d MiB (%s). Event-sources would fail to declare its streams and every retry "+
		"would fail the same way. Nothing has been changed. %s",
		reserved>>20, ceiling>>20, source, remedy)
}

// precheckJetStreamReservation composes the instance configuration this run is about to
// write, loads it the way the services do, and refuses when its reservation does not fit the
// broker's real ceiling. It runs before anything is written, the declaration included.
func precheckJetStreamReservation(ctx context.Context, st *State) error {
	volume, maxStore := st.LiveVolumes.JetStream, st.LiveMaxFileStore
	if volume == "" {
		// --skip-infrastructure reads nothing of the broker; the services still meet its limits.
		infra, err := readLiveInfra(ctx, st.KubeContext, st.Instance)
		if err != nil {
			return err
		}
		if infra.Broker == nil {
			// Only an infrastructure apply recreates the StatefulSet and re-derives the broker's
			// ceiling from the grown volume; the services alone would reserve against a ceiling the
			// running servers do not have.
			return fmt.Errorf("instance %q's broker StatefulSet is gone (deleted with --cascade=orphan to grow "+
				"its volume), so the broker's ceiling is not what its volume claims say until an infrastructure "+
				"apply recreates it. Nothing has been changed. Run the upgrade without --skip-infrastructure",
				st.Instance)
		}
		live, err := infra.volumes()
		if err != nil {
			return err
		}
		volume = live.JetStream
	}
	if volume == "" {
		volume = jetStreamStorageFor(st)
	}

	var ceiling int64
	var source string
	if maxStore != "" {
		q, err := resource.ParseQuantity(maxStore)
		if err != nil {
			return fmt.Errorf("the broker's max_file_store %q cannot be read: %w", maxStore, err)
		}
		ceiling, source = q.Value(), "its max_file_store, "+maxStore
	} else {
		var err error
		if ceiling, err = derivedMaxFileStore(volume); err != nil {
			return err
		}
		source = "90% of its " + volume + " JetStream volume"
	}

	ch, err := loadEmbeddedChart()
	if err != nil {
		return fmt.Errorf("loading the chart to check the JetStream reservation: %w", err)
	}
	raw, err := renderInstanceConfigDocument(ctx, ch, renderOnlyAuthoring(st))
	if err != nil {
		return fmt.Errorf("composing the instance configuration to check its JetStream reservation: %w", err)
	}
	if raw == nil {
		return nil
	}
	doc, err := loadInstanceConfigDocument(raw)
	if err != nil {
		return err
	}
	return checkJetStreamReservation(&doc.Infrastructure.Nats, ceiling, source, maxStore == "")
}

// operatorStatedValue reads the value an operator states for an instance-root variable, from
// the same places statedByOperator looks, in OpenTofu's own order of precedence: TF_VAR_<name>
// first, then terraform.tfvars, terraform.tfvars.json, and the *.auto.tfvars files in lexical
// order, each later one overriding the earlier. stated reports whether any of them declares the
// variable; readable whether the winning declaration is a plain string this can read without
// evaluating anything. A test replaces it.
var operatorStatedValue = func(st *State, name string) (value string, stated, readable bool) {
	instanceDir, err := dcdir.Instance(st.Instance)
	if err != nil {
		return statedValueIn("", name)
	}
	return statedValueIn(filepath.Join(instanceDir, "infra", assets.InstanceRootDir), name)
}

// statedValueIn is operatorStatedValue for one instance root directory.
func statedValueIn(rootdir, name string) (value string, stated, readable bool) {
	for _, e := range os.Environ() {
		if v, ok := strings.CutPrefix(e, "TF_VAR_"+name+"="); ok && v != "" {
			value, stated, readable = v, true, true
		}
	}
	entries, err := os.ReadDir(rootdir)
	if err != nil {
		return value, stated, readable
	}
	var fixed, auto []string
	for _, e := range entries {
		n := e.Name()
		lower := n
		if runtime.GOOS == "windows" {
			lower = strings.ToLower(n)
		}
		switch {
		case e.IsDir():
		case lower == "terraform.tfvars" || lower == "terraform.tfvars.json":
			fixed = append(fixed, n)
		case strings.HasSuffix(n, ".auto.tfvars") || strings.HasSuffix(n, ".auto.tfvars.json"):
			auto = append(auto, n)
		}
	}
	// terraform.tfvars before its .json form, then the auto files by name, as tofu loads them.
	slices.SortFunc(fixed, func(a, b string) int { return len(a) - len(b) })
	slices.Sort(auto)
	decl := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(name) + `\s*=(.*)$`)
	literal := regexp.MustCompile(`^\s*"([^"\$%]*)"\s*(?:(?:#|//).*)?$`)
	// Match strings (including escapes) and line comments first so their /* markers
	// stay literal. Each block comment ends at the first */, not the last in the file.
	tokens := regexp.MustCompile(`(?s)"(?:\\.|[^"\\])*"|#[^\n]*|//[^\n]*|/\*.*?\*/`)
	for _, n := range append(fixed, auto...) {
		b, err := os.ReadFile(filepath.Join(rootdir, n))
		if err != nil {
			continue
		}
		if strings.HasSuffix(n, ".json") {
			var m map[string]json.RawMessage
			if json.Unmarshal(b, &m) != nil {
				continue
			}
			raw, ok := m[name]
			if !ok {
				continue
			}
			var s string
			value, stated, readable = "", true, json.Unmarshal(raw, &s) == nil
			if readable {
				value = s
			}
			continue
		}
		uncommented := tokens.ReplaceAllStringFunc(string(b), func(token string) string {
			if !strings.HasPrefix(token, "/*") {
				return token
			}
			// Keep line boundaries and separate tokens on either side of a comment.
			return strings.Map(func(r rune) rune {
				if r == '\n' {
					return r
				}
				return ' '
			}, token)
		})
		for _, d := range decl.FindAllStringSubmatch(uncommented, -1) {
			value, stated, readable = "", true, false
			if m := literal.FindStringSubmatch(d[1]); m != nil {
				value, readable = m[1], true
			}
		}
	}
	return value, stated, readable
}

// plannedJetStreamVolume is the JetStream volume each broker server will have after this run's
// infrastructure apply, so the reservation can be judged before anything is created. A live
// volume is kept by every apply (volumeSize). Compact passes its own size as a -var, which wins
// over anything the operator states. Otherwise a size the operator states wins over the preset
// and the root default, because the apply leaves the preset's assignment out (tofuVarsFor) and
// the root default is only a default.
//
// unread is non-empty when the operator states the size in a form this cannot read without
// evaluating it (an expression, a reference): the size is then unknown before the apply, and
// the check that runs after it, against the volume the broker really got, is the one to rely on.
func plannedJetStreamVolume(st *State) (size, unread string) {
	if st.LiveVolumes.JetStream != "" {
		return st.LiveVolumes.JetStream, ""
	}
	if st.Compact {
		return compact.JetStreamStorage, ""
	}
	if v, stated, readable := operatorStatedValue(st, "nats_jetstream_storage"); stated {
		if !readable {
			return "", "the operator states nats_jetstream_storage in a form dcctl does not evaluate"
		}
		return v, ""
	}
	return jetStreamStorageFor(st), ""
}
