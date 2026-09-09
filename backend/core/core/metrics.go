// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
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
//
// Calling it records that a collector may now exist on whatever registry is attached,
// which is the fact UseMetricsRegistry refuses a late call on.
func (ms *Microservice) MetricsRegisterer() prometheus.Registerer {
	ms.metricsHandedOut.Store(true)
	if ms.metricsReg == nil {
		return nil
	}
	return ms.metricsReg
}

// metricsGatherer is what MetricsHandler serves: this microservice's own registry
// unioned with the process default registry.
//
// Both halves are load-bearing, and the failure mode of dropping either is the same
// silent one — a clean 200 whose body is simply missing metrics, with no error and no
// log line. Without the first half, everything the constructors above build disappears.
// Without the second, so does everything still registered globally: the Go runtime and
// process collectors client_golang installs on the default registry in its own init, and
// the package-level promauto vars declared in core and in the services.
func (ms *Microservice) metricsGatherer() prometheus.Gatherer {
	if ms.metricsReg == nil {
		return prometheus.DefaultGatherer
	}
	return prometheus.Gatherers{ms.metricsReg, prometheus.DefaultGatherer}
}

// UseMetricsRegistry points this microservice's metric constructors at reg. It is for
// callers that build a Microservice without NewMicroservice — tests — and need the
// metrics they then construct to be gatherable.
//
// 🔴 IT MUST BE CALLED BEFORE ANY METRIC IS CONSTRUCTED, AND IT ENFORCES THAT RATHER
// THAN ASKING FOR IT. A collector is registered where it was built and cannot be moved
// afterwards, so a late call does not redirect anything — it SPLITS: whatever was built
// first stays on the old registry while the gatherer reads the new one. Called after
// NewMicroservice, that strands the three readiness collectors and /metrics answers 200
// with no `ready` gauge, which is the same silent subtraction this ownership exists to
// end. A comment saying "call this first" would have been the assertion of an invariant
// with nothing enforcing it.
//
// It panics rather than returning an error because there is no runtime condition here
// to handle: the only way to reach it is a call in the wrong order, which is a
// programming mistake in the caller and is fixed by moving one line.
func (ms *Microservice) UseMetricsRegistry(reg *prometheus.Registry) {
	if ms.metricsHandedOut.Load() {
		panic("core: UseMetricsRegistry called after a metric was already constructed on this " +
			"Microservice; collectors cannot be moved between registries, so the earlier ones " +
			"would be stranded off the gatherer. Attach the registry before building any metric.")
	}
	ms.metricsReg = reg
}

// MetricsHandler is the http.Handler a service serves its Prometheus exposition from.
//
// It is built here, once, rather than spelled out at each endpoint that registers a
// /metrics route, because two details of it are wrong by default and neither failure
// reports itself — both answer 200 with a well-formed body that is simply missing
// things:
//
//   - It gathers metricsGatherer, NOT prometheus.DefaultGatherer. promhttp.Handler()
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
	h := promhttp.HandlerFor(ms.metricsGatherer(), promhttp.HandlerOpts{})
	reg := ms.MetricsRegisterer()
	if reg == nil {
		// InstrumentMetricHandler registers unconditionally and would dereference a nil
		// Registerer, so a Microservice with no registry — a struct literal — gets the
		// uninstrumented handler rather than a panic.
		return h
	}
	return promhttp.InstrumentMetricHandler(reg, h)
}

// metricNamePart is the grammar a metric name must satisfy. It is the Prometheus
// metric-name grammar minus the colon, which is reserved for recording rules and
// must not appear in a name a service exports directly.
var metricNamePart = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// requireMetricNamePart refuses a name fragment that cannot appear in a Prometheus
// metric name, naming the caller and the offending argument.
//
// 🔴 IT PANICS, AND IT DOES NOT SANITIZE. Both halves are deliberate.
//
// Not sanitizing, because substituting a corrected name for the one the caller asked
// for exports a series under an identifier nobody wrote down and nobody can grep for —
// a plausible value returned in place of a refusal.
//
// Panicking rather than returning an error, because there is no runtime condition here
// to handle. Every argument that reaches this is a compile-time string constant chosen
// by the author of a processing loop, so an illegal one is a programming mistake fixed
// by editing one line — the same reasoning UseMetricsRegistry panics on, and the same
// shape as promauto's own MustRegister, which these constructors already sit on top of.
// An error return would have to be plumbed out of all five metric constructors and
// through their callers, which build a processor and could do nothing with it but panic
// themselves.
//
// The failure it prevents is quiet rather than loud, which is why it is worth a guard:
// client_golang accepts an illegal name, and what a scrape then exports depends on the
// SCRAPER. A collector registered as `..._raise-alarm_inflight` is exported under the
// escaped name `..._raise_alarm_inflight` to a scraper taking the default escaping, and
// under the quoted, hyphenated name to one that negotiates escaping=allow-utf-8 — so
// the same series arrives under two different names and PromQL can only select the
// second through {__name__="..."}. Nothing anywhere reports an error.
func requireMetricNamePart(caller string, argument string, value string) string {
	if !metricNamePart.MatchString(value) {
		panic(fmt.Sprintf("core: %s was given %s=%q, which cannot appear in a Prometheus "+
			"metric name (it must match %s). Fix the name at the call site: it is not "+
			"corrected here, because a silently substituted name exports a series under an "+
			"identifier the caller never wrote.", caller, argument, value, metricNamePart))
	}
	return value
}

// requireMetricName refuses a metric this microservice could not legally export, and
// returns the subsystem and name to build it from so a caller uses the values that were
// checked. Both halves of the exported name are checked:
//
//   - the caller-supplied fragment, and
//   - the whole name it composes to, which is where an illegal FUNCTIONAL AREA shows up.
//     MetricsSubsystem only removes hyphens, so anything else illegal in the area — a
//     space, a dot, a leading digit — reaches the exported name intact.
//
// The composed name is what is checked rather than the subsystem on its own, because an
// EMPTY subsystem is legal: a Microservice built as a struct literal has no functional
// area, BuildFQName then drops the empty component, and the ~30 fixtures that build one
// go on constructing metrics exactly as the type documents.
func (ms *Microservice) requireMetricName(caller string, name string) (string, string) {
	name = requireMetricNamePart(caller, "name", name)
	sub := ms.MetricsSubsystem()
	if fq := prometheus.BuildFQName(METRICS_NAMESPACE, sub, name); !metricNamePart.MatchString(fq) {
		panic(fmt.Sprintf("core: %s cannot export a legal metric for the functional area %s: it "+
			"reduces to the subsystem %s, composing the name %s, which cannot appear in a "+
			"Prometheus metric name (it must match %s).",
			caller, strconv.Quote(ms.FunctionalArea), strconv.Quote(sub), strconv.Quote(fq), metricNamePart))
	}
	return sub, name
}

// NewProcessorMetrics builds the instrumentation for a named processing loop
// (e.g. "resolve", "persist", "state"). The metric names are prefixed with name
// and namespaced/subsystemed by the service, so two services' loops do not
// collide.
//
// It panics if name cannot appear in a Prometheus metric name; see
// requireMetricNamePart for why that is a refusal rather than a correction.
func (ms *Microservice) NewProcessorMetrics(name string) *ProcessorMetrics {
	sub, name := ms.requireMetricName("NewProcessorMetrics", name)
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
