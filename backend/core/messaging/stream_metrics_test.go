// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// streamFillRatio takes the higher of the byte- and message-fill fractions, and
// treats a non-positive (unlimited) ceiling as 0 so an unbounded dimension never
// drives a near-full warning.
func TestStreamFillRatio(t *testing.T) {
	info := func(bytes uint64, maxBytes int64, msgs uint64, maxMsgs int64) *nats.StreamInfo {
		return &nats.StreamInfo{
			State:  nats.StreamState{Bytes: bytes, Msgs: msgs},
			Config: nats.StreamConfig{MaxBytes: maxBytes, MaxMsgs: maxMsgs},
		}
	}
	cases := []struct {
		name string
		in   *nats.StreamInfo
		want float64
	}{
		{"empty stream", info(0, 1<<30, 0, 5_000_000), 0},
		{"bytes dominate", info(900, 1000, 1, 1000), 0.9},
		{"msgs dominate", info(1, 1000, 800, 1000), 0.8},
		{"bytes unlimited falls back to msgs", info(1<<40, -1, 500, 1000), 0.5},
		{"both unlimited is zero", info(1<<40, 0, 1<<40, -1), 0},
		{"at ceiling", info(1000, 1000, 0, 1000), 1.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := streamFillRatio(tc.in); got != tc.want {
				t.Errorf("streamFillRatio = %v, want %v", got, tc.want)
			}
		})
	}
}

func leaderInfo(leader string) *nats.StreamInfo {
	return &nats.StreamInfo{Cluster: &nats.ClusterInfo{Leader: leader}}
}

// A stream's leader change is counted once per change, and the series exists at zero from
// the first sample so increase() can see the first change.
func TestSampleLeaderCountsChanges(t *testing.T) {
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-sources"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	m := newStreamMetrics(ms)
	count := func() float64 { return testutil.ToFloat64(m.streamLeaderChanges.WithLabelValues("capture")) }

	m.sampleLeader("capture", leaderInfo("nats-2"))
	if got := count(); got != 0 {
		t.Fatalf("first sight counted %v, want 0", got)
	}
	m.sampleLeader("capture", leaderInfo("nats-2"))
	if got := count(); got != 0 {
		t.Fatalf("same leader counted %v, want 0", got)
	}
	m.sampleLeader("capture", leaderInfo("nats-0"))
	if got := count(); got != 1 {
		t.Fatalf("moved leader counted %v, want 1", got)
	}
	// Seen leaderless, then back to the same server: an election happened.
	m.sampleLeader("capture", leaderInfo(""))
	m.sampleLeader("capture", leaderInfo(""))
	m.sampleLeader("capture", leaderInfo("nats-0"))
	if got := count(); got != 2 {
		t.Fatalf("leaderless then same leader counted %v, want 2", got)
	}
}

// An unclustered broker reports no cluster block: that is never a leader change.
func TestSampleLeaderUnclusteredNeverCounts(t *testing.T) {
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-sources"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	m := newStreamMetrics(ms)
	for i := 0; i < 3; i++ {
		m.sampleLeader("capture", &nats.StreamInfo{})
	}
	if got := testutil.ToFloat64(m.streamLeaderChanges.WithLabelValues("capture")); got != 0 {
		t.Fatalf("unclustered counted %v, want 0", got)
	}
}
