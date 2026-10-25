// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/graphql"
	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/processor"
	"github.com/devicechain-io/dc-device-management/ruleverify"
	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/auth"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/governance"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/devicechain-io/dc-microservice/svcclient"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

var (
	Microservice  *core.Microservice
	Configuration *config.DeviceManagementConfiguration

	// Svc owns the three managers below and the order of all four of their lifecycle
	// phases. They stay named here because the rest of this service refers to them
	// directly; Svc is what decides when each one runs.
	Svc *service.Service

	RdbManager     *rdb.RdbManager
	GraphQLManager *gqlcore.GraphQLManager
	NatsManager    *messaging.NatsManager

	Api       *model.Api
	CachedApi *model.CachedApi

	// DeviceSecretKey digests and verifies device credential secrets (MQTT passwords). It is
	// derived from the instance root key in AfterRdb, before anything can store or compare
	// a secret.
	DeviceSecretKey *credential.DeviceSecretKey

	// EnrollmentKeys digest and verify provisioning profile secrets and device claim secrets,
	// each under a key of its own. They are different keys from DeviceSecretKey, derived from
	// the same root key at the same point.
	EnrollmentKeys *credential.EnrollmentSecretKeys

	// SecretTransit opens the sealed credential secrets inbound events carry; derived from
	// the same root key, in the same step, as DeviceSecretKey.
	SecretTransit *credential.SecretTransit

	InboundEventsReaders   []messaging.MessageReader
	InboundEventsProcessor *processor.InboundEventsProcessor
	ResolvedEventsWriter   messaging.OrderedWriter
	FailedEventsWriter     messaging.OrderedWriter

	// AlarmEventsWriter publishes alarm state-change events (ADR-041). It backs the
	// publisher injected into the shared Api so every alarm transition — the DETECT edge
	// integrator or an operator — emits onto the alarm-events stream.
	AlarmEventsWriter messaging.MessageWriter

	// RaiseAlarmConsumer consumes REACT raise-alarm requests (ADR-051 slice 5c / ADR-057) from
	// event-processing's dispatcher and folds each edge into the (device, alarmKey) alarm's
	// contributor set — the sole alarm-raise path since the 6d cutover retired the measurement
	// evaluator; a rule-driven alarm shares the Alarm object, ack/clear, and notification flow.
	RaiseAlarmReader   messaging.MessageReader
	RaiseAlarmConsumer *processor.RaiseAlarmConsumer

	// DeviceReportsConsumer consumes what devices report on their device-reports channel
	// (configuration sync and status) and records it; the revisions it answers with go out
	// on device-desired through the publisher injected into the shared Api.
	DeviceReportsReader   messaging.MessageReader
	DeviceReportsConsumer *processor.DeviceReportsConsumer
	DeviceDesiredWriter   messaging.MessageWriter

	// CalloutResponder answers NATS auth-callout requests for device connections
	// (ADR-025). Non-nil only when the broker is configured for auth callout (the
	// issuer seed is present in the instance config).
	CalloutResponder *processor.CalloutResponder
	// MqttStoreBackoff samples the broker's MQTT message store and has the callout refuse
	// device connects while it is above its high-water mark. Non-nil only when the callout
	// runs and infrastructure.nats.mqttFailClosed is on.
	MqttStoreBackoff *messaging.MqttStoreBackoff
	// MqttStoreBackoffMetrics and CalloutRefusedStoreFull are the back-off's series, built
	// once on the initialize path (buildMetrics) and only with the setting on.
	MqttStoreBackoffMetrics *messaging.MqttStoreBackoffMetrics
	CalloutRefusedStoreFull prometheus.Counter
	// CalloutMetrics are the callout's load series (in flight, refused busy, plaintext requests).
	CalloutMetrics *processor.CalloutMetrics

	// OwnershipDrain publishes the device ownership facts writes queue in their own
	// transactions (ADR-081, model/ownership_outbox.go), one replica at a time.
	OwnershipDrain        *core.PeriodicTask
	OwnershipDrainMetrics *core.PeriodicTaskMetrics

	// The Prometheus instruments this service exports. All are built ONCE, in the
	// initialize phase, and shared by every component the NATS manager's oncreate
	// callback builds. See buildMetrics.
	ResolveMetrics       processor.ResolveMetrics
	RaiseAlarmMetrics    processor.RaiseAlarmMetrics
	DeviceReportsMetrics processor.DeviceReportsMetrics
	AlarmEventMetrics    processor.AlarmEventMetrics
	GeoFencePublishFails prometheus.Counter
	// FactPublishFails counts detection-rule, device-roster and device-attribute facts that
	// could not be published, by fact. Each one is a change the detection engine learns of at
	// its next reconcile against this service rather than at once.
	FactPublishFails *prometheus.CounterVec
	// CredentialChecks counts the auth callout's MQTT password checks by outcome. The
	// callout's credential.Checker exports its store_full series at zero when it is built.
	CredentialChecks *prometheus.CounterVec
	// CredentialMisconfigured counts device credential checks refused because the stored
	// secret is one no presented secret can match (none stored, or a digest this key did
	// not make), by path. It is separate from wrong passwords on purpose: those are the
	// device's mistake, these are the operator's.
	CredentialMisconfigured *prometheus.CounterVec

	// DeadLetters is this service's identity as a dead-letter producer: the source its
	// letters are stamped with and the ONE dead_letter_lost_total both of its arms count
	// losses on. Built by core/service (Svc.DeadLetters), which the max-delivery recorder shares.
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

// Parses the configuration from raw bytes.
func parseConfiguration() error {
	config := &config.DeviceManagementConfiguration{}
	err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, config)
	if err != nil {
		return err
	}
	Configuration = config
	log.Info().
		Int("poolSize", rdb.EffectiveMaxOpenConnections(config.RdbConfiguration)).
		Int("resolutionWorkers", config.Resolution.Workers).
		Int("raiseAlarmWorkers", config.RaiseAlarm.Workers).
		Msg("Database worker counts in effect")
	return nil
}

// Create messaging components used by this microservice.
// buildMetrics creates this service's Prometheus instruments exactly once.
//
// 🔴 IT IS CALLED FROM THE INITIALIZE PHASE, NOT FROM createNatsComponents, WHERE THE
// COMPONENTS THAT READ THEM ARE BUILT. That callback is invoked by the NATS manager on
// EVERY start, and a collector belongs to the PROCESS whereas everything that callback
// builds belongs to the CONNECTION — registering one twice on this microservice's
// registry panics. Initialize is where the process's own singletons are made, which is
// what makes this the safe half; messaging.NewNatsManager carries the reasoning.
func buildMetrics() {
	ResolveMetrics = processor.NewResolveMetrics(Microservice)
	RaiseAlarmMetrics = processor.NewRaiseAlarmMetrics(Microservice)
	DeviceReportsMetrics = processor.NewDeviceReportsMetrics(Microservice)
	AlarmEventMetrics = processor.NewAlarmEventMetrics(Microservice)
	// core/service built it: the platform's max-delivery recorder letters under it too,
	// and a second NewProducer here would panic on the duplicate counter.
	DeadLetters = Svc.DeadLetters
	CredentialChecks = Microservice.NewCounterVec("credential_checks_total",
		"MQTT password connect checks by outcome. outcome=\"throttled\" is a connect refused "+
			"because its username is inside a backoff delay; outcome=\"unavailable\" means the "+
			"attempt store could not be reached and the connect was refused; outcome=\"store_full\" "+
			"means the attempt store was full and the connect was checked WITHOUT its backoff.",
		[]string{"kind", "outcome"})
	CredentialMisconfigured = Microservice.NewCounterVec("credential_misconfigured_total",
		"Device credential checks refused because the stored secret can never match: none is "+
			"stored, or its digest was made under a different key than this instance's root key "+
			"derives (most likely a database restored next to the wrong root key). path=\"connect\" "+
			"is the MQTT auth callout, path=\"event\" the per-event check. Wrong passwords are not "+
			"counted here.",
		[]string{"path"})
	for _, path := range []string{model.CredentialPathConnect, model.CredentialPathEvent} {
		CredentialMisconfigured.WithLabelValues(path)
	}
	OwnershipDrainMetrics = Microservice.NewPeriodicTaskMetrics("ownership_drain")
	MqttStoreBackoffMetrics, CalloutRefusedStoreFull = buildMqttStoreBackoffMetrics()
	CalloutMetrics = processor.NewCalloutMetrics(Microservice)
	GeoFencePublishFails = Microservice.NewCounter(
		"geofence_set_publish_failures_total",
		"Geofence-set manifests that could not be published — a marshal error, a broker refusal, or a transport fault. Each one means event-processing was not told about a fence edit, so containment for that tenant holds its previous fence set until a reconcile sweep repairs it. A sustained non-zero rate means fence edits are not reaching the detection engine.")
	FactPublishFails = Microservice.NewCounterVec("fact_publish_failures_total",
		"Detection-rule, device-roster, device-attribute, device-desired and device-ownership notifications that could not be published, by fact — a marshal error, a broker refusal or a transport fault. The change itself committed; the detection engine learns of it at its next reconcile against this service, a few minutes later, instead of at once (a device-desired miss is repaired by the device's next sync; a device-ownership miss stays queued and is retried by the ownership drain every second). A sustained rate means changes are taking minutes to take effect.",
		[]string{"fact"})
	// Every label value exists from startup, so an alert on the rate sees a zero series rather
	// than no series on a healthy instance.
	for _, fact := range factPublishLabels {
		FactPublishFails.WithLabelValues(fact).Add(0)
	}
}

// buildMqttStoreBackoffMetrics registers the back-off's series and the callout's count of the
// connects it refused, with infrastructure.nats.mqttFailClosed on, and nothing with it off.
//
// 🔴 ON THE INITIALIZE PATH, NOT WHERE THE BACK-OFF STARTS. afterMicroserviceStarted runs on
// every start, and a collector registered there a second time panics on the duplicate.
func buildMqttStoreBackoffMetrics() (*messaging.MqttStoreBackoffMetrics, prometheus.Counter) {
	if !Microservice.InstanceConfiguration.Infrastructure.Nats.MqttFailClosed {
		return nil, nil
	}
	return messaging.NewMqttStoreBackoffMetrics(Microservice),
		Microservice.NewCounter("callout_refused_store_full_total",
			"Device auth-callout requests refused with the generic denial because the broker's MQTT message store "+
				"was above its high-water mark.")
}

// storeBackoffGate is the half of the auth-callout responder the back-off is wired into.
type storeBackoffGate interface {
	WithStoreBackoff(storeFull func() bool, refused prometheus.Counter) *processor.CalloutResponder
}

// wireMqttStoreBackoff starts the back-off ahead of overflow and gates the callout on it, with
// the fail-closed MQTT acknowledgement on (on). Off, it starts nothing, gates nothing and returns
// nil, so the callout path is the one the setting has never touched. Stop the back-off it
// returns on the stop path; a nil one is a no-op.
func wireMqttStoreBackoff(on bool, nmgr *messaging.NatsManager, metrics *messaging.MqttStoreBackoffMetrics,
	refused prometheus.Counter, gate storeBackoffGate) *messaging.MqttStoreBackoff {
	if !on {
		return nil
	}
	b := nmgr.StartMqttStoreBackoff(context.Background(), metrics, messaging.MqttStoreBackoffInterval)
	gate.WithStoreBackoff(b.Engaged, refused)
	return b
}

// factPublishLabels are the fact label values of FactPublishFails, one per fact writer.
var factPublishLabels = []string{factDetectionRules, factDeviceRoster, factDeviceAttribute, factDeviceDesired, factDeviceOwnership}

const (
	factDetectionRules  = "detection_rules"
	factDeviceRoster    = "device_roster"
	factDeviceAttribute = "device_attribute"
	// factDeviceDesired is a configuration revision that could not be put on the device's
	// device-desired channel. Unlike the facts above, the repair is the DEVICE's next sync.
	factDeviceDesired = "device_desired"
	// factDeviceOwnership is a device ownership-changed fact (ADR-081) that could not be
	// published. It stays queued (model/ownership_outbox.go) and the next drain pass
	// publishes it again, so a sustained rate is a fact being delayed, not lost.
	factDeviceOwnership = "device_ownership"
)

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

// newInboundEventsProcessor assembles the inbound processor from this service's globals (the
// inbound readers createInboundReaders built among them), its resolver pool sized by the
// loaded configuration. It is a function of its own so
// resolution_wiring_test.go can check the configuration reaches the processor: a processor
// test builds the processor itself, so a service that stopped passing resolution.workers
// would run the default while every other test stayed green. Its loss counter is the service's
// dead-letter producer, for the same reason.
func newInboundEventsProcessor() *processor.InboundEventsProcessor {
	return processor.NewInboundEventsProcessor(Microservice, InboundEventsReaders, ResolvedEventsWriter, FailedEventsWriter,
		core.NewNoOpLifecycleCallbacks(), CachedApi, Configuration.DeviceAuthMode,
		time.Duration(Configuration.MaxEventFutureSkewSeconds)*time.Second, ResolveMetrics,
		processor.WithResolvers(Configuration.Resolution.Workers),
		processor.WithDeadLetters(DeadLetters),
		processor.WithSecretTransit(SecretTransit),
		processor.WithGatewayNotices(messaging.NewGatewayNoticePublisher(
			func() *nats.Conn { return NatsManager.Conn() }, Microservice.InstanceId)))
}

// buildApis builds the service's two Apis over the relational store and the broker: the
// plain Api every GraphQL resolver writes through, and the caching decorator the inbound
// resolution path reads through.
//
// It is a function of its own so that the wiring tests build the Apis exactly as the
// service does. 🔴 THE EVICTOR LINE IS THE ONE THAT MATTERS: every write that invalidates a
// cache — a delete's hot-path entries, and a changed credential's or device's cached
// credentials — evicts through Api.CacheEvictor, and the GraphQL mutations run on the plain
// Api. Without it every eviction is silently skipped, a revoked credential keeps
// authenticating events from memory until its copy expires, and only a test that builds
// the Apis through here can see it.
func buildApis(nmgr *messaging.NatsManager, rdbm *rdb.RdbManager, key *credential.DeviceSecretKey, enrollment *credential.EnrollmentSecretKeys,
	cfg *config.DeviceManagementConfiguration) (*model.Api, *model.CachedApi, error) {
	// Create NATS KV caches TTL'd from configuration (ADR-022 review B2), and the
	// in-process credential cache.
	caches, err := model.InitializeCaches(nmgr, cfg)
	if err != nil {
		return nil, nil, err
	}

	// Wrap api around rdb manager, then wrap a caching decorator over it for the hot
	// inbound-event resolution path.
	api := model.NewApi(rdbm)
	api.EnableCeilingMetrics(nmgr.Microservice)
	api.DeviceSecretKey = key
	api.EnrollmentKeys = enrollment
	api.MisconfiguredSecrets = CredentialMisconfigured
	// The Checker device self-registration compares through. It is built from the SAME two keys
	// the Api digests with, so that a digest it writes is one this verifies. Without both keys
	// there is nothing to compare under, and the Api answers ErrProvisioningUnavailable rather
	// than fall back to comparing anything itself.
	if key != nil && enrollment != nil {
		if api.ProvisioningChecker, err = processor.NewProvisioningChecker(key, enrollment); err != nil {
			return nil, nil, err
		}
	}
	cached := model.NewCachedApi(api, caches)
	// The write paths evict the hot-path caches through this seam (ADR-044 F2). The GraphQL
	// mutations run on the plain *Api, so the evictor is wired onto it.
	api.CacheEvictor = cached
	// An instance that runs the MQTT auth callout mints device sessions that outlive the
	// credential that opened them, so a withdrawn credential or a disabled device must also
	// END its sessions (model.SessionKicker). Without the callout there are no sessions of
	// ours to end, and the kicker stays nil.
	if nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.Auth.CalloutEnabled() {
		api.SessionKicker = natsSessionKicker{nmgr: nmgr, instanceId: nmgr.Microservice.InstanceId}
	}
	return api, cached, nil
}

// natsSessionKicker ends a device's broker sessions by asking the service that holds the
// system-account login (event-sources) over the data-plane connection. The connection is read
// at call time: it is replaced on a reconnect, and this is built before it exists.
type natsSessionKicker struct {
	nmgr       *messaging.NatsManager
	instanceId string
}

func (k natsSessionKicker) KickDeviceSessions(ctx context.Context, tenant, reason string, deviceTokens []string) error {
	return messaging.NewSessionKickRequester(k.nmgr.Conn(), k.instanceId).KickDeviceSessions(ctx, tenant, reason, deviceTokens)
}

// createPublishWriters builds the resolved and failed events writers, each with the
// configured publish window (resolution.publishWindow). It is a function of its own so
// resolution_wiring_test.go can check the configured window reaches the writers: a processor
// test builds its own writers, so a service that stopped passing the setting would run the
// default while every other test stayed green.
func createPublishWriters(nmgr *messaging.NatsManager) error {
	// Add and initialize resolved events writer. Both inbound publish loops pipeline their
	// publishes (see config.DefaultPublishWindow for why the window is a throughput limit);
	// the processor closes these writers when it stops, before the connection drains.
	revents, err := nmgr.NewOrderedWriter(streams.ResolvedEvents, messaging.WriterResolution, Configuration.Resolution.PublishWindowSize())
	if err != nil {
		return err
	}
	ResolvedEventsWriter = revents

	// Add and initialize failed events writer.
	fevents, err := nmgr.NewOrderedWriter(streams.FailedEvents, messaging.WriterResolution, Configuration.Resolution.PublishWindowSize())
	if err != nil {
		return err
	}
	FailedEventsWriter = fevents
	return nil
}

// createInboundReaders builds the configured number of pull readers on the inbound-events
// durable (resolution.inboundReaders), each subscribed to the cross-tenant wildcard. They are
// readers of ONE consumer: the broker hands each message to exactly one of them, and each
// keeps its own pull in flight, so the pod reads as deep as that many replicas would while
// keeping one set of caches. It is a function of its own so resolution_wiring_test.go can
// check the setting reaches the broker: a processor test builds its own readers.
func createInboundReaders(nmgr *messaging.NatsManager) error {
	n := Configuration.Resolution.InboundReaderCount()
	readers := make([]messaging.MessageReader, 0, n)
	for i := 0; i < n; i++ {
		r, err := nmgr.NewReader(streams.InboundEvents)
		if err != nil {
			return err
		}
		readers = append(readers, r)
	}
	InboundEventsReaders = readers
	return nil
}

func createNatsComponents(nmgr *messaging.NatsManager) error {
	// Create the readers for inbound events (wildcard across tenants).
	if err := createInboundReaders(nmgr); err != nil {
		return err
	}

	if err := createPublishWriters(nmgr); err != nil {
		return err
	}

	// The ADR-024 dead-letter writer, built once and shared by every arm in this service.
	// It is created BEFORE the arms that use it so a deployment that cannot create the
	// dead-letter stream fails at startup, beside every other stream this service needs,
	// rather than at the first failure — the one moment an arm has to work.
	deadLetters, err := newDeadLetterSink(nmgr)
	if err != nil {
		return err
	}

	// Add the alarm-events writer and inject a publisher over it into the shared Api
	// (ADR-041). CachedApi embeds this same *Api, so both the DETECT edge integrator and
	// the GraphQL operator mutations emit alarm state-change events through it.
	//
	// The publisher carries the ADR-024 arm for the LAST hop of the alarm flow: an alarm
	// whose state-change event never reaches the stream pages nobody, and the subscriber
	// cannot notice — notification-management's durable only advances over messages that
	// were published — so a give-up here is dead-lettered and counted rather than logged.
	aevents, err := nmgr.NewWriter(streams.AlarmEvents)
	if err != nil {
		return err
	}
	AlarmEventsWriter = aevents
	Api.AlarmPublisher = processor.NewAlarmEventWriter(AlarmEventsWriter, deadLetters,
		AlarmEventMetrics)

	// Add the entity-deleted writer and inject a publisher over it into the shared
	// Api (ADR-044): the delete paths (deleteEdgeEntity) emit an entity-deletion
	// event so event-management can reconcile dangling event_anchors.
	devents, err := nmgr.NewWriter(streams.EntityDeleted)
	if err != nil {
		return err
	}
	Api.EntityDeletedPublisher = processor.NewEntityDeletedWriter(devents)

	// Add the detection-rules-published writer and inject a publisher into the shared
	// Api (ADR-051 slice 4b-3): PublishDeviceProfile emits the enabled detection rules
	// frozen into a new version so event-processing's DETECT engine runs them.
	drpub, err := nmgr.NewWriter(streams.DetectionRulesPublished)
	if err != nil {
		return err
	}
	Api.DetectionRulesPublishedPublisher = processor.NewDetectionRulesPublishedWriter(drpub,
		FactPublishFails.WithLabelValues(factDetectionRules))

	// Add the device-roster writer and inject a publisher into the shared Api (ADR-051
	// slice 4c-2): device create/re-type emit a roster fact so event-processing's DETECT
	// engine can arm absence for a device that has never reported (the dead-man roster).
	rosterpub, err := nmgr.NewWriter(streams.DeviceRoster)
	if err != nil {
		return err
	}
	Api.DeviceRosterPublisher = processor.NewDeviceRosterWriter(rosterpub,
		FactPublishFails.WithLabelValues(factDeviceRoster))

	// Add the device-ownership writer (ADR-081): every write that may change a device's
	// tracked relationships publishes the device's complete current anchor set, so a
	// projection keyed by device can keep a customer-scoped stamp current between events.
	ownershippub, err := nmgr.NewWriter(streams.DeviceOwnership)
	if err != nil {
		return err
	}
	Api.DeviceOwnershipPublisher = processor.NewDeviceOwnershipWriter(ownershippub,
		FactPublishFails.WithLabelValues(factDeviceOwnership))

	// Add the device-attribute writer and inject a publisher into the shared Api (ADR-051
	// slice 4c-3): a numeric, platform-set device attribute upsert/delete emits a fact so
	// event-processing can resolve a dynamic detection threshold from the device's own
	// attribute instead of a compile-time literal.
	attrpub, err := nmgr.NewWriter(streams.DeviceAttribute)
	if err != nil {
		return err
	}
	Api.DeviceAttributePublisher = processor.NewDeviceAttributeWriter(attrpub,
		FactPublishFails.WithLabelValues(factDeviceAttribute))

	// Add the device-desired writer and inject a publisher over it into the shared Api: a
	// SHARED write that mints a per-device configuration revision delivers it to the device
	// after the transaction commits. Best-effort; the device's next sync repairs a miss.
	desiredpub, err := nmgr.NewWriter(streams.DeviceDesired)
	if err != nil {
		return err
	}
	DeviceDesiredWriter = desiredpub
	Api.DeviceDesiredPublisher = processor.NewDesiredConfigurationWriter(desiredpub,
		FactPublishFails.WithLabelValues(factDeviceDesired))

	// Add the geofence-set writer and inject a publisher into the shared Api (ADR-078): a
	// change to the fence SET — created, geometry edited, deleted — mints a new fence-set
	// version, and this fact carries that
	// version's FROZEN fence set to event-processing's live containment projection — so a
	// location event's inFence resolves from memory and never blocks the DETECT loop on a
	// read back into this service.
	//
	// It carries the MANIFEST of each version — which fences, and the content address of
	// each one's geometry — not the geometry itself, so no fence set can outgrow one
	// message. maxMsgSize is passed only so the writer can warn at startup about a ceiling
	// configured below a full manifest; the counter reports every announcement that did not
	// reach the wire, which is the question an operator actually has when containment stops
	// tracking fence edits.
	fencepub, err := nmgr.NewWriter(streams.GeoFenceSetManifest)
	if err != nil {
		return err
	}
	//
	// The failure counter is NOT built here: it comes from buildMetrics, which runs in
	// the initialize phase, because a counter belongs to the process while everything
	// this callback builds belongs to the connection.
	Api.GeoFenceSetPublisher = processor.NewGeoFenceSetWriter(fencepub,
		Microservice.InstanceConfiguration.Infrastructure.Nats.StreamMaxMsgSize,
		GeoFencePublishFails)

	// Add and initialize inbound events processor.
	InboundEventsProcessor = newInboundEventsProcessor()
	err = InboundEventsProcessor.Initialize(context.Background())
	if err != nil {
		return err
	}

	// Reader + consumer for REACT raise-alarm requests (ADR-051 slice 5c / ADR-057): the raise-alarm
	// subject is the sole alarm-raise path since the 6d cutover retired the measurement evaluator.
	rareader, err := nmgr.NewReader(streams.RaiseAlarm)
	if err != nil {
		return err
	}
	RaiseAlarmReader = rareader
	// The ADR-024 arm for the hop AFTER REACT. REACT dead-letters a detection whose
	// actions it could not dispatch; an edge it dispatched successfully could still die
	// here, with the same consequence — a raise that does not re-emit until the condition
	// re-breaches, a resolve that strands its alarm.
	RaiseAlarmConsumer = newRaiseAlarmConsumer(RaiseAlarmReader, deadLetters)
	if err = RaiseAlarmConsumer.Initialize(context.Background()); err != nil {
		return err
	}

	// Reader + consumer for what devices report on device-reports: configuration sync and
	// status. A message it cannot understand is dead-lettered (the stream's declared kind).
	drreader, err := nmgr.NewReader(streams.DeviceReports)
	if err != nil {
		return err
	}
	DeviceReportsReader = drreader
	DeviceReportsConsumer = newDeviceReportsConsumer(DeviceReportsReader, deadLetters)
	if err = DeviceReportsConsumer.Initialize(context.Background()); err != nil {
		return err
	}

	return nil
}

// ownershipDrainInterval is how often the ownership drain looks for queued facts. It bounds
// how long a committed ownership change waits to reach device-state while the broker is
// healthy; one pass on an empty queue is a lock round trip and one indexed read.
const ownershipDrainInterval = time.Second

// initOwnershipDrain builds and initializes the ownership drain. Once, in the initialize
// phase: the task belongs to the process, and the publisher it drains through is re-wired by
// createNatsComponents on every broker start.
func initOwnershipDrain() error {
	OwnershipDrain = core.NewPeriodicTask(Microservice.FunctionalArea, "ownership-drain", ownershipDrainInterval,
		func(ctx context.Context) error { return Api.DrainOwnershipFactsLocked(ctx) },
		core.NewNoOpLifecycleCallbacks(), core.WithImmediateFirstPass(), core.WithPassMetrics(OwnershipDrainMetrics))
	return OwnershipDrain.Initialize(context.Background())
}

// newDeviceReportsConsumer assembles the device-reports consumer from this service's globals.
// It hands the consumer the plain Api, not the cached one: a sync or a report resolves its
// device by token, and a device deleted on another replica must stop resolving at once rather
// than when a cached copy expires.
func newDeviceReportsConsumer(reader messaging.MessageReader, dead *deadletter.Sink) *processor.DeviceReportsConsumer {
	return processor.NewDeviceReportsConsumer(Microservice, reader,
		core.NewNoOpLifecycleCallbacks(), Api, dead, DeviceReportsMetrics)
}

// newRaiseAlarmConsumer assembles the raise-alarm consumer from this service's globals. It
// is a function of its own so raise_alarm_wiring_test.go can check which Api it is given.
//
// 🔴 IT GETS THE PLAIN Api, NOT THE CACHED ONE. The consumer drops an edge for a device
// that no longer exists (deleted between detection and dispatch), and it learns that by
// resolving the device token. Through the cached Api that read can be answered from another
// replica's process memory if the eviction message for the delete was lost (up to
// inMemoryCache.ttlSeconds), and the edge would then be applied to a device row that is gone. It is one indexed read
// per rule edge, far off the per-event path, so the database answers it every time.
//
// The worker count is read from Configuration here rather than passed in, so the same test
// sees the setting reach the consumer: a call site handing over a literal would build and run
// with one lane whatever raiseAlarm.workers says.
func newRaiseAlarmConsumer(reader messaging.MessageReader, dead *deadletter.Sink) *processor.RaiseAlarmConsumer {
	return processor.NewRaiseAlarmConsumer(Microservice, reader,
		core.NewNoOpLifecycleCallbacks(), Api, dead, RaiseAlarmMetrics, Configuration.RaiseAlarm.Workers)
}

// wireDetectionRuleValidator injects the ADR-044 sync-validation gate onto the shared
// Api when the shared service secret AND event-processing's coordinate are both
// configured, so profile publish compiles its draft detection rules against
// event-processing (ADR-051 slice 4b). It logs the enabled/disabled mode at startup so a
// misconfigured deploy (empty secret, or a config predating this feature) is visible
// rather than silently skipping validation — mirroring command-delivery's device check.
func wireDetectionRuleValidator(api *model.Api, infra mscfg.InfrastructureConfiguration, gates *governance.SafetyGates) {
	// Assigned only when non-nil: a nil *Validator stored in the interface field is NOT a nil
	// interface, so profile publish would call a method on a nil receiver instead of skipping
	// validation.
	if v := detectionRuleValidator(infra, gates); v != nil {
		api.DetectionRuleValidator = v
	}
}

// detectionRuleValidator is the decision wireDetectionRuleValidator applies: the validator, or nil
// (the gate is OFF) when the service secret or event-processing's coordinate is absent. It records
// the decision on the rule_validation safety gate either way. A separate function, taking the
// configuration and the gates, so the decision is testable without a started service.
func detectionRuleValidator(infra mscfg.InfrastructureConfiguration, gates *governance.SafetyGates) *ruleverify.Validator {
	if infra.ServiceAuth.Secret == "" {
		log.Warn().Msg("Service secret not configured — device profile publish will NOT validate detection rules against event-processing (ADR-051 slice 4b disabled).")
		gates.Set(governance.GateRuleValidation, false)
		return nil
	}
	if infra.EventProcessing.Hostname == "" || infra.EventProcessing.Port == 0 {
		log.Warn().Msg("event-processing endpoint not configured (infrastructure.eventProcessing) — device profile publish will NOT validate detection rules (ADR-051 slice 4b disabled).")
		gates.Set(governance.GateRuleValidation, false)
		return nil
	}
	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "device-management", []string{string(auth.DeviceRead)})
	url := fmt.Sprintf("http://%s:%d/graphql", infra.EventProcessing.Hostname, infra.EventProcessing.Port)
	gates.Set(governance.GateRuleValidation, true)
	log.Info().Str("eventProcessing", url).Msg("Device profile publish will validate detection rules against event-processing (ADR-051 slice 4b).")
	return ruleverify.NewValidator(client, url)
}

// calloutTenantGate is the deleted-tenant gate the device auth callout refuses connects through:
// nil when user-management is unconfigured, with the tenant_lifecycle safety gate recorded.
func calloutTenantGate(infra mscfg.InfrastructureConfiguration, gates *governance.SafetyGates) func(string) bool {
	return governance.NewTenantLifecycleGate(infra.UserManagement, infra.ServiceAuth.Secret, "device-management", gates)
}

// wireGeoFenceCapsResolver injects the PER-TENANT geofence caps onto the shared Api: an
// operator's override, resolving to the tenant's tier, resolving to the platform default
// (ADR-023 governance, packaged per ADR-065). Three caps travel together because a fence
// set has three independent costs — one fence's compile, the manifest's size on the wire,
// and the whole set's footprint in a geometry cache every tenant shares.
//
// 🔑 A NIL RESOLVER IS SAFE, NOT DISABLED, AND THAT IS WHY THE WARNING BELOW SAYS WHICH
// NUMBERS ARE IN FORCE RATHER THAN THAT SOMETHING IS OFF. Api.geoFenceCaps answers the
// platform defaults when nothing is wired, which are exactly the numbers this service
// enforced as hard constants before the caps became tier-driven — so an instance with no
// user-management coordinate meters every tenant the way the previous release did, rather
// than not at all. The direction a governance limit must fail.
//
// Guarded on the same two coordinates as wireDetectionRuleValidator, and for the same
// reason: a config document predating this feature carries no usable user-management block,
// and building http://:0/graphql would make every fence mutation pay a doomed round trip.
//
// 🔴 THE SCOPE IS tenant:read, NOT device:read. wireDetectionRuleValidator builds a client
// with device:read because it talks to EVENT-PROCESSING; this one reads user-management's
// tenantGovernance, which is the same door command-delivery reads its held-command ceiling
// through and gates on the same scope.
func wireGeoFenceCapsResolver() {
	caps := governance.DefaultGeoFenceCaps()
	infra := Microservice.InstanceConfiguration.Infrastructure
	if infra.ServiceAuth.Secret == "" {
		log.Warn().Int("geoFencePositionCeiling", caps.PositionCeiling).
			Int("geoFenceCeiling", caps.FenceCeiling).Int("geoFencePositionBudget", caps.PositionBudget).
			Msg("Service secret not configured — per-tenant geofence caps are disabled; metering every tenant at the platform defaults.")
		return
	}
	if infra.UserManagement.Hostname == "" || infra.UserManagement.Port == 0 {
		log.Warn().Int("geoFencePositionCeiling", caps.PositionCeiling).
			Int("geoFenceCeiling", caps.FenceCeiling).Int("geoFencePositionBudget", caps.PositionBudget).
			Msg("user-management endpoint not configured (infrastructure.userManagement) — per-tenant geofence caps are disabled; metering every tenant at the platform defaults.")
		return
	}
	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "device-management",
		[]string{string(auth.TenantRead)})
	url := fmt.Sprintf("http://%s:%d/graphql", infra.UserManagement.Hostname, infra.UserManagement.Port)
	Api.GeoFenceCapsResolver = governance.NewGeoFenceCapsResolver(client, url)
	log.Info().Str("userManagement", url).
		Msg("Per-tenant geofence caps enabled (override, else tier, else the platform default).")
}

// wireCalculatedMetricsCeilingResolver injects the per-tenant calculated-metrics ceiling
// (ADR-082, packaged per ADR-065): the tenant's tier setting, else the platform default of
// zero. Left nil — every tenant at zero, so no profile publishes a calculated metric — when
// the service secret or the user-management coordinate is unset, for the reasons
// wireGeoFenceCapsResolver gives. It reads tenantGovernance, so tenant:read.
func wireCalculatedMetricsCeilingResolver() {
	infra := Microservice.InstanceConfiguration.Infrastructure
	if infra.ServiceAuth.Secret == "" || infra.UserManagement.Hostname == "" || infra.UserManagement.Port == 0 {
		log.Warn().Int("calculatedMetricsPerProfile", governance.DefaultCalculatedMetricsPerProfile).
			Msg("Service secret or user-management endpoint not configured — the calculated-metrics ceiling is the platform default for every tenant.")
		return
	}
	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "device-management",
		[]string{string(auth.TenantRead)})
	url := fmt.Sprintf("http://%s:%d/graphql", infra.UserManagement.Hostname, infra.UserManagement.Port)
	Api.CalculatedMetricsCeiling = governance.NewCalculatedMetricsCeilingResolver(client, url)
}

// Called after microservice has been initialized.
// safetyGates reports whether this service's optional safety gates are wired
// (<area>_safety_gate_enabled). Built once in afterMicroserviceInitialized.
var safetyGates *governance.SafetyGates

func afterMicroserviceInitialized(ctx context.Context) error {
	// Built ONCE, here on the initialize path: the gauge registers on construction and a second
	// registration panics, and the gates below are decided in callbacks that can run again.
	safetyGates = governance.NewSafetyGates(Microservice)
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
	// in the two gaps between the three constructions.
	//
	// 🔑 THIS SERVICE USES THE SECOND GAP, WHICH IS UNUSUAL AND STRUCTURAL. Its caches are
	// NATS JetStream KV buckets (ADR-007), so the Api that wraps them cannot be built until
	// the broker manager exists — which is why almost everything below is in AfterNats
	// rather than AfterRdb.
	Svc = service.New(Microservice, service.Spec{
		Rdb: &service.RdbSpec{
			Instance:   Microservice.InstanceConfiguration.Persistence.Rdb,
			Migrations: schema.Migrations,
			Config:     Configuration.RdbConfiguration,
		},
		AfterRdb: func(ctx context.Context, m *service.Managers) error {
			RdbManager = m.Rdb

			// Device credential secrets are stored as digests under a key derived from the
			// instance root key, so this service cannot store or check an MQTT password
			// without it: no key, no start.
			var err error
			if DeviceSecretKey, err = deriveDeviceSecretKey(); err != nil {
				return err
			}
			if EnrollmentKeys, err = deriveEnrollmentSecretKeys(); err != nil {
				return err
			}
			if SecretTransit, err = deriveSecretTransit(); err != nil {
				return err
			}
			if err = digestStoredCredentialSecrets(ctx, RdbManager, DeviceSecretKey, EnrollmentKeys); err != nil {
				return err
			}

			// Build every Prometheus instrument this service exports, before the NATS manager
			// that consumes them.
			buildMetrics()
			return nil
		},
		Nats: &service.NatsSpec{OnCreate: createNatsComponents},
		// Runs once the broker manager is initialized and before the GraphQL manager is
		// built — the one point in the sequence where this service can have both a
		// relational handle and a broker to build KV buckets from.
		AfterNats: func(_ context.Context, m *service.Managers) error {
			var err error
			Api, CachedApi, err = buildApis(m.Nats, RdbManager, DeviceSecretKey, EnrollmentKeys, Configuration)
			if err != nil {
				return err
			}
			if err = initOwnershipDrain(); err != nil {
				return err
			}

			// Report the size of this schema's append-only history tables (ADR-023). Every one of
			// them only grows — nothing prunes a version history short of purging the whole tenant —
			// so the instance's storage floor rises with authoring activity and nothing today says
			// how fast. This is the measurement that has to exist before any ceiling on it can be
			// chosen honestly; it enforces nothing. The audit journal is added by the collector
			// itself, since core owns that table in every schema.
			//
			// Registered directly rather than through Microservice.NewGauge because a failed read
			// must produce NO series rather than a zero. See rdb.StorageGrowthCollector.
			prometheus.MustRegister(rdb.NewStorageGrowthCollector(Microservice, RdbManager.Database,
				&model.GeoFenceSetVersion{}, &model.GeoFenceGeometryBlob{},
				&model.DeviceProfileVersion{}, &model.DeviceProfileActivation{}, &model.EntityGroupVersion{}, &model.AssetTypeVersion{},
				&model.DeviceConfigurationRevision{}))

			// Wire the detection-rule validator (ADR-044 sync gate) so profile publish compiles
			// its draft rules against event-processing and fails closed on an uncompilable one.
			wireDetectionRuleValidator(Api, Microservice.InstanceConfiguration.Infrastructure, safetyGates)

			// Wire the per-tenant geofence caps and the counter that reports their refusals. The
			// counter is set unconditionally, unlike the resolver: with no resolver the platform
			// defaults are still enforced, so refusals still happen and still need reporting.
			wireGeoFenceCapsResolver()
			wireCalculatedMetricsCeilingResolver()
			Api.GeoFenceCapRefusals = processor.NewGeoFenceCapRefusalCounter(
				Microservice.NewCounterVec(
					"geofence_cap_refusals_total",
					"Geofence authoring calls refused by a tenant's governance cap, labelled by which cap refused (ADR-023). A sustained non-zero rate on `cap=\"geoFencePositionCeiling\"` or `cap=\"geoFenceCeiling\"` means a tenant is authoring against a packaging limit; on `cap=\"geoFencePositionBudget\"` it means a tenant's whole fence set is at its share of the shared detection geometry cache. Raise the tenant's tier, or its per-tenant override, to clear it. Deliberately NOT labelled by tenant — that would be unbounded cardinality on a multi-tenant instance; the API error names the tenant's own numbers.",
					[]string{"cap"}))
			return nil
		},
		GraphQL: &service.GraphQLSpec{
			Schema:   graphql.SchemaContent,
			Resolver: func() interface{} { return &graphql.SchemaResolver{} },
			// Evaluated after the broker manager is built, so the live alarm subscription
			// resolver (SubscribeLive, ADR-037) gets one that is already connected, before
			// the subscription server accepts a client.
			//
			// 🔴 IT READS Svc.Nats RATHER THAN THE PACKAGE VARIABLE, which is still nil at
			// this moment: NatsManager is published below, after Initialize returns, and this
			// runs inside it.
			Providers: func() map[gqlcore.ContextKey]interface{} {
				return map[gqlcore.ContextKey]interface{}{
					gqlcore.ContextRdbKey: RdbManager,
					gqlcore.ContextApiKey: Api,
					// The cached decorator is offered alongside the plain api so the profile
					// publish/rollback mutations can invalidate the shared ingest cache they
					// share with the processor (ADR-045 slice c); all other resolvers use Api.
					graphql.ContextCachedApiKey: CachedApi,
					gqlcore.ContextNatsKey:      Svc.Nats,
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

// Called after microservice has been started.
func afterMicroserviceStarted(ctx context.Context) error {
	// Rdb, then NATS, then the GraphQL server. That order now lives in core/service, and
	// for this service it is load-bearing: createNatsComponents (run by NatsManager.Start)
	// injects the alarm-event publisher into the shared Api, and doing it before the HTTP
	// server accepts traffic establishes happens-before for the resolver goroutines that
	// read Api.AlarmPublisher — so an early acknowledgeAlarm/clearAlarm mutation neither
	// races the write nor silently emits no event.
	if err := Svc.Start(ctx); err != nil {
		return err
	}

	// Start inbound events processor.
	err := InboundEventsProcessor.Start(ctx)
	if err != nil {
		return err
	}

	// Start the REACT raise-alarm consumer (ADR-051 slice 5c).
	err = RaiseAlarmConsumer.Start(ctx)
	if err != nil {
		return err
	}

	// Start the device-reports consumer: configuration sync and status from devices.
	err = DeviceReportsConsumer.Start(ctx)
	if err != nil {
		return err
	}

	// Start draining queued device ownership facts. After Svc.Start, which wired the
	// publisher the drain publishes through.
	if err = OwnershipDrain.Start(ctx); err != nil {
		return err
	}

	// Start the device auth-callout responder once the broker is configured for it
	// (ADR-025): it authenticates every device connect at the broker against the
	// device credential store. Absent an issuer seed the broker isn't running callout,
	// so there is nothing to serve.
	seeds, err := resolveCalloutSeeds(Microservice.InstanceConfiguration.Infrastructure.Nats.Auth, os.Getenv)
	if err != nil {
		return err
	}
	if seeds.Enabled {
		// The ADR-077 gate: a deleted tenant's devices hold credentials that still
		// resolve (nothing cascades a delete to the credential store), so without this
		// the fleet reconnects on its own backoff and keeps writing into data being
		// reclaimed. Nil when user-management is unconfigured — the gate is off, matching
		// the resolver's own fail-open, since the erasure guarantee is the per-area fence.
		infra := Microservice.InstanceConfiguration.Infrastructure
		gate := calloutTenantGate(infra, safetyGates)
		// Every MQTT password connect is compared through this Checker, behind a
		// per-username backoff whose counts every replica shares through the device
		// credential-attempt bucket. It fails closed when that bucket cannot be reached,
		// and OPEN, without the backoff, when it is full, since anyone can fill it by
		// presenting enough distinct usernames (the credential package doc says why).
		creds, err := processor.NewDeviceCredentialChecker(NatsManager, DeviceSecretKey, credential.WithCounter(CredentialChecks))
		if err != nil {
			return err
		}
		// The erasure-fence read: refuses a mint for a tenant whose data this area is
		// erasing, from this service's own schema, with no remote dependency.
		fenced := func(ctx context.Context, tenant string) error {
			return rdb.CheckTenantFence(ctx, RdbManager.Database, tenant)
		}
		CalloutResponder, err = processor.NewCalloutResponder(NatsManager.Conn(), CachedApi, creds, seeds.IssuerSeed, Microservice.InstanceId, gate, fenced)
		if err != nil {
			return err
		}
		// The bounds are stated rather than left to the constructor's defaults, and the load
		// metrics are attached here: a responder without them enforces its bounds but reports
		// nothing, which left the Ingest board's callout panels empty.
		CalloutResponder.Configure(processor.DefaultCalloutMaxInFlight, processor.DefaultCalloutLookupTimeout,
			processor.DefaultCalloutStopWait, CalloutMetrics)
		// The broker encrypts each authorization request to the responder's curve key when it is
		// configured with one; the seed to open them was checked against that key above.
		if seeds.XKeySeed != "" {
			if _, err = CalloutResponder.WithRequestEncryption(seeds.XKeySeed, seeds.XKeyPublic); err != nil {
				return err
			}
		}
		// The back-off ahead of overflow: with the fail-closed MQTT acknowledgement on, a full
		// message store closes the connections it refuses, and their reconnects would land here.
		// Above the store's high-water mark every device connect is refused at once instead, so
		// the devices still connected drain it. Off, nothing is sampled or registered.
		MqttStoreBackoff = wireMqttStoreBackoff(infra.Nats.MqttFailClosed, NatsManager, MqttStoreBackoffMetrics,
			CalloutRefusedStoreFull, CalloutResponder)
		if err = CalloutResponder.Start(); err != nil {
			return err
		}
	}

	return nil
}

// Called before microservice has been stopped.
func beforeMicroserviceStopped(ctx context.Context) error {
	// Stop the auth-callout responder first (before the NATS connection closes) so
	// no in-flight device connect is left unanswered.
	if CalloutResponder != nil {
		if err := CalloutResponder.Stop(); err != nil {
			return err
		}
	}
	MqttStoreBackoff.Stop()

	// Stop the ownership drain before the NATS connection drains. What it has not published
	// stays queued for whichever replica drains next.
	if err := OwnershipDrain.Stop(ctx); err != nil {
		return err
	}

	// Stop inbound events processor.
	err := InboundEventsProcessor.Stop(ctx)
	if err != nil {
		return err
	}

	// Stop the REACT raise-alarm consumer before the NATS connection drains.
	err = RaiseAlarmConsumer.Stop(ctx)
	if err != nil {
		return err
	}

	// Stop the device-reports consumer before the NATS connection drains.
	err = DeviceReportsConsumer.Stop(ctx)
	if err != nil {
		return err
	}

	// GraphQL, then NATS, then Rdb: draining the HTTP server first ensures no in-flight
	// ack/clear mutation tries to publish an alarm event to a NATS connection that is
	// already shutting down (mirrors the start order). core/service walks them in exactly
	// that order, for every service.
	return Svc.Stop(ctx)
}

// Called before microservice has been terminated.
func beforeMicroserviceTerminated(ctx context.Context) error {
	if err := OwnershipDrain.Terminate(ctx); err != nil {
		return err
	}

	// Terminate inbound events processor.
	err := InboundEventsProcessor.Terminate(ctx)
	if err != nil {
		return err
	}

	// Terminate the REACT raise-alarm consumer.
	err = RaiseAlarmConsumer.Terminate(ctx)
	if err != nil {
		return err
	}

	// Terminate the device-reports consumer.
	err = DeviceReportsConsumer.Terminate(ctx)
	if err != nil {
		return err
	}

	// GraphQL, then NATS, then Rdb — the same order as the stop above.
	//
	// This service used to terminate NATS first. The change is not observable:
	// GraphQLManager.ExecuteTerminate returns nil and carries no callback, so the only
	// thing that moved is a no-op, and NATS still terminates before Rdb — which is the pair
	// that actually closes handles, and which both consumers above still precede.
	return Svc.Terminate(ctx)
}
