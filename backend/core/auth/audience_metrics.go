// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// PromAudienceRecorder counts plane audience checks as
//
//	devicechain_<subsystem>_token_audience_checks_total{plane, audience, verdict}
//
// The counter is a request-volume signal, not a census of clients: one valid token replayed
// against a plane counts once per request.
type PromAudienceRecorder struct {
	checks *prometheus.CounterVec
}

// NewPromAudienceRecorder registers the counter on reg (nil leaves it unregistered, which is
// how a struct-literal Microservice's metrics behave). Registering twice on one registry
// panics, so a Microservice builds it once and reuses it.
func NewPromAudienceRecorder(reg prometheus.Registerer, subsystem string) *PromAudienceRecorder {
	return &PromAudienceRecorder{checks: promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
		Namespace: "devicechain",
		Subsystem: subsystem,
		Name:      "token_audience_checks_total",
		Help: "Authenticated requests by the plane they reached, the class of the token's audience " +
			"and the verdict (accepted, would_reject in observe mode, rejected in enforce mode).",
	}, []string{"plane", "audience", "verdict"})}
}

// Record implements Recorder.
func (r *PromAudienceRecorder) Record(plane Plane, class AudienceClass, verdict AudienceVerdict) {
	r.checks.WithLabelValues(string(plane), string(class), string(verdict)).Inc()
}

// CounterFor returns the counter series for one label set. It exists for tests that read a
// count back; production code only calls Record.
func (r *PromAudienceRecorder) CounterFor(plane Plane, class AudienceClass, verdict AudienceVerdict) prometheus.Counter {
	return r.checks.WithLabelValues(string(plane), string(class), string(verdict))
}
