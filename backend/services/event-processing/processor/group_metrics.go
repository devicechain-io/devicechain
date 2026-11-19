// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"strconv"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
)

// The service owns one collector set for its process lifetime, including across
// NATS reconnects. Group handles share counters/histograms, never mutable shard
// sampling cursors. A service without a registry remains useful in unit tests.
var detectMetricsCache = struct {
	sync.Mutex
	byService map[*core.Microservice]*DetectMetrics
}{byService: make(map[*core.Microservice]*DetectMetrics)}

type groupMetricSet struct {
	sync.Mutex
	ms      *core.Microservice
	legacy  *DetectMetrics
	workers []*DetectMetrics
}

// ForGroup returns a stable worker handle. Call it for EVERY worker, including
// zero, before starting workers. Count is immutable and bounded by the existing
// shard-group configuration limit (16). It does not activate sharded DETECT.
//
// Existing unlabelled gauges (and shard-only series) retain group zero's meaning.
// New detect_group_* series distinguish positions; detect_groups_* aggregates
// combine quantities, never stream sequence numbers. Calling only NewDetectMetrics
// preserves the entire legacy exposition without adding any series.
func (m *DetectMetrics) ForGroup(group, count int) *DetectMetrics {
	if m == nil {
		return nil
	}
	s := m.groups
	s.Lock()
	defer s.Unlock()
	if count < 1 || count > 16 || group < 0 || group >= count {
		panic("invalid DETECT metric group")
	}
	if len(s.workers) != 0 {
		if len(s.workers) != count {
			panic("DETECT metric group count changed")
		}
		return s.workers[group]
	}
	s.workers = make([]*DetectMetrics, count)
	for g := range s.workers {
		w := *s.legacy
		w.shard = &shardMetrics{barrierWait: m.shard.barrierWait, violations: m.shard.violations}
		s.workers[g] = &w
	}
	// Only quantities that identify a worker's progress need group labels. Other
	// gauges keep group zero's legacy reading and expose a process reduction.
	s.bind("applied_stream_seq", "", true, func(w *DetectMetrics) *prometheus.Gauge { return &w.appliedStreamSeq })
	s.bind("consumer_pending", "sum", true, func(w *DetectMetrics) *prometheus.Gauge { return &w.consumerPending })
	s.bind("consumer_ack_pending", "sum", true, func(w *DetectMetrics) *prometheus.Gauge { return &w.consumerAckPending })
	s.bind("watermark_lag_seconds", "max", true, func(w *DetectMetrics) *prometheus.Gauge { return &w.watermarkLagSeconds })
	s.bind("loop_heartbeat_timestamp_seconds", "min", true, func(w *DetectMetrics) *prometheus.Gauge { return &w.loopHeartbeat })
	s.bind("live", "min", true, func(w *DetectMetrics) *prometheus.Gauge { return &w.detectLive })
	s.bind("snapshot_bytes", "sum", false, func(w *DetectMetrics) *prometheus.Gauge { return &w.snapshotBytes })
	s.bind("live_keys", "sum", false, func(w *DetectMetrics) *prometheus.Gauge { return &w.liveKeys })
	s.bind("retained_samples", "sum", false, func(w *DetectMetrics) *prometheus.Gauge { return &w.retainedSamples })
	s.bind("pending_timers", "sum", false, func(w *DetectMetrics) *prometheus.Gauge { return &w.pendingTimers })
	s.bind("rules_active", "sum", false, func(w *DetectMetrics) *prometheus.Gauge { return &w.rulesActive })
	s.bind("tenants_over_rule_budget", "sum", false, func(w *DetectMetrics) *prometheus.Gauge { return &w.tenantsOverRuleBudget })
	s.bind("tenants_over_live_key_budget", "sum", false, func(w *DetectMetrics) *prometheus.Gauge { return &w.tenantsOverLiveKeyBudget })
	s.bind("tenants_over_retained_sample_budget", "sum", false, func(w *DetectMetrics) *prometheus.Gauge { return &w.tenantsOverRetainedSampleBudget })
	s.bind("derived_publish_outstanding", "sum", false, func(w *DetectMetrics) *prometheus.Gauge { return &w.publishOutstanding })
	s.bind("derived_publish_outstanding_bytes", "sum", false, func(w *DetectMetrics) *prometheus.Gauge { return &w.publishOutstandingBytes })
	s.bind("rule_stat_pending_keys", "sum", false, func(w *DetectMetrics) *prometheus.Gauge { return &w.ruleStatPendingKeys })
	s.bind("rule_stat_pending_bytes", "sum", false, func(w *DetectMetrics) *prometheus.Gauge { return &w.ruleStatPendingBytes })
	s.bind("rule_stat_oldest_pending_seconds", "max", false, func(w *DetectMetrics) *prometheus.Gauge { return &w.ruleStatOldestPending })
	s.bind("checkpoint_seconds", "max", false, func(w *DetectMetrics) *prometheus.Gauge { return &w.checkpointSeconds })
	s.bind("restore_seconds", "max", false, func(w *DetectMetrics) *prometheus.Gauge { return &w.restoreSeconds })
	s.bind("guard_prime_seconds", "max", false, func(w *DetectMetrics) *prometheus.Gauge { return &w.guardPrimeSeconds })
	s.bind("shards", "sum", false, func(w *DetectMetrics) *prometheus.Gauge { return &w.shards })
	s.bindShards()
	return s.workers[group]
}

type groupGaugeState struct {
	sync.Mutex
	values    []float64
	legacy    prometheus.Gauge
	series    []prometheus.Gauge
	aggregate prometheus.Gauge
	reduce    string
}

// groupGauge is a recording handle, not a separately registered collector.
type groupGauge struct {
	prometheus.Gauge
	state *groupGaugeState
	group int
}

func (s *groupMetricSet) bind(name, reduce string, labelled bool, field func(*DetectMetrics) *prometheus.Gauge) {
	st := &groupGaugeState{values: make([]float64, len(s.workers)), legacy: *field(s.legacy), reduce: reduce}
	if labelled {
		v := s.ms.NewGaugeVec("detect_group_"+name, "Per-group DETECT "+name+"; group zero also supplies the legacy gauge.", []string{"group"})
		for g := range s.workers {
			st.series = append(st.series, v.WithLabelValues(strconv.Itoa(g)))
		}
	}
	if reduce != "" {
		st.aggregate = s.ms.NewGauge("detect_groups_"+name, "Across all configured DETECT groups: "+reduce+" of "+name+". Unstarted groups contribute zero.")
	}
	for g, w := range s.workers {
		*field(w) = &groupGauge{Gauge: prometheus.NewGauge(prometheus.GaugeOpts{Name: "detect_group_handle", Help: "Unregistered worker reading."}), state: st, group: g}
	}
}

func (g *groupGauge) update(v float64, add bool) {
	s := g.state
	s.Lock()
	defer s.Unlock()
	if add {
		v += s.values[g.group]
	}
	s.values[g.group] = v
	g.Gauge.Set(v)
	if g.group == 0 {
		s.legacy.Set(v)
	}
	if len(s.series) != 0 {
		s.series[g.group].Set(v)
	}
	if s.aggregate == nil {
		return
	}
	total := s.values[0]
	for _, n := range s.values[1:] {
		switch s.reduce {
		case "sum":
			total += n
		case "min":
			if n < total {
				total = n
			}
		case "max":
			if n > total {
				total = n
			}
		}
	}
	s.aggregate.Set(total)
}

func (g *groupGauge) Set(v float64)     { g.update(v, false) }
func (g *groupGauge) Add(v float64)     { g.update(v, true) }
func (g *groupGauge) Sub(v float64)     { g.Add(-v) }
func (g *groupGauge) Inc()              { g.Add(1) }
func (g *groupGauge) Dec()              { g.Add(-1) }
func (g *groupGauge) SetToCurrentTime() { g.Set(float64(time.Now().UnixNano()) / 1e9) }

func (s *groupMetricSet) bindShards() {
	depth := s.ms.NewGaugeVec("detect_group_shard_queue_depth", "Operations queued per group and DETECT shard.", []string{"group", "shard"})
	busy := s.ms.NewCounterVec("detect_group_shard_busy_seconds_total", "Busy seconds per group and DETECT shard.", []string{"group", "shard"})
	keys := s.ms.NewGaugeVec("detect_group_shard_live_keys", "Live keys per group and DETECT shard.", []string{"group", "shard"})
	for g, w := range s.workers {
		w.shard.queueDepth, w.shard.busy, w.shard.liveKeys = depth, busy, keys
		w.shard.group = strconv.Itoa(g)
		if g == 0 {
			w.shard.legacy = s.legacy.shard
		}
	}
}
