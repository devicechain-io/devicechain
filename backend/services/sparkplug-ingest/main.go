// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command sparkplug-ingest is the DeviceChain Sparkplug B Host Application
// (ADR-069): a stateful adapter that connects to DeviceChain's own MQTT gateway,
// announces its Sparkplug 3.0 STATE, subscribes to the configured Sparkplug
// groups, and decodes edge-node / device telemetry. At SP1 it terminates and logs
// that traffic; identity mapping, presence emission, and leader-elected failover
// arrive in SP3/SP4.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"

	coreconfig "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	"github.com/devicechain-io/dc-sparkplug-ingest/host"
)

const (
	httpPort = 8080
	// mqttGatewayPort is the fixed MQTT listener on the NATS broker (the 1883
	// gateway alongside the 4222 client listener); it is not a config field.
	mqttGatewayPort = 1883
)

var (
	Microservice  *core.Microservice
	Configuration *config.SparkplugConfiguration
	HostClient    *host.Client
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

// afterMicroserviceInitialized parses config, builds the Host Application client
// against the MQTT gateway, and registers the HTTP surface (probes + metrics).
// It does NOT start an auth gate: this service validates no bearer tokens — it is
// a transport that connects to the broker with the service credential — so
// readiness is gated on the MQTT connection instead, opened from the connect
// handler.
func afterMicroserviceInitialized(_ context.Context) error {
	if err := parseConfiguration(); err != nil {
		return err
	}

	messages := Microservice.NewCounterVec("messages_total",
		"Sparkplug messages received, by message type.", []string{"type"})
	decodeErrors := Microservice.NewCounter("decode_errors_total",
		"Sparkplug messages that failed topic parse or payload decode.", nil)
	rebirthRequests := Microservice.NewCounter("rebirth_requests_total",
		"Sparkplug node rebirth commands emitted by the session machine.", nil)

	broker, err := resolveBroker(Microservice.InstanceConfiguration.Infrastructure.Nats)
	if err != nil {
		return err
	}
	// Readiness opens once the host connects to the gateway. No JWT validator is
	// involved, so the gate is opened with a nil validator.
	onReady := func() { Microservice.MarkReady(nil) }
	HostClient = host.NewClient(*Configuration, broker, time.Now, onReady,
		host.Metrics{Messages: messages, DecodeErrors: decodeErrors, RebirthRequests: rebirthRequests})

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

// resolveBroker derives the MQTT gateway connection from the instance's NATS
// configuration (ADR-025): the same host as the 4222 client listener, on the
// 1883 gateway port, with TLS and the service credential when configured. A
// Sparkplug Host connects as the callout-exempt dc_service login. A
// configured-but-unusable CA fails closed (returns an error) — repo convention is
// to reject bad config at startup rather than dial a TLS listener with no
// verification material and retry-loop silently.
func resolveBroker(natscfg coreconfig.NatsConfiguration) (host.Broker, error) {
	scheme := "tcp"
	var tlsCfg *tls.Config
	if natscfg.Tls.Enabled {
		scheme = "ssl"
		cfg, err := natscfg.TLSConfig(natscfg.Hostname)
		if err != nil {
			return host.Broker{}, fmt.Errorf("build MQTT gateway TLS config from instance NATS CA: %w", err)
		}
		tlsCfg = cfg
	}
	return host.Broker{
		URL:      fmt.Sprintf("%s://%s:%d", scheme, natscfg.Hostname, mqttGatewayPort),
		ClientID: fmt.Sprintf("dc-sparkplug-host-%s", Microservice.InstanceId),
		Username: natscfg.Auth.User,
		Password: natscfg.Auth.Password,
		TLS:      tlsCfg,
	}, nil
}

// afterMicroserviceStarted connects the Host Application and starts the HTTP
// server.
func afterMicroserviceStarted(_ context.Context) error {
	HostClient.Connect()

	httpServer = &http.Server{Addr: fmt.Sprintf(":%d", httpPort)}
	go func() {
		log.Info().Int("port", httpPort).Msg("Starting Sparkplug ingest HTTP server.")
		if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
			log.Error().Err(err).Msg("Sparkplug ingest HTTP server exited with error.")
		}
	}()
	return nil
}

// beforeMicroserviceStopped announces OFFLINE, disconnects the Host, and shuts
// the HTTP server down.
func beforeMicroserviceStopped(ctx context.Context) error {
	Microservice.Readiness.BeginDrain()
	if HostClient != nil {
		HostClient.Stop()
	}
	if httpServer == nil {
		return nil
	}
	return httpServer.Shutdown(ctx)
}
