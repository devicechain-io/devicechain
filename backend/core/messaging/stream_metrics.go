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
		warned:     map[string]bool{},
	}
}

// sample polls each stream once and updates its gauges, emitting an edge-triggered
// warning when a stream crosses the near-full threshold. A per-stream StreamInfo
// error is logged at debug and skipped (a transient broker hiccup should not spam
// or stall the sampler).
func (m *streamMetrics) sample(js nats.JetStreamContext, names []string) {
	for _, name := range names {
		info, err := js.StreamInfo(name)
		if err != nil {
			log.Debug().Err(err).Str("stream", name).Msg("Stream utilization sample failed")
			continue
		}
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
