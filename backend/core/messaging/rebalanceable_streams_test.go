// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/replication"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

// Nothing on the acknowledged-ingest path is rebalanceable, and the broker's imbalance alert counts
// exactly the streams that are. The alert's list is written out in the chart, so a stream declared
// later (or one that starts being written by an ingest area) cannot make the two disagree unseen.
func TestRebalanceableStreamsAreWhatTheImbalanceAlertCounts(t *testing.T) {
	got := RebalanceableStreams("INST")
	require.NotEmpty(t, got)
	for _, s := range streams.All {
		name := StreamName("INST", s.Suffix)
		require.Equal(t, !s.OnIngestPath(), slices.Contains(got, name), s.Suffix)
	}
	for _, n := range got {
		require.False(t, strings.HasPrefix(n, replication.MqttStreamPrefix), n)
	}

	raw, err := os.ReadFile("../../../deploy/helm/devicechain/templates/prometheusrule-broker.yaml")
	require.NoError(t, err)
	m := regexp.MustCompile(`stream_name=~"\{\{ \$id \}\}_\(([^)]*)\)"`).FindAllStringSubmatch(string(raw), -1)
	require.NotEmpty(t, m, "the alert names no streams")
	var want []string
	for _, n := range got {
		want = append(want, strings.TrimPrefix(n, "INST_"))
	}
	for _, one := range m {
		require.Equal(t, want, strings.Split(one[1], "|"), "the alert's stream list differs from RebalanceableStreams")
	}
}

// The weights placement balances: the per-event streams are the heaviest and every declared stream
// weighs something, including the ones that are never moved (they are load, and anchor the plan).
// resolved-events is the one per-event stream placement may move; the others are fixed anchors.
func TestStreamTrafficWeights(t *testing.T) {
	w := StreamTrafficWeights("INST")
	require.Len(t, w, len(streams.All))
	for _, s := range streams.All {
		require.Positive(t, w[StreamName("INST", s.Suffix)], s.Suffix)
	}
	movable := RebalanceableStreams("INST")
	for _, suffix := range []string{streams.DeviceEventsCapture, streams.InboundEvents, streams.ResolvedEvents} {
		n := StreamName("INST", suffix)
		require.Equal(t, streams.WeightPerEvent, w[n], suffix)
		require.Equal(t, suffix == streams.ResolvedEvents, slices.Contains(movable, n), suffix)
	}
	for n, v := range w {
		if v == streams.WeightPerEvent {
			continue
		}
		require.Less(t, v, streams.WeightPerEvent, n)
	}
}
