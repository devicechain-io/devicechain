// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
)

// The /metrics endpoint and where a Microservice registers its metrics are ONE
// decision, and getting them out of step is invisible from the outside.
//
// Every metric a Microservice builds is registered on a registry that Microservice
// owns. promhttp.Handler() — what this route used to serve, and the obvious thing to
// reach for — gathers from prometheus.DefaultGatherer and from nowhere else. Serving
// it here does not error, does not log, and does not fail a request: it answers 200
// with a well-formed exposition body that is simply missing every metric the service
// exports. The only symptom is a dashboard that goes blank.
//
// That is why this drives the handler and not the registry. Asserting the collectors
// are on ms's registry would PASS in exactly the state this exists to catch, because
// in that state the registry is fine and the endpoint reads a different one.
func TestMetricsRouteServesTheMicroservicesOwnCollectors(t *testing.T) {
	ms := microserviceWithRegistry("event-processing")

	// 🔴 A PLAIN COUNTER, NOT A COUNTERVEC, AND THE CHOICE IS THE TEST. A Counter
	// exports a 0 sample as soon as it is constructed, so its absence from the body
	// means the endpoint did not read this registry. A CounterVec exports NOTHING
	// until some label combination is used — so a vec probe reads absent-and-fine and
	// absent-and-broken identically, and would pass against the defect.
	ms.NewCounter("registry_probe_total", "Probe metric for the /metrics wiring test.")

	// The NATS manager's collectors are built through the same Microservice
	// constructors, so this is the half that pins the messaging layer's metrics to the
	// same endpoint. Constructing a manager touches no broker.
	messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(),
		func(*messaging.NatsManager) error { return nil })

	// jetstream_broker_clustered is likewise the one NATS collector that is a plain
	// Gauge; the replication triple beside it is a GaugeVec and reports nothing until
	// a stream is sampled, which an unconnected manager never does.
	assertScrapeMentions(t, ms,
		"devicechain_eventprocessing_registry_probe_total",
		"devicechain_eventprocessing_jetstream_broker_clustered",
	)
}

// The endpoint gathers the owned registry UNIONED with the process default one, and
// this is the second half of that union.
//
// The Go runtime and process collectors are installed on the default registry by
// client_golang's own init, and nothing moves them. Serving only the owned registry
// would therefore drop go_* and process_* from every service's /metrics — the same
// silent 200-with-less-in-it as the other direction, against anyone alerting on
// goroutine counts or process memory. It also covers the package-level promauto vars
// that core and the services declare outside any Microservice.
func TestMetricsRouteStillServesTheProcessCollectors(t *testing.T) {
	assertScrapeMentions(t, microserviceWithRegistry("device-state"),
		"go_goroutines",
		"process_start_time_seconds",
	)
}

// promhttp's own scrape instrumentation has to survive the move too, and it is the
// piece most easily lost: it is not part of the exposition handler at all.
// promhttp.Handler() is InstrumentMetricHandler wrapped around HandlerFor, and those
// two families come from the wrapper. Swapping in a bare HandlerFor — the obvious
// one-line way to point the route at another gatherer — keeps every metric this test's
// siblings check and silently drops these.
func TestMetricsRouteKeepsPromhttpScrapeInstrumentation(t *testing.T) {
	assertScrapeMentions(t, microserviceWithRegistry("user-management"),
		"promhttp_metric_handler_requests_total",
		"promhttp_metric_handler_requests_in_flight",
	)
}

// microserviceWithRegistry builds the kind of Microservice a service binary has: one
// that owns a metrics registry. NewMicroservice is not used because it reads the
// environment, installs signal handlers and reassigns the process logger.
func microserviceWithRegistry(area string) *core.Microservice {
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: area}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	return ms
}

// assertScrapeMentions runs the manager's REAL registration and scrapes /metrics
// through the mux it populated, requiring every name to appear in the body.
//
// It drives ExecuteInitialize rather than a handler the test builds for itself. That
// closes the gap this helper used to carry: it drove a named wrapper, so it would have
// kept passing if the registration had gone somewhere else entirely. Now the route has
// to be reachable at the path the chart scrapes, on the mux the server serves.
func assertScrapeMentions(t *testing.T, ms *core.Microservice, names ...string) {
	t.Helper()

	gql := &GraphQLManager{Microservice: ms, Gate: core.NewReadinessGate()}
	if err := gql.ExecuteInitialize(context.Background()); err != nil {
		t.Fatalf("ExecuteInitialize: %v", err)
	}

	rec := httptest.NewRecorder()
	ms.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics returned %d, want 200: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	for _, want := range names {
		if !strings.Contains(body, want) {
			t.Errorf("the /metrics body does not mention %q", want)
		}
	}
}
