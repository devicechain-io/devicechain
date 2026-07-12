// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/devicechain-io/dc-mcp/config"
	"github.com/devicechain-io/dc-mcp/server"
	coreauth "github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"
)

const httpPort = 8080

var (
	Microservice  *core.Microservice
	Configuration *config.McpConfiguration
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
	cfg := &config.McpConfiguration{}
	if err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, cfg); err != nil {
		return err
	}
	Configuration = cfg
	return nil
}

// afterMicroserviceInitialized parses config, starts the JWKS auth gate, and
// registers the HTTP surface (the MCP endpoint + RFC 9728 metadata + probes).
func afterMicroserviceInitialized(ctx context.Context) error {
	if err := parseConfiguration(); err != nil {
		return err
	}

	// Fetch the token validator from user-management's JWKS in the background and
	// gate readiness on it (ADR-022 decision 3) — the MCP server validates every
	// bearer with it, so it degrades (503) rather than failing startup if
	// user-management is briefly unreachable.
	Microservice.StartInstanceAuthGate(ctx)

	// The RS token verifier reads the live validator through the readiness gate
	// (nil until the gate opens → fail closed at the middleware).
	validator := func() *coreauth.Validator { return Microservice.Readiness.Validator() }
	mcpHandler, metadataHandler := server.New(Configuration.ResourceUrl, Configuration.IssuerUrl, validator)

	http.Handle("/mcp", mcpHandler)
	http.Handle(server.ProtectedResourceMetadataPath, metadataHandler)
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

// afterMicroserviceStarted starts the HTTP server.
func afterMicroserviceStarted(_ context.Context) error {
	httpServer = &http.Server{Addr: fmt.Sprintf(":%d", httpPort)}
	go func() {
		log.Info().Int("port", httpPort).Msg("Starting MCP server.")
		if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
			log.Error().Err(err).Msg("MCP server exited with error.")
		}
	}()
	return nil
}

// beforeMicroserviceStopped gracefully shuts the HTTP server down.
func beforeMicroserviceStopped(ctx context.Context) error {
	if httpServer == nil {
		return nil
	}
	return httpServer.Shutdown(ctx)
}
