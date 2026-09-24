// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
)

// Outcome label values for connector_dispatch_total. This is a fixed, small enum — the bounded
// cardinality the ADR-023 G.3 lesson requires: never a per-tenant or per-connector value.
const (
	// outcomeSent — the outbound send succeeded (a 2xx for httpCall).
	outcomeSent = "sent"
	// outcomeRetry — a transient failure (send error/non-2xx, or a secret-resolve error that may be
	// a DB blip), or a dispatch whose rate wait ran into its message's redelivery deadline so no send
	// could finish in time; the message was left unacked for AckWait-paced redelivery, bounded by the
	// redelivery cap.
	outcomeRetry = "retry"
	// outcomeDead — the message exhausted the redelivery cap and was written to the terminal
	// dead-letter subject (a permanently-failing send), never retried forever.
	outcomeDead = "dead"
	// outcomeInvalid — a message a redelivery cannot fix: a malformed/poison message at the consumer
	// (no parseable tenant, undecodable JSON, failed structural validation), which is dropped (acked);
	// or an executor-terminal dispatch (a dangling/unpublished ConnectorRef, or a malformed stored
	// config of a supported type), which is dead-lettered so an operator can see it.
	outcomeInvalid = "invalid"
	// outcomeUnsupported — a well-formed dispatch this build cannot execute: a publish whose connector
	// type has no generator shipped yet (e.g. kafka before slice C4c), or publish on an httpCall-only
	// deployment; terminal, dead-lettered so an operator can see it.
	outcomeUnsupported = "unsupported"
	// outcomeRateLimited — the dispatch's tenant was over its outbound egress rate (ADR-060 SD-3)
	// for longer than the smoothing wait budget, so it was shed to the dead-letter subject. A brief
	// burst is admitted by the wait and never reaches this. event-processing meters the same ceiling
	// on the same trigger time and sheds an over-quota action before dispatching it, so a sustained
	// rate_limited count is NOT the ordinary sign of a tenant over quota (that is the source's
	// react_connector_egress_shed_total): it means this end refused what the source admitted -- the
	// two ends' ceilings disagree, or retries of failing sends are being metered here a second time.
	// The chart's ConnectorDispatchRateLimited alert fires on it.
	outcomeRateLimited = "rate_limited"
	// outcomeTenantDeleted — the dispatch's tenant has been through the delete door (ADR-077), so the
	// send was refused and the message dropped (acked). Counted apart from a rate shed because the two
	// mean opposite things: a rate shed is a live tenant over quota whose dead-lettered message is
	// replayable, this is a tenant that will not exist and whose message must never be sent.
	outcomeTenantDeleted = "tenant_deleted"
	// outcomeBlocked — the destination resolved to an address a tenant may not reach (a private,
	// loopback, link-local or cloud-metadata address), so the connection was refused before a byte
	// was written. Terminal and dead-lettered, never retried: waiting does not make an address
	// public, so a retry would only burn the redelivery budget and delay the dead-letter that tells
	// an operator what happened. Counted apart from `invalid` because it means something different
	// to whoever is looking: an invalid dispatch is malformed, a blocked one is well-formed and
	// aimed somewhere it must not go.
	outcomeBlocked = "blocked"
	// outcomeDeadWriteFailed — the terminal case where the dead-letter WRITE itself failed on the
	// last delivery the broker will make, so the dispatch could be neither delivered nor durably
	// dead-lettered. This label is the DISPOSITION, kept so the outcomes still sum to the dispatches
	// handled; the alertable signal is the service's dead_letter_lost_total, which the same branch
	// moves (deadletter.Producer.Lost) and which the DeadLetterWriteLost alert reads. No alert reads
	// this label, and none should: a loss is alerted on the one series every service shares.
	outcomeDeadWriteFailed = "dead_write_failed"
	// outcomeDeadIndexFailed — the give-up was durably dead-lettered on this service's own terminal
	// subject, but its INDEX entry on the platform dead-letter stream could not be written. Nothing
	// is lost that a replay needs; what is lost is the operator's view of it in the one list they
	// read, which is worth a counter of its own rather than a log line.
	outcomeDeadIndexFailed = "dead_index_failed"
)

// actionUnknown labels a message whose action kind could not be determined (malformed), so the
// metric's action label stays a bounded enum {httpCall, publish, unknown}.
const actionUnknown = "unknown"

// DispatchMetrics are the outbound-connectors observability counters (ADR-060 SD-3). Cardinality is
// bounded to a fixed action enum × a fixed outcome enum — never a per-tenant/per-connector label
// (the ADR-023 G.3 DoS lesson). Every recorder is nil-safe so a consumer built without a
// Microservice (unit tests) runs unmeasured rather than panicking on a global-registry
// double-registration.
type DispatchMetrics struct {
	dispatched *prometheus.CounterVec
	// clockFallback counts the dispatches the egress limiter metered on a fallback clock because
	// they carried no usable trigger time (core.NewRateClockFallbacks).
	clockFallback func(core.MeteringClock)
}

// NewDispatchMetrics registers the counters under the service's Prometheus namespace. A nil
// Microservice (unit tests) yields nil metrics.
//
// 🔴 CALL IT FROM THE INITIALIZE PHASE, WHICH RUNS ONCE, AND PASS THE RESULT TO
// NewDispatchConsumer. The consumer is built inside the NATS manager's oncreate callback
// — it has to be, because it holds a reader bound to the connection — and that callback
// runs on EVERY start. A counter built there is registered again whenever that
// callback is entered again — which any start retried after a failed one does — and
// MustRegister panics on the duplicate.
func NewDispatchMetrics(ms *core.Microservice) *DispatchMetrics {
	if ms == nil {
		return nil
	}
	return &DispatchMetrics{
		dispatched: ms.NewCounterVec("connector_dispatch_total",
			"Outbound connector dispatch requests processed, by action and terminal outcome (bounded enums).",
			[]string{"action", "outcome"}),
		clockFallback: core.NewRateClockFallbacks(ms),
	}
}

// recordClockFallback records which clock one dispatch was metered on; only a fallback counts.
func (m *DispatchMetrics) recordClockFallback(c core.MeteringClock) {
	if m == nil || m.clockFallback == nil {
		return
	}
	m.clockFallback(c)
}

// recordOutcome records one message's terminal disposition. action is the connectorwire kind (or
// actionUnknown for a message too malformed to classify); outcome is one of the outcome* enum.
func (m *DispatchMetrics) recordOutcome(action, outcome string) {
	if m == nil {
		return
	}
	m.dispatched.WithLabelValues(action, outcome).Inc()
}
