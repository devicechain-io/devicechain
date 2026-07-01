// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"net/http"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/friendsofgo/graphiql"
	graphql "github.com/graph-gophers/graphql-go"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"
)

const (
	GRAPHQL_PORT = 8080
)

// Manages lifecycle of microservice GraphQL server.
type GraphQLManager struct {
	Microservice     *core.Microservice
	Schema           graphql.Schema
	Server           *http.Server
	ContextProviders map[ContextKey]interface{}
	// Gate supplies the late-bound JWT validator and the readiness state the
	// /readyz probe reports (ADR-022 decision 3). Pass nil only for a deliberately
	// unauthenticated server (tests); production services pass ms.Readiness.
	Gate *core.ReadinessGate

	lifecycle core.LifecycleManager
}

// Create a new graphql manager.
func NewGraphQLManager(ms *core.Microservice, callbacks core.LifecycleCallbacks,
	schema graphql.Schema, providers map[ContextKey]interface{}, gate *core.ReadinessGate) *GraphQLManager {
	gql := &GraphQLManager{
		Microservice:     ms,
		Schema:           schema,
		ContextProviders: providers,
		Gate:             gate,
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

// Lifecycle callback that runs initialization logic.
func (gql *GraphQLManager) ExecuteInitialize(context.Context) error {
	return nil
}

// Start component.
func (gql *GraphQLManager) Start(ctx context.Context) error {
	return gql.lifecycle.Start(ctx)
}

// Lifecycle callback that runs startup logic.
func (gql *GraphQLManager) ExecuteStart(context.Context) error {
	graphiqlHandler, err := graphiql.NewGraphiqlHandler(fmt.Sprintf("/%s/%s/%s/%s",
		gql.Microservice.InstanceId, gql.Microservice.TenantId, gql.Microservice.FunctionalArea,
		"graphql"))
	if err != nil {
		panic(err)
	}

	// Add handler for queries. A WebSocket upgrade on the same /graphql path is
	// routed to the graphql-transport-ws subscription handler (ADR-037); a plain
	// POST goes to the HTTP relay handler. Sharing one path lets a client derive
	// the ws:// URL from the http:// one, matching GraphQL client conventions.
	http.Handle("/graphql", graphqlDispatcher(
		NewHttpHandler(&gql.Schema, gql.ContextProviders, gql.Gate),
		NewSubscriptionHandler(&gql.Schema, gql.ContextProviders, gql.Gate),
	))
	http.Handle("/graphiql", graphiqlHandler)

	// Add handler for metrics
	http.Handle("/metrics", promhttp.Handler())

	// Kubernetes probes (ADR-022 decision 3): liveness is always healthy while
	// the process runs, but readiness reports 503 until the auth gate opens, so a
	// degraded pod is pulled from Service endpoints instead of serving traffic. On
	// SIGTERM the gate flips to draining, so readiness also reports 503 for the
	// drain window before the server stops — pulling the pod from endpoints while
	// it can still finish in-flight requests (zero-downtime rollouts, §10.2).
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	http.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if gql.Gate != nil && gql.Gate.Ready() && !gql.Gate.Draining() {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	})

	// Start server in a background thread in order to continue server startup.
	go func() {
		gql.Server = &http.Server{Addr: fmt.Sprintf(":%d", GRAPHQL_PORT)}
		log.Info().Int32("port", GRAPHQL_PORT).Msg("Starting GraphQL server.")
		if err := gql.Server.ListenAndServe(); err != http.ErrServerClosed {
			log.Error().Err(err).Msg("Error starting GraphQL server.")
		}
	}()

	return nil
}

// Stop component.
func (gql *GraphQLManager) Stop(ctx context.Context) error {
	return gql.lifecycle.Stop(ctx)
}

// Lifecycle callback that runs shutdown logic.
func (gql *GraphQLManager) ExecuteStop(context.Context) error {
	err := gql.Server.Shutdown(context.Background())
	if err != nil {
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
