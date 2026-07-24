// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/graphql"
	"github.com/devicechain-io/dc-event-sources/model"
	processor "github.com/devicechain-io/dc-event-sources/processor"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/svcclient"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	Microservice  *core.Microservice
	Configuration *config.EventSourcesConfiguration
	EventSources  []core.LifecycleComponent

	GraphQLManager *gqlcore.GraphQLManager
	NatsManager    *messaging.NatsManager

	// RateLimiter meters inbound events per tenant against the platform ingest
	// ceiling; over-limit events are shed at the receive point before decode. It
	// meters on WALL-CLOCK arrival and serves every source that is keeping up.
	RateLimiter *core.TenantRateLimiter

	// BacklogRateLimiter meters events being drained from the capture stream well
	// after they were sent, on the SEND timeline rather than on arrival (ADR-030 I4).
	//
	// It is a second limiter rather than a second clock on the first one, and that
	// separation is load-bearing rather than tidiness. A token bucket accrues from
	// the last timestamp it saw, so feeding ONE bucket both wall-clock arrivals and
	// hours-old send times makes every jump forward to now re-accrue from a stale
	// mark and refill to burst, which the following rewind then spends. That is not
	// a bounded rounding error: it mints roughly `burst` admissions per interleave,
	// so a tenant ingesting over HTTP while their capture backlog drains can pace
	// live posts against the drain and bypass their ceiling entirely. Measured on
	// the shared-bucket design this replaces, one second of consumer lag was enough
	// to turn a 100/s ceiling into ~2000 admissions.
	//
	// Keeping each bucket on exactly ONE clock removes the whole category: the live
	// bucket only ever sees now, the backlog bucket only ever sees send times (which
	// are monotonic in stream order), so neither can rewind and neither can mint.
	//
	// The cost is that a tenant who is simultaneously live AND draining a real
	// backlog can be admitted up to twice their ceiling for the duration of the
	// drain. That is bounded, predictable, and strictly smaller than the exposure
	// the platform already carries from running N replicas with independent
	// limiters — where an unbounded, lag-scaled bypass is a different category of
	// problem entirely.
	BacklogRateLimiter *core.TenantRateLimiter

	// ShedPriorityResolver resolves a tenant's ADR-063 shed priority (band) from
	// user-management, cached like the ingest ceiling. It is what turns the contention
	// floor into a per-tenant shed factor and what labels a shed by its class. nil when
	// user-management is not configured (the degenerate no-overrides path), in which
	// case every tenant resolves to the fail-safe bronze band.
	ShedPriorityResolver *governance.ShedPriorityResolver

	// Messaging.
	InboundEventsWriter messaging.MessageWriter
	FailedDecodeWriter  messaging.MessageWriter
	// CaptureReader is the durable consumer of raw device telemetry (ADR-030
	// amendment). It is the gateway's ingest path: the broker persists a device's
	// publish here before it PUBACKs, so the message is durable before our code
	// runs. Shared across pods — one durable, messages distributed — which is what
	// lets event-sources scale past a single replica at all.
	CaptureReader messaging.MessageReader
	// GatewaySource is the capture-stream source, held from the INITIALIZE phase
	// (where sources are built) so that START (where readers are created) can hand
	// it CaptureReader. nil when no source is pointed at the platform broker.
	GatewaySource *processor.GatewayJetStreamSource

	// Metrics
	MessagesCounter     *prometheus.CounterVec
	DecodedCounter      *prometheus.CounterVec
	FailedDecodeCounter *prometheus.CounterVec
	RateLimitedCounter  *prometheus.CounterVec
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
	config := &config.EventSourcesConfiguration{}
	err := core.LoadConfiguration(Microservice.MicroserviceConfigurationRaw, config)
	if err != nil {
		return err
	}
	Configuration = config
	return nil
}

// Initialize metrics.
func initializeMetrics() {
	MessagesCounter = Microservice.NewCounterVec(
		"total_inbound_messages",
		"Count of total inbound messages from event sources",
		[]string{"source"})
	DecodedCounter = Microservice.NewCounterVec(
		"total_msg_decode_successful",
		"Count of total messages successfully decoded",
		[]string{"source"})
	FailedDecodeCounter = Microservice.NewCounterVec(
		"total_msg_failed_decode",
		"Count of total messages that failed to decode",
		[]string{"source"})
	RateLimitedCounter = Microservice.NewCounterVec(
		"total_msg_rate_limited",
		"Count of inbound messages shed at the per-tenant ingest ceiling, by ADR-063 shed class",
		// shed_class is the ADR-063 P4 bounded label — a shed is attributed to the
		// tenant's class (gold/silver/bronze/best-effort), NOT the tenant (which is an
		// unbounded, attacker-influenceable cardinality vector, ADR-023 G.3). It lets an
		// operator see WHICH tier is being shed under a contention floor without leaking
		// per-tenant cardinality into the metric.
		[]string{"source", "shed_class"})
}

// contentionLevel is the effective ADR-063 shed level. At GA it is just the operator
// manual floor (the composite L = max(auto, manual) collapses to manual because the
// auto controller is deferred). A function so the deferred runtime signal can slot in
// without touching the shed path.
func contentionLevel() int { return Configuration.Contention.ManualFloor }

// shedAdjusted wraps a base ceiling resolver with the ADR-063 shed factor: at the
// active contention level, a shed class's effective ceiling is lowered (best-effort
// first, then bronze, then silver; gold's factor is always 1.0, so gold's ceiling is
// never touched — the promise). The existing ADR-023 limiter then sheds the overflow
// at its normal 429 path; this adds no new drop, it only tightens the ceiling.
//
// Level 0 (the resting default, contention off) is a fast path: base is returned
// untouched and the shed priority is never resolved, so the whole mechanism is
// zero-cost until an operator sets a floor.
func shedAdjusted(base func(string) (float64, int), shedPriority func(string) (int, bool)) func(string) (float64, int) {
	return func(tenant string) (float64, int) {
		rps, burst := base(tenant)
		level := contentionLevel()
		if level <= 0 {
			return rps, burst
		}
		prio, resolved := shedPriority(tenant)
		if !resolved {
			// Do NOT shed a tenant whose priority has not resolved yet — the cold-cache
			// window right after a floor-activation restart, or user-management
			// transiently unreachable. Its default is a bronze band, and shedding an
			// unclassified tenant on it could shed a GOLD tenant (ADR-063 "gold is never
			// shed" would hold only once fetched, and the restart that activates a floor
			// is exactly what empties the cache). An unresolved tenant is admitted at its
			// base ADR-023 ceiling — still metered, never unlimited — until its priority
			// is known (within the 60s TTL). This is the one place the fail-safe direction
			// is "don't shed the unknown" rather than "shed the unknown as bronze".
			return rps, burst
		}
		class := governance.ShedClassOf(prio)
		l := governance.Limits{MessagesPerSecond: rps, Burst: burst}.Shed(governance.ShedFactor(class, level))
		return l.MessagesPerSecond, l.Burst
	}
}

// buildRateLimiter constructs the per-tenant ingest limiter. When the service
// secret and user-management endpoint are configured, per-tenant overrides
// (ADR-023) and shed priorities (ADR-063) are fetched from user-management over a
// service token and cached, failing open to the platform default; otherwise every
// tenant is metered at the platform default and resolves to the fail-safe shed band.
// Either way the ceiling is a real limit — never unlimited — since ApplyDefaults
// guarantees positive platform defaults.
func buildRateLimiter() {
	def := governance.Limits{
		MessagesPerSecond: Configuration.IngestRateLimit.MessagesPerSecond,
		Burst:             Configuration.IngestRateLimit.Burst,
	}
	infra := Microservice.InstanceConfiguration.Infrastructure
	if infra.ServiceAuth.Secret == "" || infra.UserManagement.Hostname == "" || infra.UserManagement.Port == 0 {
		log.Warn().Msg("Service secret or user-management endpoint not configured — per-tenant ingest overrides disabled; metering every tenant at the platform default.")
		flat := func(string) (float64, int) { return def.MessagesPerSecond, def.Burst }
		// No user-management ⇒ no shed priorities to differentiate tenants; everyone
		// resolves to the fail-safe bronze band. Still honor the floor on the LIVE
		// limiter so a drill in a no-UM dev instance behaves, but nothing here reads a
		// tenant's tier. The backlog limiter is NOT shed (see below).
		// No user-management ⇒ no per-tenant priorities. Everyone resolves to the
		// fail-safe bronze band, and it IS resolved (there is no authority to be
		// transiently down, so the default is the real answer here — unlike the
		// UM-backed path, where an unfetched tenant is genuinely unresolved).
		shedPrio := func(string) (int, bool) { return governance.DefaultShedPriority, true }
		RateLimiter = core.NewTenantRateLimiter(shedAdjusted(flat, shedPrio))
		BacklogRateLimiter = core.NewTenantRateLimiter(flat)
		return
	}
	client := svcclient.New(infra.UserManagement, infra.ServiceAuth.Secret, "event-sources", []string{string(auth.TenantRead)})
	umURL := fmt.Sprintf("http://%s:%d/graphql", infra.UserManagement.Hostname, infra.UserManagement.Port)
	resolver := governance.NewServiceLimitResolver(client, umURL, def, governance.Ingest)
	ShedPriorityResolver = governance.NewShedPriorityResolver(client, umURL)
	// Both limiters share the ceiling resolver, so a tenant's ADR-023 override applies
	// to their live traffic and their backlog alike — the ceiling is the same, only the
	// timeline each bucket measures against differs.
	//
	// The ADR-063 shed factor rides on the LIVE limiter ONLY. The backlog limiter meters
	// the capture-stream drain — messages the broker already PUBACKed (ADR-030), i.e.
	// DURABLY ACCEPTED. ADR-063's invariant is that priority acts only at ADMISSION,
	// "before an event is durably accepted into the log", never on an accepted stream;
	// shedding the backlog would retroactively discard already-accepted, already-durable
	// data — a violation of that invariant AND of ADR-030's durability promise. So the
	// backlog drains un-shed: contention shapes new ingress, never recovery of committed data.
	RateLimiter = core.NewTenantRateLimiter(shedAdjusted(resolver.Resolve, ShedPriorityResolver.Resolve))
	BacklogRateLimiter = core.NewTenantRateLimiter(resolver.Resolve)
	log.Info().Str("userManagement", umURL).Int("contentionFloor", contentionLevel()).
		Msg("Per-tenant ingest overrides + ADR-063 shed priorities enabled (fail-open to platform default).")
}

// Create decoder based on event source configuration.
func createDecoder(source config.EventSource) (processor.Decoder, error) {
	switch source.Decoder.Type {
	case processor.DECODER_TYPE_JSON:
		return processor.NewJsonDecoder(source.Decoder.Configuration), nil
	default:
		return nil, fmt.Errorf("unkown decoder type: %s", source.Type)
	}
}

// Use configuration to build event sources.
func buildEventSources() error {
	created := make([]core.LifecycleComponent, 0)
	gatewaySourceBuilt := false
	for _, source := range Configuration.EventSources {
		// Create decoder.
		decoder, err := createDecoder(source)
		if err != nil {
			return err
		}

		// Create event source.
		switch source.Type {
		case processor.TYPE_MQTT:
			// THE GATEWAY SOURCE IS NO LONGER AN MQTT CLIENT (ADR-030 amendment). A
			// source pointed at our OWN broker consumes the durable capture stream the
			// broker writes every device publish into before it PUBACKs the device, so
			// the message is durable before any of our code runs.
			//
			// An MQTT client could not have been made durable here at all, which is why
			// this is a replacement rather than a fix. NATS discards a CleanSession
			// session and every unacked message on disconnect — exactly the SIGKILL case
			// — and it speaks MQTT 3.1.1, where shared subscriptions do not exist: a
			// "$share" subscribe is accepted and then delivers nothing, so that design
			// could never have exceeded one replica either.
			//
			// With the gateway off this path, everything the MQTT client needed for it
			// goes too: the NATS TLS material, the shared service credential, and the
			// derived device-events topic filter. What remains below is the
			// EXTERNAL-broker source, which keeps paho and stays at-most-once by
			// decision — on a broker we do not own, session and retention are the
			// operator's configuration, so a durability claim would be unenforceable.
			//
			// The dispatch is exact string equality against the configured NATS
			// hostname. A source that names the same broker by IP, by an FQDN alias, or
			// past a hostname override therefore falls through to the paho branch and
			// silently gets the OLD architecture — at-most-once, plaintext, anonymous,
			// and subscribed to "+/#", which re-ingests the service's own internal
			// traffic (the PR #458 defect, whose denylist names only two of the internal
			// suffixes). With broker auth on this fails loudly at connect; with auth off
			// it is silent. Worth tightening, but the comparison predates this change
			// and the shipped defaults match on both sides.
			natscfg := Microservice.InstanceConfiguration.Infrastructure.Nats
			if source.Configuration["host"] == natscfg.Hostname {
				// One gateway source only. Every gateway source would wrap the SAME
				// CaptureReader, and a natsReader is single-consumer by construction —
				// its pending buffer and timeout counter are mutated without a lock — so
				// a second read loop on it is a data race, not merely redundant work.
				if gatewaySourceBuilt {
					return fmt.Errorf("event source %q is a second source pointed at the platform broker %q: "+
						"the capture-stream consumer is single-reader, so only one gateway source may be configured",
						source.Id, natscfg.Hostname)
				}
				gatewaySourceBuilt = true
				gateway := processor.NewGatewayJetStreamSource(source.Id, decoder,
					onMessageReceived, onEventDecoded, onEventDecodeFailed,
					processor.NewRateGate(RateLimiter, BacklogRateLimiter, onRateShed))
				// Held so createNatsComponents can hand it the capture reader once that
				// reader exists. Sources are built in the INITIALIZE phase and readers are
				// created in START, so there is nothing to wire here yet.
				GatewaySource = gateway
				created = append(created, gateway)
				continue
			}
			// An external broker must NOT be forced to verify against the NATS CA (it
			// would dial ssl:// at a plaintext port, or fail verification), and keeps
			// its own credentials. Per-source TLS for external brokers is a later
			// concern, so this dials plaintext and anonymous.
			mqtt, err := processor.NewMqttEventSource(source.Id, source.Configuration, nil, "", "",
				decoder, onMessageReceived, onEventDecoded, onEventDecodeFailed,
				processor.NewRateGate(RateLimiter, BacklogRateLimiter, onRateShed))
			if err != nil {
				return err
			}
			created = append(created, mqtt)
		case processor.TYPE_HTTP:
			http, err := processor.NewHttpEventSource(source.Id, source.Configuration, Microservice.InstanceId,
				decoder, onMessageReceived, onEventDecoded, onEventDecodeFailed,
				processor.NewRateGate(RateLimiter, BacklogRateLimiter, onRateShed))
			if err != nil {
				return err
			}
			created = append(created, http)
		default:
			return fmt.Errorf("unkown event source type: %s", source.Type)
		}
	}
	EventSources = created
	return nil
}

// Handle accounting for received messages.
func onMessageReceived(source string, raw []byte) {
	// Increment counter for metrics.
	MessagesCounter.WithLabelValues(source).Inc()
}

// onRateShed accounts for one shed inbound message, recording it against the
// per-(source, shed_class) metric so an operator can see WHICH tier is being shed
// under a contention floor. Called at the receive point of each transport, before
// decode.
//
// The admission decision itself — including which timeline a message is metered on —
// lives in processor.NewRateGate; this is only the shed accounting.
func onRateShed(source string, tenant string) {
	RateLimitedCounter.WithLabelValues(source, shedClassOf(tenant)).Inc()
	// Per-tenant attribution is logged (debug) rather than carried as a metric label:
	// this service does not verify tenant existence, so a tenant label would be an
	// unbounded, attacker-influenceable cardinality vector (ADR-023 G.3). The metric
	// carries the BOUNDED shed class instead (ADR-063 P4).
	if log.Debug().Enabled() {
		log.Debug().Str("source", source).Str("tenant", tenant).
			Msg("Shed inbound event exceeding per-tenant ingest rate limit")
	}
}

// shedClassOf resolves a tenant's ADR-063 shed class for the bounded shed metric. It
// serves off the same cached resolver the shed factor uses, so it costs a cached
// lookup, not a query. When user-management is not configured (no resolver) every
// tenant reads as the fail-safe bronze band — the same class the flat limiter sheds by.
func shedClassOf(tenant string) string {
	prio := governance.DefaultShedPriority
	if ShedPriorityResolver != nil {
		// The metric labels every shed by class, including a not-yet-resolved tenant's
		// (banded from the fail-safe default) — a shed of an unresolved tenant is a
		// base-ceiling shed, and bronze is a fine label for the rare cold-cache case.
		prio, _ = ShedPriorityResolver.Resolve(tenant)
	}
	return governance.ShedClassOf(prio).String()
}

// Called by event sources when an event is successfully decoded. The tenant is
// derived by the source from its own addressing (MQTT topic / HTTP path) before
// the event reaches here; an empty tenant cannot be published to a tenant-scoped
// subject, so the event is dropped (fail-closed) rather than published unscoped.
// It returns whether the event was DURABLY published. A source consuming a
// durable capture stream acknowledges its broker on this answer (ADR-030
// amendment), so a swallowed error here would ack a message that was never
// forwarded — reintroducing exactly the silent loss the capture stream exists to
// remove. Sources with nothing to acknowledge ignore the result.
//
// A nil error means "do not send this again", so the two fail-closed drops below
// return nil: they are terminal decisions, and reporting them as failures would
// ask the broker to redeliver a message that will be dropped identically forever.
func onEventDecoded(source string, tenant string, event *model.UnresolvedEvent, payload interface{},
	captureSeq uint64) error {
	// Increment counter for metrics.
	DecodedCounter.WithLabelValues(source).Inc()

	event.Source = source
	event.Payload = payload
	// A device controls the raw bytes a Decoder parses, so the device-facing decode
	// path must NEVER be able to mark an event transport-authenticated (that marker
	// bypasses the required-mode credential check, and is legitimately set only by the
	// internal ingest services that emit straight to inbound-events, not through here).
	// No current Decoder carries the field; clamp it to false unconditionally so the
	// guarantee is STRUCTURAL for every future decoder rather than a per-decoder pin.
	event.AuthenticatedTransport = false

	if tenant == "" {
		log.Warn().Msg(fmt.Sprintf("Dropping decoded event from source %q with no tenant", source))
		return nil
	}
	ctx := core.WithTenant(context.Background(), tenant)

	// Marshal event message to protobuf.
	bytes, err := esproto.MarshalUnresolvedEvent(event)
	if err != nil {
		log.Error().Err(err).Msg("unable to marshal event to protobuf")
		return nil
	}

	// Create and deliver message (writer derives the scoped subject from ctx).
	//
	// DedupID makes the publish idempotent within the stream's duplicate window, so
	// a capture message redelivered after a crash between publish and ack is stored
	// once rather than twice. It is empty for a transport with no capture sequence
	// (HTTP, external MQTT), which publishes no dedup header at all.
	msg := messaging.Message{
		Key:     []byte(event.Device),
		Value:   bytes,
		DedupID: processor.DedupID(tenant, captureSeq),
	}
	err = InboundEventsWriter.WriteMessages(ctx, msg)
	InboundEventsWriter.HandleResponse(err)
	return err
}

// Handle failed decoding. It returns whether the undecodable payload was durably
// routed to the failed-decode path, so a source consuming a durable stream can
// acknowledge on the real outcome rather than on having tried.
func onEventDecodeFailed(source string, tenant string, raw []byte, err error) error {
	// Increment counter for metrics.
	FailedDecodeCounter.WithLabelValues(source).Inc()

	// A message that could not be decoded is still routed to the failed-decode
	// subject for the originating tenant; without a tenant it cannot be scoped, so
	// it is dropped fail-closed — terminally, hence a nil error.
	if tenant == "" {
		log.Warn().Msg(fmt.Sprintf("Dropping failed-decode message from source %q with no tenant", source))
		return nil
	}
	ctx := core.WithTenant(context.Background(), tenant)

	// Create and deliver message.
	msg := messaging.Message{
		Key:   []byte(source),
		Value: raw,
	}
	senderr := FailedDecodeWriter.WriteMessages(ctx, msg)
	FailedDecodeWriter.HandleResponse(senderr)
	return senderr
}

// Create messaging components used by this microservice.
func createNatsComponents(nmgr *messaging.NatsManager) error {
	ievents, err := nmgr.NewWriter(streams.InboundEvents)
	if err != nil {
		return err
	}
	InboundEventsWriter = ievents

	// The capture stream's durable consumer. Created here rather than inside the
	// source so the stream is ensured at startup even before any device publishes:
	// the stream must EXIST before the broker can write a device's message into it,
	// so a fresh instance whose first device connects before event-sources starts
	// would otherwise lose exactly the messages this is meant to make durable.
	capture, err := nmgr.NewReader(streams.DeviceEventsCapture)
	if err != nil {
		return err
	}
	CaptureReader = capture
	// Wire the reader into the source that consumes it. This is the phase peer
	// consumers do the same in (command-delivery, event-processing), and it is the
	// earliest point at which the reader exists at all — buildEventSources ran back
	// in INITIALIZE, when there was nothing to give it.
	if GatewaySource != nil {
		GatewaySource.SetReader(capture)
	}

	failed, err := nmgr.NewWriter(streams.FailedDecode)
	if err != nil {
		return err
	}
	FailedDecodeWriter = failed
	return nil
}

// Called after microservice has been initialized.
func afterMicroserviceInitialized(ctx context.Context) error {
	// Parse configuration.
	err := parseConfiguration()
	if err != nil {
		return err
	}

	// Build the per-tenant ingest rate limiter before the sources that use it.
	buildRateLimiter()

	// Build event sources from configuration.
	err = buildEventSources()
	if err != nil {
		return err
	}

	// Initialize metrics.
	initializeMetrics()

	// Create and initialize nats manager.
	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), createNatsComponents)
	err = NatsManager.Initialize(ctx)
	if err != nil {
		return err
	}

	// Map of providers that will be injected into graphql http context.
	providers := map[gqlcore.ContextKey]interface{}{}

	// Create and initialize graphql manager.
	schema := graphql.SchemaContent
	parsed := gqlcore.MustParseSchema(schema, &graphql.SchemaResolver{})

	// Auth degrades instead of failing startup (ADR-022 decision 3): fetch the
	// validator in the background and gate the data plane on readiness rather
	// than exiting when user-management is briefly unreachable (amends ADR-008).
	Microservice.StartInstanceAuthGate(ctx)

	GraphQLManager = gqlcore.NewGraphQLManager(Microservice, core.NewNoOpLifecycleCallbacks(), parsed, providers, Microservice.Readiness)
	err = GraphQLManager.Initialize(ctx)
	if err != nil {
		return err
	}

	// Initialize each event source.
	for _, source := range EventSources {
		err = source.Initialize(ctx)
		if err != nil {
			return err
		}
	}

	return nil
}

// Called after microservice has been started.
func afterMicroserviceStarted(ctx context.Context) error {
	// Start nats manager.
	err := NatsManager.Start(ctx)
	if err != nil {
		return err
	}

	// Start graphql manager.
	err = GraphQLManager.Start(ctx)
	if err != nil {
		return err
	}

	// Start each event source.
	for _, source := range EventSources {
		err = source.Start(ctx)
		if err != nil {
			return err
		}
	}

	// Bound the MQTT gateway's own JetStream streams, which nats-server creates
	// with no size limit at all and offers no option to bound.
	//
	// This service is the right owner of that: it IS the MQTT client whose
	// connection causes the gateway to create those streams in the first place,
	// so by this point they exist — and it is its own QoS-1 subscription across
	// the whole device-plane topic tree that keeps $MQTT_msgs interested in every
	// device subject, which is what makes a QoS>=1 device publish get stored
	// twice. The service that creates the exposure bounds it.
	//
	// Deliberately NOT fatal. An unbounded gateway stream is a disk-budget risk
	// that shows up under load; refusing to start ingest over it would turn that
	// risk into an immediate outage, which is the worse trade. It is logged loudly
	// and retried on the next startup.
	natscfg := Microservice.InstanceConfiguration.Infrastructure.Nats
	if err := NatsManager.ReconcileMqttStores(ctx, natscfg.MqttStoreMaxBytes, natscfg.MqttQoS2StoreMaxBytes); err != nil {
		log.Error().Err(err).Msg(
			"Could not bound the MQTT gateway's JetStream streams. They are UNBOUNDED as " +
				"nats-server creates them and share the same max_file_store as the platform's " +
				"streams, so QoS>=1 traffic can consume the disk budget's headroom. Ingest continues.")
	}

	return nil
}

// Called before microservice has been stopped.
func beforeMicroserviceStopped(ctx context.Context) error {
	// Stop each event source.
	for _, source := range EventSources {
		err := source.Stop(ctx)
		if err != nil {
			return err
		}
	}

	// Stop graphql manager.
	err := GraphQLManager.Stop(ctx)
	if err != nil {
		return err
	}

	// Stop nats manager.
	err = NatsManager.Stop(ctx)
	if err != nil {
		return err
	}

	return nil
}

// Called before microservice has been terminated.
func beforeMicroserviceTerminated(ctx context.Context) error {
	// Terminate each event source.
	for _, source := range EventSources {
		err := source.Terminate(ctx)
		if err != nil {
			return err
		}
	}

	// Terminate graphql manager.
	err := GraphQLManager.Terminate(ctx)
	if err != nil {
		return err
	}

	// Terminate nats manager.
	err = NatsManager.Terminate(ctx)
	if err != nil {
		return err
	}

	return nil
}
