// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/rs/zerolog/log"
)

const (
	TYPE_HTTP = "http"
	// DEFAULT_HTTP_PORT is the listen port for HTTP ingest when none is configured.
	// It is distinct from the service's GraphQL/readiness server (8080) so device
	// telemetry and the management API do not share a listener.
	DEFAULT_HTTP_PORT = 8081
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

	server    *http.Server
	lifecycle core.LifecycleManager
	received  func(string, []byte)
	decoded   func(string, string, *model.UnresolvedEvent, interface{})
	failed    func(string, string, []byte, error)
	// allow meters an inbound request against its tenant's ingest rate limit
	// before the body is read/decoded; a false return sheds the request with a
	// 429. nil disables metering (used by tests that exercise decoding directly).
	allow func(string, string) bool
}

// Create a new HTTP event source based on the given configuration. instanceId is
// the literal first path segment the ingest route is scoped under (ADR-048).
func NewHttpEventSource(id string, config map[string]string, instanceId string, decoder Decoder,
	received func(string, []byte),
	decoded func(string, string, *model.UnresolvedEvent, interface{}),
	failed func(string, string, []byte, error),
	allow func(string, string) bool) (*HttpEventSource, error) {
	port := DEFAULT_HTTP_PORT
	if raw, ok := config["port"]; ok && raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid http event source port %q: %w", raw, err)
		}
		port = parsed
	}

	es := &HttpEventSource{
		Id:         id,
		Port:       port,
		InstanceId: instanceId,
		Decoder:    decoder,
		received:   received,
		decoded:    decoded,
		failed:     failed,
		allow:      allow,
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
	if es.allow != nil && !es.allow(es.Id, tenant) {
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
	es.decoded(es.Id, tenant, event, payload)
	w.WriteHeader(http.StatusAccepted)
}

// Initialize event source
func (es *HttpEventSource) Initialize(ctx context.Context) error {
	return es.lifecycle.Initialize(ctx)
}

// Initialize event source (as called by lifecycle manager)
func (es *HttpEventSource) ExecuteInitialize(ctx context.Context) error {
	es.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", es.Port),
		Handler: es.handler(),
	}
	log.Info().Msg("HTTP event source initialized.")
	return nil
}

// Start event source
func (es *HttpEventSource) Start(ctx context.Context) error {
	return es.lifecycle.Start(ctx)
}

// Start event source (as called by lifecycle manager)
func (es *HttpEventSource) ExecuteStart(ctx context.Context) error {
	go func() {
		log.Info().Int("port", es.Port).Msg("HTTP event source listening.")
		if err := es.server.ListenAndServe(); err != http.ErrServerClosed {
			log.Error().Err(err).Msg("HTTP event source server stopped unexpectedly.")
		}
	}()
	return nil
}

// Stop event source
func (es *HttpEventSource) Stop(ctx context.Context) error {
	return es.lifecycle.Stop(ctx)
}

// Stop event source (as called by lifecycle manager)
func (es *HttpEventSource) ExecuteStop(ctx context.Context) error {
	return es.server.Shutdown(ctx)
}

// Terminate event source
func (es *HttpEventSource) Terminate(ctx context.Context) error {
	return es.lifecycle.Terminate(ctx)
}

// Terminate event source (as called by lifecycle manager)
func (es *HttpEventSource) ExecuteTerminate(ctx context.Context) error {
	return nil
}
