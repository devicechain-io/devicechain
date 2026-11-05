// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"strconv"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
)

// The metrics of a concurrent engine (detectShards above 1). They carry a `shard` label whose values
// are the integers 0..K-1, and K is validated to 1..64 at startup, so the label set is bounded.
//
//	detect_shard_queue_depth{shard}          operations queued for the shard (sampled each tick)
//	detect_shard_busy_seconds_total{shard}   seconds the shard spent applying operations; its rate is
//	                                         the shard's utilisation, and above 0.9 for ten minutes
//	                                         is the DetectShardSaturated alert
//	detect_shard_live_keys{shard}            live keys the shard holds (sampled on the state-budget tick)
//	detect_barrier_wait_seconds              how long the loop waited for every shard to drain
//	detect_shard_invariant_violations_total  shards that disagreed on the watermark or position at a
//	                                         snapshot; the snapshot is refused. This pages.
type shardMetrics struct {
	queueDepth *prometheus.GaugeVec
	busy       *prometheus.CounterVec
	liveKeys   *prometheus.GaugeVec

	barrierWait prometheus.Observer
	violations  prometheus.Counter

	// Bound by bindShards for the engine in force; read and written only from the loop.
	depth    []prometheus.Gauge
	busyCtr  []prometheus.Counter
	live     []prometheus.Gauge
	lastBusy []uint64
}

func newShardMetrics(ms *core.Microservice) *shardMetrics {
	return &shardMetrics{
		queueDepth: ms.NewGaugeVec("detect_shard_queue_depth",
			"Operations queued for each detection shard, sampled on the loop's tick. A queue that stays near its bound means that shard is the slowest and the loop is waiting on it.",
			[]string{"shard"}),
		busy: ms.NewCounterVec("detect_shard_busy_seconds_total",
			"Seconds each detection shard has spent applying operations. The rate is the shard's utilisation; one shard far above the others is a hot key, all of them near 1 means detectShards is the limit.",
			[]string{"shard"}),
		liveKeys: ms.NewGaugeVec("detect_shard_live_keys",
			"Live windows, timers and latches each detection shard holds, sampled on the state-budget tick. An uneven spread is key skew.",
			[]string{"shard"}),
		barrierWait: ms.NewHistogramVec("detect_barrier_wait_seconds",
			"How long the loop waited for every detection shard to drain its queue, for a snapshot, a rule change or a query that needs all of them. Most of a checkpoint's cost with concurrent shards is here.",
			nil, waitBuckets).WithLabelValues(),
		violations: ms.NewCounter("detect_shard_invariant_violations_total",
			"Times the detection shards disagreed on the watermark or the position at a snapshot, or with the loop on the position. The snapshot is refused. Any increase is a defect."),
	}
}

// bindShards resolves the per-shard series for an engine of k shards.
func (m *DetectMetrics) bindShards(k int) {
	if m == nil || m.shard == nil {
		return
	}
	s := m.shard
	s.depth, s.busyCtr, s.live = make([]prometheus.Gauge, k), make([]prometheus.Counter, k), make([]prometheus.Gauge, k)
	s.lastBusy = make([]uint64, k)
	for i := 0; i < k; i++ {
		l := strconv.Itoa(i)
		s.depth[i] = s.queueDepth.WithLabelValues(l)
		s.busyCtr[i] = s.busy.WithLabelValues(l)
		s.live[i] = s.liveKeys.WithLabelValues(l)
	}
}

// sampleShards publishes each shard's queue depth and the busy time it has added since the last sample.
func (m *DetectMetrics) sampleShards(stats []detectcore.ShardStat) {
	if m == nil || m.shard == nil || len(m.shard.depth) != len(stats) {
		return
	}
	s := m.shard
	for i, st := range stats {
		s.depth[i].Set(float64(st.QueueDepth))
		if st.BusyNanos > s.lastBusy[i] {
			s.busyCtr[i].Add(float64(st.BusyNanos-s.lastBusy[i]) / 1e9)
			s.lastBusy[i] = st.BusyNanos
		}
	}
}

// sampleShardKeys publishes each shard's live-key count.
func (m *DetectMetrics) sampleShardKeys(keys []int) {
	if m == nil || m.shard == nil || len(m.shard.live) != len(keys) {
		return
	}
	for i, n := range keys {
		m.shard.live[i].Set(float64(n))
	}
}

// BarrierWait implements detectcore.PipelineObserver.
func (m *DetectMetrics) BarrierWait(d time.Duration) {
	if m == nil || m.shard == nil {
		return
	}
	m.shard.barrierWait.Observe(d.Seconds())
}

// InvariantViolation implements detectcore.PipelineObserver.
func (m *DetectMetrics) InvariantViolation() {
	if m == nil || m.shard == nil {
		return
	}
	m.shard.violations.Inc()
}

var _ detectcore.PipelineObserver = (*DetectMetrics)(nil)
