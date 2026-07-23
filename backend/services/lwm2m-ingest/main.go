// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command lwm2m-ingest is the DeviceChain LwM2M ingest adapter (ADR-075): a stateful
// service that terminates OMA LwM2M over CoAP/UDP+DTLS from constrained devices and
// folds their registration (presence), telemetry, and commands onto the canonical
// device-management model. Slice L0 stands up the transport substrate only — a
// DTLS-PSK-authenticated CoAP server that carries many concurrent sessions, fails
// closed on authentication, and (by default) issues a DTLS Connection ID (RFC 9146) so
// a session survives a client's source-address change without re-handshaking. The
// LwM2M semantic layer (the /rd registration interface, presence, Observe→measurement,
// downlink) arrives in later slices as handlers on the server's CoAP mux.
//
// HA posture (ADR-075 C1, ADR-070): unlike the Sparkplug adapter — which connects OUT
// to a broker, so a warm standby is safe — LwM2M devices connect IN to one bound UDP
// socket. A Kubernetes Service would spray device datagrams across replicas, so a warm
// standby would silently receive and drop half of them. GA therefore runs a SINGLE
// serving replica; a fenced ownership lease (rollout fencing + future N-shard) is wired
// in a later slice. This L0 skeleton is single-replica: readiness is normal, and the
// server binds while the pod is up.
package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-lwm2m-ingest/config"
	"github.com/devicechain-io/dc-lwm2m-ingest/server"
	"github.com/devicechain-io/dc-microservice/core"
)

const httpPort = 8080

var (
	Microservice  *core.Microservice
	Configuration *config.Lwm2mConfiguration
	Server        *server.Server
	httpServer    *http.Server
)

func main() {
	callbacks := core.LifecycleCallbacks{
		Initializer: core.LifecycleCallback{
			Preprocess:  func(context.Context) error { return nil },
			Postprocess: afterMicroserviceInitialized,
		},
		Starter: core.LifecycleCallback{
			Preprocess:  func(context.Context) error { return nil },
			Postprocess: afterMicroserviceStarted,
		},
		Stopper: core.LifecycleCallback{
			Preprocess:  beforeMicroserviceStopped,
			Postprocess: func(context.Context) error { return nil },
		},
		Terminator: core.LifecycleCallback{
			Preprocess:  func(context.Context) error { return nil },
			Postprocess: func(context.Context) error { return nil },
		},
	}
	Microservice = core.NewMicroservice(callbacks)
	Microservice.Run()
}

// parseConfiguration loads the typed config from the mounted document.
func parseConfiguration() error {
	cfg := &config.Lwm2mConfiguration{}
	if err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, cfg); err != nil {
		return err
	}
	Configuration = cfg
	return nil
}

// afterMicroserviceInitialized parses config, resolves the PSK credential map from the
// environment (fail-closed), builds the CoAP/DTLS transport, and registers the HTTP
// surface (probes + metrics). It starts no bearer-token auth gate: this service
// authenticates devices at the DTLS layer, not with platform JWTs.
func afterMicroserviceInitialized(ctx context.Context) error {
	if err := parseConfiguration(); err != nil {
		return err
	}

	// Credentials are resolved here (not in the server) so a missing/mis-projected PSK
	// Secret crashes startup rather than degrading into a listener that cannot
	// authenticate the identity it was meant to. An empty identity set is a valid inert
	// posture (bind, authenticate no one).
	creds, err := Configuration.ResolveCredentials()
	if err != nil {
		return err
	}

	metrics := server.Metrics{
		Handshakes: Microservice.NewCounter("handshakes_total",
			"Completed DTLS handshakes (each is a new authenticated LwM2M session).", nil),
		HandshakeFailures: Microservice.NewCounter("handshake_failures_total",
			"DTLS handshakes refused because the client presented an unprovisioned PSK identity (the fail-closed auth floor).", nil),
		ActiveSessions: Microservice.NewGauge("active_sessions",
			"Live DTLS sessions currently held.", nil),
		SessionsRejected: Microservice.NewCounter("sessions_rejected_total",
			"New sessions refused because the live-session ceiling (maxSessions) was reached.", nil),
		Requests: Microservice.NewCounterVec("coap_requests_total",
			"CoAP requests handled, by response code.", []string{"code"}),
	}

	srv, err := server.New(server.Config{
		Addr:             fmt.Sprintf("%s:%d", Configuration.Listen.Host, Configuration.Listen.Port),
		Credentials:      creds,
		CIDLength:        Configuration.ConnectionIdLengthOrDefault(),
		HandshakeTimeout: durationSeconds(Configuration.Security.HandshakeTimeoutSeconds),
		MaxSessions:      Configuration.Security.MaxSessions,
		IdleTimeout:      durationSeconds(Configuration.Security.IdleTimeoutSeconds),
	}, metrics)
	if err != nil {
		return err
	}
	Server = srv
	log.Info().Str("addr", srv.Addr().String()).Int("cidLength", Configuration.ConnectionIdLengthOrDefault()).
		Int("credentials", len(creds)).Msg("Built LwM2M CoAP/DTLS transport.")

	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	http.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if Microservice.Readiness.Ready() && !Microservice.Readiness.Draining() {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	})
	return nil
}

// afterMicroserviceStarted begins serving the CoAP/DTLS transport in the background,
// opens readiness, and starts the HTTP server. Readiness is not gated on any device
// connecting — a device-less deployment is healthy.
func afterMicroserviceStarted(_ context.Context) error {
	go func() {
		if err := Server.Serve(); err != nil {
			log.Error().Err(err).Msg("LwM2M CoAP/DTLS transport exited with error.")
		}
	}()
	Microservice.MarkReady(nil)

	httpServer = &http.Server{Addr: fmt.Sprintf(":%d", httpPort)}
	go func() {
		log.Info().Int("port", httpPort).Msg("Starting LwM2M ingest HTTP server.")
		if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
			log.Error().Err(err).Msg("LwM2M ingest HTTP server exited with error.")
		}
	}()
	return nil
}

// beforeMicroserviceStopped drains readiness, stops the transport, and shuts the HTTP
// server down.
func beforeMicroserviceStopped(ctx context.Context) error {
	Microservice.Readiness.BeginDrain()
	if Server != nil {
		Server.Stop()
	}
	if httpServer == nil {
		return nil
	}
	return httpServer.Shutdown(ctx)
}

// durationSeconds converts a whole-second config value to a Duration (0 stays 0).
func durationSeconds(seconds int) time.Duration {
	return time.Duration(seconds) * time.Second
}
