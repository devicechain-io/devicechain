// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"

	"github.com/devicechain-io/dc-mcp/config"
	"github.com/devicechain-io/dc-mcp/server"
	coreauth "github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/rs/zerolog/log"
)

const httpPort = 8080

var (
	Microservice  *core.Microservice
	Configuration *config.McpConfiguration
	httpServer    *core.HttpServer
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

	// This service's whole HTTP surface, registered in the INITIALIZE phase. See
	// registerHttpRoutes for why it is here and not where the server starts.
	registerHttpRoutes(Configuration.ResourceUrl, Configuration.IssuerUrl, validator)
	return nil
}

// registerHttpRoutes mounts this service's HTTP surface on the microservice's OWN mux
// rather than on http.DefaultServeMux. Six patterns, in two groups:
//
//   - server.Routes contributes the MCP endpoint at "/" and the two RFC 9728
//     protected-resource metadata locations — the exact path and its subtree form.
//     Which paths those are is a routing decision that belongs to the server package,
//     which has tests for it.
//   - RegisterProbes contributes /healthz, /readyz and /metrics.
//
// 🔴 THAT ROUTE SET IS A PROTOCOL CONTRACT, NOT A CONVENIENCE. A client discovers this
// server by spec: it POSTs the resource identifier, reads the WWW-Authenticate
// challenge, and fetches the metadata URL the challenge names. Dropping any one of the
// three server.Routes patterns does not degrade the service, it breaks discovery.
//
// 🔴 AND BECAUSE THE MCP ENDPOINT IS MOUNTED AT "/", A ROUTE THAT STOPS BEING REGISTERED
// HERE DOES NOT 404 — IT FALLS THROUGH TO THE CATCH-ALL AND IS ANSWERED WITH A 401
// BEARER CHALLENGE. For the metadata document that means a public discovery request is
// answered with an authentication demand, sending the client back round the same loop.
// For a probe it is worse still: /readyz can then never return 200, so every pod stays
// unready forever and the area leaves its Service endpoints — with a symptom that points
// at authentication rather than at routing.
//
// 🔴 CALLED FROM THE INITIALIZE PHASE, NOT FROM WHERE THE SERVER STARTS. Both
// registrars go through ServeMux.Handle, which panics on a duplicate pattern, and
// LifecycleComponent does not promise ExecuteStart runs once: a start that fails
// restores the component to Initialized, so a retried start enters the start path
// again, and registering from there turns that retry into a crash.
//
// 🔴 It is a named function taking its inputs as parameters so a test can drive the
// REGISTRATION ITSELF. A test that called server.Routes and RegisterProbes on its own
// would be asserting against its own copy of the wiring: it would keep passing if this
// went back to http.DefaultServeMux, which is exactly the regression the switchover
// exists to prevent. The uncovered remainder is one line — that the initializer calls
// this — because the initializer needs configuration and a JWKS endpoint.
func registerHttpRoutes(resourceURL, issuerURL string, validator func() *coreauth.Validator) {
	server.Routes(Microservice.Mux(), resourceURL, issuerURL, validator)
	Microservice.RegisterProbes(Microservice.Readiness)
}

// afterMicroserviceStarted starts the HTTP server.
func afterMicroserviceStarted(_ context.Context) error {
	return startHttpServer(httpPort)
}

// startHttpServer builds this service's HTTP server over the microservice's own mux
// and starts it, returning any bind failure.
//
// 🔴 A FRESH SERVER PER START, AND IT REGISTERS NOTHING. Both halves matter, and they
// pull in opposite directions:
//
//   - It registers nothing because ServeMux.Handle panics on a duplicate pattern, and
//     this is entered again by any start retried after a failed one. The routes are
//     registered once, in the initialize phase.
//   - It builds a new server because an http.Server cannot be restarted: Shutdown
//     latches its shuttingDown flag permanently, so reusing one would bind and then
//     serve nothing.
//
// The port is a parameter so a test can ask for an ephemeral one rather than racing
// whatever holds 8080.
func startHttpServer(port int32) error {
	httpServer = Microservice.NewHttpServer(port)
	if err := httpServer.Start(); err != nil {
		return err
	}
	log.Info().Str("addr", httpServer.Addr()).Msg("Started MCP server.")
	return nil
}

// beforeMicroserviceStopped gracefully shuts the HTTP server down.
func beforeMicroserviceStopped(ctx context.Context) error {
	if httpServer == nil {
		return nil
	}
	return httpServer.Shutdown(ctx)
}
