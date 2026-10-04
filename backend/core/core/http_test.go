// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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
// that race finds nothing to close: Serve later sees the server shutting down and closes
// the listener on its way out. Unless Shutdown waits for that, it reports a clean stop
// over a bound socket, and a restart on the same port, which is what a production restart
// does, fails with "address already in use". Each round here is start, immediate stop,
// and a rebind of the same port. The window is a scheduling race, so this is a
// probabilistic check rather than a deterministic one, and a weak one: with the wait
// removed it fails only rarely and can pass many runs in a row. The window is held
// deterministically by TestHttpServerStopWaitsForAListenerCloseServeHasBegun. This one
// stays because it goes through Start and Serve as a service does.
//
// The port is chosen outside the kernel's auto-assign range (ip_local_port_range), which
// the kernel never hands out on its own. Binding port 0 took each port from inside that
// range, and in the moment between the stop and the rebind any other socket on the host
// (a parallel test's dial, a listener on port 0) could be handed the port just released,
// so the rebind failed with "address already in use" over a release that was on time.
// Loopback rather than every interface is deliberate and does not change what is tested:
// the window is the scheduling of the serve goroutine, not the address. The inode of the
// bound socket is captured inside the listen hook, before Start launches the serve
// goroutine, so reading it does not widen the gap between Start and Shutdown.
//
// The rebind is strict and is never retried. Every way this test has to fail is the port
// being released LATE, and a retry is exactly what passes a late release. A rebind that
// fails still fails the test; the kernel's socket table is then read so the failure says
// whether this round's own listener still holds the port or nothing does ("late release")
// or some other socket does ("taken by another socket"). Only the Start bind before a
// round is retried, and only for "address already in use", because it is not the thing
// under test.
func TestHttpServerShutdownReleasesThePortEvenBeforeServing(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("picks its port outside the kernel's auto-assign range, which only Linux " +
			"publishes in /proc; the release is pinned on every OS by " +
			"TestHttpServerStopWaitsForAListenerCloseServeHasBegun")
	}
	lo, hi, err := ephemeralPortRange()
	if err != nil {
		t.Fatalf("reading the auto-assign port range: %v", err)
	}
	ms := &Microservice{FunctionalArea: "immediate-stop"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())

	for i := 0; i < 300; i++ {
		srv, bound, boundInode := startOnUnassignablePort(t, ms, lo, hi, i)
		addr := srv.Addr()
		if err := srv.Shutdown(context.Background()); err != nil {
			t.Fatalf("round %d: Shutdown: %v", i, err)
		}
		// The listener Start bound has been CLOSED by the time Shutdown returns. This
		// sees the close having begun, not the socket having been released (a close
		// another goroutine started but has not finished already reads as closed here),
		// so the release itself is checked by the rebind below and, deterministically, by
		// TestHttpServerStopWaitsForAListenerCloseServeHasBegun.
		if err := listenerControl(bound); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("round %d: the listener Start bound on %s was not closed when Shutdown "+
				"returned: control = %v, want %v", i, addr, err, net.ErrClosed)
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			if !errors.Is(err, syscall.EADDRINUSE) {
				t.Fatalf("round %d: rebinding %s after Shutdown returned: %v", i, addr, err)
			}
			verdict, report := probePortHolders(bound.Addr().(*net.TCPAddr).Port, boundInode)
			t.Fatalf("round %d: rebinding %s after Shutdown returned: %v\n%s: %s", i, addr, err, verdict, report)
		}
		ln.Close()
	}
}

// listenerControl runs a no-op against the listener's socket, which fails with
// net.ErrClosed once anyone has begun closing it.
func listenerControl(ln net.Listener) error {
	sc, ok := ln.(syscall.Conn)
	if !ok {
		return errors.New("listener does not expose its socket")
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return err
	}
	return rc.Control(func(uintptr) {})
}

// stallingListener stands in for a TCP listener whose close another goroutine has begun but
// not finished. It mirrors how the runtime closes a socket two goroutines close at once:
// the FIRST Close performs the release and does not return until it is done; any other
// Close returns net.ErrClosed immediately, without waiting. Here the first close is held
// open until the test opens gate, which stands in for that goroutine being descheduled
// mid-close.
type stallingListener struct {
	net.Listener // a real listener on 127.0.0.1:0; Addr and Accept delegate to it

	first      atomic.Bool
	closeBegun chan struct{} // closed when the first Close starts
	laterClose chan struct{} // closed when any later Close is called
	laterOnce  sync.Once
	gate       chan struct{} // the first Close releases the socket only after this closes
	released   atomic.Bool   // set once the real socket has been closed
}

func (l *stallingListener) Close() error {
	if !l.first.CompareAndSwap(false, true) {
		l.laterOnce.Do(func() { close(l.laterClose) })
		return net.ErrClosed
	}
	close(l.closeBegun)
	<-l.gate
	err := l.Listener.Close()
	l.released.Store(true)
	return err
}

// Shutdown and Close must not return while a close of the listener that the serve loop
// began is still in progress.
//
// When a stop lands before Serve has registered the listener, net/http's shutdown has
// nothing to close, and Serve returns at once and closes the listener on its way out. A
// stop that does not wait for that close to finish reports success over a bound port.
// This test builds that state deterministically: net/http's shutdown is latched before
// Start, so Serve takes the early return, and its close is held open while the stop has
// every chance to return.
//
// The wait must not be bounded by the stop's deadline either, which the expired-deadline
// case pins: a stop that gave up waiting would return over the same bound port.
//
// And the stop must not close the listener itself. The serve loop is the one closer, and a
// second one is how the port came to be released late in the first place: of two
// concurrent closes, only the first waits for the socket to be released, so a stop that
// closed it too and returned on its own close's word would be back to reporting success
// over a bound port the moment the wait went. This fixture counts the closes, so a stop
// that adds its own fails here even with the wait in place.
func TestHttpServerStopWaitsForAListenerCloseServeHasBegun(t *testing.T) {
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	for _, stop := range []struct {
		name string
		call func(*HttpServer) error
		// okErr reports whether the error the stop returned is acceptable. Only the
		// expired-deadline case may see one: whether net/http reports the deadline when
		// there is nothing left to drain depends on the order it checks things in, which
		// is not what this test is about.
		okErr func(error) bool
	}{
		{"Shutdown", func(s *HttpServer) error { return s.Shutdown(context.Background()) },
			func(err error) bool { return err == nil }},
		{"Close", func(s *HttpServer) error { return s.Close() },
			func(err error) bool { return err == nil }},
		{"Shutdown with an expired deadline", func(s *HttpServer) error { return s.Shutdown(expired) },
			func(err error) bool { return err == nil || errors.Is(err, context.Canceled) }},
	} {
		t.Run(stop.name, func(t *testing.T) {
			inner, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("binding: %v", err)
			}
			fake := &stallingListener{
				Listener:   inner,
				closeBegun: make(chan struct{}),
				laterClose: make(chan struct{}),
				gate:       make(chan struct{}),
			}
			var gateOnce sync.Once
			openGate := func() { gateOnce.Do(func() { close(fake.gate) }) }
			// A failed assertion must not leave Serve's close blocked on the gate.
			t.Cleanup(func() {
				openGate()
				_ = inner.Close()
			})

			srv := NewHttpServerForHandler(0, http.NotFoundHandler())
			srv.listen = func(string, string) (net.Listener, error) { return fake, nil }

			// Latch net/http's shutdown before Start. This is the state a stop that beat
			// the serve goroutine leaves behind: Serve refuses to register the listener,
			// returns ErrServerClosed, and closes the listener itself on its way out.
			_ = srv.server.Shutdown(context.Background())

			if err := srv.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}
			// Positive control: without it, a stop that waits for nothing would pass
			// below simply because the fixture never reached the window.
			select {
			case <-fake.closeBegun:
			case <-time.After(5 * time.Second):
				t.Fatal("Serve never began closing the listener; the fixture did not reach the window")
			}

			type result struct {
				err              error
				releasedAtReturn bool
			}
			results := make(chan result, 1)
			go func() {
				err := stop.call(srv)
				results <- result{err: err, releasedAtReturn: fake.released.Load()}
			}()

			select {
			case r := <-results:
				madeOwnClose := false
				select {
				case <-fake.laterClose:
					madeOwnClose = true
				default:
				}
				t.Fatalf("%s returned (err=%v) while the close Serve began was still in progress: "+
					"released=%v at return, want true (the stop made its own close: %v); a restart "+
					"on this port would fail with \"address already in use\"",
					stop.name, r.err, r.releasedAtReturn, madeOwnClose)
			case <-time.After(250 * time.Millisecond):
				// Still waiting, which is right: the socket has not been released.
			}

			openGate()
			var r result
			select {
			case r = <-results:
			case <-time.After(5 * time.Second):
				t.Fatalf("%s did not return after Serve's close of the listener finished", stop.name)
			}
			if !stop.okErr(r.err) {
				t.Errorf("%s = %v, want a clean stop", stop.name, r.err)
			}
			if !r.releasedAtReturn {
				t.Errorf("%s returned before the listener's socket was released", stop.name)
			}
			select {
			case <-fake.laterClose:
				t.Errorf("%s closed the listener itself as well as the serve loop; the serve "+
					"loop must be its only closer, because a concurrent second close returns "+
					"before the socket is released", stop.name)
			default:
			}
			// The kernel agrees: the port is free.
			rebound, err := net.Listen("tcp", fake.Addr().String())
			if err != nil {
				t.Fatalf("rebinding %s after %s returned: %v", fake.Addr(), stop.name, err)
			}
			rebound.Close()
		})
	}
}

// Close on its own must stop a server whose serve loop has registered the listener, and
// release the port before it returns.
//
// Every caller in the tree reaches Close only after a Shutdown, by which time the serve
// loop has exited and Close's wait is already satisfied. That hides the order Close's two
// steps must run in: the serve loop exits only once net/http's Close has closed the
// listener it registered, so a Close that waited for the serve loop FIRST would wait
// forever. This is the case that can see it.
func TestHttpServerCloseStopsAServingServer(t *testing.T) {
	ms := &Microservice{FunctionalArea: "close-alone"}
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

	// A served request proves Serve has registered the listener, so this is the path
	// where net/http owns the close, not the pre-Serve window.
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	resp.Body.Close()

	done := make(chan error, 1)
	go func() { done <- srv.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return on a serving server; it must close the listener " +
			"before it waits for the serve loop that only that close ends")
	}
	rebound, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("rebinding %s after Close returned: %v", addr, err)
	}
	rebound.Close()
}

// Shutdown must not cut off a request that is already being served, and must report
// net/http's result.
//
// The scope of this test is narrower than it may look. It pins two things about what
// Shutdown does around net/http's own Shutdown: that releasing the LISTENER does not sever
// a connection it already accepted, and that the error Shutdown returns is net/http's
// (here, the expired deadline), not something of its own. A forceful close in its place
// (http.Server.Close) would get both wrong, and this is the test that says so.
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

	srv := NewHttpServerForHandler(0, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		_, _ = io.WriteString(w, "drained")
	}))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// A failed assertion must not leave the handler blocked. The release has to come
	// BEFORE the Shutdown in this one cleanup: cleanups run last-registered-first, and a
	// Shutdown with no deadline waits for the blocked handler, so a separate unblock
	// cleanup registered earlier would never run and a failure would hang the package
	// until go test's timeout, with its message lost.
	t.Cleanup(func() {
		unblock()
		_ = srv.Shutdown(context.Background())
	})
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
			"must report net/http's result", err, context.DeadlineExceeded)
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
// test is for is the -race run: every call goes through net/http's Shutdown, which closes
// the registered listener once, and then waits on the same served channel, concurrently
// with the others. Under -race it checks that none of that races; without -race it proves
// only that no call panics or hangs, that none of the stops reports a failure, and that
// the port ends up free.
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

// socketInode returns N from the "socket:[N]" link of the listener's descriptor.
func socketInode(ln net.Listener) (string, error) {
	sc, ok := ln.(syscall.Conn)
	if !ok {
		return "", errors.New("listener does not expose its socket")
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		return "", err
	}
	var inode string
	var linkErr error
	if err := rc.Control(func(fd uintptr) {
		inode, linkErr = socketLinkInode("/proc/self/fd/" + strconv.FormatUint(uint64(fd), 10))
	}); err != nil {
		return "", err
	}
	return inode, linkErr
}

// socketLinkInode reads one /proc/self/fd link and returns the inode of a socket link.
func socketLinkInode(path string) (string, error) {
	target, err := os.Readlink(path)
	if err != nil {
		return "", err
	}
	inner, ok := strings.CutPrefix(target, "socket:[")
	if !ok || !strings.HasSuffix(inner, "]") {
		return "", fmt.Errorf("%s is %q, not a socket", path, target)
	}
	return strings.TrimSuffix(inner, "]"), nil
}

// ownSocketInodes returns the inode of every socket descriptor this process holds.
func ownSocketInodes() (map[string]bool, error) {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return nil, err
	}
	own := map[string]bool{}
	for _, e := range entries {
		// A descriptor can close between the listing and the read, and a non-socket
		// descriptor has no inode to report; neither is an error for this purpose.
		if inode, err := socketLinkInode("/proc/self/fd/" + e.Name()); err == nil {
			own[inode] = true
		}
	}
	return own, nil
}

// portHolder is one row of /proc/net/tcp{,6} whose local port is the one asked about.
type portHolder struct {
	table  string // "tcp" or "tcp6"
	local  string // the kernel's hex form, e.g. "0100007F:B2F3"
	remote string
	state  string // LISTEN, ESTABLISHED, TIME_WAIT, ...; an unknown code is kept as "st=XX"
	inode  string // "0" when no process owns the socket
}

var tcpStateNames = map[string]string{
	"01": "ESTABLISHED", "02": "SYN_SENT", "03": "SYN_RECV", "04": "FIN_WAIT1",
	"05": "FIN_WAIT2", "06": "TIME_WAIT", "07": "CLOSE", "08": "CLOSE_WAIT",
	"09": "LAST_ACK", "0A": "LISTEN", "0B": "CLOSING",
}

// parseProcNetTCP returns the rows of one /proc/net/tcp{,6} table whose LOCAL port is port.
func parseProcNetTCP(table string, r io.Reader, port int) ([]portHolder, error) {
	var rows []portHolder
	sc := bufio.NewScanner(r)
	sc.Scan() // header
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 10 {
			continue
		}
		colon := strings.LastIndex(f[1], ":")
		if colon < 0 {
			return nil, fmt.Errorf("%s: malformed local address %q", table, f[1])
		}
		p, err := strconv.ParseInt(f[1][colon+1:], 16, 32)
		if err != nil {
			return nil, fmt.Errorf("%s: malformed local port in %q: %w", table, f[1], err)
		}
		if int(p) != port {
			continue
		}
		state, ok := tcpStateNames[strings.ToUpper(f[3])]
		if !ok {
			state = "st=" + f[3]
		}
		rows = append(rows, portHolder{table: table, local: f[1], remote: f[2], state: state, inode: f[9]})
	}
	return rows, sc.Err()
}

type holderVerdict int

const (
	holderLateRelease  holderVerdict = iota // this round's listener holds the port, or nothing does by now
	holderTakenByOther                      // a socket that is not this round's listener holds it
	holderUnexplained                       // the probe could not decide
)

func (v holderVerdict) String() string {
	switch v {
	case holderLateRelease:
		return "late release"
	case holderTakenByOther:
		return "taken by another socket"
	default:
		return "unexplained"
	}
}

// classifyRebindFailure says who holds a port whose rebind just failed. It is a diagnostic:
// the test fails on every verdict, and the verdict only decides what the failure says.
//
// A row carrying boundInode is this round's own listener still holding the port, which is
// a late release even if other rows exist. No row at all is also a late release, because
// a failed REUSEADDR rebind with nothing on the port can only mean it was freed after the
// failure. Any other row is another socket; a row no process owns (inode 0) is one that
// has closed and lingers, and it blocks the rebind because Go sets SO_REUSEADDR on
// listeners and not on dialers. An unknown boundInode decides nothing.
func classifyRebindFailure(holders []portHolder, boundInode string, own map[string]bool) (holderVerdict, string) {
	if boundInode == "" {
		return holderUnexplained, "this round's listener inode is unknown, so no holder can be called foreign"
	}
	var other []string
	for _, h := range holders {
		if h.inode == boundInode {
			return holderLateRelease, fmt.Sprintf("this round's own listener (inode %s) still holds the port: %s %s %s", boundInode, h.table, h.local, h.state)
		}
		whose := "a socket of another process"
		switch {
		case h.inode == "0":
			whose = "a closed socket no process owns"
		case own[h.inode]:
			whose = "a socket of this test process"
		}
		other = append(other, fmt.Sprintf("%s (inode %s) %s %s -> %s %s", whose, h.inode, h.table, h.local, h.remote, h.state))
	}
	if len(other) == 0 {
		return holderLateRelease, "nothing holds the port now: it was freed after the rebind failed"
	}
	return holderTakenByOther, strings.Join(other, "; ")
}

// probePortHolders reads the kernel's socket tables for port and classifies the holders.
func probePortHolders(port int, boundInode string) (holderVerdict, string) {
	var holders []portHolder
	for _, table := range []string{"tcp", "tcp6"} {
		f, err := os.Open("/proc/net/" + table)
		if err != nil {
			if table == "tcp6" && errors.Is(err, os.ErrNotExist) {
				continue
			}
			return holderUnexplained, fmt.Sprintf("reading /proc/net/%s: %v", table, err)
		}
		rows, err := parseProcNetTCP(table, f, port)
		f.Close()
		if err != nil {
			return holderUnexplained, err.Error()
		}
		holders = append(holders, rows...)
	}
	own, err := ownSocketInodes()
	if err != nil {
		return holderUnexplained, fmt.Sprintf("reading /proc/self/fd: %v", err)
	}
	return classifyRebindFailure(holders, boundInode, own)
}

// ephemeralPortRange reads the range the kernel auto-assigns local ports from.
func ephemeralPortRange() (lo, hi int, err error) {
	raw, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		return 0, 0, err
	}
	f := strings.Fields(string(raw))
	if len(f) != 2 {
		return 0, 0, fmt.Errorf("ip_local_port_range is %q, want two numbers", raw)
	}
	if lo, err = strconv.Atoi(f[0]); err != nil {
		return 0, 0, err
	}
	if hi, err = strconv.Atoi(f[1]); err != nil {
		return 0, 0, err
	}
	return lo, hi, nil
}

// unassignablePortFloor keeps clear of the low fixed ports other suites bind.
const unassignablePortFloor = 10000

// startBindAttempts bounds the pre-round Start retries on "address already in use".
const startBindAttempts = 20

// pickPortOutside returns a port in [floor, 65535] that is not in [lo, hi], where rnd(n)
// returns a number in [0, n). The candidates are [floor, lo-1] and then [hi+1, 65535].
func pickPortOutside(lo, hi, floor int, rnd func(n int) int) (int, error) {
	const top = 65535
	belowFirst, belowLast := floor, min(lo-1, top)
	aboveFirst, aboveLast := max(hi+1, floor), top
	below := max(0, belowLast-belowFirst+1)
	above := max(0, aboveLast-aboveFirst+1)
	if below+above == 0 {
		return 0, fmt.Errorf("no port in [%d, %d] lies outside the auto-assign range [%d, %d] "+
			"(net.ipv4.ip_local_port_range); this test needs room outside that range", floor, top, lo, hi)
	}
	n := rnd(below + above)
	if n < below {
		return belowFirst + n, nil
	}
	return aboveFirst + n - below, nil
}

// startOnUnassignablePort starts a loopback server on a port the kernel will not auto-assign
// and returns it with the listener it bound and that listener's socket inode. The inode is
// read inside the listen hook, which Start calls before it launches the serve goroutine.
func startOnUnassignablePort(t *testing.T, ms *Microservice, lo, hi, round int) (*HttpServer, net.Listener, string) {
	t.Helper()
	for attempt := 0; attempt < startBindAttempts; attempt++ {
		port, err := pickPortOutside(lo, hi, unassignablePortFloor, rand.IntN)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		srv := NewHttpServerAt(net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), ms.Mux(), HttpServerOptions{})
		var bound net.Listener
		var inode string
		var inodeErr error
		srv.listen = func(network, address string) (net.Listener, error) {
			ln, err := net.Listen(network, address)
			if err == nil {
				bound = ln
				inode, inodeErr = socketInode(ln)
			}
			return ln, err
		}
		if err := srv.Start(); err != nil {
			if errors.Is(err, syscall.EADDRINUSE) {
				continue
			}
			t.Fatalf("round %d: Start on port %d: %v", round, port, err)
		}
		if inodeErr != nil || inode == "" {
			t.Fatalf("round %d: reading the bound socket's inode: %v", round, inodeErr)
		}
		a, ok := bound.Addr().(*net.TCPAddr)
		if !ok || !a.IP.Equal(net.IPv4(127, 0, 0, 1)) || (a.Port >= lo && a.Port <= hi) {
			t.Fatalf("round %d: bound %v, want 127.0.0.1 on a port outside the auto-assign range [%d, %d]",
				round, bound.Addr(), lo, hi)
		}
		return srv, bound, inode
	}
	t.Fatalf("round %d: %d picks in a row were already in use", round, startBindAttempts)
	return nil, nil, ""
}

func TestPickPortOutsideTheAutoAssignRange(t *testing.T) {
	at := func(n int) func(int) int { return func(int) int { return n } }
	last := func(n int) int { return n - 1 }
	cases := []struct {
		name          string
		lo, hi, floor int
		rnd           func(int) int
		want          int
		wantErr       bool
	}{
		{"first below", 32768, 60999, 10000, at(0), 10000, false},
		{"last below", 32768, 60999, 10000, at(22767), 32767, false},
		{"first above", 32768, 60999, 10000, at(22768), 61000, false},
		{"last above", 32768, 60999, 10000, last, 65535, false},
		{"range reaches below the floor", 5000, 60999, 10000, at(0), 61000, false},
		{"range reaches the top", 32768, 65535, 10000, last, 32767, false},
		{"range covers everything", 1024, 65535, 10000, at(0), 0, true},
		{"nothing left above the floor", 10000, 65535, 10000, at(0), 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := pickPortOutside(c.lo, c.hi, c.floor, c.rnd)
			if c.wantErr {
				if err == nil {
					t.Fatalf("got port %d, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("port = %d, want %d", got, c.want)
			}
			if got >= c.lo && got <= c.hi {
				t.Errorf("port %d lies inside the auto-assign range [%d, %d]", got, c.lo, c.hi)
			}
		})
	}
}

func TestParseProcNetTCPReadsOnlyTheRowsOnThePort(t *testing.T) {
	const header = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	tcp := header +
		"   0: 0100007F:B2F3 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 123456 1 0000000000000000 100 0 0 10 0\n" +
		"   1: 0100007F:B2F3 0100007F:9C40 01 00000000:00000000 00:00000000 00000000  1000        0 0 1 0000000000000000 100 0 0 10 0\n" +
		"   2: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 777 1 0000000000000000 100 0 0 10 0\n"
	tcp6 := header +
		"   0: 00000000000000000000000000000000:B2F3 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 654321 1 0000000000000000 100 0 0 10 0\n"

	got, err := parseProcNetTCP("tcp", strings.NewReader(tcp), 0xB2F3)
	if err != nil {
		t.Fatal(err)
	}
	got6, err := parseProcNetTCP("tcp6", strings.NewReader(tcp6), 0xB2F3)
	if err != nil {
		t.Fatal(err)
	}
	want := []portHolder{
		{"tcp", "0100007F:B2F3", "00000000:0000", "LISTEN", "123456"},
		{"tcp", "0100007F:B2F3", "0100007F:9C40", "ESTABLISHED", "0"},
		{"tcp6", "00000000000000000000000000000000:B2F3", "00000000000000000000000000000000:0000", "LISTEN", "654321"},
	}
	all := append(got, got6...)
	if len(all) != len(want) {
		t.Fatalf("rows = %+v, want %+v", all, want)
	}
	for i := range want {
		if all[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, all[i], want[i])
		}
	}
}

func TestClassifyRebindFailure(t *testing.T) {
	ours := portHolder{"tcp", "0100007F:B2F3", "00000000:0000", "LISTEN", "100"}
	foreign := portHolder{"tcp", "0100007F:B2F3", "0100007F:9C40", "ESTABLISHED", "200"}
	lingering := portHolder{"tcp", "0100007F:B2F3", "0100007F:9C40", "TIME_WAIT", "0"}
	cases := []struct {
		name    string
		holders []portHolder
		bound   string
		own     map[string]bool
		want    holderVerdict
		report  string
	}{
		{"own listener still holds it", []portHolder{ours}, "100", nil, holderLateRelease, "own listener"},
		{"own listener and a foreign row", []portHolder{foreign, ours}, "100", map[string]bool{"200": true}, holderLateRelease, "own listener"},
		{"nothing holds it now", nil, "100", nil, holderLateRelease, "nothing holds the port"},
		{"foreign socket of this process", []portHolder{foreign}, "100", map[string]bool{"200": true}, holderTakenByOther, "this test process"},
		{"foreign socket of another process", []portHolder{foreign}, "100", nil, holderTakenByOther, "another process"},
		{"closed socket nobody owns", []portHolder{lingering}, "100", nil, holderTakenByOther, "no process owns"},
		{"our inode unknown", []portHolder{foreign}, "", nil, holderUnexplained, "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, report := classifyRebindFailure(c.holders, c.bound, c.own)
			if got != c.want {
				t.Errorf("verdict = %v, want %v (%s)", got, c.want, report)
			}
			if !strings.Contains(report, c.report) {
				t.Errorf("report = %q, want it to contain %q", report, c.report)
			}
		})
	}
}

// The probe's failure mode is reporting nothing, which would read as a quiet test. This
// points it at a port it must see held, against the real kernel.
func TestPortHolderProbeNamesTheHolderOfALivePort(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc/net/tcp")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	inode, err := socketInode(l)
	if err != nil || inode == "" {
		t.Fatalf("socketInode = %q, %v", inode, err)
	}

	if v, report := probePortHolders(port, inode); v != holderLateRelease || !strings.Contains(report, "LISTEN") {
		t.Errorf("held by the listener itself: verdict %v, report %q; want late release naming LISTEN", v, report)
	}
	if v, report := probePortHolders(port, "1"); v != holderTakenByOther || !strings.Contains(report, "this test process") {
		t.Errorf("held by a socket that is not ours: verdict %v, report %q; want taken by another socket of this process", v, report)
	}
	l.Close()
	if v, report := probePortHolders(port, inode); v != holderLateRelease || !strings.Contains(report, "nothing holds") {
		t.Errorf("after the close: verdict %v, report %q; want late release with no holder", v, report)
	}
}
