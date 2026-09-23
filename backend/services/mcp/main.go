// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"

	"github.com/devicechain-io/dc-mcp/config"
	"github.com/devicechain-io/dc-mcp/server"
	coreauth "github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/service"
)

var (
	Microservice  *core.Microservice
	Configuration *config.McpConfiguration

	// Svc holds no managers — this service has no database, no broker and no GraphQL
	// plane — so what it assembles is the HTTP surface alone: the probes, and the server
	// that carries them and the MCP routes. Its start and stop are the whole lifecycle.
	Svc *service.Service
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
			Preprocess:  beforeMicroserviceTerminated,
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
	registerHttpRoutes(Configuration.ResourceUrl, Configuration.IssuerUrl, validator,
		server.NewGraphQLClient())
	return initializeService(ctx)
}

// initializeService assembles the Service, which registers the probes on the same mux the
// MCP routes are already on. It is its own function so a test can drive it.
func initializeService(ctx context.Context) error {
	Svc = service.New(Microservice, service.Spec{})
	return Svc.Initialize(ctx)
}

// registerHttpRoutes mounts this service's MCP routes on the microservice's OWN mux
// rather than on http.DefaultServeMux. The mux ends up carrying six patterns, in two
// groups:
//
//   - server.Routes, called here, contributes the MCP endpoint at "/" and the two RFC 9728
//     protected-resource metadata locations — the exact path and its subtree form.
//     Which paths those are is a routing decision that belongs to the server package,
//     which has tests for it.
//   - core/service contributes /healthz, /readyz and /metrics, as it does for every
//     service; initializeService is where that happens.
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
// 🔴 CALLED FROM THE INITIALIZE PHASE, NOT FROM WHERE THE SERVER STARTS. server.Routes
// goes through ServeMux.Handle, which panics on a duplicate pattern, and
// LifecycleComponent does not promise ExecuteStart runs once: a start that fails
// restores the component to Initialized, so a retried start enters the start path
// again, and registering from there turns that retry into a crash.
//
// 🔴 It is a named function taking its inputs as parameters so a test can drive the
// REGISTRATION ITSELF. A test that called server.Routes on its own would be asserting
// against its own copy of the wiring: it would keep passing if this
// went back to http.DefaultServeMux, which is exactly the regression the switchover
// exists to prevent. The uncovered remainder is one line — that the initializer calls
// this — because the initializer needs configuration and a JWKS endpoint.
func registerHttpRoutes(resourceURL, issuerURL string, validator func() *coreauth.Validator,
	gql *server.GraphQLClient) {
	server.Routes(Microservice.Mux(), resourceURL, issuerURL, validator, gql)
}

// afterMicroserviceStarted starts the HTTP server, which core/service builds fresh on each
// start and whose bind error it returns.
func afterMicroserviceStarted(ctx context.Context) error {
	return Svc.Start(ctx)
}

// beforeMicroserviceStopped shuts the HTTP server down, letting in-flight requests finish.
func beforeMicroserviceStopped(ctx context.Context) error {
	return Svc.Stop(ctx)
}

// beforeMicroserviceTerminated completes the lifecycle core/service drives.
func beforeMicroserviceTerminated(ctx context.Context) error {
	return Svc.Terminate(ctx)
}
