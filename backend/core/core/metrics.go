// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
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

// MetricsSubsystem is the Prometheus subsystem every metric this service exports sits
// under: the functional area with its hyphens removed, since a hyphen is not legal in a
// metric name.
//
// It exists so the rule is stated ONCE. It was written inline at each of the constructors
// below and, once a component outside this package needed it, was copied there too — at
// which point changing it here would silently move every metric except that one, while
// dashboards and alerts go on matching the literal old prefix.
func (ms *Microservice) MetricsSubsystem() string {
	return strings.ReplaceAll(ms.FunctionalArea, "-", "")
}

// MetricsRegisterer is where every metric this microservice constructs is registered.
//
// It returns a prometheus.Registerer interface built from a concrete *prometheus.Registry
// field, and the nil branch is not defensive noise. promauto.With reads a nil Registerer
// as "construct the collector but register it nowhere", and an interface value holding a
// typed nil pointer is NOT nil — so returning ms.metricsReg unconditionally would hand
// promauto a non-nil interface wrapping a nil registry, and the first metric built would
// panic on the nil dereference rather than quietly going unregistered.
//
// A Microservice built by NewMicroservice always has a registry. One built as a struct
// literal — which is how tests build them — has none, and its metrics are constructed
// unregistered rather than landing on the process-global default registry. That is the
// safe direction: an unregistered counter still counts, so the code under test behaves
// identically, while two literals sharing a functional area no longer collide.
func (ms *Microservice) MetricsRegisterer() prometheus.Registerer {
	if ms.metricsReg == nil {
		return nil
	}
	return ms.metricsReg
}

// MetricsGatherer is what a /metrics endpoint must be served from: this microservice's
// own registry unioned with the process default registry.
//
// Both halves are load-bearing, and the failure mode of dropping either is the same
// silent one — a clean 200 whose body is simply missing metrics, with no error and no
// log line. Without the first half, everything the constructors above build disappears.
// Without the second, so does everything still registered globally: the Go runtime and
// process collectors client_golang installs on the default registry in its own init, and
// the package-level promauto vars declared in core and in the services.
func (ms *Microservice) MetricsGatherer() prometheus.Gatherer {
	if ms.metricsReg == nil {
		return prometheus.DefaultGatherer
	}
	return prometheus.Gatherers{ms.metricsReg, prometheus.DefaultGatherer}
}

// UseMetricsRegistry points this microservice's metric constructors at reg.
//
// It is for callers that build a Microservice without NewMicroservice — tests — and
// need the metrics they then construct to be gatherable. Call it before constructing
// any metric: a collector is registered where it was built, so this cannot move one
// that already exists.
func (ms *Microservice) UseMetricsRegistry(reg *prometheus.Registry) {
	ms.metricsReg = reg
}

// MetricsHandler is the http.Handler a service serves its Prometheus exposition from.
//
// It is built here, once, rather than spelled out at each endpoint that registers a
// /metrics route, because two details of it are wrong by default and neither failure
// reports itself — both answer 200 with a well-formed body that is simply missing
// things:
//
//   - It gathers MetricsGatherer, NOT prometheus.DefaultGatherer. promhttp.Handler()
//     gathers the default one and nothing else, so it cannot see a single metric this
//     microservice constructs.
//   - It keeps promhttp's own scrape instrumentation. That is not part of HandlerFor:
//     promhttp.Handler() wraps its handler in InstrumentMetricHandler, which is where
//     promhttp_metric_handler_requests_total and promhttp_metric_handler_requests_in_flight
//     come from. Swapping in a bare HandlerFor drops both.
//
// The scrape instrumentation registers on THIS microservice's registry, which also ends
// the process-global collision it used to carry: two promhttp.Handler() calls in one
// process registered the same two collectors on the default registry.
func (ms *Microservice) MetricsHandler() http.Handler {
	h := promhttp.HandlerFor(ms.MetricsGatherer(), promhttp.HandlerOpts{})
	reg := ms.MetricsRegisterer()
	if reg == nil {
		// InstrumentMetricHandler registers unconditionally and would dereference a nil
		// Registerer, so a Microservice with no registry — a struct literal — gets the
		// uninstrumented handler rather than a panic.
		return h
	}
	return promhttp.InstrumentMetricHandler(reg, h)
}

// NewProcessorMetrics builds the instrumentation for a named processing loop
// (e.g. "resolve", "persist", "state"). The metric names are prefixed with name
// and namespaced/subsystemed by the service, so two services' loops do not
// collide.
func (ms *Microservice) NewProcessorMetrics(name string) *ProcessorMetrics {
	sub := ms.MetricsSubsystem()
	auto := promauto.With(ms.MetricsRegisterer())
	return &ProcessorMetrics{
		processed: auto.NewCounterVec(prometheus.CounterOpts{
			Namespace: METRICS_NAMESPACE, Subsystem: sub,
			Name: name + "_messages_total", Help: "Messages handled by the " + name + " loop, by result.",
		}, []string{"result"}),
		duration: auto.NewHistogram(prometheus.HistogramOpts{
			Namespace: METRICS_NAMESPACE, Subsystem: sub,
			Name: name + "_duration_seconds", Help: "Per-message handling duration for the " + name + " loop.",
			Buckets: prometheus.DefBuckets,
		}),
		inflight: auto.NewGauge(prometheus.GaugeOpts{
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
//
// 🔴 "failed" MEANT "routed to the dead-letter path" FOR A LONG TIME BEFORE ANY SUCH PATH
// EXISTED, and one of the consumers using it said so in a TODO beside the line. The label
// is true now (ADR-024) for the consumers that dead-letter — but making it true was not
// only a matter of building the sink, because one consumer using it never intended to
// route anywhere. That is what "dropped" is for: a projection that gives up is not
// deferring the work, it is discarding it, and the two must not report the same word.
const (
	ResultOK      = "ok"      // handled successfully
	ResultInvalid = "invalid" // poison: unparseable / no tenant (dropped where found)
	ResultFailed  = "failed"  // gave up and DEAD-LETTERED: the work is recorded (ADR-024)
	ResultDropped = "dropped" // gave up and DISCARDED: nothing records it, deliberately
	ResultRetry   = "retry"   // transient failure, redelivery requested
)
