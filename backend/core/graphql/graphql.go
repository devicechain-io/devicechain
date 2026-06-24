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

	lifecycle core.LifecycleManager
}

// Create a new rdb manager.
func NewGraphQLManager(ms *core.Microservice, callbacks core.LifecycleCallbacks,
	schema graphql.Schema, providers map[ContextKey]interface{}) *GraphQLManager {
	gql := &GraphQLManager{
		Microservice:     ms,
		Schema:           schema,
		ContextProviders: providers,
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

	// Add handler for queries
	http.Handle("/graphql", NewHttpHandler(&gql.Schema, gql.ContextProviders))
	http.Handle("/graphiql", graphiqlHandler)

	// Add handler for metrics
	http.Handle("/metrics", promhttp.Handler())

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
