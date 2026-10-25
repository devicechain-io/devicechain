// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
)

// The result every request is counted under. The set is closed, every series is created at
// zero when the metrics are built (so a rate alert sees a zero series rather than none on a
// healthy instance), and the only label is the result: before authentication a request has no
// tenant that can be trusted as a label value, and an attacker-chosen one would grow the
// series without bound.
const (
	ResultCreated            = "created"
	ResultAdopted            = "adopted"
	ResultReplayed           = "replayed"
	ResultAuthFailed         = "auth_failed"
	ResultInvalid            = "invalid"
	ResultReadTimeout        = "read_timeout"
	ResultRateLimitedSource  = "rate_limited_source"
	ResultRateLimitedTenant  = "rate_limited_tenant"
	ResultBusy               = "busy"
	ResultNotRegistered      = "not_registered"
	ResultNotEarmarked       = "not_earmarked"
	ResultDisabled           = "disabled"
	ResultTypeMismatch       = "type_mismatch"
	ResultTokenCollision     = "token_collision"
	ResultAlreadyProvisioned = "already_provisioned"
	ResultLimitExceeded      = "limit_exceeded"
	ResultUnavailable        = "unavailable"
	ResultMisconfigured      = "misconfigured"
	ResultUnsupportedType    = "unsupported_type"
	ResultError              = "error"
)

// Results lists every result, for seeding and for the test that pins the set.
var Results = []string{
	ResultCreated, ResultAdopted, ResultReplayed, ResultAuthFailed, ResultInvalid, ResultReadTimeout,
	ResultRateLimitedSource, ResultRateLimitedTenant, ResultBusy, ResultNotRegistered, ResultNotEarmarked,
	ResultDisabled, ResultTypeMismatch, ResultTokenCollision, ResultAlreadyProvisioned, ResultLimitExceeded,
	ResultUnavailable, ResultMisconfigured, ResultUnsupportedType, ResultError,
}

// Metrics are the endpoint's instruments.
type Metrics struct {
	Requests *prometheus.CounterVec
	InFlight prometheus.Gauge
	Duration *prometheus.HistogramVec
}

// NewMetrics registers the endpoint's instruments on the microservice's registry. Like every
// collector it belongs to the process: call it once, from the initialize path.
func NewMetrics(ms *core.Microservice) *Metrics {
	m := &Metrics{
		Requests: ms.NewCounterVec("provisioning_requests_total",
			"Device self-registration requests by result. result=\"auth_failed\" is every way the provision key or secret "+
				"can be refused (unknown tenant or key, wrong secret, a profile that is disabled or expired), counted together "+
				"on purpose; result=\"rate_limited_source\" and \"rate_limited_tenant\" are 429s, \"busy\" a 503 for want of a free "+
				"slot, and \"read_timeout\" a body that did not arrive in time.",
			[]string{"result"}),
		InFlight: ms.NewGauge("provisioning_in_flight",
			"Device self-registrations being worked on by this replica right now, after the body was read and before the answer. "+
				"A request still sending its body is not counted."),
		Duration: ms.NewHistogramVec("provisioning_request_duration_seconds",
			"Time to answer a device self-registration request, by result, from the moment the handler starts (so a slow "+
				"body is included).",
			[]string{"result"}, []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}),
	}
	for _, r := range Results {
		m.Requests.WithLabelValues(r)
		m.Duration.WithLabelValues(r)
	}
	return m
}

func (m *Metrics) observe(result string, d time.Duration) {
	m.Requests.WithLabelValues(result).Inc()
	m.Duration.WithLabelValues(result).Observe(d.Seconds())
}
