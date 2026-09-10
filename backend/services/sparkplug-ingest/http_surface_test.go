// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// leftBehindPath stands in for a route that a switchover forgot to move: registered
// on http.DefaultServeMux, exactly as this service's probes used to be.
const leftBehindPath = "/sparkplug-left-behind"

// registerLeftBehind puts that route on the default mux ONCE per process.
//
// ServeMux panics on a duplicate pattern and `go test -count=2` runs every test twice
// in one binary, so a plain registration inside the test would be the shape this
// repository keeps having to fix: green under `go test`, a panic on a rerun.
var registerLeftBehind = sync.OnceFunc(func() {
	http.HandleFunc(leftBehindPath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
})

// newTestMicroservice installs a Microservice with its own mux and metrics registry,
// restoring the package globals afterwards. Each call gets a fresh one, so the probe
// registrations of one test cannot collide with another's. It returns the registry,
// which is what the metric assertions below read.
//
// 🔴 THE REGISTRY IS NOT OPTIONAL SCAFFOLDING. A bare &core.Microservice{} has a nil
// registerer, and promauto reads nil as "construct the collector but register it
// nowhere" — every metric then builds happily, twice, and a test written on one could
// not observe a duplicate registration at all.
func newTestMicroservice(t *testing.T) *prometheus.Registry {
	t.Helper()

	prevMs, prevSrv := Microservice, httpServer
	prevNats, prevGauge := NatsManager, leaderGauge
	t.Cleanup(func() {
		Microservice, httpServer = prevMs, prevSrv
		NatsManager, leaderGauge = prevNats, prevGauge
	})

	Microservice = &core.Microservice{
		InstanceId:     "test",
		FunctionalArea: "sparkplug-ingest",
		Readiness:      core.NewReadinessGate(),
	}
	registry := prometheus.NewRegistry()
	Microservice.UseMetricsRegistry(registry)
	// No validator, matching production: this service authenticates at the broker and
	// verifies no JWT, so the gate is opened without an auth surface rather than left
	// closed (which would make every /readyz below a 503 for the wrong reason).
	Microservice.Readiness.MarkReadyWithoutAuthSurface()
	httpServer = nil
	leaderGauge = nil
	return registry
}

// registeredMetrics is the sorted set of metric family names currently registered. It
// reads the registry rather than /metrics because the assertion has to be made BEFORE
// the first server start, when there is no server to read it from.
//
// 🔴 IT DOES NOT SEE A VEC WITH NO CHILDREN, and reading the registry instead of the
// text output does not change that: Gather() builds families from collected series, so
// a CounterVec/GaugeVec nobody has observed a label value on contributes nothing —
// messages_total and the eight jetstream_* GaugeVecs are absent from every list this
// returns. The consequence for the caller is worth stating plainly: a plain Counter or
// Gauge moved onto the start path is caught by the set comparison a pass before it
// would panic, but a childless Vec is invisible here and is caught by the PANIC ARM
// alone, on the second entry. Coverage holds either way; the early, legible failure
// does not.
func registeredMetrics(t *testing.T, registry *prometheus.Registry) []string {
	t.Helper()

	families, err := registry.Gather()
	require.NoError(t, err)
	names := make([]string, 0, len(families))
	for _, family := range families {
		names = append(names, family.GetName())
	}
	sort.Strings(names)
	return names
}

// get returns the status code for a path on the running server.
func get(t *testing.T, path string) int {
	t.Helper()
	resp, err := http.Get("http://" + httpServer.Addr() + path)
	require.NoError(t, err)
	defer resp.Body.Close()
	return resp.StatusCode
}

// A second entry into the start phase must not panic, and that phase must build nothing
// that can only be built once.
//
// 🔴 THIS IS THE TRAP THE START PHASE MAKES EASY TO WALK INTO, and it has TWO doors in
// this service. LifecycleComponent does not promise ExecuteStart runs once — a start
// that fails restores the component to Initialized, so a retried start enters it again
// — and both of these panic on a duplicate:
//
//   - the probe routes go through ServeMux.Handle, which panics on a duplicate pattern;
//   - the leader gauge goes through promauto, which panics on a duplicate registration.
//
// Build either from the start path and a retried start is a crash — not at the next
// deploy, which is why neither door has ever been walked through in production.
//
// So the whole start sequence is driven twice here, with the initialize phase run once
// beforehand, exactly as a retry after a failed start reaches it. Both assertions matter
// and they fail differently:
//
//   - a construction moved onto the start path PANICS on the second pass, which fails
//     the test binary rather than this test. This arm catches EVERY instrument;
//   - and for an instrument that yields a series on construction — a plain Counter or
//     Gauge, as leaderGauge is — the metric-set comparison catches it one pass earlier,
//     as a plain failure, because the start phase changed the registry between the
//     snapshot and the check. A Vec with no children is invisible to that comparison;
//     see registeredMetrics.
func TestStartPhaseRestartDoesNotPanic(t *testing.T) {
	registry := newTestMicroservice(t)

	// The INITIALIZE phase, through the SERVICE's own functions rather than copies of
	// them: a test that called RegisterProbes or built its own gauge would be asserting
	// against its own wiring, and would keep passing if the real one moved.
	registerHttpRoutes()
	buildMetrics()
	// The lease hangs off the NatsManager, which production also builds in initialize.
	// This one is never Initialized, so its JetStream context is nil and startLeadership
	// fails at the lease — after the point where a start-phase instrument would be
	// constructed, which is all this test needs to drive.
	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(),
		func(*messaging.NatsManager) error { return nil })

	afterInitialize := registeredMetrics(t, registry)
	// The counterweight: "the start phase registers nothing new" is also satisfied by a
	// buildMetrics that builds nothing at all.
	require.Contains(t, afterInitialize, "devicechain_sparkplugingest_is_leader",
		"the leader gauge is not built in the initialize phase")
	// The rebirth queue's counters are named here for the same reason, and they are the
	// half of buildMetrics most likely to be added later by someone reaching for the
	// nearest constructor: a plain Counter yields a series the moment it is built, so if
	// one of these moved onto the start path the equality check below would catch it —
	// but only while something asserts it was in the initialize snapshot to begin with.
	require.Contains(t, afterInitialize, "devicechain_sparkplugingest_rebirth_enqueued_total",
		"the rebirth enqueued counter is not built in the initialize phase")
	require.Contains(t, afterInitialize, "devicechain_sparkplugingest_rebirth_dropped_total",
		"the rebirth dropped counter is not built in the initialize phase")

	// start runs everything afterMicroserviceStarted does that a test can drive.
	start := func() {
		require.ErrorContains(t, startLeadership(), "JetStream context not initialized",
			"the lease got further than this test can drive it; the assertions below no longer cover the start path")
		require.NoError(t, startHttpServer(0), "start refused")
	}

	start()
	t.Cleanup(func() { _ = httpServer.Shutdown(context.Background()) })
	require.Equal(t, http.StatusOK, get(t, "/healthz"), "first start does not serve")
	require.Equal(t, afterInitialize, registeredMetrics(t, registry),
		"the start phase registered a metric; it must construct none, or a restart panics")

	require.NoError(t, httpServer.Shutdown(context.Background()))

	// Reaching the far side of this at all is half the assertion: a construction moved
	// into the start path panics, and a panic fails the binary rather than this test.
	start()

	// And the other half, over the wire: a restart that binds but serves nothing is the
	// failure http.Server's latched shuttingDown flag produces, and it is invisible to
	// a nil error.
	require.Equal(t, http.StatusOK, get(t, "/healthz"), "restarted server does not serve")
	require.Equal(t, afterInitialize, registeredMetrics(t, registry),
		"the restarted start phase registered a metric")
}

// The server must serve the microservice's own mux and NOTHING ELSE.
//
// This is the negative control for the switchover itself. Giving an http.Server an
// explicit Handler silently unmounts everything still on http.DefaultServeMux:
// http.Handle keeps compiling and keeps registering, onto a mux nobody serves, with no
// error and no log line. A route left behind by the move would therefore look exactly
// like a route that was never written.
//
// 🔴 READ WHICH ASSERTION CATCHES WHAT, because the two here fail for different
// regressions and only one of them is about this service:
//
//   - The 404 below fires only if the SERVER stops serving the owned mux — if
//     NewHttpServer stopped setting Handler, say. A route this service leaves behind
//     keeps 404ing either way, because it was never on the owned mux to begin with.
//   - The counterweight assertions below it — the probe routes answering 200 — are what
//     catch a registration left on the default mux: those routes stop being served and
//     turn 404. That is the assertion that fires for the switchover regression.
//
// Both are worth having; conflating them would leave the reader expecting the wrong one
// to go red.
func TestServerDoesNotServeTheDefaultMux(t *testing.T) {
	newTestMicroservice(t)
	// The SERVICE's registration, not a copy of it: a test that called RegisterProbes
	// itself would keep passing if this went back to http.Handle on the default mux.
	registerHttpRoutes()
	registerLeftBehind()

	require.NoError(t, startHttpServer(0))
	t.Cleanup(func() { _ = httpServer.Shutdown(context.Background()) })

	require.Equal(t, http.StatusNotFound, get(t, leftBehindPath),
		"a route on http.DefaultServeMux is being served; this server is not serving the microservice's own mux")

	// The counterweight, and it is not decoration: "serves nothing from the default
	// mux" is also satisfied by a server that serves nothing at all, which is the very
	// failure this whole change is about.
	require.Equal(t, http.StatusOK, get(t, "/healthz"), "the owned probe routes are not served")
	require.Equal(t, http.StatusOK, get(t, "/readyz"))
	require.Equal(t, http.StatusOK, get(t, "/metrics"))
}
