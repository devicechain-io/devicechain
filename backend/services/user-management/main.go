// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	gql "github.com/graph-gophers/graphql-go"

	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/config"
	"github.com/devicechain-io/dc-user-management/graphql"
	"github.com/devicechain-io/dc-user-management/identity"
	"github.com/devicechain-io/dc-user-management/schema"
)

var (
	Microservice  *core.Microservice
	Configuration *config.UserManagementConfiguration

	RdbManager      *rdb.RdbManager
	NatsManager     *messaging.NatsManager
	GraphQLManager  *gqlcore.GraphQLManager
	IdentityManager *identity.Manager
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

// Parses the configuration from raw bytes.
func parseConfiguration() error {
	cfg := &config.UserManagementConfiguration{}
	if err := json.Unmarshal(Microservice.MicroserviceConfigurationRaw, cfg); err != nil {
		return err
	}
	Configuration = cfg
	return nil
}

// Called after microservice has been initialized.
func afterMicroserviceInitialized(ctx context.Context) error {
	// Parse configuration.
	if err := parseConfiguration(); err != nil {
		return err
	}

	// Create and initialize rdb manager (runs migrations, registers tenant scope).
	RdbManager = rdb.NewRdbManager(Microservice, core.NewNoOpLifecycleCallbacks(), schema.Migrations,
		Microservice.InstanceConfiguration.Persistence.Rdb, Configuration.RdbConfiguration)
	if err := RdbManager.Initialize(ctx); err != nil {
		return err
	}

	// Create and initialize nats manager (used for the refresh-token KV store).
	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(),
		func(*messaging.NatsManager) error { return nil })
	if err := NatsManager.Initialize(ctx); err != nil {
		return err
	}

	// Build the identity manager: load/create the signing key, wire the refresh
	// store, and seed the bootstrap admin (ADR-008).
	accessTTL := time.Duration(Configuration.Auth.AccessTokenTtlSeconds) * time.Second
	refreshTTL := time.Duration(Configuration.Auth.RefreshTokenTtlSeconds) * time.Second
	refreshKV, err := NatsManager.KeyValueStore(identity.RefreshBucket, refreshTTL)
	if err != nil {
		return err
	}
	IdentityManager = identity.NewManager(Microservice, RdbManager, accessTTL, refreshTTL, identity.BootstrapConfig{
		Tenant:   Configuration.Auth.BootstrapTenant,
		Username: Configuration.Auth.BootstrapUsername,
		Password: Configuration.Auth.BootstrapPassword,
	})
	if err := IdentityManager.Initialize(ctx, refreshKV); err != nil {
		return err
	}

	// Serve the platform public key for the other services to validate tokens.
	registerPublicKeyHandler()

	// Map of providers injected into the graphql http context.
	providers := map[gqlcore.ContextKey]interface{}{
		graphql.ContextIdentityKey: IdentityManager,
	}

	// Create and initialize graphql manager. user-management validates its own
	// API requests with the local public key; login/refresh remain reachable
	// without a token (an absent token is allowed through, see the auth handler).
	parsed := gql.MustParseSchema(graphql.SchemaContent, &graphql.SchemaResolver{})
	GraphQLManager = gqlcore.NewGraphQLManager(Microservice, core.NewNoOpLifecycleCallbacks(), *parsed,
		providers, IdentityManager.Validator())
	if err := GraphQLManager.Initialize(ctx); err != nil {
		return err
	}

	return nil
}

// registerPublicKeyHandler serves the instance JWT public key (PKIX PEM) on the
// shared http server so the other services can build their validators (ADR-008).
func registerPublicKeyHandler() {
	http.HandleFunc("/auth/public-key", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-pem-file")
		_, _ = w.Write(IdentityManager.PublicKeyPEM())
	})
}

// Called after microservice has been started.
func afterMicroserviceStarted(ctx context.Context) error {
	if err := RdbManager.Start(ctx); err != nil {
		return err
	}
	if err := NatsManager.Start(ctx); err != nil {
		return err
	}
	return GraphQLManager.Start(ctx)
}

// Called before microservice has been stopped.
func beforeMicroserviceStopped(ctx context.Context) error {
	if err := GraphQLManager.Stop(ctx); err != nil {
		return err
	}
	if err := NatsManager.Stop(ctx); err != nil {
		return err
	}
	return RdbManager.Stop(ctx)
}

// Called before microservice has been terminated.
func beforeMicroserviceTerminated(ctx context.Context) error {
	if err := GraphQLManager.Terminate(ctx); err != nil {
		return err
	}
	if err := NatsManager.Terminate(ctx); err != nil {
		return err
	}
	return RdbManager.Terminate(ctx)
}
