// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// probeTarget is one row of the probe contract: the path the chart asks for, and what
// the pod must answer in each readiness state.
//
// The paths are literals rather than references to a constant, deliberately. The chart
// names them independently — deployment.yaml's liveness, readiness and startup probes
// and servicemonitor.yaml's scrape path — and this module cannot see deploy/. Sharing a
// constant with the production code would let both sides move together and leave the
// chart pointing at a path nothing serves, which is the one failure this table exists
// to catch.
type probeTarget struct {
	path        string
	whenUnready int
	whenReady   int
	whenDrained int
}

var probeContract = []probeTarget{
	// Liveness answers for the life of the process. It must NOT track readiness: a pod
	// that reports unhealthy while it waits for its auth gate gets killed and restarted
	// into the same wait, which is a crash loop caused by the probe rather than by the
	// service.
	{path: "/healthz", whenUnready: 200, whenReady: 200, whenDrained: 200},

	// Readiness gates the data plane on auth being live, and flips back to 503 for the
	// drain window a SIGTERM opens — that second transition is what lets a terminating
	// pod leave its Service endpoints before it stops accepting connections.
	{path: "/readyz", whenUnready: 503, whenReady: 200, whenDrained: 503},

	// Metrics are always scrapable. A scrape that started failing whenever a pod was
	// draining would blind the dashboards for exactly the window an operator is
	// watching them.
	{path: "/metrics", whenUnready: 200, whenReady: 200, whenDrained: 200},
}

func TestRegisterProbesServesTheChartsProbeContract(t *testing.T) {
	ms := &Microservice{FunctionalArea: "probe-area"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	gate := NewReadinessGate()
	ms.RegisterProbes(gate)

	status := func(path string) int {
		rec := httptest.NewRecorder()
		ms.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
	}

	for _, p := range probeContract {
		if got := status(p.path); got != p.whenUnready {
			t.Errorf("%s before the gate opens = %d, want %d", p.path, got, p.whenUnready)
		}
	}

	gate.MarkReadyWithoutAuthSurface()
	for _, p := range probeContract {
		if got := status(p.path); got != p.whenReady {
			t.Errorf("%s once ready = %d, want %d", p.path, got, p.whenReady)
		}
	}

	gate.BeginDrain()
	for _, p := range probeContract {
		if got := status(p.path); got != p.whenDrained {
			t.Errorf("%s while draining = %d, want %d", p.path, got, p.whenDrained)
		}
	}
}

// A nil gate must read NOT READY.
//
// The direction is the whole assertion. A server whose readiness gate was never wired
// has no evidence it can serve, and answering 200 puts it into its Service endpoints on
// the strength of a missing argument. Failing closed keeps it out until something says
// otherwise, which is what the GraphQL server has always done.
func TestRegisterProbesTreatsANilGateAsNotReady(t *testing.T) {
	ms := &Microservice{FunctionalArea: "nil-gate"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ms.RegisterProbes(nil)

	rec := httptest.NewRecorder()
	ms.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/readyz with no gate = %d, want 503; a server with no readiness evidence must not join Service endpoints", rec.Code)
	}

	// The counterweight: failing closed on readiness must not take liveness with it, or
	// a pod with an unwired gate is restarted forever instead of merely held back.
	rec = httptest.NewRecorder()
	ms.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("/healthz with no gate = %d, want 200", rec.Code)
	}
}

// /metrics must be the microservice's own instrumented handler, not a fresh bare one.
//
// RegisterProbes is a new place that could plausibly reach for promhttp.Handler(), and
// doing so would answer 200 with every metric this microservice exports missing — the
// silent failure the owned registry exists to end. The probe above only checks the
// status code, which that mistake would still pass.
func TestRegisterProbesServesTheMicroservicesOwnMetrics(t *testing.T) {
	ms := &Microservice{FunctionalArea: "metrics-probe"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	// A plain Counter: it exports a sample as soon as it is built. A CounterVec exports
	// nothing until a label combination is used, so it could not tell a missing metric
	// from an idle one.
	ms.NewCounter("probe_wiring_total", "Probe metric for the /metrics route.", nil)
	ms.RegisterProbes(NewReadinessGate())

	rec := httptest.NewRecorder()
	ms.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	for _, want := range []string{
		"devicechain_metricsprobe_probe_wiring_total", // the owned registry
		"go_goroutines", // the default registry, via the union
		"promhttp_metric_handler_requests_in_flight", // promhttp's own scrape instrumentation
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics does not mention %q", want)
		}
	}
}

// Two microservices in one process must get two muxes.
//
// A shared one would reintroduce, one layer up, exactly what moving off
// http.DefaultServeMux removes: ServeMux panics on a duplicate pattern, so two
// microservices registering /healthz would take the process down.
func TestEachMicroserviceGetsItsOwnMux(t *testing.T) {
	first := &Microservice{FunctionalArea: "mux-a"}
	second := &Microservice{FunctionalArea: "mux-b"}
	first.UseMetricsRegistry(prometheus.NewRegistry())
	second.UseMetricsRegistry(prometheus.NewRegistry())

	if first.Mux() == second.Mux() {
		t.Fatal("two microservices share one mux; registering the same path on both would panic")
	}
	if first.Mux() != first.Mux() {
		t.Fatal("Mux() returned a different mux on the second call; routes registered through one would not be served by the other")
	}

	// Reaching past both is the assertion: on a shared mux the second panics.
	first.RegisterProbes(NewReadinessGate())
	second.RegisterProbes(NewReadinessGate())
}

// The lifecycle test binds a REAL listener, because HttpServer's whole risk is
// ordering and a test that never binds a port cannot see it.
//
// It covers the four transitions a service's startup and teardown actually make: a
// bind that succeeds and serves; a bind that fails and must SAY so rather than log
// from inside a goroutine; a shutdown that stops serving; and a teardown that runs
// after a startup which never got as far as starting this server.
func TestHttpServerLifecycleOverARealListener(t *testing.T) {
	ms := &Microservice{FunctionalArea: "listener"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	gate := NewReadinessGate()
	gate.MarkReadyWithoutAuthSurface()
	ms.RegisterProbes(gate)

	srv := ms.NewHttpServer(0) // 0: let the OS pick, so this test needs no fixed port
	if got := srv.Addr(); got != "" {
		t.Errorf("Addr() before Start = %q, want empty", got)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	addr := srv.Addr()
	if addr == "" {
		t.Fatal("Addr() after Start is empty; nothing can discover the bound port")
	}

	// Serving is asserted over the wire, not by reading a field. The mux being correct
	// and the server actually serving it are two different claims.
	resp, err := http.Get("http://" + addr + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz on the live listener: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /readyz = %d, want 200", resp.StatusCode)
	}

	// A second Start must refuse rather than silently leak the first listener.
	if err := srv.Start(); err == nil {
		t.Error("a second Start succeeded; the first listener would be leaked and unreachable")
	}

	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if _, err := http.Get("http://" + addr + "/readyz"); err == nil {
		t.Error("the listener still answers after Shutdown")
	}
}

// A bind failure must be RETURNED, not logged from a goroutine nobody is reading.
//
// This is the case that made the change worth building. Started as
// `go func() { ListenAndServe() }()`, a port collision leaves a service running with no
// HTTP surface at all — no probes, no metrics — while its startup reports success. The
// pod then fails its liveness probe and restarts forever, and the log line explaining
// why is one line among a service's whole startup output.
func TestHttpServerStartReturnsTheBindError(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("taking a port to collide with: %v", err)
	}
	defer occupied.Close()

	port := occupied.Addr().(*net.TCPAddr).Port
	ms := &Microservice{FunctionalArea: "bind-fail"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())

	srv := ms.NewHttpServer(int32(port))
	err = srv.Start()
	if err == nil {
		_ = srv.Shutdown(context.Background())
		t.Fatal("Start on an occupied port returned nil; the service would report a successful startup with no HTTP surface")
	}
	if !strings.Contains(err.Error(), "binding http server") {
		t.Errorf("bind error = %q, want it to name the failure as a bind", err)
	}
	if srv.Addr() != "" {
		t.Errorf("Addr() = %q after a failed bind, want empty", srv.Addr())
	}
}

// Shutdown on a server that was never started must be a no-op that leaves the server
// still startable, and the second half is the load-bearing one.
//
// A service's teardown runs after a startup that may have refused partway through, so
// Shutdown is genuinely reached on a server that was never started. Forwarding that to
// http.Server.Shutdown does not merely waste a call: it latches the server's
// shuttingDown flag permanently, and a later Serve then returns ErrServerClosed
// immediately — from inside the background goroutine, where that error is indistinguishable
// from a clean stop and is swallowed. Start would report success and nothing would ever
// be served. That is this lane's failure class exactly: a route set that silently is not
// there.
func TestHttpServerShutdownBeforeStartLeavesItStartable(t *testing.T) {
	ms := &Microservice{FunctionalArea: "never-started"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	gate := NewReadinessGate()
	gate.MarkReadyWithoutAuthSurface()
	ms.RegisterProbes(gate)

	srv := ms.NewHttpServer(0)
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown before Start = %v, want nil", err)
	}

	if err := srv.Start(); err != nil {
		t.Fatalf("Start after a no-op Shutdown: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	// Asserted over the wire: Start returning nil is not evidence that Serve did not
	// exit immediately, since that error never reaches the caller.
	resp, err := http.Get("http://" + srv.Addr() + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz after Shutdown-then-Start: %v; the server bound but never served", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz = %d, want 200", resp.StatusCode)
	}

	// And a service may stop twice.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	if err := srv.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Errorf("second Shutdown = %v, want nil or ErrServerClosed", err)
	}
}

// Start after Shutdown must refuse, and must say WHY it refuses.
//
// Both refusals are correct — http.Server cannot be restarted once shut down — but they
// send a reader to different places. "already started" describes a second Start racing a
// first and invites a hunt for that second caller; a service that stopped and wants to
// serve again needs to hear that this server is spent and it should build another.
func TestHttpServerStartAfterShutdownSaysItCannotBeRestarted(t *testing.T) {
	ms := &Microservice{FunctionalArea: "restart-refusal"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())

	srv := ms.NewHttpServer(0)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	err := srv.Start()
	if err == nil {
		t.Fatal("Start after Shutdown returned nil; http.Server latches shuttingDown, so it " +
			"would bind and then serve nothing")
	}
	if !strings.Contains(err.Error(), "cannot be restarted") {
		t.Errorf("restart refusal = %q, want it to say the server cannot be restarted rather "+
			"than that it is already started", err)
	}
}

// Addr must be safe to call from another goroutine while Start runs.
//
// The lifecycle callers are sequential, so nothing in a service races these today — but
// Addr is how anything else discovers the bound port, and an unsynchronised read of a
// field Start writes is a data race whether or not the values ever disagree. Under
// -race this fails without the mutex; without -race it proves only that nothing
// deadlocks, which is why the guard is stated here rather than left to the reader.
func TestHttpServerAddrIsSafeConcurrentlyWithStart(t *testing.T) {
	ms := &Microservice{FunctionalArea: "addr-race"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())

	srv := ms.NewHttpServer(0)
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = srv.Addr()
		}()
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	wg.Wait()

	if srv.Addr() == "" {
		t.Error("Addr() is empty after Start")
	}
}

// HttpServer must NOT satisfy LifecycleComponent.
//
// Where the HTTP stop sits relative to the NATS stop is a per-service decision, and the
// two services that reasoned about it reached opposite answers — device-management
// stops HTTP first so an in-flight mutation cannot publish onto a draining connection,
// lwm2m-ingest stops it last because hoisting the NATS stop breaks its lease release.
// A component that can be handed to NewLifecycleManager will eventually be handed to
// one, and that picks an order for every service at once.
//
// The assertion is a compile-time interface check expressed as a runtime one, so that
// gaining the methods fails HERE with this explanation rather than somewhere downstream
// as a behaviour change nobody traces back.
func TestHttpServerIsNotALifecycleComponent(t *testing.T) {
	var v interface{} = &HttpServer{}
	if _, ok := v.(LifecycleComponent); ok {
		t.Fatal("HttpServer satisfies LifecycleComponent; it must not, because the HTTP stop's " +
			"position relative to the NATS stop differs by service and both orders in the tree are correct")
	}
}
