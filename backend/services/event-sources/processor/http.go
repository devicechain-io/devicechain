// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/rs/zerolog/log"
)

const (
	// TYPE_HTTP and DEFAULT_HTTP_PORT are defined FROM the config package rather than
	// standing on their own here, because configuration validation has to know which
	// sources listen and on which port before any of them is built — and this package
	// imports that one, so the constants can only be shared in that direction.
	TYPE_HTTP = config.SourceTypeHttp
	// DEFAULT_HTTP_PORT is the listen port for HTTP ingest when none is configured.
	// It is distinct from the service's GraphQL/readiness server (8080) so device
	// telemetry and the management API do not share a listener.
	DEFAULT_HTTP_PORT = config.DefaultHttpIngestPort
	// maxBodyBytes caps an inbound event body so a single request cannot exhaust
	// memory; an event payload is small (a handful of measurements/locations).
	maxBodyBytes = 1 << 20 // 1 MiB
)

// HttpEventSource ingests device events over HTTP — the most common integration
// after MQTT (TB §2.9). A device POSTs a JSON event to
// "/{instanceId}/{tenant}/events"; the instance and tenant are taken from the path
// (mirroring the MQTT "{instanceId}/{tenant}/..." topic convention, ADR-006/ADR-048),
// the body is decoded by the configured decoder, and the result is handed to the
// same decoded/failed callbacks the MQTT source uses, so both transports share one
// publish path. The instance id is a literal path segment so an event addressed to
// another instance 404s here — the HTTP-side of the cross-instance isolation
// boundary. Device credentials ride in the event body (ADR-014), as on the MQTT
// path, so no separate transport auth is added here.
type HttpEventSource struct {
	Id         string
	Port       int
	InstanceId string
	Decoder    Decoder
	// Ingest carries the device-facing listener bounds — the header and whole-request
	// read timeouts — which are configurable precisely because this listener's callers
	// are devices on constrained links rather than a console on a datacentre network.
	Ingest config.HttpIngest

	// server is built by ExecuteStart and dropped by ExecuteStop, never reused: an
	// http.Server latches its shutting-down flag permanently, so a restarted one binds
	// nothing and serves nothing. It is nil before the first start and after every stop.
	server    *core.HttpServer
	lifecycle core.LifecycleManager
	received  func(string, []byte)
	decoded   func(string, string, *model.UnresolvedEvent, interface{}, uint64) error
	failed    func(string, string, []byte, error) error
	// allow meters an inbound request against its tenant's ingest rate limit
	// before the body is read/decoded; a false return sheds the request with a
	// 429. nil disables metering (used by tests that exercise decoding directly).
	allow RateGate
	// earlyClose accounts for a connection that closed without ever delivering a
	// request, which is what a header-timeout close looks like from the outside. nil
	// disables the accounting.
	earlyClose func(string)
}

// Create a new HTTP event source based on the given configuration. instanceId is
// the literal first path segment the ingest route is scoped under (ADR-048), and
// ingest carries the listener bounds that apply to device-facing servers only.
//
// The port comes from config.HttpSourcePort, which is the same function the
// configuration validation reads — so the port that passed validation is the port this
// source binds, and one that could not have passed is refused here too rather than
// carried as far as the bind.
func NewHttpEventSource(id string, srcConfig map[string]string, instanceId string, ingest config.HttpIngest,
	decoder Decoder,
	received func(string, []byte),
	decoded func(string, string, *model.UnresolvedEvent, interface{}, uint64) error,
	failed func(string, string, []byte, error) error,
	allow RateGate,
	earlyClose func(string)) (*HttpEventSource, error) {
	port, err := config.HttpSourcePort(srcConfig)
	if err != nil {
		return nil, err
	}

	es := &HttpEventSource{
		Id:         id,
		Port:       port,
		InstanceId: instanceId,
		Ingest:     ingest,
		Decoder:    decoder,
		received:   received,
		decoded:    decoded,
		failed:     failed,
		allow:      allow,
		earlyClose: earlyClose,
	}
	es.lifecycle = core.NewLifecycleManager("http-event-source", es, core.NewNoOpLifecycleCallbacks())
	return es, nil
}

// handler builds the routing mux. Exposed (unexported) so the lifecycle and tests
// share one definition of the routes.
func (es *HttpEventSource) handler() http.Handler {
	mux := http.NewServeMux()
	// The route is scoped under this instance's id as a literal first segment
	// (ADR-048) with the {tenant} segment mirroring the MQTT topic's tenant level
	// (ADR-006); the ServeMux only matches a non-empty segment, so a missing tenant
	// — or an event addressed to a different instance — 404s.
	mux.HandleFunc(fmt.Sprintf("POST /%s/{tenant}/events", es.InstanceId), es.handleEvent)
	return mux
}

// handleEvent decodes a single posted event and forwards it to the shared
// publish path. It returns 202 once the event is accepted into the pipeline
// (delivery to NATS is asynchronous, as on the MQTT path) and 400 when the body
// cannot be decoded.
func (es *HttpEventSource) handleEvent(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	if tenant == "" {
		http.Error(w, "missing tenant in path", http.StatusBadRequest)
		return
	}
	// A syntactically invalid tenant is knowable at request time and can never be
	// published to a valid subject, so reject it synchronously with a 400 rather
	// than accept (202) an event the async publish path will only drop. Mirrors the
	// fail-closed guard in messaging.WriteMessages (both call core.ValidateToken).
	if err := core.ValidateToken(tenant); err != nil {
		http.Error(w, fmt.Sprintf("invalid tenant in path: %v", err), http.StatusBadRequest)
		return
	}

	// Meter against the tenant's ingest ceiling before reading or decoding the
	// body, so a tenant over its limit is shed with a 429 having spent no decode
	// CPU. Advise a Retry-After (RFC 6585 §4) so a well-behaved client backs off
	// rather than immediately re-hitting the gate.
	if es.allow != nil && !es.allow(es.Id, tenant, time.Time{}, false) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "ingest rate limit exceeded for tenant", http.StatusTooManyRequests)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "unable to read request body", http.StatusBadRequest)
		return
	}
	es.received(es.Id, body)

	event, payload, err := es.Decoder.Decode(body)
	if err != nil {
		es.failed(es.Id, tenant, body, err)
		http.Error(w, fmt.Sprintf("unable to decode event: %v", err), http.StatusBadRequest)
		return
	}
	// HTTP carries no capture sequence — there is no broker redelivery to dedup on
	// this path — so it publishes no dedup id (see processor.DedupID).
	//
	// The publish result is now reported to the CLIENT rather than swallowed. 202
	// means "accepted", and returning it for an event that never reached the stream
	// told the caller its data was safe when it had been dropped — the same silent
	// loss ADR-030 removes from the MQTT path, on the one transport that can
	// actually say so. A client that retries on 503 now loses nothing.
	if err := es.decoded(es.Id, tenant, event, payload, 0); err != nil {
		http.Error(w, "unable to accept event: downstream unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// Initialize event source
func (es *HttpEventSource) Initialize(ctx context.Context) error {
	return es.lifecycle.Initialize(ctx)
}

// Initialize event source (as called by lifecycle manager)
//
// The listening server is deliberately NOT built here. It is built per start, in
// ExecuteStart, because net/http's server cannot be started twice: Shutdown latches
// its shutting-down flag permanently, so a server built once and reused would bind and
// then serve nothing. ExecuteStart is entered again by any start retried after a failed
// one, and each entry has to get its own server for that reason.
func (es *HttpEventSource) ExecuteInitialize(ctx context.Context) error {
	log.Info().Msg("HTTP event source initialized.")
	return nil
}

// Start event source
func (es *HttpEventSource) Start(ctx context.Context) error {
	return es.lifecycle.Start(ctx)
}

// Start event source (as called by lifecycle manager)
//
// A fresh server is built on every entry — see ExecuteInitialize for why it cannot be
// built once — and the bind happens synchronously, so a port already in use or a
// permission refusal FAILS THE START instead of being logged from a goroutine nobody is
// listening to. Both of those used to be silent: the source reported
// a successful start and ingested nothing, and the only symptom was device telemetry
// over HTTP that stopped arriving.
func (es *HttpEventSource) ExecuteStart(ctx context.Context) error {
	tracker := es.newConnTracker()
	server := core.NewHttpServerForHandlerWithOptions(int32(es.Port), tracker.wrap(es.handler()), core.HttpServerOptions{
		ReadHeaderTimeout: es.Ingest.HeaderTimeout(),
		// 🔴 ReadTimeout IS SET HERE AND NOWHERE ELSE IN THE TREE, because this listener
		// serves nothing long-lived. The shared servers carry the GraphQL subscription
		// endpoint, whose connections are hijacked for a WebSocket and are meant to stay
		// open; this one serves one bounded POST per connection. The body was capped in
		// BYTES (1 MiB) and not in TIME, so a client dribbling a body held a connection
		// and a goroutine indefinitely — the larger of the two holds, and the only one
		// that was unbounded.
		//
		// WriteTimeout is deliberately still unset. Its clock starts when the request
		// headers are read, so it bounds the HANDLER rather than the write — and this
		// handler hands the event to the publish path before it answers. A publish that
		// is slow under load would sever the connection AFTER the event was accepted, so
		// the device would read a delivered event as a failure and send it again.
		// Bounding a response nobody is waiting on is not worth manufacturing duplicates
		// for.
		ReadTimeout: es.Ingest.RequestTimeout(),
		ConnState:   tracker.connState,
		ConnContext: tracker.connContext,
	})
	if err := server.Start(); err != nil {
		return fmt.Errorf("unable to start http event source %q: %w", es.Id, err)
	}
	es.server = server
	log.Info().Str("addr", server.Addr()).Msg("HTTP event source listening.")
	return nil
}

// Addr is the address this source is actually serving on, or "" when it is not started.
// It is not the configured port when the port was 0, which is the case anything
// discovering the listener from outside this package has to be able to see.
func (es *HttpEventSource) Addr() string {
	server := es.server
	if server == nil {
		return ""
	}
	return server.Addr()
}

// Stop event source
func (es *HttpEventSource) Stop(ctx context.Context) error {
	return es.lifecycle.Stop(ctx)
}

// Stop event source (as called by lifecycle manager)
//
// A stop can arrive on a source that was initialized and never started — Initialized is
// on the permitted set for stop, so that a component whose start failed halfway is still
// torn down — which is why the nil server is a no-op rather than a panic. Dropping the
// reference is what lets the next start bind again.
func (es *HttpEventSource) ExecuteStop(ctx context.Context) error {
	server := es.server
	if server == nil {
		return nil
	}
	es.server = nil
	return server.Shutdown(ctx)
}

// Terminate event source
func (es *HttpEventSource) Terminate(ctx context.Context) error {
	return es.lifecycle.Terminate(ctx)
}

// Terminate event source (as called by lifecycle manager)
func (es *HttpEventSource) ExecuteTerminate(ctx context.Context) error {
	return nil
}

// connTracker counts connections to this listener that sent bytes and never produced a
// request, which is what a header-timeout close looks like from outside net/http.
//
// 🔑 IT EXISTS BECAUSE SUCH A CLOSE IS OTHERWISE INVISIBLE. net/http closes the
// connection itself, before any handler runs: there is no event, no error and no log
// line, so a device cut off mid-headers is indistinguishable from one that never called.
// An operator asking "is this bound costing us traffic?" has nothing to read, and a
// bound that is now configurable is a knob turned in the dark without one.
//
// 🔴 ConnState ALONE CANNOT ANSWER THIS, WHICH IS WHY THE HANDLER TAKES PART. StateActive
// fires as soon as ONE BYTE of a request has been read and before the request reaches a
// handler, so a connection abandoned part-way through its headers is already "active" and
// reads exactly like a served one. What separates them is whether a request ever entered
// the handler, which only the handler knows — so the handler marks its connection and the
// close hook reads the mark.
//
// 🔴 IT COUNTS WHAT IT CAN SEE, AND THE METRIC IS NAMED FOR THAT. A connection that sent
// bytes and produced no request is usually a header timeout, but it is also a client that
// hung up mid-request, a port scan that opened and wrote nothing, or a request net/http
// itself refused before routing it. It is a rate to WATCH rather than a count of
// timeouts. A slow BODY cut off by ReadTimeout is deliberately NOT counted: its headers
// were complete, so its request did reach the handler.
type connTracker struct {
	source string
	count  func(string)

	mu sync.Mutex
	// served holds the connections that have delivered at least one request. It is
	// bounded by the live connection count: an entry is only made for a connection that
	// reached the handler, and every entry is removed by the terminal state net/http
	// guarantees for each connection (closed or hijacked).
	served map[net.Conn]struct{}
}

// connContextKey is the private key this listener's connection is carried under. It is
// a distinct unexported type so nothing else can read or collide with it.
type connContextKey struct{}

// newConnTracker returns the tracker for this source, or nil when nothing is accounting
// for early closes. Every method below is nil-safe, so the nil tracker is the "off"
// switch rather than a branch at each call site.
func (es *HttpEventSource) newConnTracker() *connTracker {
	if es.earlyClose == nil {
		return nil
	}
	return &connTracker{source: es.Id, count: es.earlyClose, served: make(map[net.Conn]struct{})}
}

// wrap marks the connection a request arrived on before handing the request on.
func (t *connTracker) wrap(handler http.Handler) http.Handler {
	if t == nil {
		return handler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The value is absent when the handler is exercised directly rather than over a
		// connection, which is how most of this package's tests drive it. That is a
		// request with no connection to attribute, not an error.
		if conn, ok := r.Context().Value(connContextKey{}).(net.Conn); ok {
			t.mu.Lock()
			t.served[conn] = struct{}{}
			t.mu.Unlock()
		}
		handler.ServeHTTP(w, r)
	})
}

// connContext puts the connection on the base context of every request it carries.
func (t *connTracker) connContext(ctx context.Context, conn net.Conn) context.Context {
	if t == nil {
		return ctx
	}
	return context.WithValue(ctx, connContextKey{}, conn)
}

// connState counts a connection that ends without ever having reached the handler.
func (t *connTracker) connState(conn net.Conn, state http.ConnState) {
	if t == nil {
		return
	}
	if state != http.StateClosed && state != http.StateHijacked {
		return
	}
	t.mu.Lock()
	_, served := t.served[conn]
	delete(t.served, conn)
	t.mu.Unlock()
	if !served {
		t.count(t.source)
	}
}
