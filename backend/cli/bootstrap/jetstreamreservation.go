// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/streams"
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

// jetStreamReservation is the sum the services will ask the broker to hold, by the same
// reading TestRenderedReservationFitsTheJetStreamStore uses.
func jetStreamReservation(n *config.NatsConfiguration) int64 {
	var reserved int64
	for _, s := range streams.Suffixes() {
		copies := 1
		if n.ShardGroups != nil && slices.Contains(n.ShardGroups.Suffixes, s) {
			copies = n.ShardGroups.Count
		}
		reserved += int64(copies) * n.StreamMaxBytesFor(s)
	}
	return reserved + n.MqttStoreReservation() + n.KvReservation()
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
		grow = fmt.Sprintf("Grow the instance's JetStream volume to at least %dGi, which is what holds this "+
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
