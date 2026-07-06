// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"

	dmconfig "github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-notification-management/config"
	"github.com/devicechain-io/dc-notification-management/graphql"
	"github.com/devicechain-io/dc-notification-management/model"
	"github.com/devicechain-io/dc-notification-management/processor"
	"github.com/devicechain-io/dc-notification-management/schema"
)

var (
	Microservice  *core.Microservice
	Configuration *config.NotificationManagementConfiguration

	RdbManager     *rdb.RdbManager
	GraphQLManager *gqlcore.GraphQLManager
	NatsManager    *messaging.NatsManager
	Api            *model.Api

	AlarmEventsReader     messaging.MessageReader
	NotificationProcessor *processor.NotificationProcessor
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

// createNatsComponents creates the messaging components used by this microservice:
// a durable consumer of the alarm-events stream feeding the notification processor.
func createNatsComponents(nmgr *messaging.NatsManager) error {
	// Durable reader over the cross-tenant alarm-events wildcard (ADR-041). The
	// durable name is per instance + area, so replicas share one consumer and each
	// event is delivered to exactly one of them.
	//
	// TODO(notifications N.C): NewReader creates the durable with JetStream's default
	// DeliverAll, so the FIRST time this area is enabled on a running instance the
	// consumer replays up to streamMaxAge (7d) of retained alarm transitions. Harmless
	// for the LogNotifier (log noise) but a page-storm of stale alarms once a real
	// channel adapter lands — enabling notification-management on an existing fleet
	// would page humans about days-old alarms. Before N.C, give NewReader a
	// DeliverPolicy(New) option for this consumer (the durable position persists
	// thereafter, so downtime-safety is kept) or age-guard events in dispatchOne.
	// Corollary: the stream is LimitsPolicy, so an outage longer than streamMaxAge
	// drops events the consumer never sees — "briefly down" is safe, a week down is not.
	aevents, err := nmgr.NewReader(dmconfig.SUBJECT_ALARM_EVENTS)
	if err != nil {
		return err
	}
	AlarmEventsReader = aevents

	// First slice: log the notification the service would deliver. Later slices swap
	// in the policy-driven channel dispatcher behind the same Notifier seam.
	NotificationProcessor = processor.NewNotificationProcessor(Microservice, AlarmEventsReader,
		core.NewNoOpLifecycleCallbacks(), processor.NewLogNotifier())
	return NotificationProcessor.Initialize(context.Background())
}

// afterMicroserviceInitialized initializes components after the microservice is up.
func afterMicroserviceInitialized(ctx context.Context) error {
	// Parse configuration.
	if err := parseConfiguration(); err != nil {
		return err
	}

	// Create and initialize rdb manager (runs the notification schema migrations).
	// The per-tenant channels, policies, and per-alarm notification state are
	// served from RDB.
	RdbManager = rdb.NewRdbManager(Microservice, core.NewNoOpLifecycleCallbacks(), schema.Migrations,
		Microservice.InstanceConfiguration.Persistence.Rdb, Configuration.RdbConfiguration)
	if err := RdbManager.Initialize(ctx); err != nil {
		return err
	}
	Api = model.NewApi(RdbManager)

	// Create and initialize nats manager (which invokes createNatsComponents).
	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), createNatsComponents)
	if err := NatsManager.Initialize(ctx); err != nil {
		return err
	}

	// Create and initialize graphql manager. The schema now serves the notification
	// configuration CRUD (channels/policies) backed by the rdb Api, plus the static
	// channel-type capability list.
	providers := map[gqlcore.ContextKey]interface{}{
		gqlcore.ContextRdbKey: RdbManager,
		gqlcore.ContextApiKey: Api,
	}
	parsed := gqlcore.MustParseSchema(graphql.SchemaContent, &graphql.SchemaResolver{})

	// Auth degrades instead of failing startup (ADR-022 decision 3): fetch the
	// validator in the background and gate the data plane on readiness rather than
	// exiting when user-management is briefly unreachable (amends ADR-008).
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
	return NotificationProcessor.Start(ctx)
}

// beforeMicroserviceStopped stops components in reverse dependency order.
func beforeMicroserviceStopped(ctx context.Context) error {
	if err := NotificationProcessor.Stop(ctx); err != nil {
		return err
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
	if err := NotificationProcessor.Terminate(ctx); err != nil {
		return err
	}
	if err := NatsManager.Terminate(ctx); err != nil {
		return err
	}
	if err := GraphQLManager.Terminate(ctx); err != nil {
		return err
	}
	return RdbManager.Terminate(ctx)
}
