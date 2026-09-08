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
// Consumer lag (the #1 falling-behind signal) is NOT derivable at the dashboard from
// the series this loop emits — the stream-metrics sampler exposes stream message COUNT,
// not the last sequence, so there is no last-seq to subtract applied_stream_seq from.
// It is first-classed instead as the consumerPending/consumerAckPending gauges below,
// read from the resolved-events durable's broker-reported backlog.
type detectMetrics struct {
	checkpointsTotal    prometheus.Counter
	eventsAppliedTotal  prometheus.Counter
	appliedStreamSeq    prometheus.Gauge
	checkpointSeconds   prometheus.Gauge
	snapshotBytes       prometheus.Gauge
	watermarkLagSeconds prometheus.Gauge
	restoreSeconds      prometheus.Gauge
	isLeader            prometheus.Gauge
	detectLive          prometheus.Gauge

	// Slice-8 consumer-lag gauges (ADR-051 observability thread; the operations board's #1
	// "falling behind" signal). These exist because the derived-at-the-dashboard alternative does
	// not work: the stream-metrics sampler exposes stream message COUNT, not the last sequence, so
	// there is no last-seq to subtract applied_stream_seq from. The resolved-events durable's
	// broker-reported backlog is first-classed here instead — which is why this loop pays for a
	// Backlog probe rather than leaving the arithmetic to a dashboard that cannot do it.
	// consumerPending is UNDELIVERED work waiting on the consumer
	// (the primary lag signal); consumerAckPending is DELIVERED-BUT-UNACKED (in-flight, and a
	// peer's during a rolling-update overlap). Sampled on the single-writer ticker at the
	// checkpoint cadence off the same Backlog probe idle-advance uses (safe to call concurrently
	// with the read loop). Bounded cardinality (no labels).
	consumerPending    prometheus.Gauge
	consumerAckPending prometheus.Gauge

	// Slice-4 fan-out / derived-event gauges (ADR-051 observability thread). Cardinality is
	// bounded: no per-tenant labels (the ADR-023 G.3 lesson) — the per-tenant state budget is
	// an ADR-023 governance concern (slice 6), where the label set is budgeted. The one
	// labelled series is rejected-detection reason, a fixed two-value enum.
	rulesActive       prometheus.Gauge
	fanoutEventsTotal prometheus.Counter
	fanoutEvalErrors  prometheus.Counter
	lateSamplesTotal  prometheus.Counter
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
	// membership gate (dropSupersededDetections). Bounded cardinality (no labels). A persistently rising
	// value signals stale wheel timers surviving a rolling-update overlap (pre-Slice-6).
	staleAbsenceDropped prometheus.Counter

	// supersededFrontierDropped counts non-absence FRONTIER-triggered detections (Duration/Session
	// hold/gap timers, Aggregate pane closes) dropped at publish because their profile version is no
	// longer the device's ACTIVE version (ADR-057 D6). Starvation silences a superseded version's
	// event-driven kinds but not its watermark-fired timers/panes, which would otherwise contribute a
	// false edge under the stable contributor id. Bounded cardinality (no labels).
	supersededFrontierDropped prometheus.Counter

	// Slice-6c per-tenant state-budget gauges (ADR-023 amendment). Bounded cardinality — NO
	// per-tenant labels (the G.3 DoS lesson): liveKeys/retainedSamples are AGGREGATE totals across
	// all tenants, and the three "over budget" gauges are COUNTS of tenants breaching each ceiling,
	// not a per-tenant series. The offending tenant is found via the loop's warn log, not a metric
	// label. Recomputed each checkpoint on the single-writer loop.
	//
	// liveKeys and retainedSamples measure DIFFERENT nouns and are both required: liveKeys counts
	// state ENTRIES (key cardinality), retainedSamples counts the per-sample records a window-shaped
	// rule holds. A long-window rule on one busy series moves the second and leaves the first flat,
	// which is exactly the overrun that used to be invisible.
	liveKeys                        prometheus.Gauge
	retainedSamples                 prometheus.Gauge
	pendingTimers                   prometheus.Gauge
	tenantsOverRuleBudget           prometheus.Gauge
	tenantsOverLiveKeyBudget        prometheus.Gauge
	tenantsOverRetainedSampleBudget prometheus.Gauge
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
		// Leadership (ADR-070). Two gauges rather than one, because the interesting
		// failure is a pod that HAS the partition and is not detecting on it: a term
		// build runs a snapshot restore, three view builds and a full replay, and a
		// single "am I the leader" series cannot tell that apart from a healthy leader.
		// isLeader goes up at ACQUIRE so a long build does not read as leaderless.
		isLeader:   ms.NewGauge("detect_is_leader", "1 while this replica holds the DETECT partition lease, from acquisition rather than from the end of the term build.", nil),
		detectLive: ms.NewGauge("detect_live", "1 while this replica is consuming inside a held leadership term; 0 while standing by OR while building a term it has already acquired.", nil),

		consumerPending:    ms.NewGauge("detect_consumer_pending", "Undelivered messages waiting on the resolved-events durable consumer (the primary DETECT lag signal).", nil),
		consumerAckPending: ms.NewGauge("detect_consumer_ack_pending", "Delivered-but-unacked messages on the resolved-events durable consumer (in-flight work).", nil),

		rulesActive:       ms.NewGauge("detect_rules_active", "Rules loaded into the DETECT engine.", nil),
		fanoutEventsTotal: ms.NewCounter("detect_fanout_events_total", "Per-rule core events produced by the resolved-event fan-out.", nil),
		fanoutEvalErrors:  ms.NewCounter("detect_fanout_eval_errors_total", "Leaf-predicate evaluation errors during fan-out (the sample is skipped for that rule, not fed as a non-match).", nil),
		lateSamplesTotal:  ms.NewCounter("detect_late_samples_total", "Samples that arrived after their own trailing window had passed the watermark and so were not folded into a sliding-window rule (Repeating, SlidingAgg, Correlation). A store-and-forward device uploading buffered readings is the usual cause; a sustained non-zero rate means those rules are evaluating less than the device sent.", nil),
		derivedPublished:  ms.NewCounter("detect_derived_events_published_total", "Derived signal events published (ADR-037).", nil),
		derivedRejected:   ms.NewCounterVec("detect_derived_events_rejected_total", "Detections dropped before publish, by reason (bounded enum).", []string{"reason"}),

		idleAdvancesTotal:   ms.NewCounter("detect_idle_advances_total", "Wall-clock idle advances that produced at least one detection.", nil),
		idleDetectionsTotal: ms.NewCounter("detect_idle_detections_total", "Detections produced by wall-clock idle advance (absence/duration/session firing on silence).", nil),

		staleAbsenceDropped:       ms.NewCounter("detect_stale_absence_dropped_total", "Absence detections dropped at publish because the device left the rule's scope (deleted/re-typed/version superseded).", nil),
		supersededFrontierDropped: ms.NewCounter("detect_superseded_frontier_dropped_total", "Non-absence frontier-triggered detections (Duration/Session/Aggregate) dropped at publish because their profile version is superseded (ADR-057 D6).", nil),

		liveKeys:                 ms.NewGauge("detect_live_keys", "Total live keyed window/timer state entries across all tenants (the per-tenant state-budget aggregate).", nil),
		tenantsOverRuleBudget:    ms.NewGauge("detect_tenants_over_rule_budget", "Tenants currently exceeding the per-tenant rule-count budget (ADR-023).", nil),
		tenantsOverLiveKeyBudget: ms.NewGauge("detect_tenants_over_live_key_budget", "Tenants currently exceeding the per-tenant live-key budget (ADR-023).", nil),

		// The retained-sample axis (ADR-051 slice 6c). detect_live_keys counts state ENTRIES, which
		// is flat in the samples a long window holds — a 30-day window on one series is one key. This
		// pair is what a long-window memory overrun actually moves.
		retainedSamples:                 ms.NewGauge("detect_retained_samples", "Total per-sample records retained by window-shaped rules across all tenants (repeating/sliding-aggregate windows and correlation members).", nil),
		pendingTimers:                   ms.NewGauge("detect_pending_timers", "Entries in the timer wheel's pending-deadline heap. Grows per EVENT for absence/session rules (a deadline reset pushes a new entry and the superseded one lingers until its deadline), so neither of the two gauges above can see it.", nil),
		tenantsOverRetainedSampleBudget: ms.NewGauge("detect_tenants_over_retained_sample_budget", "Tenants currently exceeding the per-tenant retained-sample budget (ADR-023).", nil),
	}
}

// recordStateBudget publishes the per-tenant state-budget gauges at a checkpoint (slice 6c): the
// aggregate live-key total and the counts of tenants over each ceiling. Nil-safe (unit-test loops
// run unmeasured).
func (m *detectMetrics) recordStateBudget(s stateBudgetStats) {
	if m == nil {
		return
	}
	m.liveKeys.Set(float64(s.totalLiveKeys))
	m.retainedSamples.Set(float64(s.totalRetainedSamples))
	m.pendingTimers.Set(float64(s.totalPendingTimers))
	m.tenantsOverRuleBudget.Set(float64(s.tenantsOverRules))
	m.tenantsOverLiveKeyBudget.Set(float64(s.tenantsOverKeys))
	m.tenantsOverRetainedSampleBudget.Set(float64(s.tenantsOverSamples))
}

// recordStaleAbsenceDropped records one absence detection dropped by the publish-time membership gate.
func (m *detectMetrics) recordStaleAbsenceDropped() {
	if m == nil {
		return
	}
	m.staleAbsenceDropped.Inc()
}

// recordSupersededFrontierDropped records one non-absence frontier detection dropped at publish
// because its profile version is superseded (ADR-057 D6).
func (m *detectMetrics) recordSupersededFrontierDropped() {
	if m == nil {
		return
	}
	m.supersededFrontierDropped.Inc()
}

// reactMetrics are the Slice-5 REACT-dispatcher observability counters (ADR-051 REACT stage). Like
// detectMetrics they are bounded-cardinality: the one label is "action", a fixed small enum —
// sendCommand, raiseAlarm, clearAlarm (the structural falling-edge clear, ADR-057), and the two
// connector actions httpCall and publish (ADR-060), which reach RecordDispatched/RecordNotEnabled
// under their own rules.ActionType string. Never a tenant or rule value (the ADR-023 G.3 lesson).
// Every recorder is nil-safe so a dispatcher built
// without a Microservice (unit tests) runs unmeasured.
type reactMetrics struct {
	dispatched          *prometheus.CounterVec
	notEnabled          *prometheus.CounterVec
	connectorShed       *prometheus.CounterVec
	permanentlyRejected *prometheus.CounterVec
	orphan              prometheus.Counter
	poisonDropped       prometheus.Counter
	deadLettered        prometheus.Counter
	deadLetterLost      prometheus.Counter
}

// newReactMetrics registers the REACT counters under the service's Prometheus namespace. A nil
// Microservice (unit tests) yields nil metrics; every recorder is nil-safe, so the dispatcher runs
// unmeasured rather than panicking on a global-registry double-registration.
func newReactMetrics(ms *core.Microservice) *reactMetrics {
	if ms == nil {
		return nil
	}
	return &reactMetrics{
		dispatched:          ms.NewCounterVec("react_actions_dispatched_total", "REACT actions handed to their sink, by action type (includes idempotent replays).", []string{"action"}),
		notEnabled:          ms.NewCounterVec("react_actions_not_enabled_total", "REACT actions recognized but dropped because this deployment has no sink for them, by action type: sendCommand without command-delivery configured, or httpCall/publish without outbound connectors enabled. The alarm sink is always wired, so raiseAlarm/clearAlarm should never appear here.", []string{"action"}),
		connectorShed:       ms.NewCounterVec("react_connector_egress_shed_total", "Connector dispatch ATTEMPTS (httpCall/publish) shed at the source for being over the tenant's outbound egress quota (ADR-060 SD-3). Per-attempt: a sibling-failure redelivery may shed then later admit the same action, so this is not a count of permanently-dropped actions.", []string{"action"}),
		permanentlyRejected: ms.NewCounterVec("react_actions_permanently_rejected_total", "REACT actions DROPPED because the downstream service returned a typed rejection a retry cannot change, by action type: a sendCommand for a device that no longer exists, or a command outside the device's published vocabulary. These were previously retried to the redelivery cap and counted as poison, so a non-zero rate here is an authoring defect (a rule aimed at commands its devices cannot accept), not an infrastructure one.", []string{"action"}),
		orphan:              ms.NewCounter("react_events_orphaned_total", "Derived events whose rule was gone from the projection (nothing dispatched).", nil),
		poisonDropped:       ms.NewCounter("react_events_poison_dropped_total", "Derived events dropped after the redelivery cap (a persistently-failing dispatch). Now that such an event is dead-lettered (ADR-024), this counts the same events react_events_dead_lettered_total does — kept because it is what the ReactPoisonDropping alert has always fired on, and a metric an alert is built around is not renamed for tidiness.", nil),
		deadLettered:        ms.NewCounter("react_events_dead_lettered_total", "Derived events written to the dead-letter stream after the redelivery cap, so their actions can be inspected rather than vanishing (ADR-024).", nil),
		deadLetterLost:      ms.NewCounter("react_events_dead_letter_lost_total", "Derived events that could be neither dispatched NOR dead-lettered — the write to the dead-letter stream failed on a delivery that will not repeat. This is the one outcome on this path where work is silently gone, and it is the reason the counter exists separately from the one above.", nil),
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

// RecordPermanentlyRejected records one action dropped on a typed downstream rejection that a
// retry cannot change (react.Metrics).
func (m *reactMetrics) RecordPermanentlyRejected(action string) {
	if m == nil {
		return
	}
	m.permanentlyRejected.WithLabelValues(action).Inc()
}

// RecordConnectorShed records one connector action dropped at the source for being over the tenant's
// outbound egress quota (react.Metrics, ADR-060 SD-3).
func (m *reactMetrics) RecordConnectorShed(action string) {
	if m == nil {
		return
	}
	m.connectorShed.WithLabelValues(action).Inc()
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

// recordDeadLettered records one derived event written to the dead-letter stream.
func (m *reactMetrics) recordDeadLettered() {
	if m == nil {
		return
	}
	m.deadLettered.Inc()
}

// recordDeadLetterLost records one derived event that could be neither dispatched nor
// dead-lettered. It is counted apart from the one above because it is the only outcome on
// this path where the work is gone with no record of it anywhere.
func (m *reactMetrics) recordDeadLetterLost() {
	if m == nil {
		return
	}
	m.deadLetterLost.Inc()
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

// recordLateSamples records samples the engine declined as late (core.DrainLateSamples).
func (m *detectMetrics) recordLateSamples(n uint64) {
	if m == nil || n == 0 {
		return
	}
	m.lateSamplesTotal.Add(float64(n))
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

// recordConsumerLag publishes the resolved-events durable consumer's broker-reported backlog
// (undelivered + delivered-unacked). Nil-safe (unit-test loops run unmeasured).
func (m *detectMetrics) recordConsumerLag(pending, ackPending uint64) {
	if m == nil {
		return
	}
	m.consumerPending.Set(float64(pending))
	m.consumerAckPending.Set(float64(ackPending))
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

// fenceGeometryMetrics reports on the geofence ARCHIVE seam: resolving a fence-set manifest
// into evaluable geometry, and the compiled-geometry cache that makes that cheap.
//
// It is a type of its own rather than more fields on detectMetrics, because every one of these
// is recorded by the fence-set client and none of them by the detection loop. Folding them into
// the loop's metrics would have meant handing the client a reference to the loop's internals to
// record a number the loop knows nothing about — and it is the loop's metrics that get read
// when someone asks "is DETECT keeping up", a question none of these answers.
type fenceGeometryMetrics struct {
	// fenceGeometryUnresolved counts MANIFEST ENTRIES whose geometry this service could not
	// obtain — the archive answered without it, or the read failed. Bounded cardinality (no
	// labels; the tenant and fence are in the accompanying error log).
	//
	// 🔴 IT IS THE PRICE OF SPLITTING GEOMETRY OUT OF THE FACT, AND IT IS THE ONLY THING THAT
	// MAKES THAT SPLIT SAFE TO SEE. When the fact carried the whole fence set, a set either
	// arrived or did not. Now a version's fences arrive as names and their bodies are fetched,
	// so a set can be assembled with a hole in it — and a hole is the one failure containment
	// cannot report for itself, because a fence that is simply MISSING reads as "no such
	// fence" rather than as an error. The engine closes that by installing such a fence with
	// its error attached, which turns the hole into a counted eval error; this counter is what
	// says WHICH cause, and it is the difference between "some rule is erroring" and "these
	// fences have no geometry".
	//
	// Non-zero means containment for the affected fences is reporting unresolvable rather than
	// answering, and it repairs on the next reconcile sweep if the cause was transient.
	fenceGeometryUnresolved prometheus.Counter

	// fenceGeometryHashMismatch counts geometry documents that did NOT hash to the content
	// address they were requested under. Bounded cardinality (no labels).
	//
	// 🔴 THIS SHOULD ALWAYS BE ZERO, AND A NON-ZERO VALUE IS NEVER BENIGN. An address IS the
	// SHA-256 of the document stored under it, so a mismatch means one of: device-management
	// served the wrong row, something re-encoded the document in transit (a jsonb reprint or a
	// convenience unmarshal/marshal would do it, and would make EVERY fence mismatch at once),
	// or the archive is corrupt. All three are bugs, none is a transient fault, and the
	// verification that produces this number is the only check standing between a wrong body
	// and a confidently wrong containment answer that no sweep would ever repair — a verified
	// cache entry outlives every version.
	fenceGeometryHashMismatch prometheus.Counter

	// fenceArchiveSkew counts archive reads that failed because device-management does not
	// serve the manifest doors — i.e. it is running a build from before manifest delivery.
	// Bounded cardinality (no labels).
	//
	// It exists because the two services roll as independent Deployments and this arc cut over
	// both at once, so an ordering window on upgrade, or a rollback of device-management
	// alone, leaves this service asking for doors that are not there. That is a version-skew
	// condition and it repairs itself when the peer rolls forward — but its symptom is
	// identical to "the archive is unreachable", and an operator who cannot tell those apart
	// will go looking at the network. Naming it is worth the few lines.
	fenceArchiveSkew prometheus.Counter

	// fenceGeometryCacheHits / fenceGeometryCacheMisses count lookups of the compiled-geometry
	// cache, keyed by (tenant, content address). Bounded cardinality (no labels).
	//
	// The ratio is the whole economic case for manifest delivery: a fence set's geometry is
	// re-fetched and re-compiled only for addresses that actually CHANGED, so editing one
	// fence of a hundred should show one miss and ninety-nine hits. A hit rate that collapses
	// means the cache is thrashing — see fenceGeometryCacheEvictions — and every fence edit is
	// paying the full cross-service read the design exists to avoid.
	fenceGeometryCacheHits   prometheus.Counter
	fenceGeometryCacheMisses prometheus.Counter

	// fenceGeometryCacheEvictions counts entries dropped to stay inside the cache's bound.
	// Bounded cardinality (no labels).
	fenceGeometryCacheEvictions prometheus.Counter

	// fenceGeometryCacheVertices reports how much of the cache's bound is in use.
	//
	// 🔴 THE BOUND IS COUNTED IN VERTICES, NOT ENTRIES, AND THE GAUGE MATCHES IT ON PURPOSE. A
	// three-vertex box and a five-hundred-vertex polygon are the same number of entries and
	// differ ~500x in what they cost to hold, so an entry count would be a bound whose meaning
	// changed with the mix — full at three tenants or thirty, depending on what they drew.
	fenceGeometryCacheVertices prometheus.Gauge
}

// newFenceGeometryMetrics registers the archive-seam metrics under the service's namespace.
func newFenceGeometryMetrics(ms *core.Microservice) *fenceGeometryMetrics {
	return &fenceGeometryMetrics{
		fenceGeometryUnresolved:     ms.NewCounter("detect_fence_geometry_unresolved_total", "Geofence manifest entries whose geometry could not be obtained from device-management's archive. Each one leaves that fence reporting unresolvable rather than answering, repaired by the next reconcile sweep if the cause was transient.", nil),
		fenceGeometryHashMismatch:   ms.NewCounter("detect_fence_geometry_hash_mismatch_total", "Geofence geometry documents that did not hash to the content address they were requested under. Always a bug, never transient: the peer served the wrong row, something re-encoded the document in transit, or the archive is corrupt.", nil),
		fenceArchiveSkew:            ms.NewCounter("detect_fence_archive_skew_total", "Geofence archive reads that failed because device-management does not serve the manifest doors — it is running a build from before manifest delivery. Repairs itself when that service rolls forward.", nil),
		fenceGeometryCacheHits:      ms.NewCounter("detect_fence_geometry_cache_hits_total", "Compiled-geometry cache lookups served from cache, avoiding both a cross-service read and a recompile.", nil),
		fenceGeometryCacheMisses:    ms.NewCounter("detect_fence_geometry_cache_misses_total", "Compiled-geometry cache lookups that had to fetch and compile the document.", nil),
		fenceGeometryCacheEvictions: ms.NewCounter("detect_fence_geometry_cache_evictions_total", "Compiled-geometry cache entries dropped to stay inside the cache's vertex bound.", nil),
		fenceGeometryCacheVertices:  ms.NewGauge("detect_fence_geometry_cache_vertices", "Total vertices held in the compiled-geometry cache — the quantity its bound is counted in.", nil),
	}
}

// recordFenceGeometryUnresolved records n manifest entries whose geometry could not be
// obtained. Nil-safe.
func (m *fenceGeometryMetrics) recordFenceGeometryUnresolved(n int) {
	if m == nil || n <= 0 {
		return
	}
	m.fenceGeometryUnresolved.Add(float64(n))
}

// recordFenceGeometryHashMismatch records one document that did not hash to the address it was
// requested under. Nil-safe.
func (m *fenceGeometryMetrics) recordFenceGeometryHashMismatch() {
	if m == nil {
		return
	}
	m.fenceGeometryHashMismatch.Inc()
}

// recordFenceArchiveSkew records one archive read refused because the peer does not serve the
// manifest doors. Nil-safe.
func (m *fenceGeometryMetrics) recordFenceArchiveSkew() {
	if m == nil {
		return
	}
	m.fenceArchiveSkew.Inc()
}

// recordFenceGeometryCache records the outcome of a batch of cache lookups and the resulting
// occupancy. Nil-safe.
func (m *fenceGeometryMetrics) recordFenceGeometryCache(hits, misses, evictions int, vertices int64) {
	if m == nil {
		return
	}
	if hits > 0 {
		m.fenceGeometryCacheHits.Add(float64(hits))
	}
	if misses > 0 {
		m.fenceGeometryCacheMisses.Add(float64(misses))
	}
	if evictions > 0 {
		m.fenceGeometryCacheEvictions.Add(float64(evictions))
	}
	m.fenceGeometryCacheVertices.Set(float64(vertices))
}

// setLeader publishes whether this replica holds the DETECT partition lease. It is
// raised at ACQUIRE, not at the end of the term build: a build can take a snapshot
// restore plus a full replay, and a leaderless alert firing through all of it would
// be indistinguishable from a real outage.
func (m *detectMetrics) setLeader(leader bool) { m.isLeader.Set(boolGauge(leader)) }

// setDetectLive publishes whether this replica is actually CONSUMING. Paired with
// setLeader it names the one state neither gauge can express alone — leader, but
// wedged in a term build — which is otherwise a silent stall on a pod whose every
// health signal is green. The chart's DetectLeaderIsNotConsuming rule is the pair's
// consumer, and it ANDs a checkpoint term so a long replay (which checkpoints as it
// goes) does not trip it.
func (m *detectMetrics) setDetectLive(live bool) { m.detectLive.Set(boolGauge(live)) }

func boolGauge(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
