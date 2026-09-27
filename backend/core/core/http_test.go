// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
	"io"
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
	// whenNotLive is the answer once a component has called MarkNotLive, on a pod whose
	// readiness gate is OPEN and not draining — so a 503 there comes from the liveness
	// latch and nothing else.
	whenNotLive int
}

var probeContract = []probeTarget{
	// Liveness answers "can this process still do its job without a restart?". It must
	// NOT track readiness: a pod that reports unhealthy while it waits for its auth gate
	// gets killed and restarted into the same wait, which is a crash loop caused by the
	// probe rather than by the service. It fails only once a component has declared a
	// state nothing but a restart can clear, and then the kubelet restarts the pod.
	{path: "/healthz", whenUnready: 200, whenReady: 200, whenDrained: 200, whenNotLive: 503},

	// Readiness gates the data plane on auth being live, and flips back to 503 for the
	// drain window a SIGTERM opens — that second transition is what lets a terminating
	// pod leave its Service endpoints before it stops accepting connections. A process
	// that is not live is not ready either: it must not take traffic for the liveness
	// window before its restart.
	{path: "/readyz", whenUnready: 503, whenReady: 200, whenDrained: 503, whenNotLive: 503},

	// Metrics are always scrapable. A scrape that started failing whenever a pod was
	// draining — or had stopped being live — would blind the dashboards for exactly the
	// window an operator is watching them.
	{path: "/metrics", whenUnready: 200, whenReady: 200, whenDrained: 200, whenNotLive: 200},
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

	// The liveness latch is one-way, so it gets a Microservice of its own, with an OPEN
	// gate that is not draining: every 503 below is the latch's doing.
	dead := &Microservice{FunctionalArea: "probe-area-dead"}
	dead.UseMetricsRegistry(prometheus.NewRegistry())
	deadGate := NewReadinessGate()
	deadGate.MarkReadyWithoutAuthSurface()
	dead.RegisterProbes(deadGate)
	dead.MarkNotLive(errors.New("broker connection closed permanently"))
	deadStatus := func(path string) (int, string) {
		rec := httptest.NewRecorder()
		dead.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, rec.Body.String()
	}
	for _, p := range probeContract {
		got, body := deadStatus(p.path)
		if got != p.whenNotLive {
			t.Errorf("%s once not live = %d, want %d", p.path, got, p.whenNotLive)
		}
		// Port 8080 is the GraphQL port: the reason belongs in the log, not in a body any
		// caller of the port can read.
		if got == http.StatusServiceUnavailable && strings.Contains(body, "broker") {
			t.Errorf("%s leaks the not-live reason in its body: %q", p.path, body)
		}
		if p.path == "/healthz" && !strings.Contains(body, "not live") {
			t.Errorf("/healthz once not live has body %q, want the generic \"not live\"", body)
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
	ms.NewCounter("probe_wiring_total", "Probe metric for the /metrics route.")
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

// Shutdown must release the port before it returns, even when it follows Start so closely
// that the serve goroutine has not begun.
//
// http.Server.Shutdown closes only the listeners Serve has already registered. Start binds
// synchronously but hands the listener to Serve in a goroutine, so a Shutdown that wins
// that race used to leave the socket bound: Serve later saw the server shutting down and
// closed it on its way out, after Shutdown had already reported a clean stop. A restart on
// the same port, which is what a production restart does, then failed with "address
// already in use". Each round here is start, immediate stop, and a rebind of the same
// port. The window is a scheduling race, so this is a probabilistic check rather than a
// deterministic one: before the fix it failed in some runs under -race with several
// CPUs, not in every run. The deterministic form of this check is
// TestHttpServerShutdownReleasesAListenerServeHasNotTaken; this one stays because it goes
// through Start and Serve as a service does.
func TestHttpServerShutdownReleasesThePortEvenBeforeServing(t *testing.T) {
	ms := &Microservice{FunctionalArea: "immediate-stop"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())

	for i := 0; i < 300; i++ {
		srv := ms.NewHttpServer(0)
		if err := srv.Start(); err != nil {
			t.Fatalf("round %d: Start: %v", i, err)
		}
		addr := srv.Addr()
		if err := srv.Shutdown(context.Background()); err != nil {
			t.Fatalf("round %d: Shutdown: %v", i, err)
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("round %d: rebinding %s after Shutdown returned: %v", i, addr, err)
		}
		ln.Close()
	}
}

// The deterministic form of the test above. That test reaches the window by racing
// Start's serve goroutine and wins the race only in some runs, so a regression that drops
// Shutdown's own listener close passes it in the runs it loses. This one CONSTRUCTS the
// window instead: the listener is bound and recorded, exactly as Start leaves it on the
// line before its goroutine is scheduled, and Serve has never been given it. Deleting the
// close then fails here on every run.
//
// It sets the field directly because that is the only way to hold a server in that state
// without adding a seam to production code. It differs from the real window in one
// respect: Serve never runs here, whereas in the real window Serve runs later, finds the
// server shutting down, returns ErrServerClosed and closes the listener a second time on
// its way out. That later close is harmless, and the end-to-end test above covers it.
func TestHttpServerShutdownReleasesAListenerServeHasNotTaken(t *testing.T) {
	srv := NewHttpServerForHandler(0, http.NotFoundHandler())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("binding: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv.mu.Lock()
	srv.ln = ln
	srv.mu.Unlock()
	addr := ln.Addr().String()

	// Positive control: the port really is held. Without it, a successful rebind below
	// could mean the fixture never held the port rather than that Shutdown released it.
	if probe, err := net.Listen("tcp", addr); err == nil {
		probe.Close()
		t.Fatalf("rebinding %s succeeded BEFORE Shutdown; the fixture does not hold the port", addr)
	}

	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown = %v, want nil", err)
	}
	rebound, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("rebinding %s after Shutdown returned: %v; Shutdown left a listener it bound "+
			"itself open because Serve had not yet taken it", addr, err)
	}
	rebound.Close()
}

// Closing the listener must not cut off a request that is already being served, and must
// not replace what Shutdown reports.
//
// The scope of this test is narrower than it may look. It pins two things about the
// listener close Shutdown performs after net/http's own Shutdown: that closing a LISTENER
// does not sever a connection it already accepted, and that the error Shutdown returns is
// net/http's (here, the expired deadline), not the result of the close. A forceful close
// in its place (http.Server.Close) would get both wrong, and this is the test that says so.
//
// It does NOT claim that a request outliving a Shutdown whose deadline has expired is
// desirable. That is simply net/http's behaviour: Shutdown never closes active
// connections. HttpServer's own doc comment explains why the GraphQL server stops before
// NATS drains, so that a request still inside a resolver does not reach a connection that
// is going away, and a future change that force-closes after the deadline for that reason
// would be legitimate. It would have to change this test deliberately, not by accident.
func TestHttpServerShutdownLetsAnInFlightRequestFinish(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	// A failed assertion must not leave the handler goroutine blocked for the rest of
	// the package run.
	t.Cleanup(unblock)

	srv := NewHttpServerForHandler(0, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		_, _ = io.WriteString(w, "drained")
	}))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	addr := srv.Addr()

	type result struct {
		status int
		body   string
		err    error
	}
	results := make(chan result, 1)
	// A private client with keep-alives off, so no pooled connection from another test
	// can be reused here, and with a timeout, so a change that leaves the connection open
	// without answering fails rather than hangs.
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	go func() {
		resp, err := client.Get("http://" + addr + "/")
		if err != nil {
			results <- result{err: err}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		results <- result{status: resp.StatusCode, body: string(body), err: err}
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the request never reached the handler")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := srv.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown with a request in flight past its deadline = %v, want %v; Shutdown "+
			"must report net/http's result, not the listener close's", err, context.DeadlineExceeded)
	}

	// Shutdown has returned, and has closed the listener. The request is still running.
	unblock()
	var got result
	select {
	case got = <-results:
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight request never completed after it was released")
	}
	if got.err != nil || got.status != http.StatusOK || got.body != "drained" {
		t.Fatalf("in-flight request = (status %d, body %q, err %v), want (200, %q, nil); "+
			"stopping the server cut off a request it had already accepted",
			got.status, got.body, got.err, "drained")
	}

	rebound, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("rebinding %s after Shutdown: %v", addr, err)
	}
	rebound.Close()
}

// Shutdown may be called from several goroutines at once, and every call must succeed.
//
// No caller in the tree does this today; the lifecycle stops are sequential. What this
// test is for is the -race run: by the time any of these calls closes the listener,
// net/http's Shutdown has already closed it and waited for Serve to return, so EVERY call
// closes an already-closed listener, concurrently with the others. Under -race it checks
// that none of that races; without -race it proves only that no call panics, that the
// ignored error from the repeated close never surfaces as a failed stop, and that the port
// ends up free.
func TestHttpServerShutdownIsSafeToCallConcurrently(t *testing.T) {
	ms := &Microservice{FunctionalArea: "concurrent-stop"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	gate := NewReadinessGate()
	gate.MarkReadyWithoutAuthSurface()
	ms.RegisterProbes(gate)

	srv := ms.NewHttpServer(0)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	addr := srv.Addr()

	// A served request proves Serve has registered the listener, so the stops below race
	// each other over a listener net/http owns, not over the pre-Serve window.
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", resp.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const callers = 8
	errs := make([]error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = srv.Shutdown(ctx)
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent Shutdown %d = %v, want nil", i, err)
		}
	}
	rebound, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("rebinding %s after concurrent Shutdowns: %v", addr, err)
	}
	rebound.Close()
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
// services that reasoned about it reached opposite answers — device-management stops
// HTTP first so a request still inside a resolver cannot reach a draining connection,
// and every other service that serves GraphQL over a NATS connection follows it, while
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
