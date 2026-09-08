// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/friendsofgo/graphiql"
	graphql "github.com/graph-gophers/graphql-go"
	"github.com/rs/zerolog/log"
)

const (
	GRAPHQL_PORT = 8080

	// graphiqlEndpoint is the GraphQL URL the /graphiql explorer page posts to. It
	// is templated into the page's fetch() call, which resolves it against the
	// document URL — so a RELATIVE value is what makes one string correct in every
	// topology the explorer is reached through:
	//
	//	/graphiql             -> /graphql             (port-forward, straight at the pod)
	//	/api/<area>/graphiql  -> /api/<area>/graphql  (through the ingress, which
	//	                                               strips the /api/<area> prefix)
	//	/api/<area>/graphiql  -> /api/<area>/graphql  (the console's vite dev proxy,
	//	                                               which strips the same prefix)
	//
	// No absolute path can do all three: "/graphql" drops the prefix the ingress and
	// the dev proxy both expect, and "/api/<area>/graphql" is not a route this mux
	// registers. This previously read "/<instance>/<tenant>/<area>/graphql", which is
	// served by NONE of them — the page loaded and every query it sent 404'd.
	//
	// The only GraphQL route ExecuteInitialize registers is "/graphql" (it also
	// registers "/graphiql", and RegisterProbes adds "/metrics", "/healthz" and
	// "/readyz"). Keep this resolving onto it.
	graphiqlEndpoint = "graphql"
)

// Manages lifecycle of microservice GraphQL server.
type GraphQLManager struct {
	Microservice     *core.Microservice
	Schema           *graphql.Schema
	Server           *core.HttpServer
	ContextProviders map[ContextKey]interface{}
	// Gate supplies the late-bound JWT validator and the readiness state the
	// /readyz probe reports (ADR-022 decision 3). Pass nil only for a deliberately
	// unauthenticated server (tests); production services pass ms.Readiness.
	Gate *core.ReadinessGate

	// Port is the port ExecuteStart binds. NewGraphQLManager sets GRAPHQL_PORT, which
	// is what every service serves on and what the chart's container port names.
	//
	// It is a field rather than the constant read inline so a test can drive the REAL
	// ExecuteStart rather than reimplementing it — with the constant inline, a restart
	// test had to build the server itself, so it exercised its own wiring, and a
	// registration moved back into ExecuteStart went undetected.
	//
	// 🔴 ITS ZERO VALUE MEANS GRAPHQL_PORT, NOT "ANY FREE PORT", AND THAT IS WHY
	// listenPort EXISTS. A field defaulted only by the constructor is one refactor away
	// from being dropped, and net.Listen reads 0 as "pick anything" — so all eleven
	// services would bind a random port, log a successful start and report healthy,
	// while the chart's probes and its ServiceMonitor went on addressing the container
	// port by name and found nothing there. Every pod goes unready, instance-wide,
	// presenting as a probe fault rather than a port one. The zero value is the value a
	// mistake produces, so it has to be the safe one.
	Port int32

	lifecycle core.LifecycleManager
}

// Create a new graphql manager.
func NewGraphQLManager(ms *core.Microservice, callbacks core.LifecycleCallbacks,
	schema *graphql.Schema, providers map[ContextKey]interface{}, gate *core.ReadinessGate) *GraphQLManager {
	gql := &GraphQLManager{
		Microservice:     ms,
		Schema:           schema,
		ContextProviders: providers,
		Gate:             gate,
		Port:             GRAPHQL_PORT,
	}
	// Create lifecycle manager.
	gqlname := fmt.Sprintf("%s-%s", ms.FunctionalArea, "graphql")
	gql.lifecycle = core.NewLifecycleManager(gqlname, gql, callbacks)
	return gql
}

// Initialize component.
func (gql *GraphQLManager) Initialize(ctx context.Context) error {
	return gql.lifecycle.Initialize(ctx)
}

// Lifecycle callback that runs initialization logic: it registers this server's whole
// route set on the microservice's OWN mux, rather than on http.DefaultServeMux.
//
// 🔴 REGISTERED HERE, IN THE INITIALIZE PHASE, AND NOT WHERE THE SERVER STARTS.
// LifecycleComponent's contract says ExecuteStart "may happen on startup or after
// stop", and every registration below goes through ServeMux.Handle, which PANICS on a
// duplicate pattern — so registering from the start path turns a lifecycle restart into
// a crash. Initialize runs once, which is what makes this the safe half.
//
// It was in ExecuteStart until the mux switchover, and it was survivable there only
// because http.DefaultServeMux panics identically: the hazard is not new, it was
// simply never reachable by any test. TestRestartDoesNotPanic reaches it now.
//
// 🔴 THE SERVICES THAT ADD THEIR OWN ROUTES REGISTER THEM IN THE SAME PHASE, AND THE
// ORDER BETWEEN THEM GENUINELY DOES NOT MATTER. user-management registers some of its
// routes BEFORE it calls GraphQLManager.Initialize and the rest after it; ai-inference
// registers after. All of it happens inside afterMicroserviceInitialized, so everything
// lands on the one mux this manager serves before ExecuteStart binds a socket.
//
// What makes the order irrelevant is that Microservice.Mux() creates the mux on first
// use rather than in a constructor, so a service registering first does not race an
// uninitialized field — and ServeMux.Handle is itself order-independent.
func (gql *GraphQLManager) ExecuteInitialize(context.Context) error {
	mux := gql.Microservice.Mux()

	// Add handler for queries. A WebSocket upgrade on the same /graphql path is
	// routed to the graphql-transport-ws subscription handler (ADR-037); a plain
	// POST goes to the HTTP relay handler. Sharing one path lets a client derive
	// the ws:// URL from the http:// one, matching GraphQL client conventions.
	mux.Handle("/graphql", graphqlDispatcher(
		NewHttpHandler(gql.Schema, gql.ContextProviders, gql.Gate),
		NewSubscriptionHandler(gql.Schema, gql.ContextProviders, gql.Gate),
	))

	// The /graphiql explorer is developer tooling — register it only when dev tools
	// are enabled (secure by default, ADR-029). It pairs with schema introspection,
	// which MustParseSchema gates on the same flag; a prod deploy exposes neither.
	if DevToolsEnabled() {
		graphiqlHandler, err := graphiql.NewGraphiqlHandler(graphiqlEndpoint)
		if err != nil {
			return err
		}
		mux.Handle("/graphiql", graphiqlHandler)
	}

	// /metrics, /healthz and /readyz, which every DeviceChain HTTP server serves and
	// the chart addresses by name.
	gql.Microservice.RegisterProbes(gql.Gate)
	return nil
}

// Start component.
func (gql *GraphQLManager) Start(ctx context.Context) error {
	return gql.lifecycle.Start(ctx)
}

// Lifecycle callback that runs startup logic: it binds and serves the mux
// ExecuteInitialize populated.
//
// 🔴 A FRESH SERVER PER START, AND IT REGISTERS NOTHING. Both halves matter, and they
// pull in opposite directions:
//
//   - It registers nothing because ServeMux.Handle panics on a duplicate pattern and
//     this may run again after a stop. The routes belong to ExecuteInitialize.
//   - It builds a new server because an http.Server cannot be restarted: Shutdown
//     latches its shuttingDown flag permanently, so reusing one would bind and then
//     serve nothing.
//
// The bind is synchronous and its error is returned, so a port collision refuses
// startup instead of being logged from inside a goroutine while the service reports
// success and runs with no HTTP surface at all.
func (gql *GraphQLManager) ExecuteStart(context.Context) error {
	gql.Server = gql.Microservice.NewHttpServer(gql.listenPort())
	if err := gql.Server.Start(); err != nil {
		return err
	}
	log.Info().Str("addr", gql.Server.Addr()).Msg("Started GraphQL server.")
	return nil
}

// ephemeralPort asks the operating system for an unused port.
//
// It is unexported and NEGATIVE on purpose. A test needs to drive the real ExecuteStart
// without racing whatever holds 8080, and the obvious way to say that — Port: 0 — is
// exactly the value listenPort has to read as "the default was lost". A negative
// sentinel cannot be produced by a dropped assignment, and cannot be named from outside
// this package at all.
const ephemeralPort int32 = -1

// listenPort resolves Port to the port ExecuteStart binds. See Port for why 0 is the
// production default rather than an ephemeral one.
func (gql *GraphQLManager) listenPort() int32 {
	switch gql.Port {
	case 0:
		return GRAPHQL_PORT
	case ephemeralPort:
		return 0 // net.Listen's own "any free port"
	default:
		return gql.Port
	}
}

// Stop component.
func (gql *GraphQLManager) Stop(ctx context.Context) error {
	return gql.lifecycle.Stop(ctx)
}

// Lifecycle callback that runs shutdown logic.
func (gql *GraphQLManager) ExecuteStop(context.Context) error {
	// Nil until ExecuteStart has run.
	//
	// ⚠️ The lifecycle does not reach here in that state today — ShutDownNow refuses a
	// stop from phaseStarting, which is what a refused startup leaves behind — so this
	// guards against a CALLER, not against the lifecycle. GraphQLManager is exported and
	// every service drives Stop by hand; one that stops without having started would
	// otherwise get a nil dereference where a no-op belongs.
	if gql.Server == nil {
		return nil
	}
	if err := gql.Server.Shutdown(context.Background()); err != nil {
		return err
	}
	log.Info().Int32("port", GRAPHQL_PORT).Msg("GraphQL server shut down successfully.")
	return nil
}

// Terminate component.
func (gql *GraphQLManager) Terminate(ctx context.Context) error {
	return gql.lifecycle.Terminate(ctx)
}

// Lifecycle callback that runs termination logic.
func (gql *GraphQLManager) ExecuteTerminate(context.Context) error {
	return nil
}
