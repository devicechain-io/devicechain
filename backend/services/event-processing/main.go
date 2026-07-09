// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"

	dmconfig "github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-event-processing/config"
	"github.com/devicechain-io/dc-event-processing/graphql"
	"github.com/devicechain-io/dc-event-processing/processor"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
)

var (
	Microservice  *core.Microservice
	Configuration *config.EventProcessingConfiguration

	GraphQLManager *gqlcore.GraphQLManager
	NatsManager    *messaging.NatsManager

	ResolvedEventsReader    messaging.MessageReader
	ResolvedEventsProcessor *processor.ResolvedEventsProcessor
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
	cfg := &config.EventProcessingConfiguration{}
	err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, cfg)
	if err != nil {
		return err
	}
	Configuration = cfg
	return nil
}

// Create messaging components used by this microservice.
func createNatsComponents(nmgr *messaging.NatsManager) error {
	// Reader for resolved events (wildcard across tenants). This is a third,
	// independent consumer fanning out alongside event-management (persistence) and
	// device-state (projection) — event-processing's DETECT tap (ADR-051).
	revents, err := nmgr.NewReader(dmconfig.SUBJECT_RESOLVED_EVENTS)
	if err != nil {
		return err
	}
	ResolvedEventsReader = revents

	// Add and initialize the resolved-events processor (drops every event in this
	// scaffold slice; DETECT is wired later).
	ResolvedEventsProcessor = processor.NewResolvedEventsProcessor(Microservice, ResolvedEventsReader,
		core.NewNoOpLifecycleCallbacks())
	return ResolvedEventsProcessor.Initialize(context.Background())
}

// Called after microservice has been initialized.
func afterMicroserviceInitialized(ctx context.Context) error {
	// Parse configuration.
	if err := parseConfiguration(); err != nil {
		return err
	}

	// Create and initialize nats manager.
	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), createNatsComponents)
	if err := NatsManager.Initialize(ctx); err != nil {
		return err
	}

	// The scaffold GraphQL surface exists only to stand up the shared health/metrics
	// server (/healthz, /readyz, /metrics); the rule surface arrives later. No
	// context providers are needed yet (the resolver is state-free).
	providers := map[gqlcore.ContextKey]interface{}{}
	parsed := gqlcore.MustParseSchema(graphql.SchemaContent, &graphql.SchemaResolver{})

	// Auth degrades instead of failing startup (ADR-022 decision 3): fetch the
	// validator in the background and gate the data plane on readiness.
	Microservice.StartInstanceAuthGate(ctx)

	GraphQLManager = gqlcore.NewGraphQLManager(Microservice, core.NewNoOpLifecycleCallbacks(),
		parsed, providers, Microservice.Readiness)
	return GraphQLManager.Initialize(ctx)
}

// Called after microservice has been started.
func afterMicroserviceStarted(ctx context.Context) error {
	if err := GraphQLManager.Start(ctx); err != nil {
		return err
	}
	if err := NatsManager.Start(ctx); err != nil {
		return err
	}
	return ResolvedEventsProcessor.Start(ctx)
}

// Called before microservice has been stopped.
func beforeMicroserviceStopped(ctx context.Context) error {
	if err := ResolvedEventsProcessor.Stop(ctx); err != nil {
		return err
	}
	if err := NatsManager.Stop(ctx); err != nil {
		return err
	}
	return GraphQLManager.Stop(ctx)
}

// Called before microservice has been terminated.
func beforeMicroserviceTerminated(ctx context.Context) error {
	if err := ResolvedEventsProcessor.Terminate(ctx); err != nil {
		return err
	}
	if err := NatsManager.Terminate(ctx); err != nil {
		return err
	}
	return GraphQLManager.Terminate(ctx)
}
