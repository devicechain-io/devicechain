// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
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
	if err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, cfg); err != nil {
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

	// Age-based signing-key rotation (ADR-008 follow-up): rotate at startup if the
	// active key is older than the configured max age, retaining the prior key for
	// the configured window so the tokens it signed keep verifying.
	keyMaxAge := time.Duration(Configuration.Auth.SigningKeyMaxAgeDays) * 24 * time.Hour
	keyRetention := time.Duration(Configuration.Auth.SigningKeyRetentionDays) * 24 * time.Hour
	if err := IdentityManager.MaybeRotateOnAge(ctx, keyMaxAge, keyRetention); err != nil {
		return err
	}

	// Serve the platform signing keys so other services can validate tokens.
	registerKeyHandlers()

	// Map of providers injected into the graphql http context.
	providers := map[gqlcore.ContextKey]interface{}{
		graphql.ContextIdentityKey: IdentityManager,
	}

	// user-management validates its own API requests with the local public key
	// and depends on no other service, so the readiness gate opens immediately
	// (ADR-022 decision 3); login/refresh remain reachable without a token (an
	// absent token is allowed through, see the auth handler).
	Microservice.Readiness.MarkReady(IdentityManager.Validator())

	parsed := gql.MustParseSchema(graphql.SchemaContent, &graphql.SchemaResolver{})
	GraphQLManager = gqlcore.NewGraphQLManager(Microservice, core.NewNoOpLifecycleCallbacks(), *parsed,
		providers, Microservice.Readiness)
	if err := GraphQLManager.Initialize(ctx); err != nil {
		return err
	}

	return nil
}

// registerKeyHandlers serves the instance signing keys on the shared http server
// so the other services can validate tokens (ADR-008). /auth/jwks is the JWK Set
// of every retained public key — consumers select the right key by the token's
// kid, which lets a signing-key rotation propagate without restarts.
func registerKeyHandlers() {
	http.HandleFunc("/auth/jwks", func(w http.ResponseWriter, r *http.Request) {
		jwks, err := IdentityManager.JWKS()
		if err != nil {
			http.Error(w, "failed to build JWKS", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwks)
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
