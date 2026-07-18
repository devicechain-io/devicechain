// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-command-delivery/config"
	"github.com/devicechain-io/dc-command-delivery/graphql"
	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-command-delivery/processor"
	"github.com/devicechain-io/dc-command-delivery/verify"
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
	Configuration *config.CommandDeliveryConfiguration

	RdbManager     *rdb.RdbManager
	GraphQLManager *gqlcore.GraphQLManager
	NatsManager    *messaging.NatsManager

	Api *model.Api

	CommandResponsesReader   messaging.MessageReader
	DeviceCommandsWriter     messaging.MessageWriter
	CommandDeliveryProcessor *processor.CommandDeliveryProcessor
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
	config := &config.CommandDeliveryConfiguration{}
	err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, config)
	if err != nil {
		return err
	}
	Configuration = config
	return nil
}

// Create messaging components used by this microservice.
func createNatsComponents(nmgr *messaging.NatsManager) error {
	// Create reader for inbound device responses (wildcard across tenants).
	responses, err := nmgr.NewReader(config.SUBJECT_COMMAND_RESPONSES)
	if err != nil {
		return err
	}
	CommandResponsesReader = responses

	// Create writer for outbound device commands.
	commands, err := nmgr.NewWriter(config.SUBJECT_DEVICE_COMMANDS)
	if err != nil {
		return err
	}
	DeviceCommandsWriter = commands

	// Create and initialize command delivery processor.
	CommandDeliveryProcessor = processor.NewCommandDeliveryProcessor(Microservice, CommandResponsesReader,
		DeviceCommandsWriter, core.NewNoOpLifecycleCallbacks(), Api)
	err = CommandDeliveryProcessor.Initialize(context.Background())
	if err != nil {
		return err
	}
	return nil
}

// wireEnqueueValidator installs the ADR-043 decision 3 enqueue gate on the Api when
// the shared service secret is configured, logging the enabled/disabled mode at
// startup so a misconfigured deploy (empty secret) is visible rather than silently
// skipping validation at enqueue time.
//
// The gate covers device existence (W1.1b) AND command-vocabulary/payload validation
// in one call, so there is a single switch: either command-delivery can reach
// device-management and both are enforced, or it cannot and neither is.
func wireEnqueueValidator() {
	infra := Microservice.InstanceConfiguration.Infrastructure
	if infra.ServiceAuth.Secret == "" {
		log.Warn().Msg("Service secret not configured — command-delivery will NOT validate commands before enqueue (device existence and payload schema unchecked).")
		return
	}
	// Guard the device-management coordinate too: a config document predating this
	// feature carries no deviceManagement block, and building http://:0/graphql
	// would fail every enqueue. Degrade to no-validation (loudly) rather than trap.
	if infra.DeviceManagement.Hostname == "" || infra.DeviceManagement.Port == 0 {
		log.Warn().Msg("device-management endpoint not configured (infrastructure.deviceManagement) — command-delivery will NOT validate commands before enqueue (device existence and payload schema unchecked).")
		return
	}
	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "command-delivery", []string{string(auth.DeviceRead)})
	url := fmt.Sprintf("http://%s:%d/graphql", infra.DeviceManagement.Hostname, infra.DeviceManagement.Port)
	Api.EnqueueValidator = verify.NewEnqueueValidator(client, url)
	log.Info().Str("deviceManagement", url).Msg("Command enqueue will be validated against device-management (device existence + published command vocabulary + payload schema, ADR-043).")
}

// Called after microservice has been initialized.
func afterMicroserviceInitialized(ctx context.Context) error {
	// Parse configuration.
	err := parseConfiguration()
	if err != nil {
		return err
	}

	// Create and initialize rdb manager (relational persistence).
	rdbcb := core.NewNoOpLifecycleCallbacks()
	RdbManager = rdb.NewRdbManager(Microservice, rdbcb, model.Migrations,
		Microservice.InstanceConfiguration.Persistence.Rdb, Configuration.RdbConfiguration)
	err = RdbManager.Initialize(ctx)
	if err != nil {
		return err
	}

	// Create RDB caches.
	model.InitializeCaches(RdbManager)

	// Wrap api around rdb manager.
	Api = model.NewApi(RdbManager)

	// Wire the enqueue gate (ADR-043 decision 3): a synchronous check against
	// device-management before a command is enqueued (ADR-044 amendment) covering
	// device existence, the profile's published command vocabulary, and the payload's
	// conformance to the command's parameter schema. Enabled only when the shared
	// service secret is configured; otherwise the enqueue path runs unvalidated and we
	// say so loudly rather than fail closed (an unvalidated command is an integrity
	// nuisance, not a security breach, and refusing to start would take the whole
	// service down over an optional collaborator).
	wireEnqueueValidator()

	// Create and initialize nats manager.
	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), createNatsComponents)
	err = NatsManager.Initialize(ctx)
	if err != nil {
		return err
	}

	// Map of providers that will be injected into graphql http context.
	providers := map[gqlcore.ContextKey]interface{}{
		gqlcore.ContextRdbKey: RdbManager,
		gqlcore.ContextApiKey: Api,
	}

	// Create and initialize graphql manager.
	gqlcb := core.NewNoOpLifecycleCallbacks()

	schema := graphql.SchemaContent
	parsed := gqlcore.MustParseSchema(schema, &graphql.SchemaResolver{})

	// Auth degrades instead of failing startup (ADR-022 decision 3): fetch the
	// validator in the background and gate the data plane on readiness rather
	// than exiting when user-management is briefly unreachable (amends ADR-008).
	Microservice.StartInstanceAuthGate(ctx)

	GraphQLManager = gqlcore.NewGraphQLManager(Microservice, gqlcb, parsed, providers, Microservice.Readiness)
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

	// Start command delivery processor.
	err = CommandDeliveryProcessor.Start(ctx)
	if err != nil {
		return err
	}

	return nil
}

// Called before microservice has been stopped.
func beforeMicroserviceStopped(ctx context.Context) error {
	// Stop command delivery processor.
	err := CommandDeliveryProcessor.Stop(ctx)
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
