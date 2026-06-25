// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ProcessorMetrics is the shared RED-style instrumentation for a message-
// processing loop (ADR-022 review E13): a result-labeled throughput counter, a
// handling-duration histogram, and an in-flight gauge. It is constructed via
// Microservice.NewProcessorMetrics so the namespace/subsystem (the instance's
// functional area) are auto-filled, giving every service the same metric shape.
//
// It is nil-safe: a nil *ProcessorMetrics no-ops, so a processor can hold one
// unconditionally and unit tests that build it without metrics still run.
type ProcessorMetrics struct {
	processed *prometheus.CounterVec
	duration  prometheus.Histogram
	inflight  prometheus.Gauge
}

// NewProcessorMetrics builds the instrumentation for a named processing loop
// (e.g. "resolve", "persist", "state"). The metric names are prefixed with name
// and namespaced/subsystemed by the service, so two services' loops do not
// collide.
func (ms *Microservice) NewProcessorMetrics(name string) *ProcessorMetrics {
	sub := strings.ReplaceAll(ms.FunctionalArea, "-", "")
	return &ProcessorMetrics{
		processed: promauto.NewCounterVec(prometheus.CounterOpts{
			Namespace: METRICS_NAMESPACE, Subsystem: sub,
			Name: name + "_messages_total", Help: "Messages handled by the " + name + " loop, by result.",
		}, []string{"result"}),
		duration: promauto.NewHistogram(prometheus.HistogramOpts{
			Namespace: METRICS_NAMESPACE, Subsystem: sub,
			Name: name + "_duration_seconds", Help: "Per-message handling duration for the " + name + " loop.",
			Buckets: prometheus.DefBuckets,
		}),
		inflight: promauto.NewGauge(prometheus.GaugeOpts{
			Namespace: METRICS_NAMESPACE, Subsystem: sub,
			Name: name + "_inflight", Help: "Messages currently being handled by the " + name + " loop.",
		}),
	}
}

// Start marks a message as in-flight and returns a completion function to call
// when handling finishes; it records the result label and the elapsed duration
// and clears the in-flight gauge. Usage:
//
//	done := pm.Start()
//	... handle the message ...
//	done(core.ResultOK)
func (pm *ProcessorMetrics) Start() func(result string) {
	if pm == nil {
		return func(string) {}
	}
	start := time.Now()
	pm.inflight.Inc()
	return func(result string) {
		pm.inflight.Dec()
		pm.duration.Observe(time.Since(start).Seconds())
		pm.processed.WithLabelValues(result).Inc()
	}
}

// Result labels for ProcessorMetrics completion, kept as constants so every
// service reports the same vocabulary.
const (
	ResultOK      = "ok"      // handled successfully
	ResultInvalid = "invalid" // poison: unparseable / no tenant (dropped)
	ResultFailed  = "failed"  // routed to the dead-letter path
	ResultRetry   = "retry"   // transient failure, redelivery requested
)
