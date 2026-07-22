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

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	"github.com/devicechain-io/dc-sparkplug-ingest/host"
)

const httpPort = 8080

var (
	Microservice  *core.Microservice
	Configuration *config.SparkplugConfiguration
	Manager       *host.Manager
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
func afterMicroserviceInitialized(_ context.Context) error {
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
	}

	clients, err := resolveSources(Configuration, Microservice.InstanceId, metrics)
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
func resolveSources(cfg *config.SparkplugConfiguration, instanceId string, metrics host.Metrics) ([]*host.Client, error) {
	clients := make([]*host.Client, 0, len(cfg.Sources))
	for i := range cfg.Sources {
		src := cfg.Sources[i]
		broker, err := resolveBroker(src, instanceId, i)
		if err != nil {
			return nil, err
		}
		clients = append(clients, host.NewClient(src, broker, time.Now, metrics))
	}
	return clients, nil
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
func afterMicroserviceStarted(_ context.Context) error {
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
	if Manager != nil {
		Manager.Stop()
	}
	if httpServer == nil {
		return nil
	}
	return httpServer.Shutdown(ctx)
}
