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

// The per-event streams are exactly capture, inbound-events and resolved-events, and of them only
// resolved-events may be moved: the others are fixed anchors the movable one is placed away from.
func TestPerEventStreams(t *testing.T) {
	got := PerEventStreams("INST")
	want := []string{
		StreamName("INST", streams.DeviceEventsCapture),
		StreamName("INST", streams.InboundEvents),
		StreamName("INST", streams.ResolvedEvents),
	}
	slices.Sort(want)
	require.Equal(t, want, got)
	movable := RebalanceableStreams("INST")
	for _, n := range got {
		require.Equal(t, n == StreamName("INST", streams.ResolvedEvents), slices.Contains(movable, n), n)
	}
}
