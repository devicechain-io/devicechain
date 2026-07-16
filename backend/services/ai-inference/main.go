// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-ai-inference/config"
	"github.com/devicechain-io/dc-ai-inference/graphql"
	"github.com/devicechain-io/dc-ai-inference/inference"
	"github.com/devicechain-io/dc-ai-inference/model"
	"github.com/devicechain-io/dc-ai-inference/schema"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/rs/zerolog/log"
)

var (
	Microservice  *core.Microservice
	Configuration *config.AiInferenceConfiguration

	RdbManager     *rdb.RdbManager
	GraphQLManager *gqlcore.GraphQLManager

	SecretStore       secrets.SecretStore
	Api               *model.Api
	InferenceResolver *inference.Resolver
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

// buildInferenceResolver constructs the fail-closed inference resolver over the
// provider store, the configured per-call bounds, and the external-routing consent
// checker. The consent checker reads the per-tenant ai_external_enabled flag from
// user-management over a service token (least-privilege tenant:read, mirroring the
// egress-limit fetcher). When the shared service secret or the user-management
// endpoint is absent, a DENY-ALL checker is injected so the external NL-authoring path
// is cleanly unavailable rather than silently permissive (the ai:admin operator smoke
// test does not consult consent, so it still works). ADR-056.
func buildInferenceResolver() *inference.Resolver {
	bounds := inference.Bounds{
		MaxOutputTokens: Configuration.MaxOutputTokens,
		MaxPromptBytes:  Configuration.MaxPromptBytes,
		Timeout:         time.Duration(Configuration.InferenceTimeoutMs) * time.Millisecond,
	}

	infra := Microservice.InstanceConfiguration.Infrastructure
	var consent inference.ConsentChecker
	if infra.ServiceAuth.Secret == "" || infra.UserManagement.Hostname == "" || infra.UserManagement.Port == 0 {
		log.Warn().Msg("Service secret or user-management endpoint not configured — external-routing consent cannot be verified; the NL-authoring inference path is unavailable (fail-closed, ADR-056). The ai:admin provider smoke test is unaffected.")
		consent = inference.NewDeniedConsentChecker("service-to-service auth is not configured")
	} else {
		client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "ai-inference", []string{string(auth.TenantRead)})
		umURL := fmt.Sprintf("http://%s:%d/graphql", infra.UserManagement.Hostname, infra.UserManagement.Port)
		consent = inference.NewServiceConsentChecker(client, umURL)
		log.Info().Str("userManagement", umURL).Msg("External-routing consent enforced from user-management tenantGovernance (fail-closed, ADR-056).")
	}

	return inference.NewResolver(Api, consent, bounds, nil)
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

	// Build the fail-closed inference resolver: the active-provider read + the
	// external-routing consent gate + key resolution + provider construction. It is the
	// ONE place external routing is authorized (ADR-056). The root resolver holds it.
	InferenceResolver = buildInferenceResolver()

	providers := map[gqlcore.ContextKey]interface{}{
		gqlcore.ContextRdbKey: RdbManager,
		gqlcore.ContextApiKey: Api,
	}
	parsed := gqlcore.MustParseSchema(graphql.SchemaContent, &graphql.SchemaResolver{
		Area:      string(Microservice.FunctionalArea),
		Inference: InferenceResolver,
	})
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
