// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
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

	// Slice-4 fan-out / derived-event gauges (ADR-051 observability thread). Cardinality is
	// bounded: no per-tenant labels (the ADR-023 G.3 lesson) — the per-tenant state budget is
	// an ADR-023 governance concern (slice 6), where the label set is budgeted. The one
	// labelled series is rejected-detection reason, a fixed two-value enum.
	rulesActive       prometheus.Gauge
	fanoutEventsTotal prometheus.Counter
	fanoutEvalErrors  prometheus.Counter
	derivedPublished  prometheus.Counter
	derivedRejected   *prometheus.CounterVec

	// Slice-4c idle-advance counters (ADR-051 observability thread). Bounded cardinality
	// (no labels). idleAdvancesTotal counts wall-clock idle advances that PRODUCED at least one
	// detection; idleDetectionsTotal counts the detections those advances produced — a
	// silent-series signal (absence/duration/session firing off the clock, not off an event)
	// distinct from event-driven detections, so the operator can tell "device went quiet"
	// firings from "device reported a bad value" firings.
	idleAdvancesTotal   prometheus.Counter
	idleDetectionsTotal prometheus.Counter

	// Slice-4 hardening: absence detections dropped at publish because the device left the rule's
	// scope after the timer was armed (deleted / re-typed / version superseded) — the stale-timer
	// membership gate (dropStaleAbsences). Bounded cardinality (no labels). A persistently rising
	// value signals stale wheel timers surviving a rolling-update overlap (pre-Slice-6).
	staleAbsenceDropped prometheus.Counter
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

		rulesActive:       ms.NewGauge("detect_rules_active", "Rules loaded into the DETECT engine.", nil),
		fanoutEventsTotal: ms.NewCounter("detect_fanout_events_total", "Per-rule core events produced by the resolved-event fan-out.", nil),
		fanoutEvalErrors:  ms.NewCounter("detect_fanout_eval_errors_total", "Leaf-predicate evaluation errors during fan-out (rule treated as non-match).", nil),
		derivedPublished:  ms.NewCounter("detect_derived_events_published_total", "Derived signal events published (ADR-037).", nil),
		derivedRejected:   ms.NewCounterVec("detect_derived_events_rejected_total", "Detections dropped before publish, by reason (bounded enum).", []string{"reason"}),

		idleAdvancesTotal:   ms.NewCounter("detect_idle_advances_total", "Wall-clock idle advances that produced at least one detection.", nil),
		idleDetectionsTotal: ms.NewCounter("detect_idle_detections_total", "Detections produced by wall-clock idle advance (absence/duration/session firing on silence).", nil),

		staleAbsenceDropped: ms.NewCounter("detect_stale_absence_dropped_total", "Absence detections dropped at publish because the device left the rule's scope (deleted/re-typed/version superseded).", nil),
	}
}

// recordStaleAbsenceDropped records one absence detection dropped by the publish-time membership gate.
func (m *detectMetrics) recordStaleAbsenceDropped() {
	if m == nil {
		return
	}
	m.staleAbsenceDropped.Inc()
}

// reactMetrics are the Slice-5 REACT-dispatcher observability counters (ADR-051 REACT stage). Like
// detectMetrics they are bounded-cardinality: the one label is "action", a fixed two-value enum
// (sendCommand/raiseAlarm), never a tenant or rule value (the ADR-023 G.3 lesson). Every recorder is
// nil-safe so a dispatcher built without a Microservice (unit tests) runs unmeasured.
type reactMetrics struct {
	dispatched    *prometheus.CounterVec
	notEnabled    *prometheus.CounterVec
	orphan        prometheus.Counter
	poisonDropped prometheus.Counter
}

// newReactMetrics registers the REACT counters under the service's Prometheus namespace. A nil
// Microservice (unit tests) yields nil metrics; every recorder is nil-safe, so the dispatcher runs
// unmeasured rather than panicking on a global-registry double-registration.
func newReactMetrics(ms *core.Microservice) *reactMetrics {
	if ms == nil {
		return nil
	}
	return &reactMetrics{
		dispatched:    ms.NewCounterVec("react_actions_dispatched_total", "REACT actions handed to their sink, by action type (includes idempotent replays).", []string{"action"}),
		notEnabled:    ms.NewCounterVec("react_actions_not_enabled_total", "REACT actions recognized but not yet wired, by action type (raiseAlarm before slice 5c/6).", []string{"action"}),
		orphan:        ms.NewCounter("react_events_orphaned_total", "Derived events whose rule was gone from the projection (nothing dispatched).", nil),
		poisonDropped: ms.NewCounter("react_events_poison_dropped_total", "Derived events dropped after the redelivery cap (a persistently-failing dispatch).", nil),
	}
}

// RecordDispatched records one action successfully handed to its sink (react.Metrics).
func (m *reactMetrics) RecordDispatched(action string) {
	if m == nil {
		return
	}
	m.dispatched.WithLabelValues(action).Inc()
}

// RecordNotEnabled records one recognized-but-inert action (react.Metrics).
func (m *reactMetrics) RecordNotEnabled(action string) {
	if m == nil {
		return
	}
	m.notEnabled.WithLabelValues(action).Inc()
}

// RecordOrphan records one derived event whose rule was gone (react.Metrics).
func (m *reactMetrics) RecordOrphan() {
	if m == nil {
		return
	}
	m.orphan.Inc()
}

// recordPoisonDropped records one derived event dropped after exhausting the redelivery cap.
func (m *reactMetrics) recordPoisonDropped() {
	if m == nil {
		return
	}
	m.poisonDropped.Inc()
}

// setRulesActive publishes the loaded rule count (called once at startup wiring).
func (m *detectMetrics) setRulesActive(n int) {
	if m == nil {
		return
	}
	m.rulesActive.Set(float64(n))
}

// RecordFanout records one message's fan-out breadth and any leaf-eval errors (runtime.Metrics).
func (m *detectMetrics) RecordFanout(events, evalErrors int) {
	if m == nil {
		return
	}
	if events > 0 {
		m.fanoutEventsTotal.Add(float64(events))
	}
	if evalErrors > 0 {
		m.fanoutEvalErrors.Add(float64(evalErrors))
	}
}

// RecordDerivedPublished records one published derived event (runtime.Metrics).
func (m *detectMetrics) RecordDerivedPublished() {
	if m == nil {
		return
	}
	m.derivedPublished.Inc()
}

// RecordDerivedRejected records one detection dropped before publish, by reason (runtime.Metrics).
func (m *detectMetrics) RecordDerivedRejected(reason runtime.RejectReason) {
	if m == nil {
		return
	}
	m.derivedRejected.WithLabelValues(string(reason)).Inc()
}

// recordRestore records startup restore cost and the restored applied sequence.
func (m *detectMetrics) recordRestore(seconds float64, appliedSeq uint64) {
	if m == nil {
		return
	}
	m.restoreSeconds.Set(seconds)
	m.appliedStreamSeq.Set(float64(appliedSeq))
}

// recordIdleAdvance records one wall-clock idle advance that fired dets detections.
func (m *detectMetrics) recordIdleAdvance(dets int) {
	if m == nil {
		return
	}
	m.idleAdvancesTotal.Inc()
	m.idleDetectionsTotal.Add(float64(dets))
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
