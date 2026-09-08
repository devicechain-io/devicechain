// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// httpReadHeaderTimeout bounds how long a client may take to send its request
// headers. Without it a connection that dribbles headers forever occupies a server
// goroutine indefinitely, which is a cheap denial of service against an endpoint that
// is reachable from a cluster network. net/http applies no default.
const httpReadHeaderTimeout = 5 * time.Second

// Mux is the HTTP request multiplexer this microservice owns, created on first use.
//
// It exists so a service's routes live somewhere the service can name, rather than on
// http.DefaultServeMux — a package-level global shared by every registration in the
// process, including any a dependency makes.
//
// 🔴 THE DANGEROUS PART OF ADOPTING IT IS THE SWITCHOVER, NOT THIS METHOD. Giving an
// http.Server an explicit Handler SILENTLY UNMOUNTS everything still registered on the
// default mux. http.Handle keeps compiling and keeps registering; the routes simply
// stop being served, with no error and no log line. So a binary must move ALL of its
// registrations in the same commit that gives its server a Handler — for
// user-management that includes /auth/jwks, whose loss makes every other service's
// token validation fail and pulls the whole instance out of its Service endpoints.
func (ms *Microservice) Mux() *http.ServeMux {
	ms.muxOnce.Do(func() { ms.mux = http.NewServeMux() })
	return ms.mux
}

// RegisterProbes registers the three routes every DeviceChain HTTP server serves:
// /healthz, /readyz and /metrics.
//
// All four servers in the tree spell these out for themselves today, which is three
// copies too many of a contract the chart depends on: the deployment's liveness,
// readiness and startup probes all target /healthz or /readyz by name, and the
// ServiceMonitor scrapes /metrics. A copy that drifts is not a compile error.
//
// gate may be nil, and nil means NOT READY rather than ready. That is the fail-closed
// direction and it matches what the GraphQL server already does: a server whose
// readiness gate was never wired should be pulled from Service endpoints, not treated
// as healthy.
//
// Liveness and readiness are deliberately different questions. /healthz is 200 for as
// long as the process runs — a live process that is not yet serving must be restarted
// by nobody. /readyz reports 503 until the auth gate opens, and again once a SIGTERM
// starts the drain, so a pod leaves its Service endpoints while it can still finish
// the requests already in flight.
//
// It must be called at most once per mux: ServeMux panics on a duplicate pattern, and
// that panic is the enforcement — a second caller is a routing mistake, not a runtime
// condition.
func (ms *Microservice) RegisterProbes(gate *ReadinessGate) {
	mux := ms.Mux()

	// 🔴 ms.MetricsHandler(), never promhttp.Handler(). Every metric this microservice
	// constructs is registered on a registry the Microservice owns, and
	// promhttp.Handler() gathers prometheus.DefaultGatherer and nothing else — so it
	// answers 200 with every one of them missing. MetricsHandler also keeps promhttp's
	// own scrape instrumentation, which a bare promhttp.HandlerFor drops.
	mux.Handle("/metrics", ms.MetricsHandler())

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if gate != nil && gate.Ready() && !gate.Draining() {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	})
}

// HttpServer is a microservice's HTTP server, serving that microservice's own mux.
//
// 🔴 IT DELIBERATELY DOES NOT IMPLEMENT LifecycleComponent AND MUST NEVER REGISTER
// ITSELF FOR AUTOMATIC TEARDOWN. Where the HTTP stop belongs relative to the NATS stop
// is a per-service decision, and the two services that have thought about it reached
// OPPOSITE answers, both correct:
//
//   - device-management stops HTTP FIRST, so an in-flight mutation cannot publish onto
//     a connection that is already draining.
//   - lwm2m-ingest stops it LAST, because hoisting the NATS stop above it makes the
//     leadership lease release fail.
//
// A component that inserts itself into the lifecycle picks one of those orders for
// everybody, silently, and breaks whichever service needed the other. So Start and
// Shutdown are plain methods and the calling service decides when they run.
type HttpServer struct {
	server *http.Server

	// mu guards ln and stopped. The lifecycle callers are sequential, so it is not
	// there to order Start against Shutdown — it is there because Addr is the natural
	// way for anything else to discover the bound port, and reading a field another
	// goroutine writes is a data race whether or not the values ever disagree.
	mu      sync.Mutex
	ln      net.Listener
	stopped bool
}

// NewHttpServer builds an HTTP server for this microservice's mux on the given port.
// Nothing is bound and nothing is served until Start.
//
// Port 0 asks the operating system for an unused one, which is what makes a test able
// to drive a real listener; Addr reports what was actually bound.
func (ms *Microservice) NewHttpServer(port int32) *HttpServer {
	return &HttpServer{
		server: &http.Server{
			Addr:              fmt.Sprintf(":%d", port),
			Handler:           ms.Mux(),
			ReadHeaderTimeout: httpReadHeaderTimeout,
		},
	}
}

// Start binds the listening socket and then serves in the background.
//
// 🔴 THE BIND IS SYNCHRONOUS AND ITS ERROR IS RETURNED, WHICH IS THE POINT. Every
// server in this tree was started as `go func() { ListenAndServe() }()`, where a bind
// failure — a port already in use, a permission refusal — could only be logged from
// inside the goroutine. Startup carried on and reported success, and the service ran
// with no HTTP surface at all: no probes, no metrics, and no way for the lifecycle to
// know. Returning the bind error lets a service refuse to start, which is what the
// repository's fail-closed convention asks for.
//
// Only the serve loop runs in the background, and it can no longer fail in a way that
// matters: a Serve error after a successful bind is either the ErrServerClosed of a
// clean Shutdown or a genuinely exotic condition on an already-bound socket.
func (s *HttpServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 🔴 A SHUT-DOWN SERVER IS FINISHED, NOT MERELY IDLE, AND THE TWO REFUSALS SAY SO
	// DIFFERENTLY. http.Server latches its shuttingDown flag on the first Shutdown and
	// never clears it, so a later Serve returns ErrServerClosed immediately — inside
	// the goroutine below, where it is indistinguishable from a clean stop and is
	// swallowed. Restarting one therefore SUCCEEDS and serves nothing. Refusing here is
	// what turns that into an error the caller can act on, and a message that said
	// "already started" would send them looking for a second Start that does not exist.
	// A service that needs to serve again builds a new HttpServer.
	if s.stopped {
		return fmt.Errorf("http server on %s was shut down and cannot be restarted", s.server.Addr)
	}
	if s.ln != nil {
		return fmt.Errorf("http server is already started on %s", s.ln.Addr())
	}
	ln, err := net.Listen("tcp", s.server.Addr)
	if err != nil {
		return fmt.Errorf("binding http server to %s: %w", s.server.Addr, err)
	}
	s.ln = ln
	log.Info().Str("addr", ln.Addr().String()).Msg("Serving HTTP.")

	go func() {
		if err := s.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("HTTP server stopped serving.")
		}
	}()
	return nil
}

// Addr is the address actually bound, which is not the configured one when the port
// was 0. It returns "" before Start, and is safe to call from any goroutine.
func (s *HttpServer) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Shutdown stops serving, letting requests already in flight finish until ctx expires.
//
// It is safe on a server that was never started, because a service's teardown path
// runs after a startup that may have refused partway through — and a teardown that
// panics on the way out of a failed startup replaces the real error with its own. That
// case is a genuine no-op: forwarding it to http.Server.Shutdown would latch the
// shuttingDown flag and leave a server that binds but never serves.
//
// 🔴 WHAT net/http'S Shutdown DOES NOT COVER, since this wrapper looks like it might:
// it closes idle connections and waits for active ones, but HIJACKED connections it
// neither closes nor waits for. A caller that takes over a connection owns its
// teardown, and net/http stops accounting for it entirely.
//
// That is not hypothetical here. The GraphQL subscription endpoint upgrades through
// gorilla/websocket, whose Upgrader hijacks — so every live subscription is a
// connection this Shutdown will neither wait for nor close, and the server can report a
// clean stop with subscription goroutines still running on sockets it no longer knows
// about. Closing those is the subscription layer's job, not this one's (#949).
//
// MCP's event streams are the contrasting case and are NOT affected: they are ordinary
// active responses, which Shutdown does wait on.
func (s *HttpServer) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	started := s.ln != nil
	if started {
		s.stopped = true
	}
	s.mu.Unlock()

	// Deliberately outside the lock: Shutdown blocks until the active requests drain,
	// and holding mu across it would make every concurrent Addr wait out the drain.
	if !started {
		return nil
	}
	return s.server.Shutdown(ctx)
}
