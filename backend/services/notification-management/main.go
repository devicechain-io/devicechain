// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/devicechain-io/dc-microservice/governance"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-notification-management/config"
	"github.com/devicechain-io/dc-notification-management/graphql"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/devicechain-io/dc-notification-management/processor"
	"github.com/devicechain-io/dc-notification-management/schema"
)

var (
	Microservice  *core.Microservice
	Configuration *config.NotificationManagementConfiguration

	// Svc owns the three managers below and the order of all four of their lifecycle
	// phases. They stay named here because the rest of this service refers to them
	// directly; Svc is what decides when each one runs.
	Svc *service.Service

	RdbManager     *rdb.RdbManager
	GraphQLManager *gqlcore.GraphQLManager
	NatsManager    *messaging.NatsManager
	Api            *model.Api

	AlarmEventsReader     messaging.MessageReader
	Notifier              *processor.PolicyNotifier
	NotificationProcessor *processor.NotificationProcessor
	RetentionSweeper      *processor.RetentionSweeper
	EscalationScheduler   *processor.EscalationScheduler

	// NotifyMetrics is built ONCE, in the initialize phase, and shared by every
	// NotificationProcessor the NATS manager's oncreate callback builds. See
	// buildMetrics.
	NotifyMetrics processor.NotifyMetrics

	// DeadLetters is this service's identity as a dead-letter producer: the source its
	// letters are stamped with and the dead_letter_lost_total their losses count on. Built
	// once, in the initialize phase, for the reason the metrics are. See buildMetrics.
	DeadLetters *deadletter.Producer
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

// parseConfiguration parses the configuration from raw bytes.
func parseConfiguration() error {
	cfg := &config.NotificationManagementConfiguration{}
	err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, cfg)
	if err != nil {
		return err
	}
	Configuration = cfg
	return nil
}

// buildSecretStore constructs the envelope-encrypted secret store (ADR-059) from the
// instance secrets configuration. secrets.New is the single wiring point: it fails
// closed on an unknown or declared-but-unbuilt backend/KEK provider and on a missing or
// malformed instance root key, so a service that cannot form its KEK does not start
// (encryption-at-rest is not optional once wired).
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
// 🔴 IT IS CALLED FROM THE INITIALIZE PHASE, NOT FROM WHERE THE PROCESSOR IS BUILT.
// The processor is built in createNatsComponents, which the NATS manager invokes on
// EVERY start, and a collector belongs to the PROCESS whereas everything that callback
// builds belongs to the CONNECTION — registering one twice on this microservice's
// registry panics. Initialize is where the process's own singletons are made, which is
// what makes this the safe half; messaging.NewNatsManager carries the reasoning.
func buildMetrics() {
	NotifyMetrics = processor.NewNotifyMetrics(Microservice)
	DeadLetters = deadletter.NewProducer(Microservice)
}

// createNatsComponents creates the messaging components used by this microservice:
// a durable consumer of the alarm-events stream feeding the notification processor.
func createNatsComponents(nmgr *messaging.NatsManager) error {
	// Durable reader over the cross-tenant alarm-events wildcard (ADR-041). The
	// durable name is per instance + area, so replicas share one consumer and each
	// event is delivered to exactly one of them.
	//
	// DeliverNew starts the durable at the stream tail on first creation, so enabling
	// this service on a running fleet does NOT replay up to streamMaxAge (7d) of
	// retained alarm transitions and page humans about days-old alarms. Downtime-safety
	// is unaffected: once the durable exists its ack cursor persists, so a restart
	// resumes from the last ack. (The stream is LimitsPolicy, so an outage longer than
	// streamMaxAge still drops unseen events — "briefly down" is safe, a week is not.)
	// N.B created this durable with the default DeliverAll; the policy change rides a
	// fresh bring-up, per the pre-GA decisive-cutover convention.
	aevents, err := nmgr.NewReader(streams.AlarmEvents, messaging.ReaderWithDeliverNew())
	if err != nil {
		return err
	}
	AlarmEventsReader = aevents

	// The ADR-024 arm. An alarm that reaches nobody used to end as a log line; it is now
	// recorded where an operator can see which pages were never sent. Built here so a
	// deployment that cannot create the stream fails at startup, beside every other stream
	// this service needs, rather than at the first failure — the one moment it has to work.
	deadWriter, err := nmgr.NewWriter(streams.DeadLetters)
	if err != nil {
		return err
	}

	// The policy-driven channel dispatcher (N.C, built in afterMicroserviceInitialized so
	// the escalation scheduler can share it) drives the consumer behind the Notifier seam.
	// Its instruments were built once in afterMicroserviceInitialized and are handed in,
	// because a collector belongs to the process while everything this callback builds
	// belongs to the connection, and a second registration of the same collector panics.
	NotificationProcessor = processor.NewNotificationProcessor(Microservice, AlarmEventsReader,
		core.NewNoOpLifecycleCallbacks(), Notifier, DeadLetters.NewSink(deadWriter), NotifyMetrics)
	return NotificationProcessor.Initialize(context.Background())
}

// afterMicroserviceInitialized initializes components after the microservice is up.
func afterMicroserviceInitialized(ctx context.Context) error {
	// Parse configuration.
	if err := parseConfiguration(); err != nil {
		return err
	}

	// Auth degrades instead of failing startup (ADR-022 decision 3): fetch the
	// validator in the background and gate the data plane on readiness rather than
	// exiting when user-management is briefly unreachable (amends ADR-008).
	//
	// Left here rather than handed to core/service: services do not agree on how the gate
	// opens — most fetch it in the background like this, user-management has its own
	// validator and marks ready outright, and the ingest services open it with no auth
	// surface at all. Three answers is not a default.
	Microservice.StartInstanceAuthGate(ctx)

	// The three managers, their construction and the order of all four of their lifecycle
	// phases now live in core/service. What stays here is what is genuinely this service's:
	// which store, which migrations, which oncreate callback, which schema, and the wiring
	// in AfterRdb that has to happen between two of the constructions.
	Svc = service.New(Microservice, service.Spec{
		Rdb: &service.RdbSpec{
			// The per-tenant channels, policies, and per-alarm notification state are
			// served from the relational store.
			Instance:   Microservice.InstanceConfiguration.Persistence.Rdb,
			Migrations: schema.Migrations,
			Config:     Configuration.RdbConfiguration,
		},
		// Runs once the rdb manager is initialized and before the other two are built.
		// Everything here reads the relational handle and is read in turn by the NATS
		// manager's oncreate callback or the GraphQL providers, which is what makes this
		// the one point in the sequence where this service has to run.
		AfterRdb: func(ctx context.Context, m *service.Managers) error {
			RdbManager = m.Rdb

			// Build the envelope-encrypted secret store (ADR-059) over the service DB: each
			// channel's write-only delivery secret lives here, not in a column. This service is
			// the first consumer of the secret layer (S3). Only the default postgres backend +
			// instance KEK provider are implemented; a declared-but-unbuilt backend/provider,
			// or a missing/short instance root key, fails startup closed so the service can
			// never silently run without encryption-at-rest.
			secretStore, err := buildSecretStore(ctx)
			if err != nil {
				return err
			}
			Api = model.NewApi(RdbManager, secretStore)

			// The policy-driven channel dispatcher (N.C): evaluate each tenant's notification
			// policies and deliver matching alarms through the configured SMTP/webhook channels,
			// maintaining the per-alarm NotificationState. It replaces the first-slice LogNotifier
			// behind the Notifier seam and is shared by the consumer processor (event-driven
			// dispatch) and the escalation scheduler (timed re-notification), so both deliver
			// through one adapter registry and retry policy.
			//
			// It also carries the ADR-077 lifecycle gate, and this service needs one for a reason
			// the ingest fronts do not have: it EMITS. An email or a webhook POST for a deleted
			// tenant has left the platform, and no later pass of the reclamation can take it back.
			// Nor does it need new inbound traffic to do so — the escalation scheduler re-pages
			// open alarms from this service's own rows on a timer, so a deleted tenant with an
			// unacknowledged alarm would keep paging until the sweep reached those rows.
			infra := Microservice.InstanceConfiguration.Infrastructure
			// The tenant-egress boundary, carrying whatever destinations the operator has
			// explicitly allowed. A malformed CIDR fails startup rather than being skipped: a
			// silently-dropped allowance would look configured and behave as though it were not.
			egressGuard, err := egress.FromConfig(infra.Egress)
			if err != nil {
				return err
			}
			Notifier = processor.NewPolicyNotifier(Api, secretStore, Configuration.DeliveryAttempts,
				Configuration.DeliveryTimeout(),
				governance.NewTenantLifecycleGate(infra.UserManagement, infra.ServiceAuth.Secret, "notification-management"),
				egressGuard)

			// Retention sweep: prune cleared per-alarm state older than the retention window so
			// the notification state stays bounded (ADR-017 N.C). A negative interval disables
			// it (the table then grows unbounded — operator opt-out only).
			if Configuration.RetentionSweepSeconds >= 0 {
				RetentionSweeper = processor.NewRetentionSweeper(Microservice, Api,
					Configuration.StateRetention(), Configuration.RetentionSweepInterval(),
					core.NewNoOpLifecycleCallbacks())
				if err := RetentionSweeper.Initialize(ctx); err != nil {
					return err
				}
			}

			// Escalation scheduler (N.D): re-notify open alarms that stay unacknowledged and
			// uncleared past their policy's escalation window, up to a bounded number of tiers. A
			// negative interval disables escalation (alarms then page only on event transitions).
			if Configuration.EscalationSweepSeconds >= 0 {
				EscalationScheduler = processor.NewEscalationScheduler(Microservice, Api, Notifier,
					Configuration.EscalationSweepInterval(), Configuration.DefaultMaxEscalations,
					core.NewNoOpLifecycleCallbacks())
				if err := EscalationScheduler.Initialize(ctx); err != nil {
					return err
				}
			}

			// Build every Prometheus instrument this service exports, before the NATS
			// manager that consumes them.
			buildMetrics()
			return nil
		},
		Nats: &service.NatsSpec{OnCreate: createNatsComponents},
		GraphQL: &service.GraphQLSpec{
			// The schema serves the notification configuration CRUD (channels/policies)
			// backed by the rdb Api, plus the static channel-type capability list.
			Schema:   graphql.SchemaContent,
			Resolver: func() interface{} { return &graphql.SchemaResolver{} },
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
	// Built by the NATS manager's oncreate callback, so it does not exist until the line
	// above has run.
	if err := NotificationProcessor.Start(ctx); err != nil {
		return err
	}
	if RetentionSweeper != nil {
		if err := RetentionSweeper.Start(ctx); err != nil {
			return err
		}
	}
	if EscalationScheduler != nil {
		return EscalationScheduler.Start(ctx)
	}
	return nil
}

// beforeMicroserviceStopped stops components in reverse dependency order.
func beforeMicroserviceStopped(ctx context.Context) error {
	if EscalationScheduler != nil {
		if err := EscalationScheduler.Stop(ctx); err != nil {
			return err
		}
	}
	if RetentionSweeper != nil {
		if err := RetentionSweeper.Stop(ctx); err != nil {
			return err
		}
	}
	if err := NotificationProcessor.Stop(ctx); err != nil {
		return err
	}
	// GraphQL, then NATS, then Rdb. Nothing in this service's GraphQL plane touches the
	// broker today — policy and delivery-record resolvers read the database, and the alarm
	// consumer that does read the broker is stopped above — so this is the platform's one
	// shutdown order rather than a local requirement. It is worth holding anyway: the day a
	// policy mutation publishes anything, the safe order is already the one core/service
	// walks. Pinned next door in graphql_shutdown_order_test.go, which drives this function.
	return Svc.Stop(ctx)
}

// beforeMicroserviceTerminated terminates components in reverse dependency order.
func beforeMicroserviceTerminated(ctx context.Context) error {
	if EscalationScheduler != nil {
		if err := EscalationScheduler.Terminate(ctx); err != nil {
			return err
		}
	}
	if RetentionSweeper != nil {
		if err := RetentionSweeper.Terminate(ctx); err != nil {
			return err
		}
	}
	if err := NotificationProcessor.Terminate(ctx); err != nil {
		return err
	}
	// GraphQL, then NATS, then Rdb — the same order as the stop above.
	//
	// This service used to terminate NATS first. The change is not observable:
	// GraphQLManager.ExecuteTerminate returns nil and carries no callback, so the only
	// thing that moved is a no-op, and NATS still terminates before Rdb — which is the pair
	// that actually closes handles.
	return Svc.Terminate(ctx)
}
