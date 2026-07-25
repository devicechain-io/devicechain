// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"math"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

const (
	// streamMetricsSampleInterval is how often each stream's fill is sampled. The
	// stream ceilings (ADR-023) evict the oldest messages via DiscardOld when a
	// stream fills, which is otherwise silent; this sampling surfaces the fill as a
	// gauge and an edge-triggered warning so an operator sees a backlog building
	// BEFORE data is dropped. A 30s cadence is cheap (one StreamInfo per stream) and
	// fast enough to catch a backlog well before the 7-day/size window closes.
	streamMetricsSampleInterval = 30 * time.Second

	// streamNearFullThreshold is the fill fraction (bytes or messages, whichever is
	// higher) at which a stream is warned as near-full.
	streamNearFullThreshold = 0.8
)

// streamMetrics exposes per-stream JetStream fill as Prometheus gauges and logs an
// edge-triggered warning when a stream nears its ceiling. Stream names are a small,
// known, platform-controlled set (one per suffix), so the {stream} label is bounded
// — unlike a tenant-derived label, it is not a cardinality risk.
type streamMetrics struct {
	usedBytes  *prometheus.GaugeVec
	limitBytes *prometheus.GaugeVec
	usedMsgs   *prometheus.GaugeVec
	limitMsgs  *prometheus.GaugeVec

	// The replication triple (ADR-020 A0). These exist because every other check
	// that an instance is highly available is made at INSTALL time — a rendered
	// value, a tofu plan, a preflight — and install-time checks go stale by
	// construction. That staleness is the entire bug A0 closes: replication used to
	// be applied only at stream creation, so a cluster could carry a correct-looking
	// config and single-replica streams indefinitely with nothing to say so.
	//
	// An HA claim is therefore asserted from BROKER STATE, never from config.
	// desired is what the operator asked for; actual is what JetStream reports; and
	// peersCurrent is the one that survives contact with reality — a stream can
	// report Replicas:3 while two of its peers are stale or offline, which is not
	// replication, it is a label. Alert on desired != actual, and on
	// peersCurrent < actual.
	replicasDesired *prometheus.GaugeVec
	replicasActual  *prometheus.GaugeVec
	peersCurrent    *prometheus.GaugeVec

	// warned tracks whether a stream is currently above the near-full threshold, so
	// the warning fires once on the way up (and an info once on the way back down)
	// rather than every sample. Accessed only from the single sampler goroutine.
	warned map[string]bool
}

func newStreamMetrics(ms *core.Microservice) *streamMetrics {
	return &streamMetrics{
		usedBytes:  ms.NewGaugeVec("jetstream_stream_used_bytes", "Current on-disk bytes stored in a JetStream stream.", []string{"stream"}),
		limitBytes: ms.NewGaugeVec("jetstream_stream_limit_bytes", "Configured MaxBytes ceiling for a JetStream stream.", []string{"stream"}),
		usedMsgs:   ms.NewGaugeVec("jetstream_stream_used_messages", "Current message count in a JetStream stream.", []string{"stream"}),
		limitMsgs:  ms.NewGaugeVec("jetstream_stream_limit_messages", "Configured MaxMsgs ceiling for a JetStream stream.", []string{"stream"}),
		replicasDesired: ms.NewGaugeVec("jetstream_replicas_desired",
			"Replica count this instance is configured for (instance.config.infrastructure.nats.streamReplicas).",
			[]string{"stream"}),
		replicasActual: ms.NewGaugeVec("jetstream_replicas_actual",
			"Replica count JetStream reports for this stream or KV bucket.", []string{"stream"}),
		peersCurrent: ms.NewGaugeVec("jetstream_peers_current",
			"RAFT peers currently caught up and online for this stream or KV bucket, including the leader. "+
				"Below jetstream_replicas_actual means the stream is labelled replicated but is not.",
			[]string{"stream"}),
		warned: map[string]bool{},
	}
}

// sampleReplication records the replication triple for one stream or KV bucket.
// Split out because KV buckets get ONLY this, not the fill gauges — see sample.
func (m *streamMetrics) sampleReplication(name string, info *nats.StreamInfo, desired int) {
	actual := info.Config.Replicas
	if actual < 1 {
		actual = 1
	}
	m.replicasDesired.WithLabelValues(name).Set(float64(desired))
	m.replicasActual.WithLabelValues(name).Set(float64(actual))
	m.peersCurrent.WithLabelValues(name).Set(float64(currentPeers(info)))
}

// currentPeers counts the RAFT peers that are caught up AND online, including the
// leader.
//
// The leader is counted from the Leader field rather than from the Replicas slice,
// because JetStream reports the OTHER peers there — a 3-replica stream lists two.
// An unclustered broker reports no cluster block at all, and there the single
// server holding the stream is the one current peer.
func currentPeers(info *nats.StreamInfo) int {
	if info.Cluster == nil {
		return 1
	}
	n := 0
	if info.Cluster.Leader != "" {
		n++
	}
	for _, p := range info.Cluster.Replicas {
		if p.Current && !p.Offline {
			n++
		}
	}
	return n
}

// sample polls each stream and KV bucket once and updates its gauges, emitting an
// edge-triggered warning when a stream crosses the near-full threshold. A
// per-stream StreamInfo error is logged at debug and skipped (a transient broker
// hiccup should not spam or stall the sampler).
//
// Buckets deliberately get the replication triple and NOT the fill gauges, even
// though a KV bucket is a stream and its fill is just as real. The reason is the
// alert built on those gauges: EventProcessingStreamNearFull selects
// `max by (stream)` over every stream a service reports, with no name filter. A
// Cache bucket is created DiscardNew and is SUPPOSED to sit near its ceiling —
// that is a bounded cache working correctly, not a backlog — so folding buckets
// into the fill gauges would fire a warning-severity alert as designed behaviour.
// Bucket disk is already accounted for up front by the ADR-023 reservation, which
// is the right place for it. Replication is different: it is a correctness
// property, it is the same property for a bucket as for a stream, and for
// dc_leases it is the one that decides whether failover works at all.
func (m *streamMetrics) sample(js nats.JetStreamContext, names, buckets []string, desired int) {
	for _, name := range buckets {
		info, err := js.StreamInfo(name)
		if err != nil {
			log.Debug().Err(err).Str("bucket", name).Msg("KV bucket replication sample failed")
			continue
		}
		m.sampleReplication(name, info, desired)
	}
	for _, name := range names {
		info, err := js.StreamInfo(name)
		if err != nil {
			log.Debug().Err(err).Str("stream", name).Msg("Stream utilization sample failed")
			continue
		}
		m.sampleReplication(name, info, desired)
		m.usedBytes.WithLabelValues(name).Set(float64(info.State.Bytes))
		m.usedMsgs.WithLabelValues(name).Set(float64(info.State.Msgs))
		m.limitBytes.WithLabelValues(name).Set(float64(info.Config.MaxBytes))
		m.limitMsgs.WithLabelValues(name).Set(float64(info.Config.MaxMsgs))

		pct := streamFillRatio(info)
		switch {
		case pct >= streamNearFullThreshold && !m.warned[name]:
			m.warned[name] = true
			log.Warn().Str("stream", name).Float64("utilization", pct).
				Uint64("bytes", info.State.Bytes).Int64("maxBytes", info.Config.MaxBytes).
				Uint64("msgs", info.State.Msgs).Int64("maxMsgs", info.Config.MaxMsgs).
				Msg("JetStream stream is near its size ceiling; the oldest messages will be evicted (DiscardOld) once it is full")
		case pct < streamNearFullThreshold && m.warned[name]:
			m.warned[name] = false
			log.Info().Str("stream", name).Float64("utilization", pct).
				Msg("JetStream stream utilization recovered below the near-full threshold")
		}
	}
}

// streamFillRatio returns the higher of the stream's byte- and message-fill
// fractions. A dimension whose ceiling is non-positive (unlimited) contributes 0,
// so an unbounded stream never reports as near-full.
func streamFillRatio(info *nats.StreamInfo) float64 {
	frac := func(used uint64, limit int64) float64 {
		if limit <= 0 {
			return 0
		}
		return float64(used) / float64(limit)
	}
	return math.Max(
		frac(info.State.Bytes, info.Config.MaxBytes),
		frac(info.State.Msgs, info.Config.MaxMsgs),
	)
}
