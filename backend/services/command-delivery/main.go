// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"strings"
	"time"

	"github.com/devicechain-io/dc-command-delivery/config"
	"github.com/devicechain-io/dc-command-delivery/graphql"
	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-command-delivery/presence"
	"github.com/devicechain-io/dc-command-delivery/processor"
	"github.com/devicechain-io/dc-command-delivery/verify"
	"github.com/devicechain-io/dc-microservice/actiongate"
	"github.com/devicechain-io/dc-microservice/auth"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/governance"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-microservice/storehold"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/rs/zerolog/log"
)

var (
	Microservice  *core.Microservice
	Configuration *config.CommandDeliveryConfiguration
	servicePeers  *svcclient.Peers

	// Svc owns the three managers below and the order of all four of their lifecycle
	// phases. They stay named here because the rest of this service refers to them
	// directly; Svc is what decides when each one runs.
	Svc *service.Service

	RdbManager     *rdb.RdbManager
	GraphQLManager *gqlcore.GraphQLManager
	NatsManager    *messaging.NatsManager

	Api *model.Api

	CommandResponsesReader   messaging.MessageReader
	DeviceCommandsWriter     messaging.MessageWriter
	CommandDeliveryProcessor *processor.CommandDeliveryProcessor
	DeadLetterWriteback      *processor.DeadLetterWriteback
	// CampaignScheduler plans and fires campaigns. It runs in every replica; see its type.
	CampaignScheduler *processor.CampaignScheduler

	// RosterConsumer keeps the record of disabled devices current from device-management's
	// device-roster facts, so commands queued for a disabled device are held, not sent.
	RosterConsumer *processor.RosterConsumer

	// DeletionConsumer cancels commands covered by device-deletion facts.
	DeletionConsumer    *processor.DeletionConsumer
	AgentKillConsumer   *processor.AgentKillConsumer
	AgentProposalReaper *core.PeriodicTask

	// The Prometheus instruments the two components above export. Both are built ONCE,
	// in the initialize phase, and shared by every component the NATS manager's oncreate
	// callback builds. See buildMetrics.
	DeliveryMetrics  processor.DeliveryMetrics
	WritebackMetrics *processor.WritebackMetrics

	// DeadLetters is this service's identity as a dead-letter producer: the source its
	// letters are stamped with and the dead_letter_lost_total their losses count on. Built by
	// core/service (Svc.DeadLetters), which the max-delivery recorder shares. See buildMetrics.
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
			Postprocess: func(ctx context.Context) error { return Svc.StopRefreshes(ctx) },
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
// buildMetrics creates this service's per-component Prometheus instruments exactly once.
// See the call site in afterMicroserviceInitialized for why it is not called from
// createNatsComponents, where the components that read them are built.
func buildMetrics() {
	DeliveryMetrics = processor.NewDeliveryMetrics(Microservice)
	WritebackMetrics = processor.NewWritebackMetrics(Microservice)
	// core/service built it: the platform's max-delivery recorder letters under it too,
	// and a second NewProducer here would panic on the duplicate counter.
	DeadLetters = Svc.DeadLetters
}

// newDeadLetterSink builds this service's sink over the platform dead-letter stream, stamped
// by DeadLetters (the producer core/service built, which the max-delivery recorder shares).
//
// It is a function of its own so dead_letter_wiring_test.go builds the sink exactly as this
// service does and reads what lands on the stream: a sink over the wrong stream, or from a
// producer other than this service's, writes every letter where no reader looks, and nothing
// else here would notice.
func newDeadLetterSink(nmgr *messaging.NatsManager) (*deadletter.Sink, error) {
	w, err := nmgr.NewWriter(streams.DeadLetters)
	if err != nil {
		return nil, err
	}
	return DeadLetters.NewSink(w), nil
}

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
	// The ADR-024 arm. A device's answer that could not be recorded used to end as a log
	// line, leaving its command looking unanswered with nothing to say the device had in
	// fact replied. Built here so a deployment that cannot create the stream fails at
	// startup, beside every other stream this service needs, rather than at the first
	// failure — the one moment the arm has to work.
	deadLetters, err := newDeadLetterSink(nmgr)
	if err != nil {
		return err
	}

	infra := Microservice.InstanceConfiguration.Infrastructure
	presenceGate, presenceMissing := presenceReader(infra, servicePeers, safetyGates)
	CommandDeliveryProcessor = processor.NewCommandDeliveryProcessor(Microservice, CommandResponsesReader,
		DeviceCommandsWriter, core.NewNoOpLifecycleCallbacks(), Api,
		tenantLifecycleGate(servicePeers, safetyGates),
		withPullChannels(presenceGate), deadLetters, DeliveryMetrics)
	// What switched the presence gate off, if anything: the processor counts and re-warns every
	// dispatch made without it, so the off state is not one WARN line at startup.
	CommandDeliveryProcessor.PresenceGateOff = presenceMissing
	// 🔴 SET HERE, WHERE THE PROCESSOR EXISTS, AND NOT BESIDE THE Api.* ASSIGNMENTS IN
	// afterMicroserviceInitialized -- WHICH IS WHERE THEY BELONG BY APPEARANCE AND WHERE
	// THEY WOULD NIL-PANIC. This function is the NatsManager's construction callback, and
	// the manager itself is not created until LATER in that same function, so the package
	// variable is still nil while those assignments run. It compiles, it reads as
	// consistent with its neighbours, and it dies on startup.
	//
	// Floored positive by ApplyDefaults, so what lands here is always usable and never the
	// "unset" zero the field treats as "use the platform default".
	CommandDeliveryProcessor.Hold = newStoreHold("command-responses")
	CommandDeliveryProcessor.SweepInterval = time.Duration(Configuration.SweepIntervalSeconds) * time.Second
	CommandDeliveryProcessor.DisabledSource = disabledSource(infra, servicePeers)
	// The re-stamp pass reads windows from the same owner enqueue asked. Set here for the
	// reason the line above is: the processor does not exist until this callback runs.
	CommandDeliveryProcessor.WindowOpenings = Api.WindowOpenings
	// Dispatch stamps the command's impact for an adapter that gates on it, from the same owner
	// and for the same reason as the line above: the processor exists only inside this callback.
	CommandDeliveryProcessor.CommandImpacts = Api.CommandImpacts
	// Whether a Sparkplug command is dispatched or failed on sight. Set here for the reason the
	// lines above are; the chart derives it from the rendered Sparkplug sources.
	CommandDeliveryProcessor.SparkplugCommands = Configuration.SparkplugCommands
	CommandDeliveryProcessor.LpwanDownlink = Configuration.LpwanDownlink

	// The dispatch nudge: enqueueing a command hands its DEVICE to the processor's bounded
	// queue, whose workers dispatch that device's backlog through the same gates the sweep
	// applies. Without it nothing dispatches until the next tick, so QUEUED -> SENT is
	// uniform on [0, sweepInterval] — a command an operator issues from the console sits
	// for half the interval on average, with the platform idle the whole time.
	//
	// 🔴 SET HERE FOR THE REASON THE LINE ABOVE IS, AND THE OTHER HALF OF THE SAME TRAP.
	// The processor does not exist until this callback runs, so binding the nudger beside
	// the Api.* assignments in afterMicroserviceInitialized — where it belongs by
	// appearance — would bind a nil. Api is created there and is live by the time this
	// runs, so this is the one window where both ends exist.
	//
	// A nil nudger would be safe (the nudge is simply off and every command waits for the
	// sweep), which is exactly why the wiring is worth stating: nothing else would fail.
	Api.Nudger = CommandDeliveryProcessor.Nudger()

	err = CommandDeliveryProcessor.Initialize(context.Background())
	if err != nil {
		return err
	}

	// The other half of that arm: writing the dead letter stops the MESSAGE going round
	// forever, and this stops the COMMAND reading as though it were still in flight. It
	// consumes the same stream the writer above publishes to, so a response the platform
	// gave up on settles its command minutes later — against a database that has had time
	// to come back, which a write issued from inside the original failure would not have.
	//
	// 🔴 BUILT HERE, INSIDE THE CALLBACK, NOT MERELY ITS READER. This function runs in the
	// NatsManager's ExecuteSTART, not its Initialize, so a component constructed out in
	// afterMicroserviceInitialized would capture a nil reader by value and panic on its
	// first read. That defect nearly shipped in the identity service's dead-letter
	// consumer and nothing in the test estate would have caught it, because nothing
	// exercises main.go's wiring. Its constructor refuses a nil reader for the same
	// reason: a wiring mistake must stop the service starting, never turn into a consumer
	// that reads everything and writes nothing.
	deadReader, err := nmgr.NewReader(streams.DeadLetters)
	if err != nil {
		return err
	}
	DeadLetterWriteback, err = processor.NewDeadLetterWriteback(Microservice, deadReader, Api,
		core.NewNoOpLifecycleCallbacks(), WritebackMetrics)
	if err != nil {
		return err
	}
	DeadLetterWriteback.Hold = newStoreHold("dead-letter-writeback")
	if err := DeadLetterWriteback.Initialize(context.Background()); err != nil {
		return err
	}

	// The device-roster fact names whether a device is disabled on every roster change. The
	// durable starts at the stream tail: replaying the retention window would apply old state
	// as if it were new, and the sync above covers what was disabled before this first ran.
	// Built here, inside the callback, for the reason the write-back's reader is.
	rosterReader, err := nmgr.NewReader(streams.DeviceRoster, messaging.ReaderWithDeliverNew())
	if err != nil {
		return err
	}
	RosterConsumer = processor.NewRosterConsumer(Microservice, rosterReader, core.NewNoOpLifecycleCallbacks(),
		Api, CommandDeliveryProcessor, deadLetters)
	RosterConsumer.Hold = newStoreHold("device-roster")
	if err := RosterConsumer.Initialize(context.Background()); err != nil {
		return err
	}
	// Replay retained facts: every cancellation is bounded by the deletion time.
	deletionReader, err := nmgr.NewReader(streams.EntityDeleted)
	if err != nil {
		return err
	}
	DeletionConsumer = processor.NewDeletionConsumer(Microservice, deletionReader, core.NewNoOpLifecycleCallbacks(), Api, deadLetters)
	DeletionConsumer.Hold = newStoreHold("entity-deleted")
	if err := DeletionConsumer.Initialize(context.Background()); err != nil {
		return err
	}
	// A rolling broker auth callout may not yet grant this new subscription.
	// Bind in the consumer's background retry loop; a missing feed fails execution closed.
	Api.KillFeed = model.NewKillFeed(0, nil)
	AgentKillConsumer = processor.NewAgentKillConsumer(Microservice, nil, Api, Api.KillFeed, deadLetters)
	AgentKillConsumer.BindReader = func() (messaging.MessageReader, error) {
		return nmgr.NewReader(streams.AgentActuationKills)
	}
	return AgentKillConsumer.Initialize(context.Background())
}

// wireCommandProposals has no dependency on the service secret or service-token peers.
func wireCommandProposals(infra mscfg.InfrastructureConfiguration) {
	Api.CommandProposalsEnabled = infra.Mcp.CommandProposals
	log.Info().Bool("enabled", Api.CommandProposalsEnabled).Msg("Agent command proposals (Preview)")
	Api.AgentDecider = nil
	if um := infra.UserManagement; um.Hostname != "" && um.Port != 0 {
		Api.AgentDecider = actiongate.NewCallerDecider(um.Endpoint().GraphQLURL())
	}
	Api.ProposalTargets = nil
	if dm := infra.DeviceManagement; dm.Hostname != "" && dm.Port != 0 {
		Api.ProposalTargets = verify.NewProposalTargetReader(dm.Endpoint().GraphQLURL())
	}
}

func initAgentProposalReaper() error {
	AgentProposalReaper = core.NewPeriodicTask(Microservice.FunctionalArea, "agent-proposal-reaper", time.Minute,
		func(ctx context.Context) error {
			expired, err := Api.ExpireAgentProposals(ctx)
			if err != nil {
				log.Warn().Err(err).Msg("Could not expire agent command proposals")
			} else {
				log.Debug().Int64("expired", expired).Msg("Expired agent command proposals")
			}
			purged, purgeErr := Api.PurgeAgentProposals(ctx)
			if purgeErr != nil {
				log.Warn().Err(purgeErr).Msg("Could not purge decided agent command proposals")
			} else {
				log.Debug().Int64("purged", purged).Msg("Purged decided agent command proposals")
			}
			if err != nil {
				return err
			}
			return purgeErr
		}, core.NewNoOpLifecycleCallbacks(), Microservice.NewPeriodicTaskMetrics("agent_proposal_reaper"))
	return AgentProposalReaper.Initialize(context.Background())
}

// newStoreHold builds the Holder that keeps a consumer's messages unacked, outside their
// delivery budget, while this service's relational store cannot take writes (core/storehold).
func newStoreHold(consumer string) *storehold.Holder {
	return storehold.New(Microservice, consumer, storehold.RDBProbe(RdbManager))
}

// disabledSource builds the correction source for the disabled-device record, or nil when
// device-management or user-management cannot be reached.
//
// 🔴 NIL IS A DEGRADED MODE AND IS SAID SO. The record then rests on the device-roster facts
// alone: a device disabled before this service first ran, or whose fact never arrived, keeps
// receiving its queued commands until its next roster change. It is not a stall, because the
// alternative -- holding every command for an authority that cannot be asked -- would turn a
// configuration gap into a platform-wide outage.
func disabledSource(infra mscfg.InfrastructureConfiguration, peers *svcclient.Peers) processor.DisabledSource {
	if infra.ServiceAuth.Secret == "" {
		log.Warn().Msg("Service secret not configured -- the disabled-device record is NOT synced from device-management; commands queued for a device disabled before this service started will still be sent.")
		return nil
	}
	if infra.DeviceManagement.Hostname == "" || infra.DeviceManagement.Port == 0 ||
		infra.UserManagement.Hostname == "" || infra.UserManagement.Port == 0 {
		log.Warn().Msg("device-management or user-management endpoint not configured -- the disabled-device record is NOT synced; commands queued for a device disabled before this service started will still be sent.")
		return nil
	}
	seam, _ := peers.Seam(infra.DeviceManagement.Endpoint(), auth.DeviceRead, auth.TenantRead)
	client, dmURL := seam.Client, seam.URL
	umURL, _ := peers.URL(infra.UserManagement.Endpoint())
	log.Info().Str("deviceManagement", dmURL).
		Msg("Disabled-device sync enabled; commands queued for a disabled device are held until it is enabled.")
	return verify.NewDisabledSource(client, dmURL, umURL, Microservice.InstanceId)
}

// withPullChannels layers the HTTP pull channel over the presence reader, so a device whose type
// takes its commands over it is held for its pull instead of being published to. With no
// device-management to ask there are no pull devices, and the reader is returned as it is.
//
// 🔴 IT HOLDS THE LAYERING EVEN WHEN THERE IS NO PRESENCE READER (an instance without
// device-state), because the pull channel does not depend on presence: a nil reader becomes one
// that knows nothing and the pull devices are still held.
func withPullChannels(reader presence.Reader) presence.Reader {
	if Api == nil || Api.PullChannels == nil {
		return reader
	}
	return presence.WithPullChannels(reader, Api.PullChannels)
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
//
// 🔑 WHY ABSENCE IS NOT A STARTUP REFUSAL. Upgrades roll, and an instance config document
// supplied through instance.existingSecret, written before the deviceState block existed,
// still starts every release before this one; refusing it would stall that instance's
// upgrade on a key it was never asked for. So absence keeps its safe default, and what this
// returns instead is the name of every missing key: the WARN below names them, the safety
// gate gauge reads 0 (which the SafetyGateDisabled alert watches), and the processor counts
// and re-warns each command it then dispatches unchecked (see PresenceGateOff). Empty means
// the gate is wired.
func presenceReader(infra mscfg.InfrastructureConfiguration, peers *svcclient.Peers, gates *governance.SafetyGates) (presence.Reader, string) {
	if missing := missingPresenceConfig(infra); missing != "" {
		log.Warn().Str("missing", missing).
			Msg("The presence gate is OFF because its configuration is missing; commands to devices known to be offline will be published and silently dropped by the broker. Set the missing keys and restart command-delivery.")
		gates.Set(governance.GatePresence, false)
		return nil, missing
	}
	client := peers.Client(auth.StateRead)
	url, _ := peers.URL(infra.DeviceState.Endpoint())
	log.Info().Str("deviceState", url).
		Msg("Presence gate enabled; commands to devices a transport reports as absent will be withheld rather than published.")
	gates.Set(governance.GatePresence, true)
	return presence.NewGraphQLReader(client, url), ""
}

// missingPresenceConfig names, comma-separated and in document order, every key the presence
// gate needs that the configuration leaves unset, or "" when it has them all. Every one is
// named, not just the first, so a document missing two keys is fixed in one restart.
func missingPresenceConfig(infra mscfg.InfrastructureConfiguration) string {
	var missing []string
	if infra.ServiceAuth.Secret == "" {
		missing = append(missing, "infrastructure.serviceAuth.secret")
	}
	if infra.DeviceState.Hostname == "" {
		missing = append(missing, "infrastructure.deviceState.hostname")
	}
	if infra.DeviceState.Port == 0 {
		missing = append(missing, "infrastructure.deviceState.port")
	}
	return strings.Join(missing, ", ")
}

// tenantLifecycleGate is the deleted-tenant gate the dispatch path refuses work through: nil when
// user-management is unconfigured, with the tenant_lifecycle safety gate recorded either way.
func tenantLifecycleGate(peers *svcclient.Peers, gates *governance.SafetyGates) func(string) bool {
	return governance.NewTenantLifecycleGateFor(peers, gates)
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
func wireDeviceManagementGates(infra mscfg.InfrastructureConfiguration, peers *svcclient.Peers) {
	if infra.ServiceAuth.Secret == "" {
		log.Warn().Msg("Service secret not configured — command-delivery will NOT validate commands before enqueue (device existence and payload schema unchecked), including fleet-wide batches, and will NOT hold disruptive commands for maintenance windows; group-targeted batches will be refused.")
		return
	}
	// Guard the device-management coordinate too: a config document predating this
	// feature carries no deviceManagement block, and building http://:0/graphql
	// would fail every enqueue. Degrade to no-validation (loudly) rather than trap.
	if infra.DeviceManagement.Hostname == "" || infra.DeviceManagement.Port == 0 {
		log.Warn().Msg("device-management endpoint not configured (infrastructure.deviceManagement) — command-delivery will NOT validate commands before enqueue (device existence and payload schema unchecked), including fleet-wide batches, and will NOT hold disruptive commands for maintenance windows; group-targeted batches will be refused.")
		return
	}
	client := peers.Client(auth.DeviceRead)
	url, _ := peers.URL(infra.DeviceManagement.Endpoint())
	Api.EnqueueValidator = verify.NewEnqueueValidator(client, url)
	Api.DeviceAnchors = verify.NewDeviceAnchorLookup(client, url)
	Api.BatchValidator = verify.NewBatchValidator(client, url)
	Api.GroupTargetResolver = verify.NewGroupResolver(client, url)
	// Solar campaign triggers read each device's installed position from the same owner, on a
	// client of their own: location:installed:read is newer than the authorities above, and a
	// user-management replica of the previous release refuses to mint a token naming it. Sharing
	// the client would turn that refusal, during a rolling upgrade, into failed command
	// validation; on its own client it fails only solar planning, which says so and retries.
	// Keep the original ability to build these clients before the mint endpoint is configured.
	// Peers.Seam would additionally require user-management to be ready.
	site := svcclient.Seam{
		Client: peers.Client(auth.DeviceRead, auth.LocationInstalledRead),
		URL:    url,
	}
	Api.SiteResolver = verify.NewSiteResolver(site.Client, site.URL)
	// The maintenance-window gate shares the validator's coordinates and its fate: with no
	// device-management endpoint there is nobody to ask, and the branch above has already
	// returned and said so.
	windows := verify.NewWindowScheduler(client, url)
	Api.DispatchScheduler = windows
	Api.WindowOpenings = windows
	Api.CommandImpacts = windows
	// Which devices take their commands over the HTTP pull channel: asked of the same owner, so
	// a device type's channel is never copied here. It layers over the presence reader (see
	// createNatsComponents) and refuses delivery options for a device that is not on it.
	Api.PullChannels = verify.NewPullChannels(client, url)
	log.Info().Str("deviceManagement", url).Msg("Command enqueue will be validated against device-management (device existence + published command vocabulary + payload schema, ADR-043), for single commands and for fleet batches; entity groups will be resolved there as batch targets; disruptive commands will be held for their device type's maintenance window.")
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
func wireHeldCeilingResolver(infra mscfg.InfrastructureConfiguration, peers *svcclient.Peers) {
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
	seam, _ := peers.UserManagement(auth.TenantRead)
	client, url := seam.Client, seam.URL
	Api.HeldCeilingResolver = governance.NewHeldCommandCeilingResolver(client, url, Configuration.HeldCommandCeiling)
	log.Info().Str("userManagement", url).Int("default", Configuration.HeldCommandCeiling).
		Msg("Per-tenant held-command ceilings enabled (override, else tier, else the instance default).")
}

// Called after microservice has been initialized.
// safetyGates reports whether this service's optional safety gates are wired
// (<area>_safety_gate_enabled). Built once in afterMicroserviceInitialized.
var safetyGates *governance.SafetyGates

func afterMicroserviceInitialized(ctx context.Context) error {
	infra := Microservice.InstanceConfiguration.Infrastructure
	peers := svcclient.NewPeers(infra, Microservice.FunctionalArea)
	servicePeers = peers
	// Parse configuration.
	err := parseConfiguration()
	if err != nil {
		return err
	}

	// Auth degrades instead of failing startup (ADR-022 decision 3): fetch the
	// validator in the background and gate the data plane on readiness rather
	// than exiting when user-management is briefly unreachable (amends ADR-008).
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
	//
	// 🔴 THE START ORDER IS NOT A STYLISTIC CHOICE FOR THIS SERVICE. createNatsComponents
	// binds Api.Nudger, and the GraphQL server must not be accepting before that has run —
	// see nats_start_wiring_test.go, which pins the dependency that makes it load-bearing.
	// core/service is where the order now lives.
	Svc = service.New(Microservice, service.Spec{
		GovernanceRefreshes: true,
		Rdb: &service.RdbSpec{
			AppendOnly: nil, // Audit journal only.
			Instance:   Microservice.InstanceConfiguration.Persistence.Rdb,
			Migrations: model.Migrations,
			Config:     Configuration.RdbConfiguration,
		},
		// Runs once the rdb manager is initialized and before the other two are built. The
		// Api wraps that manager, the oncreate callback binds into the Api, and the GraphQL
		// providers carry it — so this is the one point in the sequence where this service
		// has to run.
		AfterRdb: func(_ context.Context, m *service.Managers) error {
			RdbManager = m.Rdb

			// Create RDB caches.
			model.InitializeCaches(RdbManager)

			// Wrap api around rdb manager. The default command TTL (floored positive in
			// ApplyDefaults) gives every enqueued command a terminal horizon when its creator
			// supplies no explicit expiresAt (ADR-075 L4b).
			Api = model.NewApi(RdbManager)
			wireCommandProposals(infra)
			if err := initAgentProposalReaper(); err != nil {
				return err
			}
			Api.DefaultCommandTTL = time.Duration(Configuration.DefaultCommandTTLSeconds) * time.Second

			// How many failed dispatches one command may accumulate before the release path stops
			// returning it to the queue and records FAILED instead. Floored positive in
			// ApplyDefaults and bounded at both ends in Validate, so this is always a real bound: a
			// missing or zero configured value means the platform default and NEVER "retry
			// forever", which is the behaviour it exists to end.
			Api.MaxDispatchFailures = Configuration.MaxDispatchFailures

			// Fleet-write counters. This is the only place they are built: they register on the
			// registry this Microservice owns, and the lifecycle manager guarantees this
			// initializer runs once. Every test builds its Api by literal, leaves this nil, and
			// records nothing.
			Api.BatchMetrics = model.NewBatchMetrics(Microservice)

			// The delivery processor's and write-back's instruments, built here for the same
			// reason and NOT where those two components are: they are constructed in
			// createNatsComponents, which the NATS manager invokes on EVERY start, and a
			// collector belongs to the PROCESS whereas everything that callback builds belongs
			// to the CONNECTION — registering one twice on this microservice's registry panics.
			// This initializer runs once.
			buildMetrics()

			// The held-command ceiling this instance falls back to. It is floored positive in
			// ApplyDefaults, so this is always a real bound: a missing or zero configured value
			// means the platform default and NEVER unlimited.
			//
			// It is the fallback, not the whole answer: wireHeldCeilingResolver below adds the
			// per-tenant cascade on top of it.
			Api.DefaultHeldCommandCeiling = Configuration.HeldCommandCeiling
			wireHeldCeilingResolver(infra, peers)

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
			wireDeviceManagementGates(infra, peers)
			// The campaign scheduler needs only the Api, so it is built here rather than in
			// the NATS callback: it reaches the database and device-management, never the broker.
			CampaignScheduler = processor.NewCampaignScheduler(Microservice, Api, core.NewNoOpLifecycleCallbacks())
			return CampaignScheduler.Initialize(ctx)
		},
		Nats: &service.NatsSpec{OnCreate: createNatsComponents},
		GraphQL: &service.GraphQLSpec{
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
	safetyGates = Svc.SafetyGates()
	if err := Svc.Initialize(ctx); err != nil {
		return err
	}
	// Published for the rest of the service, which names these managers directly.
	NatsManager, GraphQLManager = Svc.Nats, Svc.GraphQL
	return nil
}

// Called after microservice has been started.
func afterMicroserviceStarted(ctx context.Context) error {
	// Rdb, then NATS, then the GraphQL server. That order, and the reason the broker has to
	// be up before the HTTP server accepts traffic, now live in core/service. For this
	// service the reason is not hypothetical: createNatsComponents binds Api.Nudger, and a
	// createCommand mutation arriving before it has run takes the nil branch and waits for
	// the sweep instead of dispatching promptly.
	if err := Svc.Start(ctx); err != nil {
		return err
	}

	// Both of these are built by the NATS manager's oncreate callback, so they do not
	// exist until the line above has run.
	err := CommandDeliveryProcessor.Start(ctx)
	if err != nil {
		return err
	}

	err = DeadLetterWriteback.Start(ctx)
	if err != nil {
		return err
	}

	if err := RosterConsumer.Start(ctx); err != nil {
		return err
	}
	if err := DeletionConsumer.Start(ctx); err != nil {
		return err
	}
	if err := AgentKillConsumer.Start(ctx); err != nil {
		return err
	}
	if err := AgentProposalReaper.Start(ctx); err != nil {
		return err
	}
	// Last: a pass creates batches, which hand commands to the processor started above.
	return CampaignScheduler.Start(ctx)
}

// Called before microservice has been stopped.
func beforeMicroserviceStopped(ctx context.Context) error {
	if AgentProposalReaper != nil {
		if err := AgentProposalReaper.Stop(ctx); err != nil {
			return err
		}
	}
	if AgentKillConsumer != nil {
		if err := AgentKillConsumer.Stop(ctx); err != nil {
			return err
		}
	}
	// The scheduler first: it creates commands for the processor stopped below.
	if CampaignScheduler != nil {
		if err := CampaignScheduler.Stop(ctx); err != nil {
			return err
		}
	}
	// Stop the deletion consumer before its database and broker dependencies.
	if DeletionConsumer != nil {
		if err := DeletionConsumer.Stop(ctx); err != nil {
			return err
		}
	}
	// The roster consumer can resume a device through the processor stopped below.
	if RosterConsumer != nil {
		if err := RosterConsumer.Stop(ctx); err != nil {
			return err
		}
	}

	// Stop the dead-letter write-back before the broker it reads from and the database it
	// writes to, in the reverse of the start order.
	err := DeadLetterWriteback.Stop(ctx)
	if err != nil {
		return err
	}

	// Stop command delivery processor.
	err = CommandDeliveryProcessor.Stop(ctx)
	if err != nil {
		return err
	}

	// GraphQL, then NATS, then Rdb, so that the last mutation of a rolling restart finishes
	// against a broker connection that is still whole rather than one that is already
	// draining. Nothing in this service's GraphQL plane publishes on the caller's own
	// goroutine today — CreateCommand's dispatch nudge hands the device to the processor's
	// queue, and that processor is stopped above — but the enqueue path is the one that
	// grows a publish, and the order that survives it costs nothing to hold now.
	//
	// core/service stops them in exactly that order, for every service. Pinned next door in
	// graphql_shutdown_order_test.go, which drives this function.
	return Svc.Stop(ctx)
}

// Called before microservice has been terminated.
func beforeMicroserviceTerminated(ctx context.Context) error {
	if AgentProposalReaper != nil {
		if err := AgentProposalReaper.Terminate(ctx); err != nil {
			return err
		}
	}
	if AgentKillConsumer != nil {
		if err := AgentKillConsumer.Terminate(ctx); err != nil {
			return err
		}
	}
	if CampaignScheduler != nil {
		if err := CampaignScheduler.Terminate(ctx); err != nil {
			return err
		}
	}
	if DeletionConsumer != nil {
		if err := DeletionConsumer.Terminate(ctx); err != nil {
			return err
		}
	}
	if RosterConsumer != nil {
		if err := RosterConsumer.Terminate(ctx); err != nil {
			return err
		}
	}

	// Terminate the dead-letter write-back before the nats manager it reads through.
	err := DeadLetterWriteback.Terminate(ctx)
	if err != nil {
		return err
	}

	// GraphQL, then NATS, then Rdb — the same order as the stop above.
	//
	// This service used to terminate NATS first. The change is not observable:
	// GraphQLManager.ExecuteTerminate returns nil and carries no callback, so the only
	// thing that moved is a no-op, and NATS still terminates before Rdb — which is the pair
	// that actually closes handles, and which the write-back above still precedes.
	return Svc.Terminate(ctx)
}
