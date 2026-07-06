// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"time"

	dmconfig "github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-event-management/config"
	"github.com/devicechain-io/dc-event-management/graphql"
	"github.com/devicechain-io/dc-event-management/model"
	"github.com/devicechain-io/dc-event-management/processor"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/rs/zerolog/log"
)

var (
	Microservice  *core.Microservice
	Configuration *config.EventManagementConfiguration

	RdbManager     *rdb.RdbManager
	GraphQLManager *gqlcore.GraphQLManager
	NatsManager    *messaging.NatsManager

	Api *model.Api

	ResolvedEventsReader      messaging.MessageReader
	EventPersistenceProcessor *processor.EventPersistenceProcessor
	FailedEventsWriter        messaging.MessageWriter

	EntityDeletedReader    messaging.MessageReader
	EntityAnchorReconciler *processor.EntityAnchorReconciler

	AnchorSweep *processor.AnchorReconciliationSweep
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
	config := &config.EventManagementConfiguration{}
	err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, config)
	if err != nil {
		return err
	}
	Configuration = config
	return nil
}

// Create messaging components used by this microservice.
func createNatsComponents(nmgr *messaging.NatsManager) error {
	// Create reader for resolved events (wildcard across tenants).
	revents, err := nmgr.NewReader(dmconfig.SUBJECT_RESOLVED_EVENTS)
	if err != nil {
		return err
	}
	ResolvedEventsReader = revents

	// Add and initialize failed events writer.
	fevents, err := nmgr.NewWriter(dmconfig.SUBJECT_FAILED_EVENTS)
	if err != nil {
		return err
	}
	FailedEventsWriter = fevents

	// Add and initialize inbound events processor.
	EventPersistenceProcessor = processor.NewEventPersistenceProcessor(Microservice, ResolvedEventsReader,
		FailedEventsWriter, core.NewNoOpLifecycleCallbacks(), Api)
	err = EventPersistenceProcessor.Initialize(context.Background())
	if err != nil {
		return err
	}

	// Reader + reconciler for entity-deletion events (ADR-044): drops event_anchors
	// rows referencing an entity deleted in device-management. Durable, idempotent.
	dentity, err := nmgr.NewReader(dmconfig.SUBJECT_ENTITY_DELETED)
	if err != nil {
		return err
	}
	EntityDeletedReader = dentity
	EntityAnchorReconciler = processor.NewEntityAnchorReconciler(Microservice, EntityDeletedReader,
		Api, core.NewNoOpLifecycleCallbacks())
	if err := EntityAnchorReconciler.Initialize(context.Background()); err != nil {
		return err
	}
	return nil
}

// wireAnchorSweep builds the reconciliation sweep (ADR-044 decision 3) when it is
// configured — a positive interval plus the shared service secret and the
// device-management endpoint it queries. It is disabled (with a log line) otherwise:
// the entity.deleted consumer remains the primary path, so a missing sweep degrades
// gracefully rather than failing startup.
func wireAnchorSweep(ctx context.Context) error {
	interval := Configuration.AnchorSweepIntervalSeconds
	if interval <= 0 {
		log.Info().Msg("Anchor reconciliation sweep disabled (interval <= 0).")
		return nil
	}
	infra := Microservice.InstanceConfiguration.Infrastructure
	if infra.ServiceAuth.Secret == "" || infra.DeviceManagement.Hostname == "" || infra.DeviceManagement.Port == 0 ||
		infra.UserManagement.Hostname == "" || infra.UserManagement.Port == 0 {
		log.Warn().Msg("Service secret, device-management, or user-management endpoint not configured — anchor reconciliation sweep disabled.")
		return nil
	}
	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "event-management", []string{string(auth.DeviceRead)})
	dmURL := fmt.Sprintf("http://%s:%d/graphql", infra.DeviceManagement.Hostname, infra.DeviceManagement.Port)
	AnchorSweep = processor.NewAnchorReconciliationSweep(Microservice, Api, client, dmURL,
		time.Duration(interval)*time.Second, core.NewNoOpLifecycleCallbacks())
	if err := AnchorSweep.Initialize(ctx); err != nil {
		return err
	}
	log.Info().Str("deviceManagement", dmURL).Int("intervalSeconds", interval).
		Msg("Anchor reconciliation sweep enabled (ADR-044 decision 3).")
	return nil
}

// Called after microservice has been initialized.
func afterMicroserviceInitialized(ctx context.Context) error {
	// Parse configuration.
	err := parseConfiguration()
	if err != nil {
		return err
	}

	// Create and initialize rdb manager.
	rdbcb := core.NewNoOpLifecycleCallbacks()
	RdbManager = rdb.NewRdbManager(Microservice, rdbcb, model.Migrations,
		Microservice.InstanceConfiguration.Persistence.Tsdb, Configuration.TsdbConfiguration)
	err = RdbManager.Initialize(ctx)
	if err != nil {
		return err
	}

	// Create RDB caches.
	model.InitializeCaches(RdbManager)

	// Wrap api around rdb manager.
	Api = model.NewApi(RdbManager)

	// Create and initialize nats manager.
	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), createNatsComponents)
	err = NatsManager.Initialize(ctx)
	if err != nil {
		return err
	}

	// Build the reconciliation sweep (ADR-044 decision 3), the backstop for
	// entity-deletion events missed by the primary consumer.
	if err := wireAnchorSweep(ctx); err != nil {
		return err
	}

	// Map of providers that will be injected into graphql http context. The NATS
	// manager backs the live subscription resolvers (SubscribeLive); it is already
	// connected here (Initialize above), before the subscription server accepts a
	// client.
	providers := map[gqlcore.ContextKey]interface{}{
		gqlcore.ContextRdbKey:  RdbManager,
		gqlcore.ContextApiKey:  Api,
		gqlcore.ContextNatsKey: NatsManager,
	}

	// Create and initialize graphql manager.
	gqlcb := core.NewNoOpLifecycleCallbacks()

	schema := graphql.SchemaContent
	parsed := gqlcore.MustParseSchema(schema, &graphql.SchemaResolver{})

	// Auth degrades instead of failing startup (ADR-022 decision 3): fetch the
	// validator in the background and gate the data plane on readiness rather
	// than exiting when user-management is briefly unreachable (amends ADR-008).
	Microservice.StartInstanceAuthGate(ctx)

	GraphQLManager = gqlcore.NewGraphQLManager(Microservice, gqlcb, *parsed, providers, Microservice.Readiness)
	err = GraphQLManager.Initialize(ctx)
	if err != nil {
		return err
	}

	return nil
}

// Called after microservice has been started.
func afterMicroserviceStarted(ctx context.Context) error {
	err := RdbManager.Start(ctx)
	if err != nil {
		return err
	}

	err = GraphQLManager.Start(ctx)
	if err != nil {
		return err
	}

	// Start nats manager.
	err = NatsManager.Start(ctx)
	if err != nil {
		return err
	}

	// Start event persistence processor.
	err = EventPersistenceProcessor.Start(ctx)
	if err != nil {
		return err
	}

	// Start entity-anchor reconciler.
	err = EntityAnchorReconciler.Start(ctx)
	if err != nil {
		return err
	}

	// Start the reconciliation sweep, if configured.
	if AnchorSweep != nil {
		if err := AnchorSweep.Start(ctx); err != nil {
			return err
		}
	}

	return nil
}

// Called before microservice has been stopped.
func beforeMicroserviceStopped(ctx context.Context) error {
	// Stop the reconciliation sweep, if running.
	if AnchorSweep != nil {
		if err := AnchorSweep.Stop(ctx); err != nil {
			return err
		}
	}

	// Stop entity-anchor reconciler.
	if err := EntityAnchorReconciler.Stop(ctx); err != nil {
		return err
	}

	// Stop event persistence processor.
	err := EventPersistenceProcessor.Stop(ctx)
	if err != nil {
		return err
	}

	// Stop nats manager.
	err = NatsManager.Stop(ctx)
	if err != nil {
		return err
	}

	// Stop graphql manager.
	err = GraphQLManager.Stop(ctx)
	if err != nil {
		return err
	}

	// Stop rdb manager.
	err = RdbManager.Stop(ctx)
	if err != nil {
		return err
	}

	return nil
}

// Called before microservice has been terminated.
func beforeMicroserviceTerminated(ctx context.Context) error {
	// Terminate nats manager.
	err := NatsManager.Terminate(ctx)
	if err != nil {
		return err
	}

	// Terminate graphql manager.
	err = GraphQLManager.Terminate(ctx)
	if err != nil {
		return err
	}

	// Terminate rdb manager.
	err = RdbManager.Terminate(ctx)
	if err != nil {
		return err
	}

	return nil
}
