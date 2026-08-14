// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-command-delivery/config"
	"github.com/devicechain-io/dc-command-delivery/graphql"
	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-command-delivery/presence"
	"github.com/devicechain-io/dc-command-delivery/processor"
	"github.com/devicechain-io/dc-command-delivery/verify"
	"github.com/devicechain-io/dc-microservice/auth"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/streams"
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
	responses, err := nmgr.NewReader(streams.CommandResponses)
	if err != nil {
		return err
	}
	CommandResponsesReader = responses

	// Create writer for outbound device commands.
	commands, err := nmgr.NewWriter(streams.DeviceCommands)
	if err != nil {
		return err
	}
	DeviceCommandsWriter = commands

	// Create and initialize command delivery processor. The ADR-077 gate stops the
	// delivery sweep from publishing into a deleted tenant — a physical actuation on an
	// offboarded customer's hardware, which nothing else on this path would prevent: the
	// sweep loads QUEUED commands cross-tenant under a system context, and a command
	// enqueued before the delete is still queued after it.
	infra := Microservice.InstanceConfiguration.Infrastructure
	CommandDeliveryProcessor = processor.NewCommandDeliveryProcessor(Microservice, CommandResponsesReader,
		DeviceCommandsWriter, core.NewNoOpLifecycleCallbacks(), Api,
		governance.NewTenantLifecycleGate(infra.UserManagement, infra.ServiceAuth.Secret, "command-delivery"),
		presenceReader(infra))
	err = CommandDeliveryProcessor.Initialize(context.Background())
	if err != nil {
		return err
	}
	return nil
}

// presenceReader builds the presence gate's read side, or nil to run the sweep ungated.
//
// 🔴 FAIL-OPEN, AND LOUDLY, which is the opposite of wireEnqueueValidator's neighbours in
// this file and is a deliberate difference. The enqueue validator refuses work it cannot
// validate; this gate only ever WITHHOLDS work, so an unavailable authority must not stop
// commands. Nil here means every command dispatches exactly as it did before the gate
// existed — the behaviour the platform has always had, not a new failure. Withholding
// instead would turn a device-state outage, or an instance that simply does not deploy
// device-state, into a platform-wide command stall.
//
// Both guards are configurations, not mistakes: an instance can legitimately run a profile
// without device-state, and a config document predating this feature carries no
// deviceState block. Each logs at WARN with what is lost, because a gate that is off is
// invisible in every other way — commands flow, nothing is held, and the silent losses it
// exists to prevent resume without a single error.
func presenceReader(infra mscfg.InfrastructureConfiguration) presence.Reader {
	if infra.ServiceAuth.Secret == "" {
		log.Warn().Msg("Service secret not configured — the presence gate is OFF; commands to devices known to be offline will be published and silently dropped by the broker.")
		return nil
	}
	if infra.DeviceState.Hostname == "" || infra.DeviceState.Port == 0 {
		log.Warn().Msg("device-state endpoint not configured (infrastructure.deviceState) — the presence gate is OFF; commands to devices known to be offline will be published and silently dropped by the broker.")
		return nil
	}
	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "command-delivery",
		[]string{string(auth.StateRead)})
	url := fmt.Sprintf("http://%s:%d/graphql", infra.DeviceState.Hostname, infra.DeviceState.Port)
	log.Info().Str("deviceState", url).
		Msg("Presence gate enabled; commands to devices a transport reports as absent will be withheld rather than published.")
	return presence.NewGraphQLReader(client, url)
}

// wireDeviceManagementGates installs the three collaborators command-delivery reaches
// device-management for, when the shared service secret is configured: the ADR-043
// decision 3 enqueue gate, its fleet counterpart, and group-target resolution. The
// enabled/disabled mode is logged at startup so a misconfigured deploy (empty secret) is
// visible rather than silently skipping validation at enqueue time.
//
// They share one switch because they share one identity and one endpoint — either
// command-delivery can reach device-management under its device:read service token, or it
// cannot. The enqueue gate covers device existence (W1.1b) AND command-vocabulary/payload
// validation in one call, and the batch gate answers the same question for many devices.
//
// 🔴 THE BATCH GATE IS PART OF THIS SET, NOT A LATER ADDITION, BECAUSE THE DEGRADED MODE
// IS NOT THE SAME SIZE ON BOTH PATHS. An unwired single validator means one unvalidated
// command; an unwired batch validator means a fan-out of up to ten thousand of them, none
// of which was checked against any device's vocabulary. The warnings below therefore say
// what is lost for FLEET writes too — an operator who reads "commands unvalidated" and
// infers a per-command nuisance has been told the smaller half of the truth.
//
// Group resolution has no degraded mode at all: with no resolver, a group-targeted batch
// is refused outright rather than quietly becoming an empty one.
func wireDeviceManagementGates() {
	infra := Microservice.InstanceConfiguration.Infrastructure
	if infra.ServiceAuth.Secret == "" {
		log.Warn().Msg("Service secret not configured — command-delivery will NOT validate commands before enqueue (device existence and payload schema unchecked), including fleet-wide batches; group-targeted batches will be refused.")
		return
	}
	// Guard the device-management coordinate too: a config document predating this
	// feature carries no deviceManagement block, and building http://:0/graphql
	// would fail every enqueue. Degrade to no-validation (loudly) rather than trap.
	if infra.DeviceManagement.Hostname == "" || infra.DeviceManagement.Port == 0 {
		log.Warn().Msg("device-management endpoint not configured (infrastructure.deviceManagement) — command-delivery will NOT validate commands before enqueue (device existence and payload schema unchecked), including fleet-wide batches; group-targeted batches will be refused.")
		return
	}
	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "command-delivery", []string{string(auth.DeviceRead)})
	url := fmt.Sprintf("http://%s:%d/graphql", infra.DeviceManagement.Hostname, infra.DeviceManagement.Port)
	Api.EnqueueValidator = verify.NewEnqueueValidator(client, url)
	Api.BatchValidator = verify.NewBatchValidator(client, url)
	Api.GroupTargetResolver = verify.NewGroupResolver(client, url)
	log.Info().Str("deviceManagement", url).Msg("Command enqueue will be validated against device-management (device existence + published command vocabulary + payload schema, ADR-043), for single commands and for fleet batches; entity groups will be resolved there as batch targets.")
}

// wireHeldCeilingResolver wires the PER-TENANT held-command ceiling: an operator's
// override, resolving to the tenant's tier, resolving to this instance's configured
// default. Consumed the way event-sources consumes governance.ShedPriorityResolver,
// and it needs no adapter — that resolver's Resolve(tenant) int already satisfies
// model.HeldCommandCeilingResolver, and it folds the configured default in itself.
//
// 🔑 A NIL RESOLVER IS SAFE, NOT DISABLED. The instance-wide ceiling
// (Api.DefaultHeldCommandCeiling, floored positive in ApplyDefaults) still bounds
// every tenant, so an unconfigured collaborator degrades to one bound for everyone
// rather than to no bound at all — the direction a governance limit must fail.
//
// Guarded on the same two coordinates as wireEnqueueValidator, and for the same
// reason: a config document predating this feature carries no usable
// user-management block, and building http://:0/graphql would make every enqueue
// pay a doomed round trip. Degrade loudly rather than trap.
func wireHeldCeilingResolver() {
	infra := Microservice.InstanceConfiguration.Infrastructure
	if infra.ServiceAuth.Secret == "" {
		log.Warn().Int("ceiling", Configuration.HeldCommandCeiling).
			Msg("Service secret not configured — per-tenant held-command ceilings are disabled; metering every tenant at the instance default.")
		return
	}
	if infra.UserManagement.Hostname == "" || infra.UserManagement.Port == 0 {
		log.Warn().Int("ceiling", Configuration.HeldCommandCeiling).
			Msg("user-management endpoint not configured (infrastructure.userManagement) — per-tenant held-command ceilings are disabled; metering every tenant at the instance default.")
		return
	}
	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "command-delivery",
		[]string{string(auth.TenantRead)})
	url := fmt.Sprintf("http://%s:%d/graphql", infra.UserManagement.Hostname, infra.UserManagement.Port)
	Api.HeldCeilingResolver = governance.NewHeldCommandCeilingResolver(client, url, Configuration.HeldCommandCeiling)
	log.Info().Str("userManagement", url).Int("default", Configuration.HeldCommandCeiling).
		Msg("Per-tenant held-command ceilings enabled (override, else tier, else the instance default).")
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

	// Wrap api around rdb manager. The default command TTL (floored positive in
	// ApplyDefaults) gives every enqueued command a terminal horizon when its creator
	// supplies no explicit expiresAt (ADR-075 L4b).
	Api = model.NewApi(RdbManager)
	Api.DefaultCommandTTL = time.Duration(Configuration.DefaultCommandTTLSeconds) * time.Second

	// Fleet-write counters. This is the only place they are registered: promauto
	// registers against the process-global registry and panics on a duplicate, and the
	// lifecycle manager guarantees this initializer runs once. Every test builds its Api
	// by literal, leaves this nil, and records nothing.
	Api.BatchMetrics = model.NewBatchMetrics(Microservice)

	// The held-command ceiling this instance falls back to. It is floored positive in
	// ApplyDefaults, so this is always a real bound: a missing or zero configured value
	// means the platform default and NEVER unlimited.
	//
	// It is the fallback, not the whole answer: wireHeldCeilingResolver below adds the
	// per-tenant cascade on top of it.
	Api.DefaultHeldCommandCeiling = Configuration.HeldCommandCeiling
	wireHeldCeilingResolver()

	// The share of whatever ceiling is in force that only a platform service token may
	// draw on, so one fleet write cannot consume the whole ceiling and leave every
	// automated send-command for that tenant refused until the backlog drains. Operator
	// configuration only — deliberately no per-tenant or tier path, since a tenant able
	// to lower this could defeat the protection that exists against its own batches.
	// Floored positive in ApplyDefaults and capped in Validate, so this is always a real
	// share: absent or zero means the platform reserve, never "no reserve".
	Api.DeliveryMachineryReserve = Configuration.DeliveryMachineryReserve

	// Wire the enqueue gate (ADR-043 decision 3): a synchronous check against
	// device-management before a command is enqueued (ADR-044 amendment) covering
	// device existence, the profile's published command vocabulary, and the payload's
	// conformance to the command's parameter schema — for single commands and for
	// fleet batches — plus resolution of entity groups named as batch targets.
	// Enabled only when the shared service secret is configured; otherwise the enqueue
	// path runs unvalidated and we say so loudly rather than fail closed (an
	// unvalidated command is an integrity nuisance, not a security breach, and
	// refusing to start would take the whole service down over an optional
	// collaborator). Group targeting is the exception and fails closed: a group that
	// cannot be resolved must not silently become an empty fleet write.
	wireDeviceManagementGates()

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
