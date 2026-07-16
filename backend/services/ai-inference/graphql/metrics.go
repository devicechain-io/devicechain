// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"errors"

	"github.com/devicechain-io/dc-ai-inference/inference"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
)

// Outcome label values for ai_inference_calls_total. A fixed, small enum — the
// bounded cardinality the ADR-023 G.3 lesson requires: never a per-tenant, per-model,
// or per-provider value (the tenant reaching this path is authenticated, but a
// Prometheus series is never evicted, so a per-tenant label is a cardinality DoS).
const (
	// outcomeServed — the provider answered and a candidate was returned.
	outcomeServed = "served"
	// outcomeRateLimited — the tenant was over its per-tenant inference rate ceiling
	// (ADR-056 §6 / ADR-023) and the call was shed before any provider was reached. A
	// rising count is the operator's signal that a tenant is sustained over quota —
	// this is deliberately the metric rather than a per-call log line.
	outcomeRateLimited = "rate_limited"
	// outcomeConsentRequired — the tenant has not opted in to external routing, so no
	// data left the boundary. Expected for a tenant that simply has not been granted
	// consent; not an error condition.
	outcomeConsentRequired = "consent_required"
	// outcomeUnavailable — a fail-closed gate tripped for an operator-facing reason (no
	// active provider, disabled, missing key, no tenant). Which gate is deliberately
	// NOT a label: it would leak configuration shape into a metric and is already in
	// the server-side log.
	outcomeUnavailable = "unavailable"
	// outcomeProviderError — the provider itself failed the call (transport, non-2xx,
	// empty completion). Distinguished from unavailable because it means the external
	// dependency is misbehaving rather than the platform being unconfigured.
	outcomeProviderError = "provider_error"
)

// Token direction label values for ai_inference_tokens_total (bounded enum).
const (
	directionInput  = "input"
	directionOutput = "output"
)

// Metrics are the ai-inference observability counters (ADR-056). They make inference
// SPEND observable — nothing else in the platform counts tokens — without building an
// accounting substrate: these are counters an operator watches and alerts on, not a
// per-tenant ledger and not a budget anything enforces.
//
// Every recorder is nil-safe so a resolver built without a Microservice (unit tests)
// runs unmeasured rather than panicking on a global-registry double-registration.
type Metrics struct {
	calls  *prometheus.CounterVec
	tokens *prometheus.CounterVec
}

// NewMetrics registers the counters under the service's Prometheus namespace. A nil
// Microservice (unit tests) yields nil metrics.
func NewMetrics(ms *core.Microservice) *Metrics {
	if ms == nil {
		return nil
	}
	return &Metrics{
		calls: ms.NewCounterVec("ai_inference_calls_total",
			"Tenant inference requests by terminal outcome (bounded enum).",
			[]string{"outcome"}),
		tokens: ms.NewCounterVec("ai_inference_tokens_total",
			"Inference tokens reported by the provider, by direction (bounded enum). The spend signal.",
			[]string{"direction"}),
	}
}

// recordCall records one tenant inference request's terminal outcome.
func (m *Metrics) recordCall(outcome string) {
	if m == nil {
		return
	}
	m.calls.WithLabelValues(outcome).Inc()
}

// recordUsage records what a completed provider call cost. A provider that does not
// report usage yields zeros, which are skipped rather than counted — an unreported
// call must not read as a free one.
func (m *Metrics) recordUsage(in, out int) {
	if m == nil {
		return
	}
	if in > 0 {
		m.tokens.WithLabelValues(directionInput).Add(float64(in))
	}
	if out > 0 {
		m.tokens.WithLabelValues(directionOutput).Add(float64(out))
	}
}

// outcomeFor classifies a resolution/inference error into the bounded outcome enum.
// It reads the sentinels rather than error text, so it cannot drift with wording.
func outcomeFor(err error) string {
	switch {
	case err == nil:
		return outcomeServed
	case errors.Is(err, inference.ErrRateLimited):
		return outcomeRateLimited
	case errors.Is(err, inference.ErrConsentRequired):
		return outcomeConsentRequired
	case errors.Is(err, inference.ErrUnavailable):
		return outcomeUnavailable
	default:
		return outcomeProviderError
	}
}
