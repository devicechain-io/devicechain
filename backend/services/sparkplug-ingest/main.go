// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command sparkplug-ingest is the DeviceChain Sparkplug B Host Application
// (ADR-069): a stateful adapter that connects OUT to one or more per-tenant
// customer MQTT brokers, announces its Sparkplug 3.0 STATE on each, subscribes to
// the configured Sparkplug groups, and decodes edge-node / device telemetry.
// Tenancy is connection-scoped (M7): every message is attributed to the tenant
// configured for the broker it arrived on, never parsed from the untrusted
// Sparkplug topic. At SP3a it terminates and logs that traffic, tenant-tagged;
// identity mapping, presence emission, and leader-elected failover arrive in
// SP3b/SP4.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/auth"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	"github.com/devicechain-io/dc-sparkplug-ingest/host"
)

const httpPort = 8080

var (
	Microservice  *core.Microservice
	Configuration *config.SparkplugConfiguration
	Manager       *host.Manager
	NatsManager   *messaging.NatsManager
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
			Postprocess: afterMicroserviceTerminated,
		},
	}
	Microservice = core.NewMicroservice(callbacks)
	Microservice.Run()
}

// parseConfiguration loads the typed config from the mounted document.
func parseConfiguration() error {
	cfg := &config.SparkplugConfiguration{}
	if err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, cfg); err != nil {
		return err
	}
	Configuration = cfg
	return nil
}

// afterMicroserviceInitialized parses config, builds one tenant-bound Host
// Application client per configured source, and registers the HTTP surface
// (probes + metrics). It starts no auth gate: this service validates no bearer
// tokens — it is a transport that connects out to customer brokers — so readiness
// is opened by the Manager once it is supervising (afterMicroserviceStarted).
func afterMicroserviceInitialized(ctx context.Context) error {
	if err := parseConfiguration(); err != nil {
		return err
	}

	// Metrics are shared across every source. They are intentionally NOT labeled by
	// tenant: a per-tenant label on a counter driven by broker traffic is a
	// cardinality risk (the governance lesson) and the aggregate is enough here —
	// per-source attribution lives in the (cardinality-safe) logs.
	metrics := host.Metrics{
		Messages: Microservice.NewCounterVec("messages_total",
			"Sparkplug messages received, by message type.", []string{"type"}),
		DecodeErrors: Microservice.NewCounter("decode_errors_total",
			"Sparkplug messages that failed topic parse or payload decode.", nil),
		RebirthRequests: Microservice.NewCounter("rebirth_requests_total",
			"Sparkplug node rebirth commands emitted by the session machine.", nil),
		ConnectFailures: Microservice.NewCounter("connect_failures_total",
			"Failed broker connect attempts across all sources (readiness is not broker-gated; this is how a broken source config surfaces).", nil),
		IngestFailures: Microservice.NewCounter("ingest_failures_total",
			"Accepted messages whose samples were dropped after the in-handler ingest retry budget was exhausted (device-management or NATS unreachable).", nil),
	}

	// Bring up the durable emit path. The writer is created SYNCHRONOUSLY here, right
	// after Initialize sets the JetStream context — NOT in the NatsManager's oncreate
	// callback, which does not run until Start. Capturing the oncreate-populated
	// global at this point would hand the emitter a nil writer and panic the receive
	// goroutine on the first message; NewWriter only needs the JS context, so it is
	// safe (and correct) to build the writer now and pass it in by value.
	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(),
		func(*messaging.NatsManager) error { return nil })
	if err := NatsManager.Initialize(ctx); err != nil {
		return err
	}

	// The ingest pipeline (device resolution + durable emit) is REQUIRED whenever the
	// adapter has any source to ingest from — a source with an unreachable device
	// path would silently drop all its telemetry. Build it only when there are
	// sources so an inert (empty-sources) deployment does not demand service auth it
	// will never use.
	var ingester *host.Ingester
	if len(Configuration.Sources) > 0 {
		writer, err := NatsManager.NewWriter(streams.InboundEvents)
		if err != nil {
			return err
		}
		built, err := buildIngester(writer)
		if err != nil {
			return err
		}
		ingester = built
	}

	clients, err := resolveSources(Configuration, Microservice.InstanceId, ingester, metrics)
	if err != nil {
		return err
	}
	Manager = host.NewManager(clients)
	log.Info().Int("sources", len(clients)).Msg("Built Sparkplug source connections.")

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

// resolveSources turns each configured source into a resolved Host Application
// client. Failing here (bad scheme, an intended-but-empty password env) fails the
// service closed at startup rather than dialing a mis-authenticated broker and
// retry-looping silently — repo convention (ADR-022).
func resolveSources(cfg *config.SparkplugConfiguration, instanceId string, ingester *host.Ingester, metrics host.Metrics) ([]*host.Client, error) {
	// Convert a possibly-nil *Ingester into the interface as a real nil rather than a
	// typed nil: passing a nil *Ingester directly would wrap into a NON-nil interface,
	// so the Client's `ingester != nil` guard would pass and Ingest would panic. This
	// makes the "non-nil whenever there are sources" invariant structural, not a
	// comment. (In production the caller only builds an ingester when Sources is
	// non-empty, so this stays nil only for the zero-source case.)
	var si host.SampleIngester
	if ingester != nil {
		si = ingester
	}
	clients := make([]*host.Client, 0, len(cfg.Sources))
	for i := range cfg.Sources {
		src := cfg.Sources[i]
		broker, err := resolveBroker(src, instanceId, i)
		if err != nil {
			return nil, err
		}
		clients = append(clients, host.NewClient(src, broker, si, time.Now, metrics))
	}
	return clients, nil
}

// buildIngester assembles the device-resolution + durable-emit pipeline around a
// live inbound-events writer. It fails closed when the ingest path is not configured
// (or the writer is nil): a Sparkplug adapter that cannot reach device-management or
// NATS would resolve no devices / emit nothing and silently drop every measurement,
// so a misconfigured deploy or a wiring-order regression must be visible at startup,
// not at runtime (ADR-022). The service token carries device:read (lookup by
// external id) and device:write (auto-registration) only — least privilege.
func buildIngester(writer messaging.MessageWriter) (*host.Ingester, error) {
	if writer == nil {
		return nil, fmt.Errorf("inbound-events writer is nil — the durable emit path was not wired before the ingester was built")
	}
	infra := Microservice.InstanceConfiguration.Infrastructure
	graphqlURL, err := ingestEndpoint(infra)
	if err != nil {
		return nil, err
	}
	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "sparkplug-ingest",
		[]string{string(auth.DeviceRead), string(auth.DeviceWrite)})

	ingestMetrics := host.IngestMetrics{
		MeasurementsEmitted: Microservice.NewCounter("measurements_emitted_total",
			"Numeric Sparkplug samples durably written to the inbound-events stream.", nil),
		DevicesRegistered: Microservice.NewCounter("devices_registered_total",
			"Devices auto-registered on first sight of their Sparkplug identity.", nil),
		UnknownDropped: Microservice.NewCounter("unknown_device_dropped_total",
			"Samples dropped for an unregistered device on a source with auto-registration off.", nil),
	}
	registrar := host.NewRegistrar(client, graphqlURL)
	emitter := host.NewEmitter(writer, time.Now)
	log.Info().Str("deviceManagement", graphqlURL).Msg("Sparkplug ingest will resolve + emit telemetry via device-management (ADR-044).")
	return host.NewIngester(registrar, emitter, ingestMetrics), nil
}

// ingestEndpoint validates the infrastructure the ingest path needs and returns
// device-management's GraphQL URL. It fails closed: a Sparkplug adapter that cannot
// reach device-management resolves no devices and silently drops every measurement,
// so a missing service secret or device-management coordinate must surface at
// startup, not as invisible data loss at runtime.
func ingestEndpoint(infra mscfg.InfrastructureConfiguration) (string, error) {
	if infra.ServiceAuth.Secret == "" {
		return "", fmt.Errorf("service secret not configured (infrastructure.serviceAuth) — sparkplug-ingest cannot resolve devices and would drop all telemetry")
	}
	if infra.DeviceManagement.Hostname == "" || infra.DeviceManagement.Port == 0 {
		return "", fmt.Errorf("device-management endpoint not configured (infrastructure.deviceManagement) — sparkplug-ingest cannot resolve devices and would drop all telemetry")
	}
	return fmt.Sprintf("http://%s:%d/graphql", infra.DeviceManagement.Hostname, infra.DeviceManagement.Port), nil
}

// resolveBroker builds one source's MQTT connection: TLS is selected by the URL
// scheme (ssl:// ⇒ TLS with the system root CAs; custom-CA / mutual-TLS to a
// private broker is a post-GA follow-up), the password is read once from the named
// environment variable (a Kubernetes Secret projected by the chart, never
// cleartext in config), and the client id is made unique per source so two sources
// on one broker cannot race for the same id.
func resolveBroker(src config.SparkplugSource, instanceId string, index int) (host.Broker, error) {
	// The URL was already parsed + scheme-validated by config.Validate; parse again
	// only to derive the transport. Keep the switch to the schemes Validate accepts
	// (tcp / ssl / tls) so the two cannot drift.
	u, err := url.Parse(src.Broker.URL)
	if err != nil {
		return host.Broker{}, fmt.Errorf("sources[%d]: broker.url %q is not a valid URL: %w", index, src.Broker.URL, err)
	}
	var tlsCfg *tls.Config
	switch u.Scheme {
	case "ssl", "tls":
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
	case "tcp":
		// plaintext transport; no TLS config
	default:
		return host.Broker{}, fmt.Errorf("sources[%d]: unsupported broker URL scheme %q (want tcp:// or ssl://)", index, u.Scheme)
	}

	var password string
	if src.Broker.PasswordEnv != "" {
		password = os.Getenv(src.Broker.PasswordEnv)
		if password == "" {
			return host.Broker{}, fmt.Errorf("sources[%d]: passwordEnv %q is set but that environment variable is empty (the broker Secret was not projected)", index, src.Broker.PasswordEnv)
		}
	}

	return host.Broker{
		URL:      src.Broker.URL,
		ClientID: fmt.Sprintf("dc-sparkplug-host-%s-%d", instanceId, index),
		Username: src.Broker.Username,
		Password: password,
		TLS:      tlsCfg,
	}, nil
}

// afterMicroserviceStarted starts the Manager (which connects every source in the
// background) and opens readiness, then starts the HTTP server. Readiness is not
// gated on any broker being reachable — a customer broker being down degrades that
// one source, never the pod.
func afterMicroserviceStarted(ctx context.Context) error {
	// Start the durable writer BEFORE the sources connect, so the JetStream publish
	// path is live before the first broker message can arrive.
	if err := NatsManager.Start(ctx); err != nil {
		return err
	}
	Manager.Start()
	Microservice.MarkReady(nil)

	httpServer = &http.Server{Addr: fmt.Sprintf(":%d", httpPort)}
	go func() {
		log.Info().Int("port", httpPort).Msg("Starting Sparkplug ingest HTTP server.")
		if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
			log.Error().Err(err).Msg("Sparkplug ingest HTTP server exited with error.")
		}
	}()
	return nil
}

// beforeMicroserviceStopped announces OFFLINE on every source, disconnects them,
// and shuts the HTTP server down.
func beforeMicroserviceStopped(ctx context.Context) error {
	Microservice.Readiness.BeginDrain()
	// Stop the sources first (announce OFFLINE, disconnect) so no further message can
	// be emitted, THEN stop the writer.
	if Manager != nil {
		Manager.Stop()
	}
	if NatsManager != nil {
		if err := NatsManager.Stop(ctx); err != nil {
			log.Error().Err(err).Msg("Error stopping the NATS manager.")
		}
	}
	if httpServer == nil {
		return nil
	}
	return httpServer.Shutdown(ctx)
}

// afterMicroserviceTerminated releases the NATS manager's resources.
func afterMicroserviceTerminated(ctx context.Context) error {
	if NatsManager != nil {
		if err := NatsManager.Terminate(ctx); err != nil {
			log.Error().Err(err).Msg("Error terminating the NATS manager.")
		}
	}
	return nil
}
