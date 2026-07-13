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
	// a DB blip); the message was naked for redelivery, bounded by the redelivery cap.
	outcomeRetry = "retry"
	// outcomeDead — the message exhausted the redelivery cap and was written to the terminal
	// dead-letter subject (a permanently-failing send), never retried forever.
	outcomeDead = "dead"
	// outcomeInvalid — a malformed/poison message (no parseable tenant, undecodable JSON, or a
	// failed structural validation); a redelivery cannot fix it, so it was dropped (acked).
	outcomeInvalid = "invalid"
	// outcomeUnsupported — a well-formed dispatch this build cannot execute (a publish before the
	// Bento tier / Connector entity, slice C4b); terminal, dead-lettered so an operator can see it.
	outcomeUnsupported = "unsupported"
	// outcomeRateLimited — the dispatch's tenant was over its outbound egress rate (ADR-060 SD-3)
	// for longer than the smoothing wait budget, so it was shed to the dead-letter subject. A brief
	// burst is admitted by the wait and never reaches this; a rising rate_limited count is a tenant
	// sustained over its outbound quota.
	outcomeRateLimited = "rate_limited"
	// outcomeDeadWriteFailed — the terminal case where the dead-letter WRITE itself failed on the
	// last delivery the broker will make, so the dispatch could be neither delivered nor durably
	// dead-lettered: an explicit, alertable LOSS signal (never silently swallowed).
	outcomeDeadWriteFailed = "dead_write_failed"
)

// actionUnknown labels a message whose action kind could not be determined (malformed), so the
// metric's action label stays a bounded enum {httpCall, publish, unknown}.
const actionUnknown = "unknown"

// dispatchMetrics are the outbound-connectors observability counters (ADR-060 SD-3). Cardinality is
// bounded to a fixed action enum × a fixed outcome enum — never a per-tenant/per-connector label
// (the ADR-023 G.3 DoS lesson). Every recorder is nil-safe so a consumer built without a
// Microservice (unit tests) runs unmeasured rather than panicking on a global-registry
// double-registration.
type dispatchMetrics struct {
	dispatched *prometheus.CounterVec
}

// newDispatchMetrics registers the counters under the service's Prometheus namespace. A nil
// Microservice (unit tests) yields nil metrics.
func newDispatchMetrics(ms *core.Microservice) *dispatchMetrics {
	if ms == nil {
		return nil
	}
	return &dispatchMetrics{
		dispatched: ms.NewCounterVec("connector_dispatch_total",
			"Outbound connector dispatch requests processed, by action and terminal outcome (bounded enums).",
			[]string{"action", "outcome"}),
	}
}

// recordOutcome records one message's terminal disposition. action is the connectorwire kind (or
// actionUnknown for a message too malformed to classify); outcome is one of the outcome* enum.
func (m *dispatchMetrics) recordOutcome(action, outcome string) {
	if m == nil {
		return
	}
	m.dispatched.WithLabelValues(action, outcome).Inc()
}
