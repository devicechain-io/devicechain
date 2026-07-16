// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-ai-inference/config"
	"github.com/devicechain-io/dc-ai-inference/graphql"
	"github.com/devicechain-io/dc-ai-inference/model"
	"github.com/devicechain-io/dc-ai-inference/schema"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
)

var (
	Microservice  *core.Microservice
	Configuration *config.AiInferenceConfiguration

	RdbManager     *rdb.RdbManager
	GraphQLManager *gqlcore.GraphQLManager

	SecretStore secrets.SecretStore
	Api         *model.Api
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

// parseConfiguration parses the typed configuration from raw bytes (unknown keys rejected).
func parseConfiguration() error {
	cfg := &config.AiInferenceConfiguration{}
	if err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, cfg); err != nil {
		return err
	}
	Configuration = cfg
	return nil
}

// buildSecretStore constructs the envelope-encrypted secret store (ADR-059) from the
// instance secrets configuration. It fails closed on an unknown/not-yet-implemented
// backend or KEK provider and on a missing/malformed instance root key, so a service
// that cannot form its KEK does not start (a resolved provider key is required to
// authenticate an inference call; encryption-at-rest is not optional once wired).
// Mirrors outbound-connectors' S3 wiring.
func buildSecretStore() (secrets.SecretStore, error) {
	cfg := Microservice.InstanceConfiguration.Infrastructure.Secrets
	backend := cfg.Backend
	if backend == "" {
		backend = secrets.BackendPostgres
	}
	kekProvider := cfg.KEKProvider
	if kekProvider == "" {
		kekProvider = secrets.InstanceKEKProvider
	}
	if err := (secrets.Config{Backend: backend, KEKProvider: kekProvider}).Validate(); err != nil {
		return nil, err
	}
	if backend != secrets.BackendPostgres || kekProvider != secrets.InstanceKEKProvider {
		return nil, fmt.Errorf("secrets: only backend %q with KEK provider %q is implemented (got backend=%q kekProvider=%q)",
			secrets.BackendPostgres, secrets.InstanceKEKProvider, backend, kekProvider)
	}
	rootKey, err := cfg.DecodedRootKey()
	if err != nil {
		return nil, err
	}
	kek, err := secrets.NewInstanceKeyProvider(rootKey)
	if err != nil {
		return nil, err
	}
	return secrets.NewStore(RdbManager.Database, kek), nil
}

// afterMicroserviceInitialized initializes components after the microservice is up.
// The service is synchronous (no NATS): it persists the provider list and, from slice
// 0c, serves inference requests over GraphQL.
func afterMicroserviceInitialized(ctx context.Context) error {
	if err := parseConfiguration(); err != nil {
		return err
	}

	// Create and initialize the rdb manager (runs the secret-store + provider
	// migrations under the startup advisory lock). It must be initialized before the
	// secret store, which reads from the same database.
	RdbManager = rdb.NewRdbManager(Microservice, core.NewNoOpLifecycleCallbacks(), schema.Migrations,
		Microservice.InstanceConfiguration.Persistence.Rdb, Configuration.RdbConfiguration)
	if err := RdbManager.Initialize(ctx); err != nil {
		return err
	}

	// Build the envelope-encrypted secret store (ADR-059) over the service DB: each
	// provider API key lives here, resolved server-internal at inference time. Fails
	// startup closed on an unbuilt backend/provider or a missing instance root key.
	store, err := buildSecretStore()
	if err != nil {
		return err
	}
	SecretStore = store

	// Build the provider store and inject it as a GraphQL provider so the resolvers
	// resolve it (and its secret store, for hasSecret) from the request context.
	Api = model.NewApi(RdbManager, SecretStore)

	providers := map[gqlcore.ContextKey]interface{}{
		gqlcore.ContextRdbKey: RdbManager,
		gqlcore.ContextApiKey: Api,
	}
	parsed := gqlcore.MustParseSchema(graphql.SchemaContent, &graphql.SchemaResolver{Area: string(Microservice.FunctionalArea)})
	Microservice.StartInstanceAuthGate(ctx)
	GraphQLManager = gqlcore.NewGraphQLManager(Microservice, core.NewNoOpLifecycleCallbacks(),
		parsed, providers, Microservice.Readiness)
	return GraphQLManager.Initialize(ctx)
}

// afterMicroserviceStarted starts components after the microservice is started.
func afterMicroserviceStarted(ctx context.Context) error {
	if err := RdbManager.Start(ctx); err != nil {
		return err
	}
	return GraphQLManager.Start(ctx)
}

// beforeMicroserviceStopped stops components in reverse dependency order.
func beforeMicroserviceStopped(ctx context.Context) error {
	if err := GraphQLManager.Stop(ctx); err != nil {
		return err
	}
	return RdbManager.Stop(ctx)
}

// beforeMicroserviceTerminated terminates components in reverse dependency order.
func beforeMicroserviceTerminated(ctx context.Context) error {
	if err := GraphQLManager.Terminate(ctx); err != nil {
		return err
	}
	return RdbManager.Terminate(ctx)
}
