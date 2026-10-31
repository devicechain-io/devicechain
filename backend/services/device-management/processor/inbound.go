// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/devicechain-io/dc-microservice/storehold"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/devicechain-io/dc-device-management/config"
	dmodel "github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

const (
	// MESSAGE_BACKLOG_SIZE is how many inbound messages can wait for a free resolver. With
	// every resolver busy, events wait here and, behind these, in the reader's fetched batch
	// (up to config.DefaultFetchBatch, 256, with the next batch already fetched ahead of
	// the reader): the occupancy measured by BenchmarkInboundStageOccupancy, which matches
	// the ack-pending the inbound consumer shows on a saturated kind cluster. That queue is inherent in a saturated stage; the
	// pool width (resolution.workers) is what sets how fast it drains. The consumer's
	// MaxAckPending is far above it, so that is not what caps the in-flight count.
	MESSAGE_BACKLOG_SIZE        = 100
	FAILED_EVENT_BACKLOG_SIZE   = 100 // Number of failed events that can be waiting to publish
	RESOLVED_EVENT_BACKLOG_SIZE = 100 // Number of resolved events that can be waiting to publish
)

// ackCoord coordinates acknowledgement of one source message across the 1->N
// resolved-event fan-out it produced. The source is acked once every resolved
// event has been durably published, or left unacked if any publish fails so the
// whole message is redelivered after AckWait (ADR-022 review A3). Once OnResolvedEvent has
// built it, it is only ever touched by the resolved writer's settle goroutine (see
// settleResolved), so no locking is required.
type ackCoord struct {
	src       messaging.Message
	remaining int
	failed    bool
}

// resolvedItem pairs a resolved event with the tenant it belongs to so the
// outbound producer can publish to the tenant's subject (the tenant is derived
// from the inbound subject and must travel with the event across the channel).
// coord ties the item back to its source message for ack coordination.
// correlation carries the inbound message's correlation id so the outbound
// resolved event is stamped with it and stays traceable end to end (E15).
// index is the event's position in its source's fan-out; with the source's
// stream sequence it names the publish for the broker's dedup (resolvedDedupID).
type resolvedItem struct {
	tenant      string
	event       resolvedevent.ResolvedEvent
	coord       *ackCoord
	index       int
	correlation string
}

// failedItem pairs a failed event with its tenant for the same reason.
// correlation carries the inbound message's correlation id onto the outbound
// failed event for the same end-to-end traceability (E15). src is the inbound
// message the record is about: it is acked only once the record is stored (see
// ProcessFailedEvent).
type failedItem struct {
	tenant      string
	event       resolvedevent.FailedEvent
	src         messaging.Message
	correlation string
}

type InboundEventsProcessor struct {
	Microservice *core.Microservice
	// InboundEventsReaders are the pull readers on the inbound-events durable, one read loop
	// each (resolution.inboundReaders). They read ONE consumer, so the broker hands each
	// message to exactly one of them, and everything after the read is shared: the resolver
	// pool, its caches, and the single submitter feeding each publish writer. Start refuses an
	// empty list or a nil entry: a processor with no reader resolves nothing while reporting
	// started.
	InboundEventsReaders []messaging.MessageReader
	ResolvedEventsWriter messaging.OrderedWriter
	FailedEventsWriter   messaging.OrderedWriter
	Api                  dmodel.DeviceManagementApi
	AuthMode             string
	secretTransit        *credential.SecretTransit
	// gatewayNoticer tells a gateway its publish for a child was refused; nil sends no notice.
	gatewayNoticer GatewayNoticer
	// MaxFutureSkew bounds how far a device-reported instant may lead the server's
	// own clock before resolution replaces it with the ceiling. It is the platform's
	// ONE event-time knob and it lives here because resolution is the one place the
	// event time is decided (see EventTimePolicy).
	MaxFutureSkew time.Duration

	// hold is the resolver pool's storehold.Holder (WithStoreHold); holdCancel ends every
	// hold at shutdown so a stop is not blocked behind a store that is down, leaving the held
	// events unacked for the next pod.
	hold       *storehold.Holder
	holdCtx    context.Context
	holdCancel context.CancelFunc

	messages  chan messaging.Message
	failed    chan failedItem
	resolved  chan resolvedItem
	resolvers []*EventResolver

	// resolverCount is the size of the resolver pool, set by WithResolvers from the
	// service's resolution.workers. 0 (a processor built without the option, or by literal
	// in a test) means the configuration's default, which initializeEventResolvers applies.
	resolverCount int

	// metrics is every Prometheus instrument this processor exports. It is built ONCE,
	// in the initialize phase, and handed in — NOT built here — because the processor
	// itself is constructed inside the NATS manager's oncreate callback, which is
	// connection-scoped and is entered again by any start retried after a failed one; a
	// collector belongs to the PROCESS, and MustRegister panics on the second
	// registration.
	//
	// A value rather than a pointer because this struct is also assembled by literal in
	// tests that never run the constructor: a zero value gives them the all-nil
	// instruments the readers already tolerate, where a nil pointer would be dereferenced.
	metrics ResolveMetrics

	// deadLetters is the service's dead-letter producer. This processor writes no dead letters
	// itself; it counts on the producer's dead_letter_lost_total the one loss it can cause: a
	// record of a failed event that cannot be encoded, so the failure is recorded nowhere (see
	// loseRecord). Set by WithDeadLetters, whose absence is caught where the loss is counted
	// (Producer.Lost refuses a nil producer) and, in production, by the service's wiring test.
	// It is also what the resolvers ask whether a failed event's tenant has been deleted (see
	// tenantDeleted), since a failed-event record is a new record of that tenant's data.
	deadLetters *deadletter.Producer

	// Shutdown coordination (A5): procCancel stops the read loop; the WaitGroups
	// let ExecuteStop drain senders before closing the channels they feed, so a
	// resolver or the reader can never send on a closed channel at SIGTERM.
	procCtx    context.Context
	procCancel context.CancelFunc
	readerWG   sync.WaitGroup
	workerWG   sync.WaitGroup
	outboundWG sync.WaitGroup

	// readPacers space out the retries after a non-EOF read error and end a loop once its
	// errors stop clearing: ONE PER READER, because a pacer belongs to one read goroutine (its
	// own contract) and one reader's errors are no evidence about another's. ExecuteStart
	// builds every one before it starts a loop; pacer() builds a missing one for a test that
	// drives readLoop itself.
	readPacers []*core.ReadPacer

	lifecycle core.LifecycleManager
}

// pacer returns reader i's error pacer, building it when it is missing. ExecuteStart calls
// it for every reader before any loop starts, so a loop only ever reads its own.
func (iproc *InboundEventsProcessor) pacer(i int) *core.ReadPacer {
	for len(iproc.readPacers) <= i {
		iproc.readPacers = append(iproc.readPacers, nil)
	}
	if iproc.readPacers[i] == nil {
		what := "inbound events"
		if i > 0 {
			what = fmt.Sprintf("inbound events (reader %d)", i+1)
		}
		iproc.readPacers[i] = core.NewReadPacer(iproc.Microservice, what)
	}
	return iproc.readPacers[i]
}

// ResolveMetrics is every Prometheus instrument the inbound resolve loop exports.
//
// It is a type of its own so it can be built in a DIFFERENT PHASE from the processor
// that reads it. See NewResolveMetrics.
type ResolveMetrics struct {
	// red is RED instrumentation for the resolve loop (E13); it is shared across every
	// resolver worker.
	red *core.ProcessorMetrics

	// eventTimeBounded counts reported event times refused for leading the server clock.
	// ONE counter for the whole worker pool: the workers share the inbound channel, so a
	// per-worker counter would report a fleet's clock skew as N unrelated series.
	eventTimeBounded prometheus.Counter

	// eventTimeTooOld counts events refused for a reported time more than eventtime.MaxAge
	// before the platform received them. ONE counter for the pool, for the same reason.
	eventTimeTooOld prometheus.Counter

	// deviceDisabled counts events refused because their device is disabled. ONE counter
	// for the pool, for the same reason.
	deviceDisabled prometheus.Counter

	// integrationAliasMismatch counts inbound integration events refused because their device no
	// longer holds the alias they were resolved from.
	integrationAliasMismatch prometheus.Counter

	// notGatewayChild counts a gateway's publishes refused because the device it named is not
	// assigned to it (or does not exist). ONE label-free counter for the pool; each refusal is
	// also dead-lettered with reason NotGatewayChild.
	notGatewayChild prometheus.Counter

	// workers is the resolver pool width this pod runs. Beside red's resolve_inflight it
	// says whether the pool is saturated. Nil in a zero ResolveMetrics.
	workers prometheus.Gauge

	// calc counts calculated-metric outcomes (ADR-082), shared by the pool.
	calc *CalcMetrics
}

// NewResolveMetrics builds the inbound resolve loop's instruments.
//
// 🔴 CALL IT FROM THE INITIALIZE PHASE, WHICH RUNS ONCE. The processor is built inside
// the NATS manager's oncreate callback — it has to be, because it holds a reader bound
// to the connection — and that callback runs on EVERY start. A Prometheus collector
// built there belongs to the wrong lifetime: it is registered again whenever that
// callback is entered again — which any start retried after a failed one does — and
// MustRegister panics on the duplicate.
func NewResolveMetrics(ms *core.Microservice) ResolveMetrics {
	calc := newCalcMetrics(ms)
	return ResolveMetrics{
		calc: &calc,
		red:  ms.NewProcessorMetrics("resolve"),
		eventTimeBounded: ms.NewCounter(
			"resolve_event_time_bounded_total",
			"Reported event times refused for leading the server clock by more than the configured tolerance, and replaced with the ceiling"),
		eventTimeTooOld: ms.NewCounter(
			"resolve_event_time_too_old_total",
			"Events refused because a reported time is more than 366 days before the platform received it; each is dead-lettered with reason Invalid"),
		deviceDisabled: ms.NewCounter(
			"resolve_device_disabled_total",
			"Events refused because their device is disabled; each is discarded, not dead-lettered"),
		notGatewayChild: ms.NewCounter(
			"resolve_not_gateway_child_total",
			"Events a gateway published for a device that is not assigned to it, or that does not exist; each is refused, dead-lettered with reason NotGatewayChild and announced to the gateway"),
		integrationAliasMismatch: ms.NewCounter(
			"resolve_integration_alias_mismatch_total",
			"Inbound integration events refused because their device no longer holds the alias the event was resolved from (removed or moved); each is dead-lettered with reason IntegrationAliasMismatch"),
		workers: ms.NewGauge("resolve_workers",
			"Resolvers this pod runs (resolution.workers). resolve_inflight held at this value means every resolver is busy and inbound events are queueing in front of them."),
	}
}

// InboundOption configures an InboundEventsProcessor.
type InboundOption func(*InboundEventsProcessor)

// WithResolvers sets the resolver pool width from the service's configuration
// (resolution.workers). Without it the processor runs the configuration's default.
func WithResolvers(n int) InboundOption {
	return func(p *InboundEventsProcessor) { p.resolverCount = n }
}

// Resolvers is the resolver pool width: the one WithResolvers set, or, once the processor
// is initialized, the default it applied when none was set.
func (iproc *InboundEventsProcessor) Resolvers() int {
	return iproc.resolverCount
}

// WithSecretTransit hands the processor the key that opens the sealed credential secrets
// inbound events carry. It panics on nil: without it every MQTT password would fail to
// authenticate, and that is a wiring mistake to refuse, not a state to run in.
func WithSecretTransit(t *credential.SecretTransit) InboundOption {
	if t == nil {
		panic("processor: WithSecretTransit needs the service's secret transit key; without it no sealed credential secret opens")
	}
	return func(iproc *InboundEventsProcessor) { iproc.secretTransit = t }
}

// WithGatewayNotices hands the processor the sender of the notices a refused gateway publish
// earns (see GatewayNoticer). Without it a refusal is still dead-lettered, counted and logged;
// only the best-effort notice to the gateway is not sent.
func WithGatewayNotices(n GatewayNoticer) InboundOption {
	return func(iproc *InboundEventsProcessor) { iproc.gatewayNoticer = n }
}

// WithStoreHold hands the resolver pool the Holder that keeps an event unacked, and outside its
// delivery budget, while the service's relational store cannot take writes. Production wiring
// always passes one (the service's wiring test pins it); without one an outage is retried
// against the delivery budget as before.
func WithStoreHold(h *storehold.Holder) InboundOption {
	return func(iproc *InboundEventsProcessor) { iproc.hold = h }
}

// StoreHold is the Holder WithStoreHold set, or nil, for the service's wiring test.
func (iproc *InboundEventsProcessor) StoreHold() *storehold.Holder { return iproc.hold }

// SecretTransit is the key WithSecretTransit set. It exists so the service's wiring test can
// check main.go hands on the key it derived.
func (iproc *InboundEventsProcessor) SecretTransit() *credential.SecretTransit {
	return iproc.secretTransit
}

// WithDeadLetters hands the processor the service's dead-letter producer, on whose
// dead_letter_lost_total a failure record that cannot be encoded is counted. It panics on nil:
// passing no producer is a wiring mistake, and a nil here would make that loss silent.
func WithDeadLetters(p *deadletter.Producer) InboundOption {
	if p == nil {
		panic("processor: WithDeadLetters needs the service's dead-letter producer; it is where " +
			"a failure record that cannot be encoded is counted")
	}
	return func(iproc *InboundEventsProcessor) { iproc.deadLetters = p }
}

// DeadLetters is the producer WithDeadLetters set, or nil. It exists so the service's wiring
// test can check main.go hands on the producer core/service built.
func (iproc *InboundEventsProcessor) DeadLetters() *deadletter.Producer { return iproc.deadLetters }

// tenantDeleted answers the dead-letter producer's tenant-deletion gate, reading "not deleted"
// with no producer. It is a method, and the resolvers hold it as a method value, so the
// producer is read when a resolution fails rather than copied when the pool is built.
func (iproc *InboundEventsProcessor) tenantDeleted(tenant string) bool {
	return iproc.deadLetters != nil && iproc.deadLetters.TenantDeleted(tenant)
}

// Create a new inbound events processor over inbound, the pull readers of the inbound-events
// durable (one read loop each). authMode is the device authentication policy applied while
// resolving inbound events (transport security, ADR-014); maxFutureSkew bounds a
// device-reported event time against the server's own clock.
//
// metrics is built once in the initialize phase (see NewResolveMetrics) and shared by
// every processor this service constructs, because this constructor runs again on every
// start.
func NewInboundEventsProcessor(ms *core.Microservice, inbound []messaging.MessageReader, resolved messaging.OrderedWriter,
	failed messaging.OrderedWriter, callbacks core.LifecycleCallbacks, api dmodel.DeviceManagementApi, authMode string,
	maxFutureSkew time.Duration, metrics ResolveMetrics, opts ...InboundOption) *InboundEventsProcessor {
	iproc := &InboundEventsProcessor{
		Microservice:         ms,
		InboundEventsReaders: inbound,
		ResolvedEventsWriter: resolved,
		FailedEventsWriter:   failed,
		Api:                  api,
		AuthMode:             authMode,
		MaxFutureSkew:        maxFutureSkew,
		metrics:              metrics,
	}
	for _, opt := range opts {
		opt(iproc)
	}

	// Create lifecycle manager.
	ipname := fmt.Sprintf("%s-%s", ms.FunctionalArea, "inbound-event-proc")
	iproc.lifecycle = core.NewLifecycleManager(ipname, iproc, callbacks)
	return iproc
}

// ProcessFailedEvent publishes one failed-event record, and acks the inbound message it is
// about only once the broker has stored that record.
//
// A record that does not reach the stream leaves its source unacked, so the source is
// redelivered and resolved again — or, at its last delivery, recorded by the platform's
// max-delivery recorder from the source stream. Either way the failure is recorded
// somewhere. Acking at hand-off, as this loop once did, lost it whenever the publish failed.
//
// The one exception is a record that cannot be encoded: it is not published, its source is
// acked, and the loss is counted on dead_letter_lost_total (see loseRecord).
//
// A record that was stored but whose PubAck was lost is published again when its source is
// redelivered; the dedup id (sourceDedupID) keeps that second copy out of the stream.
func (iproc *InboundEventsProcessor) ProcessFailedEvent(ctx context.Context) bool {
	item, more := <-iproc.failed
	if !more {
		return true
	}
	log.Debug().Str("message", item.event.Message).Msg("received failed event")

	// 🔴 A RECORD THAT WILL NOT ENCODE IS NOT PUBLISHED. Falling through published a message with
	// a nil Value, which proto3 decodes WITHOUT ERROR as an empty failure with reason 0 — a record
	// that reads as plausible and says nothing, stored in place of the real one, with the source
	// acked behind it.
	//
	// The encoder can refuse: PFailedEvent is proto3, and a proto3 string field holding invalid
	// UTF-8 does not marshal. Nothing a device sends reaches it today; this is the defect path.
	//
	// It settles through the writer's Fail, like every other outcome on it, so it is acked in
	// order behind the publishes already in flight and on the writer's settle goroutine. (Acking
	// out of order would not be a correctness fault, the consumers are AckExplicit; the order is
	// kept so one writer has one story, and TestAnUnencodableRecordIsAckedBehindEarlierOutcomes
	// pins it.) Fail does not back the writer off, and the settle loop does not log it as a
	// broker failure: nothing is wrong with the broker.
	bytes, err := resolvedevent.MarshalFailedEvent(&item.event)
	if err != nil {
		iproc.FailedEventsWriter.Fail(err, func(error) {
			iproc.loseRecord("a failed event's record", item.tenant, item.event.Reason, item.src,
				item.correlation, err)
		})
		return false
	}

	// Create and deliver message on the failed event's tenant subject.
	msg := messaging.Message{
		Key:   []byte(strconv.FormatInt(int64(item.event.Reason), 10)),
		Value: bytes,
		// A source produces one record, so its fan-out position is always 0.
		DedupID: sourceDedupID(dedupKindFailed, item.tenant, item.src, 0),
	}.WithCorrelationID(item.correlation)
	src := item.src
	iproc.FailedEventsWriter.Publish(core.WithTenant(ctx, item.tenant), msg, func(err error) {
		if err == nil {
			_ = src.Ack()
		}
	})
	return false
}

// loseRecord settles an inbound message whose record of failure cannot be encoded, so there is
// nothing to publish. The source is acked, because this is terminal: a redelivery resolves to
// the same failure and the same record, which will not encode either. The failure is then
// recorded nowhere, which is a loss, so it is counted on the service's dead_letter_lost_total —
// the series DeadLetterWriteLost selects — and logged as LOST.
//
// Both arms that can reach it (the failed-event record and the archived unresolved event) go
// through here, so the policy is written once. A failed Ack leaves the message to be
// redelivered and so counts the loss again on the next delivery: the count may overstate.
//
// The log carries server-derived fields only; the record's own text is what failed to encode.
func (iproc *InboundEventsProcessor) loseRecord(what string, tenant string, reason uint,
	src messaging.Message, correlation string, err error) {
	iproc.deadLetters.Lost()
	log.Error().Err(err).Str("tenant", tenant).Uint("reason", reason).
		Str("subject", src.Subject).Uint64("streamSeq", src.StreamSeq).Str("correlation", correlation).
		Msgf("LOST: %s could not be encoded, a defect in device-management; nothing was published and "+
			"the source is acknowledged, so the failure is recorded nowhere else", what)
	_ = src.Ack()
}

// invalidEventErrorCap bounds the decode error recorded on an undecodable message.
//
// The error is kept, because it is the only thing that says HOW the message was
// malformed. But it is not purely server-derived: UnmarshalUnresolvedEvent fails at
// time.Parse for bytes that decode as an event carrying an unparseable instant, and
// time.Parse quotes the offending string back verbatim and unboundedly. Recorded raw,
// that puts a payload-derived blob of arbitrary size into the archived record — which
// is the thing dropping the payload removes, arriving through a second door. The cap
// is well clear of every decode error the platform's own encoders can produce, so
// bounding costs nothing on the failures an operator actually reads.
const invalidEventErrorCap = 256

// boundDecodeError caps a decode error's text at invalidEventErrorCap, marking the
// truncation so a reader is never shown a shortened error that looks complete. It cuts
// on a rune boundary, since the text it bounds can be arbitrary decoded content and a
// mid-rune cut would put invalid UTF-8 into a durable record.
func boundDecodeError(text string) string {
	if len(text) <= invalidEventErrorCap {
		return text
	}
	cut := invalidEventErrorCap
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "... (truncated)"
}

// Called when a message can not be unmarshaled to an event. The tenant is
// re-derived from the message subject (the resolver only reaches this callback
// after confirming the subject carries a parseable tenant).
//
// The undecodable bytes are NOT archived on the record. They are device-originated
// content that failed to parse, which is exactly the case where what they hold is
// least predictable, and the record is durable — retained for the failed-events
// stream's window and exported whole by an operator debugging ingest.
//
// Dropping them costs nothing that has to be paid, because this message is not the
// only copy of itself: it was read from the inbound-events stream and it is still
// there, untouched, at the stream sequence recorded below. So the record LOCATES the
// original instead of copying it — the shape the platform's dead-letter envelope
// already uses for a connector dispatch whose body lives one stream over.
//
// 🔴 THE POINTER IS TO THE SOURCE STREAM, AND IT IS ONLY GOOD WHILE THAT STREAM STILL
// HOLDS THE MESSAGE. inbound-events carries every event the platform ingests, so on a
// busy instance it rolls off its byte ceiling long before this cold error stream rolls
// off its own. The locator is for the operator looking at a fault while it is
// happening, which is when an undecodable message is worth looking at. It is not an
// archive and is not written as one.
//
// Everything the record does carry is server-derived — the subject, the stream
// sequence, the delivery count, the byte length — so nothing here can echo the content
// it declines to store. What does not change is that the record still reads as a
// failure: the reason stays Invalid and the decode error still travels, so nothing
// downstream can mistake a message the platform could not parse for one it handled.
func (iproc *InboundEventsProcessor) OnInvalidEvent(err error, msg messaging.Message) {
	tenant, ok := messaging.ParseTenantFromSubject(msg.Subject)
	if !ok {
		// Nothing can be recorded against no tenant, and redelivery cannot supply one, so
		// the message is dropped as the resolver drops any untenanted message.
		log.Warn().Msg(fmt.Sprintf("Dropping invalid event with no parseable tenant in subject %q", msg.Subject))
		_ = msg.Ack()
		return
	}
	// Where the original is and how big it is. It goes in the record's message because
	// FailedEvent has no structured place for it, and the failed-events stream has no
	// consumer to give one to — this text and the log line below are what an operator
	// actually reads.
	locator := fmt.Sprintf("%d bytes on subject %q at stream sequence %d, delivery %d",
		len(msg.Value), msg.Subject, msg.StreamSeq, msg.NumDelivered)
	bounded := boundDecodeError(err.Error())
	// Logged at warn because nothing consumes failed-events: a record published there is
	// retained for the cold tier's window and expires unread, so without this line the
	// only operator-visible trace of an undecodable message is a RED counter that says
	// neither which message nor why.
	log.Warn().Str("correlation", msg.CorrelationID()).
		Msg(fmt.Sprintf("Dead-lettering an inbound message that could not be parsed (%s): %s", locator, bounded))
	failed := resolvedevent.NewFailedEvent(uint(resolvedevent.FailureReason_Invalid), iproc.Microservice.FunctionalArea,
		"message could not be parsed; payload not retained ("+locator+")", err, nil)
	failed.Error = bounded
	iproc.failed <- failedItem{tenant: tenant, event: *failed, src: msg, correlation: msg.CorrelationID()}
}

// Called when an event can not be resolved. src is the inbound message, acked once the
// failed-event record is stored; its correlation id is carried onto that record for
// traceability (E15).
func (iproc *InboundEventsProcessor) OnUnresolvedEvent(src messaging.Message, tenant string, reason uint, unrez esmodel.UnresolvedEvent, rezerr error) {
	correlation := src.CorrelationID()
	// Drop the presented credential before the event is archived. The dead-letter
	// record is durable — it outlives the request by the stream's retention and is
	// the thing an operator exports when debugging ingest — and by the time
	// resolution has failed the credential has already served its only purpose, so
	// the archived record does not need it and must not keep it. The resolution
	// REASON, which is server-derived, is what identifies an authentication failure
	// here and in the log line below.
	//
	// unrez arrives by value and WithoutPresentedCredential returns a copy, so this
	// cannot reach the resolver's own event: the authentication decision was already
	// made, upstream, on the full credential. Nothing here can turn a rejection into
	// something that reads as a success — the reason and the error travel unchanged.
	archived := unrez.WithoutPresentedCredential()

	// Marshal event message to protobuf.
	bytes, err := esproto.MarshalUnresolvedEvent(&archived)
	if err != nil {
		// Terminal either way: the same event will not marshal on a redelivery.
		iproc.loseRecord("an unresolved event's archive", tenant, reason, src, correlation, err)
	} else {
		// Log the resolution failure REASON before dead-lettering. Without this the
		// cause travels only inside the dead-lettered FailedEvent payload — an
		// authentication rejection, a bad presence enum, a foreign device token all
		// looked identical from the logs (a "could not be resolved" debug line with no
		// error), which hid a real presence-auth bug for a long time. WARN, not ERROR:
		// a misbehaving producer can flood this, and each line is a single dropped event.
		//
		// A refused gateway publish has its own rate-limited WARN naming tenant, gateway and
		// child (gatewayRefusals.refused), so this unbounded line is Debug for it: a gateway
		// misassigned over a whole site would otherwise log once per event.
		level := log.Warn()
		if reason == uint(resolvedevent.FailureReason_NotGatewayChild) {
			level = log.Debug()
		}
		level.Err(rezerr).Uint("reason", reason).Str("tenant", tenant).
			Str("source", unrez.Source).Str("device", unrez.Device).
			Str("eventType", unrez.EventType.String()).Str("correlation", correlation).
			Msg("event could not be resolved; dead-lettering")
		failed := resolvedevent.NewFailedEvent(reason, iproc.Microservice.FunctionalArea,
			"event could not be resolved", rezerr, bytes)
		iproc.failed <- failedItem{tenant: tenant, event: *failed, src: src, correlation: correlation}
	}
}

// Handle case where event was successfully resolved. The source message is acked
// only after the last resolved event it produced has been durably published, and
// left unacked if any publish fails so AckWait redelivers the whole message (A3).
//
// The publish is handed to the resolved writer, which keeps up to resolution.publishWindow (config.DefaultPublishWindow) in
// flight and reports each outcome to settleResolved in the order submitted here.
func (iproc *InboundEventsProcessor) ProcessResolvedEvent(ctx context.Context) bool {
	item, more := <-iproc.resolved
	if !more {
		return true
	}
	coord := item.coord
	settle := func(err error) { iproc.settleResolved(coord, err) }
	bytes, err := resolvedevent.MarshalResolvedEvent(&item.event)
	if err != nil {
		log.Error().Err(err).Msg("unable to marshal resolved event to protobuf")
		// Through the writer, not inline: the outcome must reach the coordinator on the
		// writer's settle goroutine, in order, like every published one.
		iproc.ResolvedEventsWriter.Fail(err, settle)
		return false
	}
	msg := messaging.Message{
		// The source device token, carried as the producer-local Key. Read the
		// next paragraphs before relying on it for anything: this is NOT what
		// keeps a device's events in order.
		//
		// Key was the Kafka partition key, and on Kafka it did order a device's
		// events by hashing them onto one partition. On JetStream there are no
		// partitions and Key is not even transmitted — the writer builds the
		// nats.Msg from Value plus headers only (core/messaging/nats.go), and
		// nothing on the read side reconstructs it, which is exactly what the
		// Message doc means by "producer-local and not transmitted, since no
		// consumer reads it".
		//
		// What DETECT actually relies on is the stream's own order, not the number
		// of writers. JetStream gives every message on resolved-events a gapless,
		// monotonic stream sequence; the engine applies events in that order,
		// checkpoints it and replays from it, so a replay sees exactly the order the
		// live run saw however many pods wrote the stream.
		//
		// There is more than one writer, and nothing should assume otherwise. Every
		// device-management replica runs this loop; a rolling update runs the old
		// and the new pod side by side; and even inside one pod the resolver pool
		// (resolution.workers resolvers wide) finishes events out of arrival order, so
		// two events from one device can reach this loop — and the stream — in either
		// order. The writer then keeps
		// several publishes in flight (messaging.OrderedWriter); they reach the
		// stream in the order they are submitted here.
		//
		// So a device's events can arrive out of event-time order in two ways, and
		// only the first is small:
		//
		//   - a REORDER, from the resolver pool, several replicas or a rollout: a
		//     fraction of a second. DETECT still applies the late event in stream
		//     order; windowed rules count it if it arrives within their allowed
		//     lateness, and every other rule kind discards a reading older than one
		//     it has already seen.
		//   - a REDELIVERY: a publish that fails leaves its source unacked, and the
		//     source is resolved and published again no sooner than the inbound
		//     AckWait (60 s) later. That is far beyond the default lateness, so the
		//     event is LATE to detection. Nothing here removes that; only a lateness
		//     above AckWait would absorb it.
		//
		// No partition key and no writer count restores a per-device publish order.
		//
		// The token is still the right value to put here — it is 1:1 with the
		// device and no id crosses the seam (ADR-044) — so it stays as fidelity
		// for a transport that does read a key. It is not load-bearing today.
		Key:     []byte(item.event.SourceDeviceToken),
		Value:   bytes,
		DedupID: resolvedDedupID(item.tenant, coord, item.index),
	}.WithCorrelationID(item.correlation)
	iproc.ResolvedEventsWriter.Publish(core.WithTenant(ctx, item.tenant), msg, settle)
	return false
}

// resolvedDedupID names one resolved-event publish for the broker's duplicate window; see
// sourceDedupID.
func resolvedDedupID(tenant string, coord *ackCoord, index int) string {
	if coord == nil {
		return ""
	}
	return sourceDedupID(dedupKindResolved, tenant, coord.src, index)
}

// sourceDedupID names one publish made on behalf of inbound message src — the index-th
// resolved event it produced, or its failed-event record — for the broker's duplicate
// window, so a publish that was stored but whose acknowledgement was lost (a timeout, a
// reconnect, a leader change) is not stored a second time when its source is redelivered
// and published again. Both streams declare the redelivery span as their window
// (streams.RedeliveryDuplicateWindowSeconds, 600 s), which outlasts the last of a
// source's MaxDeliver redeliveries; the first comes one AckWait (60 s) after the fetch.
//
// The id is built only from values that survive a redelivery unchanged and cannot be
// supplied by a device: the tenant, the source's inbound stream sequence and the
// publish's position in the source's fan-out. The sequence alone is unique across tenants
// (one inbound stream carries them all); the tenant is there because a dedup id is
// stream-scoped and every id on a shared stream is kept tenant-scoped (see
// messaging.Message.DedupID). kind keeps the two streams' ids apart for a reader of either.
//
// The id is COMPACT because it is paid for many times over: it rides every resolved event,
// which is stored on every replica and delivered to every consumer, and every broker server
// holds it in memory for the whole window. So the kind is one letter and the sequence and
// index are in base 36 — "r:acme:21i3v9:0", not "resolved:acme:123456789:0". Base 36 is a
// bijection, and no field can contain the ':' between them (the tenant is a token), so the
// id still names exactly one (kind, tenant, sequence, index): compactness costs neither the
// determinism a redelivery relies on nor the uniqueness that keeps a real event from being
// discarded as a duplicate. TestSourceDedupIDIsLossless decodes it back to prove both.
//
// 🔴 CHANGING THIS SHAPE IS AN UPGRADE EVENT. A source published by one build and
// redelivered to a pod running another carries two different ids, and the window lets both
// through. A rolling upgrade drains each pod before it stops (every publish settles and its
// source is acked before the connection drains), so what is exposed is a publish whose
// acknowledgement was lost in the rollout itself, redelivered to a pod of the other build:
// stored twice, which event-management's event key absorbs and DETECT applies twice.
// And a new shape must be DISJOINT from the old one, not merely different: an id of one
// build that equals an id the other wrote for a DIFFERENT source is answered as a duplicate
// and that event is discarded. Every decimal string is also a base-36 one, so it is the
// one-letter kinds ("r", not the previous build's "resolved") that keep them apart;
// TestSourceDedupIDNeverMatchesAPreviousBuildsID.
//
// A source with no stream sequence (broker metadata unavailable) gets NO id, never a
// shared one: every such source would carry the same sequence, 0, and the second would
// be discarded as a duplicate of the first.
func sourceDedupID(kind, tenant string, src messaging.Message, index int) string {
	if src.StreamSeq == 0 {
		return ""
	}
	// Concatenated rather than formatted: one is built for every resolved event published.
	return kind + ":" + tenant + ":" + strconv.FormatUint(src.StreamSeq, 36) + ":" + strconv.FormatUint(uint64(index), 36)
}

// The kinds sourceDedupID is called with: one letter each, since the id rides every event.
const (
	dedupKindResolved = "r"
	dedupKindFailed   = "f"
)

// settleResolved records the outcome of publishing one resolved event against
// its source's ack coordinator: a publish failure latches failure so the source
// is left unacked (AckWait redelivers the whole message); success decrements the
// outstanding count and acks the source when the last resolved event has been
// published.
//
// It runs only on the resolved writer's settle goroutine (messaging.OrderedWriter),
// in the order the events were submitted, and err is nil only after the broker's
// PubAck.
func (iproc *InboundEventsProcessor) settleResolved(coord *ackCoord, err error) {
	if coord == nil {
		return
	}
	if err != nil {
		// A publish failed: latch failure so the source is never acked, which leaves
		// the whole message for AckWait-paced redelivery. Do NOT nak — an immediate
		// nak would burn MaxDeliver in ~1.4ms inside a downstream outage (ADR-030).
		coord.failed = true
		return
	}
	coord.remaining--
	if coord.remaining == 0 && !coord.failed {
		_ = coord.src.Ack()
	}
}

// Called when an event is successfully resolved. An event resolving to no tracked
// relationships produces no output, so the source is acked immediately; otherwise
// a coordinator acks it once all of its resolved events have been published.
func (iproc *InboundEventsProcessor) OnResolvedEvent(src messaging.Message, tenant string, events []EventResolutionResults) {
	if len(events) == 0 {
		_ = src.Ack()
		return
	}
	coord := &ackCoord{src: src, remaining: len(events)}
	correlation := src.CorrelationID()
	for i, event := range events {
		iproc.resolved <- resolvedItem{tenant: tenant, event: *event.Resolved, coord: coord, index: i, correlation: correlation}
	}
}

// Initialize pool of workers for resolving events.
func (iproc *InboundEventsProcessor) initializeEventResolvers(ctx context.Context) error {
	// Make channels and workers for distributed processing.
	iproc.messages = make(chan messaging.Message, MESSAGE_BACKLOG_SIZE)
	iproc.resolvers = make([]*EventResolver, 0)
	// ONE memo for the whole pool (ADR-078). The workers all read the same channel, so
	// successive events from one device land on arbitrary workers; a per-worker memo
	// would bound the undeclared-position warning per worker and report the same
	// misconfiguration once per worker instead of once.
	locationMemo := newUndeclaredLocationMemo()
	// ONE slow-resolve reporter for the pool, for the same reason: a stall holds every
	// worker at once, and a reporter per worker would log it once per worker.
	slow := newSlowResolveReporter()
	// ONE refusal reporter for the pool too: the WARN limit is per (tenant, gateway, child), and
	// a limiter per worker would log the same pair once per worker.
	gateway := newGatewayRefusals()
	gateway.counter = iproc.metrics.notGatewayChild
	gateway.noticer = iproc.gatewayNoticer
	// ONE counter for the whole pool, for the same reason as the memo above: the
	// workers share the inbound channel, so a per-worker counter would report a
	// fleet's clock skew as N unrelated series. It comes from the instruments built in
	// the initialize phase rather than being constructed here, because a counter belongs
	// to the process, while this processor — and so this method — is built anew every
	// time the connection-scoped oncreate callback runs.
	eventTime := EventTimePolicy{
		MaxFutureSkew: iproc.MaxFutureSkew,
		Bounded:       iproc.metrics.eventTimeBounded,
		TooOld:        iproc.metrics.eventTimeTooOld,
	}
	// The ONE place the pool's width is decided: the configured count, or the configuration's
	// default when none was set. A count the configuration would refuse is refused here too,
	// rather than starting a pool that resolves nothing.
	width := config.ResolutionConfiguration{Workers: iproc.resolverCount}
	width.ApplyDefaults()
	count := width.Workers
	if count < 1 {
		return fmt.Errorf("the inbound resolver pool needs at least one resolver, got %d", count)
	}
	iproc.resolverCount = count
	if iproc.metrics.workers != nil {
		iproc.metrics.workers.Set(float64(count))
	}
	log.Info().Int("resolution.workers", count).Msg("Resolving inbound events")
	iproc.holdCtx, iproc.holdCancel = context.WithCancel(context.Background())
	for w := 1; w <= count; w++ {
		resolver := NewEventResolver(w, iproc.Api, iproc.AuthMode, eventTime, iproc.messages,
			iproc.OnInvalidEvent, iproc.OnResolvedEvent, iproc.OnUnresolvedEvent, iproc.metrics.red,
			locationMemo)
		resolver.slow = slow
		resolver.SecretTransit = iproc.secretTransit
		resolver.tenantDeleted = iproc.tenantDeleted
		resolver.deviceDisabled = iproc.metrics.deviceDisabled
		resolver.integrationAliasMismatch = iproc.metrics.integrationAliasMismatch
		resolver.gateway = gateway
		resolver.hold = iproc.hold
		resolver.holdCtx = iproc.holdCtx
		resolver.calcObs.metrics = iproc.metrics.calc
		iproc.resolvers = append(iproc.resolvers, resolver)
		// Resolvers run on a background context (not the cancelable read context)
		// so that on shutdown they drain the remaining buffered messages to
		// completion and ack them rather than aborting in-flight resolution.
		iproc.workerWG.Add(1)
		go func(r *EventResolver) {
			defer iproc.workerWG.Done()
			r.Process(context.Background())
		}(resolver)
	}
	return nil
}

// Initialize outbound processing.
func (iproc *InboundEventsProcessor) initializeOutboundProcessing(ctx context.Context) {
	iproc.failed = make(chan failedItem, FAILED_EVENT_BACKLOG_SIZE)
	iproc.resolved = make(chan resolvedItem, RESOLVED_EVENT_BACKLOG_SIZE)
}

// Initialize component.
func (iproc *InboundEventsProcessor) Initialize(ctx context.Context) error {
	return iproc.lifecycle.Initialize(ctx)
}

// Lifecycle callback that runs initialization logic.
func (iproc *InboundEventsProcessor) ExecuteInitialize(ctx context.Context) error {
	// Derive the cancelable context the read loop runs under (E10/A5).
	iproc.procCtx, iproc.procCancel = context.WithCancel(ctx)

	// Initialize pool of event resolvers.
	if err := iproc.initializeEventResolvers(ctx); err != nil {
		return err
	}

	// Initialize outbound processing channels.
	iproc.initializeOutboundProcessing(ctx)
	return nil
}

// Start component.
func (iproc *InboundEventsProcessor) Start(ctx context.Context) error {
	return iproc.lifecycle.Start(ctx)
}

// readLoop drains the inbound-events stream through reader i into the resolvers until the
// context is cancelled, the reader reports EOF, or a run of non-EOF read errors outlasts the
// pacer's budget — at which point the pacer has already ended the process, because a service
// that cannot read its own inbound stream is doing nothing while reporting healthy, and a
// restart is the remedy for most of what causes it.
//
// Every reader runs this loop on its own goroutine, and all of them hand off into the one
// messages channel. A reader is never read by two loops (a natsReader refuses an overlapping
// read with ErrConcurrentRead), so each keeps its own pull in flight and N readers keep N.
//
// It is a named method rather than the body of the goroutine in ExecuteStart so the pacing
// tests can drive the SAME line production runs.
func (iproc *InboundEventsProcessor) readLoop(ctx context.Context, i int) {
	messaging.RunConsumer(ctx, iproc.InboundEventsReaders[i], iproc.pacer(i), func(msg messaging.Message) bool {
		return iproc.handOff(ctx, msg)
	})
}

// handOff gives one inbound event to the resolvers, and reports whether the read loop should
// carry on. It abandons the handoff on shutdown so the loop can exit instead of blocking on
// a full channel (A5). The message is unacked at that point, so it redelivers after restart.
func (iproc *InboundEventsProcessor) handOff(ctx context.Context, msg messaging.Message) bool {
	select {
	case iproc.messages <- msg:
		return true
	case <-ctx.Done():
		return false
	}
}

// Lifecycle callback that runs startup logic.
func (iproc *InboundEventsProcessor) ExecuteStart(ctx context.Context) error {
	// Refused before anything starts, so a refusal leaves no goroutine behind to drain.
	if len(iproc.InboundEventsReaders) == 0 {
		return fmt.Errorf("the inbound events processor has no reader: it would resolve nothing")
	}
	for i, r := range iproc.InboundEventsReaders {
		if r == nil {
			return fmt.Errorf("inbound reader %d of %d is nil", i+1, len(iproc.InboundEventsReaders))
		}
		// Built here, before any loop runs, so no two goroutines build pacers at once.
		iproc.pacer(i)
	}
	// Processing loop for failed events (drains until the failed channel closes).
	iproc.outboundWG.Add(1)
	go func() {
		defer iproc.outboundWG.Done()
		for {
			eof := iproc.ProcessFailedEvent(ctx)
			if eof {
				break
			}
		}
	}()
	// Processing loop for resolved events (drains until the resolved channel closes).
	iproc.outboundWG.Add(1)
	go func() {
		defer iproc.outboundWG.Done()
		for {
			eof := iproc.ProcessResolvedEvent(ctx)
			if eof {
				break
			}
		}
	}()
	// One read loop per inbound reader, each under the cancelable context so ExecuteStop can
	// stop every one of them before the channels are closed. They are counted on readerWG
	// together, so the stop waits for the LAST reader: a reader whose pull is still in flight
	// when the context ends returns within the fetch timeout, and handing off what it brings
	// back after messages closed would be a send on a closed channel.
	log.Info().Int("resolution.inboundReaders", len(iproc.InboundEventsReaders)).Msg("Reading inbound events")
	iproc.readerWG.Add(len(iproc.InboundEventsReaders))
	for i := range iproc.InboundEventsReaders {
		go func(i int) {
			defer iproc.readerWG.Done()
			iproc.readLoop(iproc.procCtx, i)
		}(i)
	}
	return nil
}

// Stop component.
func (iproc *InboundEventsProcessor) Stop(ctx context.Context) error {
	return iproc.lifecycle.Stop(ctx)
}

// Lifecycle callback that runs shutdown logic. It unwinds the pipeline in
// dependency order so no goroutine ever sends on a closed channel (A5): stop the
// reader, then close the channel it feeds, then wait for the resolvers it feeds
// before closing the channels they feed, then wait for the outbound loops.
func (iproc *InboundEventsProcessor) ExecuteStop(context.Context) error {
	// A resolver holding an event for a store that is down would block the drain below
	// forever: end every hold first. Those events stay unacked and the next pod gets them.
	if iproc.holdCancel != nil {
		iproc.holdCancel()
	}
	if iproc.procCancel != nil {
		iproc.procCancel()
	}
	iproc.readerWG.Wait() // every reader stopped: no more sends to messages
	// Nothing more is fetched from here on, so a failing stream no longer needs its
	// consumption slowed: what is left is only what this pod already holds, and every
	// source a failure leaves unacked is redelivered either way.
	if iproc.ResolvedEventsWriter != nil {
		iproc.ResolvedEventsWriter.Draining()
	}
	if iproc.FailedEventsWriter != nil {
		iproc.FailedEventsWriter.Draining()
	}
	close(iproc.messages)   //
	iproc.workerWG.Wait()   // resolvers drained + exited: no more sends to resolved/failed
	close(iproc.resolved)   //
	close(iproc.failed)     //
	iproc.outboundWG.Wait() // submitters drained: every item has been handed to a writer
	// Every handed-over publish now settles — its source acked on its PubAck, or left
	// unacked for redelivery — before this returns, which is before the NATS drain. The
	// publishes in flight together each wait at most the 5 s publish ceiling, and failures
	// are no longer backed off (Draining, above); the service's teardown budget bounds the
	// whole, and a source it cuts off is simply left unacked. A source redelivered while its
	// publish was still settling here — held past AckWait — is published again by whichever
	// pod gets it, and the dedup id (sourceDedupID) keeps that copy out of the stream for the
	// stream's duplicate window (streams.RedeliveryDuplicateWindowSeconds).
	if iproc.ResolvedEventsWriter != nil {
		iproc.ResolvedEventsWriter.Close()
	}
	if iproc.FailedEventsWriter != nil {
		iproc.FailedEventsWriter.Close()
	}
	return nil
}

// Terminate component.
func (iproc *InboundEventsProcessor) Terminate(ctx context.Context) error {
	return iproc.lifecycle.Terminate(ctx)
}

// Lifecycle callback that runs termination logic.
func (iproc *InboundEventsProcessor) ExecuteTerminate(context.Context) error {
	return nil
}
