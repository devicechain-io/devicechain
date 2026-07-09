// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
)

// detectMetrics are the Slice-2 observability gauges/counters for the DETECT
// checkpoint loop (ADR-051 observability thread). They are emitted from the
// component that OWNS the state — the single-writer loop — because the operations
// surface (Slice 8) can only render what was instrumented here. Cardinality is
// bounded (no per-tenant labels) per the ADR-023 G.3 lesson.
//
// Every recorder method is nil-safe: a processor built without a Microservice (unit
// tests) leaves metrics nil and the loop runs unmeasured rather than panicking on a
// global-registry double-registration.
//
// Consumer lag (the #1 falling-behind signal) is derivable at the dashboard as
// (stream last-seq, from the messaging stream-metrics sampler) − applied stream seq
// below; a first-class consumer NumPending gauge is owned by the NATS layer and is
// a Slice-8 follow-up rather than something this loop can read without a
// ConsumerInfo call.
type detectMetrics struct {
	checkpointsTotal    prometheus.Counter
	eventsAppliedTotal  prometheus.Counter
	appliedStreamSeq    prometheus.Gauge
	checkpointSeconds   prometheus.Gauge
	snapshotBytes       prometheus.Gauge
	watermarkLagSeconds prometheus.Gauge
	restoreSeconds      prometheus.Gauge
}

// newDetectMetrics registers the checkpoint-loop metrics under the service's
// Prometheus namespace/subsystem.
func newDetectMetrics(ms *core.Microservice) *detectMetrics {
	return &detectMetrics{
		checkpointsTotal:    ms.NewCounter("detect_checkpoints_total", "Committed DETECT snapshot checkpoints.", nil),
		eventsAppliedTotal:  ms.NewCounter("detect_events_applied_total", "Resolved events fed into the DETECT engine.", nil),
		appliedStreamSeq:    ms.NewGauge("detect_applied_stream_seq", "Highest JetStream stream sequence captured in the committed snapshot.", nil),
		checkpointSeconds:   ms.NewGauge("detect_checkpoint_seconds", "Wall-clock cost of the last snapshot commit.", nil),
		snapshotBytes:       ms.NewGauge("detect_snapshot_bytes", "Serialized size of the last DETECT snapshot payload.", nil),
		watermarkLagSeconds: ms.NewGauge("detect_watermark_lag_seconds", "Wall-clock time minus the engine watermark at the last checkpoint.", nil),
		restoreSeconds:      ms.NewGauge("detect_restore_seconds", "Time to restore engine state from the snapshot store at startup.", nil),
	}
}

// recordRestore records startup restore cost and the restored applied sequence.
func (m *detectMetrics) recordRestore(seconds float64, appliedSeq uint64) {
	if m == nil {
		return
	}
	m.restoreSeconds.Set(seconds)
	m.appliedStreamSeq.Set(float64(appliedSeq))
}

// recordApplied records one resolved event that advanced the engine.
func (m *detectMetrics) recordApplied() {
	if m == nil {
		return
	}
	m.eventsAppliedTotal.Inc()
}

// recordCheckpoint records a committed snapshot's cost, size, position, and lag.
func (m *detectMetrics) recordCheckpoint(appliedSeq uint64, seconds float64, bytes int, lagSeconds float64) {
	if m == nil {
		return
	}
	m.checkpointsTotal.Inc()
	m.appliedStreamSeq.Set(float64(appliedSeq))
	m.checkpointSeconds.Set(seconds)
	m.snapshotBytes.Set(float64(bytes))
	m.watermarkLagSeconds.Set(lagSeconds)
}
