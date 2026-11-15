// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

const metricFixtureHeader = "# Copyright The DeviceChain Authors\n# SPDX-License-Identifier: Apache-2.0\n\n"

func metricService() (*core.Microservice, *prometheus.Registry) {
	r := prometheus.NewRegistry()
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-processing"}
	ms.UseMetricsRegistry(r)
	return ms, r
}

func metricText(t *testing.T, r *prometheus.Registry) []byte {
	t.Helper()
	rec := httptest.NewRecorder()
	promhttp.HandlerFor(r, promhttp.HandlerOpts{}).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != 200 {
		t.Fatalf("metrics status %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.Bytes()
}

// The fixture was captured with the S2a production sources, not this constructor.
// Re-registration is part of the same contract: retrying initialization must leave
// the original unsharded exposition byte-identical.
func TestDetectMetricsLegacyRegistration(t *testing.T) {
	ms, reg := metricService()
	m := NewDetectMetrics(ms)
	m.bindShards(2)
	want, err := os.ReadFile("testdata/detect_metrics_s2a.prom")
	if err != nil {
		t.Fatal(err)
	}
	want = bytes.TrimPrefix(want, []byte(metricFixtureHeader))
	if got := metricText(t, reg); !bytes.Equal(got, want) {
		t.Fatal("unsharded metrics differ from S2a")
	}
	if NewDetectMetrics(ms) != m {
		t.Fatal("registration did not return the process-owned set")
	}
	if got := metricText(t, reg); !bytes.Equal(got, want) {
		t.Fatal("re-registration changed exposition")
	}
}

func assertMetric(t *testing.T, reg *prometheus.Registry, suffix string, labels map[string]string, want float64) {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != core.METRICS_NAMESPACE+"_eventprocessing_detect_"+suffix {
			continue
		}
		for _, m := range f.Metric {
			if len(m.Label) != len(labels) {
				continue
			}
			match := true
			for _, l := range m.Label {
				if labels[l.GetName()] != l.GetValue() {
					match = false
				}
			}
			if !match {
				continue
			}
			got := m.GetGauge().GetValue()
			if m.Counter != nil {
				got = m.Counter.GetValue()
			}
			if got != want {
				t.Fatalf("%s %v = %v, want %v", suffix, labels, got, want)
			}
			return
		}
	}
	t.Fatalf("missing %s %v", suffix, labels)
}

func TestDetectGroupMetrics(t *testing.T) {
	for _, k := range []int{1, 3} {
		t.Run(string(rune('0'+k)), func(t *testing.T) {
			ms, reg := metricService()
			root := NewDetectMetrics(ms)
			p0 := &ResolvedEventsProcessor{metrics: root, worker: &workerIdentity{group: 0, partition: singletonPartition}, Lease: &messaging.DistributedLease{}, Gate: NewTermGate()}
			p1 := &ResolvedEventsProcessor{worker: &workerIdentity{group: 1, partition: groupPartition(1)}}
			if err := p0.attachGroupWorkers(p1); err != nil {
				t.Fatal(err)
			}
			a, b := p0.metrics, p1.metrics
			if root.ForGroup(1, 2) != b || NewDetectMetrics(ms).ForGroup(0, 2) != a {
				t.Fatal("worker handle recreated")
			}
			// All configured workers count, including one not started yet.
			a.setDetectLive(true)
			a.loopHeartbeat.Set(100)
			assertMetric(t, reg, "groups_live", nil, 0)
			assertMetric(t, reg, "groups_loop_heartbeat_timestamp_seconds", nil, 0)
			b.setDetectLive(true)
			b.loopHeartbeat.Set(90)
			a.appliedStreamSeq.Set(1000)
			b.appliedStreamSeq.Set(2)
			a.consumerPending.Set(4)
			b.consumerPending.Set(7)
			a.consumerAckPending.Set(2)
			b.consumerAckPending.Set(3)
			a.watermarkLagSeconds.Set(6)
			b.watermarkLagSeconds.Set(9)
			a.snapshotBytes.Set(20)
			b.snapshotBytes.Set(30)
			a.liveKeys.Set(3)
			b.liveKeys.Set(8)
			a.eventsAppliedTotal.Inc()
			b.eventsAppliedTotal.Inc()
			assertMetric(t, reg, "group_applied_stream_seq", map[string]string{"group": "0"}, 1000)
			assertMetric(t, reg, "group_applied_stream_seq", map[string]string{"group": "1"}, 2)
			assertMetric(t, reg, "applied_stream_seq", nil, 1000)
			assertMetric(t, reg, "consumer_pending", nil, 4)
			assertMetric(t, reg, "groups_consumer_pending", nil, 11)
			assertMetric(t, reg, "groups_consumer_ack_pending", nil, 5)
			assertMetric(t, reg, "groups_watermark_lag_seconds", nil, 9)
			assertMetric(t, reg, "groups_snapshot_bytes", nil, 50)
			assertMetric(t, reg, "groups_live_keys", nil, 11)
			assertMetric(t, reg, "groups_loop_heartbeat_timestamp_seconds", nil, 90)
			assertMetric(t, reg, "groups_live", nil, 1)
			assertMetric(t, reg, "events_applied_total", nil, 2)
			for _, w := range []*DetectMetrics{a, b} {
				w.bindShards(k)
			}
			for g, w := range []*DetectMetrics{a, b} {
				stats := make([]detectcore.ShardStat, k)
				keys := make([]int, k)
				for i := range stats {
					stats[i].QueueDepth = g + 1
					stats[i].BusyNanos = uint64(g+1) * 1e9
					keys[i] = g + 10
				}
				w.sampleShards(stats)
				w.sampleShards(stats)
				w.sampleShardKeys(keys)
			}
			assertMetric(t, reg, "group_shard_queue_depth", map[string]string{"group": "0", "shard": "0"}, 1)
			assertMetric(t, reg, "group_shard_queue_depth", map[string]string{"group": "1", "shard": "0"}, 2)
			assertMetric(t, reg, "group_shard_busy_seconds_total", map[string]string{"group": "1", "shard": "0"}, 2)
			assertMetric(t, reg, "group_shard_live_keys", map[string]string{"group": "1", "shard": "0"}, 11)
			assertMetric(t, reg, "shard_queue_depth", map[string]string{"shard": "0"}, 1)
			assertMetric(t, reg, "shard_busy_seconds_total", map[string]string{"shard": "0"}, 1)
			// Losing one term clears only that group's state and process reductions.
			b.setDetectLive(false)
			b.clearTermGauges()
			assertMetric(t, reg, "groups_live", nil, 0)
			assertMetric(t, reg, "groups_consumer_pending", nil, 4)
			assertMetric(t, reg, "groups_snapshot_bytes", nil, 20)
			assertMetric(t, reg, "group_applied_stream_seq", map[string]string{"group": "0"}, 1000)
			if bytes.Contains(metricText(t, reg), []byte("detect_groups_applied_stream_seq")) {
				t.Fatal("sequence positions were aggregated")
			}
		})
	}
}

func TestDetectGroupMetricsConcurrent(t *testing.T) {
	ms, reg := metricService()
	root := NewDetectMetrics(ms)
	workers := []*DetectMetrics{root.ForGroup(0, 2), root.ForGroup(1, 2)}
	var wg sync.WaitGroup
	for _, w := range workers {
		wg.Add(1)
		go func(w *DetectMetrics) {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				w.liveKeys.Inc()
				w.liveKeys.Add(2)
				w.liveKeys.Sub(1)
				w.liveKeys.Dec()
			}
		}(w)
	}
	wg.Wait()
	assertMetric(t, reg, "groups_live_keys", nil, 2000)
	for _, w := range workers {
		if v := testutil.ToFloat64(w.liveKeys); v != 1000 {
			t.Fatalf("worker gauge = %v", v)
		}
	}
}
