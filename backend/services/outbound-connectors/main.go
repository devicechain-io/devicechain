// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"time"

	epconfig "github.com/devicechain-io/dc-event-processing/config"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/devicechain-io/dc-outbound-connectors/config"
	"github.com/devicechain-io/dc-outbound-connectors/governance"
	"github.com/devicechain-io/dc-outbound-connectors/graphql"
	"github.com/devicechain-io/dc-outbound-connectors/model"
	"github.com/devicechain-io/dc-outbound-connectors/processor"
	"github.com/devicechain-io/dc-outbound-connectors/schema"
	"github.com/rs/zerolog/log"
)

// deadLetterSuffix is the terminal dead-letter subject suffix (ADR-060 SD-2): a connector dispatch
// that exhausts the redelivery cap or is terminally undeliverable is written to
// "{instance}.{tenant}.connector-dispatch.dead" and never retried forever.
const deadLetterSuffix = epconfig.SUBJECT_CONNECTOR_DISPATCH + ".dead"

var (
	Microservice  *core.Microservice
	Configuration *config.OutboundConnectorsConfiguration

	RdbManager     *rdb.RdbManager
	GraphQLManager *gqlcore.GraphQLManager
	NatsManager    *messaging.NatsManager

	SecretStore secrets.SecretStore
	RateLimiter *core.TenantRateLimiter
	Consumer    *processor.DispatchConsumer
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
	cfg := &config.OutboundConnectorsConfiguration{}
	if err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, cfg); err != nil {
		return err
	}
	Configuration = cfg
	return nil
}

// buildSecretStore constructs the envelope-encrypted secret store (ADR-059) from the instance
// secrets configuration. It fails closed on an unknown/not-yet-implemented backend or KEK provider
// and on a missing/malformed instance root key, so a service that cannot form its KEK does not start
// (a resolved credential is required to authenticate an outbound call; encryption-at-rest is not
// optional once wired). Mirrors notification-management's S3 wiring.
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

// createNatsComponents wires the durable connector-dispatch consumer and its dead-letter writer.
func createNatsComponents(nmgr *messaging.NatsManager) error {
	// Dead-letter writer: a terminal sink for a dispatch that exhausts the redelivery cap or is
	// terminally undeliverable (SD-2). Creating the writer auto-provisions its stream.
	dead, err := nmgr.NewWriter(deadLetterSuffix)
	if err != nil {
		return err
	}

	// Dispatch reader: a durable pull consumer over the cross-tenant connector-dispatch wildcard.
	// DeliverNew starts the durable at the stream tail on first creation, so enabling this service on
	// a running fleet does NOT replay the backlog of dispatch requests event-processing has published
	// since the C2b sink went live — replaying that history would flood stale outbound calls. Once
	// the durable exists its ack cursor persists, so a restart resumes from the last ack.
	reader, err := nmgr.NewReader(epconfig.SUBJECT_CONNECTOR_DISPATCH, messaging.ReaderWithDeliverNew())
	if err != nil {
		return err
	}

	resolver := processor.NewSecretResolver(SecretStore)
	executor := processor.NewExecutor(resolver, time.Duration(Configuration.SendTimeoutMs)*time.Millisecond)
	Consumer = processor.NewDispatchConsumer(Microservice, reader, dead, executor,
		RateLimiter, time.Duration(Configuration.EgressWaitBudgetMs)*time.Millisecond,
		Configuration.MaxConcurrentSends, Configuration.DispatchBacklog)
	return nil
}

// buildEgressLimiter constructs the per-tenant OUTBOUND egress limiter (ADR-060 SD-3). When the
// service secret and user-management endpoint are configured, per-tenant overrides are fetched from
// user-management over a service token and cached, failing open to the platform default; otherwise
// every tenant is metered at the platform default. Either way the ceiling is a real limit — never
// unlimited — since ApplyDefaults/Validate guarantee positive platform defaults. Mirrors
// event-sources' ingest buildRateLimiter (the ingest and outbound dimensions are independent).
func buildEgressLimiter() *core.TenantRateLimiter {
	def := governance.Limits{
		MessagesPerSecond: Configuration.OutboundMessagesPerSecond,
		Burst:             Configuration.OutboundBurst,
	}
	infra := Microservice.InstanceConfiguration.Infrastructure
	if infra.ServiceAuth.Secret == "" || infra.UserManagement.Hostname == "" || infra.UserManagement.Port == 0 {
		log.Warn().Msg("Service secret or user-management endpoint not configured — per-tenant outbound overrides disabled; metering every tenant at the platform default.")
		return core.NewTenantRateLimiter(func(string) (float64, int) {
			return def.MessagesPerSecond, def.Burst
		})
	}
	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "outbound-connectors", []string{string(auth.TenantRead)})
	umURL := fmt.Sprintf("http://%s:%d/graphql", infra.UserManagement.Hostname, infra.UserManagement.Port)
	resolver := governance.NewTenantLimitResolver(governance.NewServiceFetcher(client, umURL, def), def)
	log.Info().Str("userManagement", umURL).Msg("Per-tenant outbound overrides enabled (fail-open to platform default).")
	return core.NewTenantRateLimiter(resolver.Resolve)
}

// afterMicroserviceInitialized initializes components after the microservice is up.
func afterMicroserviceInitialized(ctx context.Context) error {
	if err := parseConfiguration(); err != nil {
		return err
	}

	// Create and initialize the rdb manager (runs the secret-store migration under the startup
	// advisory lock). It must be initialized before the secret store and the NATS manager.
	RdbManager = rdb.NewRdbManager(Microservice, core.NewNoOpLifecycleCallbacks(), schema.Migrations,
		Microservice.InstanceConfiguration.Persistence.Rdb, Configuration.RdbConfiguration)
	if err := RdbManager.Initialize(ctx); err != nil {
		return err
	}

	// Build the envelope-encrypted secret store (ADR-059) over the service DB: each outbound
	// credential lives here, resolved server-internal at dispatch. Fails startup closed on an
	// unbuilt backend/provider or a missing instance root key.
	store, err := buildSecretStore()
	if err != nil {
		return err
	}
	SecretStore = store

	// Build the per-tenant outbound egress limiter (ADR-060 SD-3) before the NATS manager, since
	// createNatsComponents binds it into the consumer. Fail-open to the platform default when
	// per-tenant overrides are not wired (never unlimited).
	RateLimiter = buildEgressLimiter()

	// Create and initialize the nats manager (which invokes createNatsComponents to build the
	// consumer). The secret store must already exist so the executor's resolver can bind it.
	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), createNatsComponents)
	if err := NatsManager.Initialize(ctx); err != nil {
		return err
	}

	// The GraphQL surface carries the service identity plus the per-tenant, versioned
	// Connector CRUD (ADR-060 slice C4). The api is injected as a provider so the resolvers
	// resolve it (and its secret store, for hasSecret) from the request context. Auth
	// degrades instead of failing startup (ADR-022 decision 3).
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
	if err := GraphQLManager.Start(ctx); err != nil {
		return err
	}
	if err := NatsManager.Start(ctx); err != nil {
		return err
	}
	// Start the consumer last (after its reader is live).
	return Consumer.Start(ctx)
}

// beforeMicroserviceStopped stops components in reverse dependency order.
func beforeMicroserviceStopped(ctx context.Context) error {
	// Stop the consumer first (before its reader is torn down with the NATS manager), symmetric with
	// start, so no worker sends on a torn-down reader/writer.
	if Consumer != nil {
		if err := Consumer.Stop(ctx); err != nil {
			return err
		}
	}
	if err := NatsManager.Stop(ctx); err != nil {
		return err
	}
	if err := GraphQLManager.Stop(ctx); err != nil {
		return err
	}
	return RdbManager.Stop(ctx)
}

// beforeMicroserviceTerminated terminates components in reverse dependency order.
func beforeMicroserviceTerminated(ctx context.Context) error {
	if err := NatsManager.Terminate(ctx); err != nil {
		return err
	}
	if err := GraphQLManager.Terminate(ctx); err != nil {
		return err
	}
	return RdbManager.Terminate(ctx)
}
