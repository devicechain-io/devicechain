// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-microservice/governance"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/devicechain-io/dc-outbound-connectors/config"
	"github.com/devicechain-io/dc-outbound-connectors/graphql"
	"github.com/devicechain-io/dc-outbound-connectors/model"
	"github.com/devicechain-io/dc-outbound-connectors/processor"
	"github.com/devicechain-io/dc-outbound-connectors/schema"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

var (
	Microservice  *core.Microservice
	Configuration *config.OutboundConnectorsConfiguration

	// Svc owns the three managers below and the order of all four of their lifecycle
	// phases. They stay named here because the rest of this service refers to them
	// directly; Svc is what decides when each one runs.
	Svc *service.Service

	RdbManager     *rdb.RdbManager
	GraphQLManager *gqlcore.GraphQLManager
	NatsManager    *messaging.NatsManager

	SecretStore secrets.SecretStore
	RateLimiter *core.TenantRateLimiter
	Consumer    *processor.DispatchConsumer
	// DispatchMetrics is built ONCE, in the initialize phase, and shared by every
	// DispatchConsumer the NATS manager's oncreate callback builds. See buildMetrics.
	DispatchMetrics *processor.DispatchMetrics
	Api             *model.Api
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

// buildSecretStore constructs the envelope-encrypted secret store (ADR-059) from the
// instance secrets configuration. secrets.New is the single wiring point: it fails
// closed on an unknown or declared-but-unbuilt backend/KEK provider and on a missing or
// malformed instance root key, so a service that cannot form its KEK does not start (a
// resolved credential is required to authenticate an outbound call).
func buildSecretStore(ctx context.Context) (secrets.SecretStore, error) {
	cfg := Microservice.InstanceConfiguration.Infrastructure.Secrets
	// DecodedRootKey is passed as a source rather than called here so New keeps this
	// wiring's original check order: an external backend that owns its own keys is
	// refused for not being built, not for lacking an instance root key.
	return secrets.New(
		ctx,
		secrets.Config{Backend: cfg.Backend, KEKProvider: cfg.KEKProvider},
		RdbManager.Database,
		cfg.DecodedRootKey,
	)
}

// buildMetrics creates this service's Prometheus instruments exactly once.
//
// 🔴 IT IS CALLED FROM THE INITIALIZE PHASE, NOT FROM WHERE THE CONSUMER IS BUILT.
// The consumer is built in createNatsComponents, which the NATS manager invokes on
// EVERY start, and a collector belongs to the PROCESS whereas everything that callback
// builds belongs to the CONNECTION — registering one twice on this microservice's
// registry panics. Initialize is where the process's own singletons are made, which is
// what makes this the safe half; messaging.NewNatsManager carries the reasoning.
func buildMetrics() {
	DispatchMetrics = processor.NewDispatchMetrics(Microservice)
}

// createNatsComponents wires the durable connector-dispatch consumer and its dead-letter writer.
func createNatsComponents(nmgr *messaging.NatsManager) error {
	// Dead-letter writer: a terminal sink for a dispatch that exhausts the redelivery cap or is
	// terminally undeliverable (SD-2). Creating the writer auto-provisions its stream.
	dead, err := nmgr.NewWriter(streams.ConnectorDispatchDead)
	if err != nil {
		return err
	}

	// The ADR-024 index writer. The subject above is this service's own terminal sink and has no
	// reader, no store and no query surface, so a dispatch written there was invisible to the one
	// list an operator asks "what has the platform given up on" — and then aged out with the stream.
	// Every other arm in the platform lands in that list; this one now does too. Created here, beside
	// the stream it indexes, so a deployment that cannot create it fails at startup rather than at
	// the first give-up — the one moment the arm has to work.
	deadIndex, err := nmgr.NewWriter(streams.DeadLetters)
	if err != nil {
		return err
	}

	// Dispatch reader: a durable pull consumer over the cross-tenant connector-dispatch wildcard.
	// DeliverNew starts the durable at the stream tail on first creation, so enabling this service on
	// a running fleet does NOT replay the backlog of dispatch requests event-processing has published
	// since the C2b sink went live — replaying that history would flood stale outbound calls. Once
	// the durable exists its ack cursor persists, so a restart resumes from the last ack.
	reader, err := nmgr.NewReader(streams.ConnectorDispatch, messaging.ReaderWithDeliverNew())
	if err != nil {
		return err
	}

	// ADR-077 lifecycle gate. This service EMITS rather than retains, so it carries the gate for the
	// same reason the ingest fronts do, with more at stake: an ingest message admitted a moment too
	// late is a row the sweep reclaims on its next pass, an outbound dispatch admitted a moment too
	// late is a POST that has already landed on somebody else's server, where no purge of ours can
	// reach it.
	//
	// It is NOT the only service that can do that. notification-management pages a tenant's humans
	// over SMTP and webhooks, and its escalation scheduler does so from its OWN rows on a timer,
	// needing no inbound traffic at all; it carries the same gate at PolicyNotifier. Naming it here
	// rather than claiming this path is unique is deliberate — that claim was written into several
	// files and was wrong, and an "only path" comment is exactly what stops the next person looking
	// for the second one. The consumer's own gate comment carries the full argument.
	//
	// Built here rather than beside the rate limiter because this is where the consumer that uses it
	// is constructed. It mints its own service token (scoped to tenant:read alone) and its own
	// resolver cache — the same MECHANISM the egress limiter uses, not the same instance, so the two
	// do not share a cache and neither one's misses warm the other.
	infra := Microservice.InstanceConfiguration.Infrastructure
	tenantDeleted := governance.NewTenantLifecycleGate(infra.UserManagement, infra.ServiceAuth.Secret, "outbound-connectors")

	resolver := processor.NewSecretResolver(SecretStore)
	// The tenant-egress boundary, carrying whatever destinations the operator has
	// explicitly allowed. A malformed CIDR fails startup rather than being skipped: a
	// silently-dropped allowance would look configured and behave as though it were not.
	egressGuard, err := egress.FromConfig(Microservice.InstanceConfiguration.Infrastructure.Egress)
	if err != nil {
		return err
	}
	executor := processor.NewExecutor(resolver, Api, &http.Client{Transport: egressGuard.Transport()},
		time.Duration(Configuration.SendTimeoutMs)*time.Millisecond)
	// Its counters were built once in afterMicroserviceInitialized and are handed in,
	// because a collector belongs to the process while everything this callback builds
	// belongs to the connection, and a second registration of the same collector panics.
	//
	// Its read pacer is handed in for a different reason and is built HERE, per connection,
	// precisely BECAUSE this callback is connection-scoped: a pacer holds the state of one
	// unbroken run of read failures, so a reconnect should start a fresh one rather than inherit
	// the failures of the connection that just went away. It is passed rather than built inside
	// the consumer because the consumer deliberately holds no Microservice, and reporting an
	// exhausted budget is the one thing a pacer needs one for.
	Consumer = processor.NewDispatchConsumer(reader, dead, deadIndex, executor,
		RateLimiter, time.Duration(Configuration.EgressWaitBudgetMs)*time.Millisecond,
		tenantDeleted, Configuration.MaxConcurrentSends, Configuration.DispatchBacklog,
		DispatchMetrics, core.NewReadPacer(Microservice, "connector dispatch"))
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
	resolver := governance.NewServiceLimitResolver(client, umURL, def, governance.Outbound)
	log.Info().Str("userManagement", umURL).Msg("Per-tenant outbound overrides enabled (fail-open to platform default).")
	return core.NewTenantRateLimiter(resolver.Resolve)
}

// afterMicroserviceInitialized initializes components after the microservice is up.
func afterMicroserviceInitialized(ctx context.Context) error {
	if err := parseConfiguration(); err != nil {
		return err
	}

	// Auth degrades instead of failing startup (ADR-022 decision 3): fetch the validator in
	// the background and gate the data plane on readiness rather than exiting when
	// user-management is briefly unreachable (amends ADR-008).
	//
	// Left here rather than handed to core/service: services do not agree on how the gate
	// opens — most fetch it in the background like this, user-management has its own
	// validator and marks ready outright, and the ingest services open it with no auth
	// surface at all. Three answers is not a default.
	Microservice.StartInstanceAuthGate(ctx)

	// The three managers, their construction and the order of all four of their lifecycle
	// phases now live in core/service. What stays here is what is genuinely this service's:
	// which migrations, which oncreate callback, which schema, and the wiring in AfterRdb
	// that has to happen between two of the constructions.
	Svc = service.New(Microservice, service.Spec{
		Rdb: &service.RdbSpec{
			// This chain includes the secret-store migration, which the rdb manager runs
			// under the startup advisory lock.
			Migrations: schema.Migrations,
			Instance:   Microservice.InstanceConfiguration.Persistence.Rdb,
			Config:     Configuration.RdbConfiguration,
		},
		// Runs once the rdb manager is initialized and before the other two are built. Every
		// line below needs the relational handle and is needed by the NATS manager's
		// oncreate callback, which makes this the one point in the sequence where this
		// service has to run.
		AfterRdb: func(ctx context.Context, m *service.Managers) error {
			RdbManager = m.Rdb

			// The envelope-encrypted secret store (ADR-059) over the service DB: each
			// outbound credential lives here, resolved server-internal at dispatch. Fails
			// startup closed on an unbuilt backend/provider or a missing instance root key,
			// since a resolved credential is required to authenticate an outbound call.
			store, err := buildSecretStore(ctx)
			if err != nil {
				return err
			}
			SecretStore = store

			// The per-tenant outbound egress limiter (ADR-060 SD-3). Fail-open to the
			// platform default when per-tenant overrides are not wired (never unlimited).
			RateLimiter = buildEgressLimiter()

			// The connector store (ADR-060 C4a). createNatsComponents binds it into the
			// executor (publish resolves a ConnectorRef to its latest published version),
			// and the GraphQL providers below reuse the same instance.
			Api = model.NewApi(RdbManager, SecretStore)

			// Report the size of this schema's append-only history tables (ADR-023): a connector version history only
			// grows, and so does the audit journal the collector adds for every schema. Nothing
			// prunes either, so the storage floor rises with use and nothing else says how fast.
			// Registered directly rather than through Microservice.NewGauge because a failed read
			// must produce NO series rather than a zero. See rdb.StorageGrowthCollector.
			prometheus.MustRegister(rdb.NewStorageGrowthCollector(Microservice, RdbManager.Database,
				&model.ConnectorVersion{}))

			// Build every Prometheus instrument this service exports, before the NATS
			// manager that consumes them.
			buildMetrics()
			return nil
		},
		// The secret store, the rate limiter and the Api must already exist when this runs,
		// which the order above guarantees.
		Nats: &service.NatsSpec{OnCreate: createNatsComponents},
		GraphQL: &service.GraphQLSpec{
			// The GraphQL surface carries the service identity plus the per-tenant,
			// versioned Connector CRUD (ADR-060 slice C4). The Api built in AfterRdb is
			// injected as a provider so the resolvers resolve it (and its secret store, for
			// hasSecret) from the request context.
			Schema:   graphql.SchemaContent,
			Resolver: &graphql.SchemaResolver{Area: string(Microservice.FunctionalArea)},
			Providers: func() map[gqlcore.ContextKey]interface{} {
				return map[gqlcore.ContextKey]interface{}{
					gqlcore.ContextRdbKey: RdbManager,
					gqlcore.ContextApiKey: Api,
				}
			},
		},
	})
	if err := Svc.Initialize(ctx); err != nil {
		return err
	}
	// Published for the rest of the service, which names these managers directly.
	NatsManager, GraphQLManager = Svc.Nats, Svc.GraphQL
	return nil
}

// afterMicroserviceStarted starts components after the microservice is started.
func afterMicroserviceStarted(ctx context.Context) error {
	// Rdb, then NATS, then the GraphQL server. That order, and the reason the broker has to
	// be up before the HTTP server accepts traffic, now live in core/service.
	//
	// This service's createNatsComponents injects nothing the resolvers read, so the order
	// is not load-bearing here today. It is uniform so that a later injection into Api
	// cannot open the window it opened in command-delivery.
	if err := Svc.Start(ctx); err != nil {
		return err
	}
	// Start the consumer last (after its reader is live). It is built by the NATS manager's
	// oncreate callback, so it does not exist until the line above has run.
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
	// GraphQL, then NATS, then Rdb. Nothing in this service's GraphQL plane touches the
	// broker today — connector authoring and publishing are database writes, and the
	// dispatch consumer that does read the broker is stopped above — so this is the
	// platform's one shutdown order rather than a local requirement. It is worth holding
	// anyway: the day a connector mutation publishes anything, the safe order is already
	// the one core/service walks.
	return Svc.Stop(ctx)
}

// beforeMicroserviceTerminated terminates components in reverse dependency order.
func beforeMicroserviceTerminated(ctx context.Context) error {
	// GraphQL, then NATS, then Rdb — the same order as the stop above.
	//
	// This service used to terminate NATS first. The change is not observable:
	// GraphQLManager.ExecuteTerminate returns nil and carries no callback, so the only
	// thing that moved is a no-op, and NATS still terminates before Rdb — which is the pair
	// that actually closes handles.
	return Svc.Terminate(ctx)
}
