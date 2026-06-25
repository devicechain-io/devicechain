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
// after MQTT (TB §2.9). A device POSTs a JSON event to "/dc/{tenant}/events"; the
// tenant is taken from the path (mirroring the MQTT "dc/{tenant}/..." topic
// convention, ADR-006), the body is decoded by the configured decoder, and the
// result is handed to the same decoded/failed callbacks the MQTT source uses, so
// both transports share one publish path. Device credentials ride in the event
// body (ADR-014), as on the MQTT path, so no separate transport auth is added here.
type HttpEventSource struct {
	Id      string
	Port    int
	Decoder Decoder

	server    *http.Server
	lifecycle core.LifecycleManager
	received  func(string, []byte)
	decoded   func(string, string, *model.UnresolvedEvent, interface{})
	failed    func(string, string, []byte, error)
}

// Create a new HTTP event source based on the given configuration.
func NewHttpEventSource(id string, config map[string]string, decoder Decoder,
	received func(string, []byte),
	decoded func(string, string, *model.UnresolvedEvent, interface{}),
	failed func(string, string, []byte, error)) (*HttpEventSource, error) {
	port := DEFAULT_HTTP_PORT
	if raw, ok := config["port"]; ok && raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid http event source port %q: %w", raw, err)
		}
		port = parsed
	}

	es := &HttpEventSource{
		Id:       id,
		Port:     port,
		Decoder:  decoder,
		received: received,
		decoded:  decoded,
		failed:   failed,
	}
	es.lifecycle = core.NewLifecycleManager("http-event-source", es, core.NewNoOpLifecycleCallbacks())
	return es, nil
}

// handler builds the routing mux. Exposed (unexported) so the lifecycle and tests
// share one definition of the routes.
func (es *HttpEventSource) handler() http.Handler {
	mux := http.NewServeMux()
	// The {tenant} path segment mirrors the MQTT topic's tenant level (ADR-006);
	// the ServeMux only matches a non-empty segment, so a missing tenant 404s.
	mux.HandleFunc("POST /dc/{tenant}/events", es.handleEvent)
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
