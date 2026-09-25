// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	dmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/proto"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

const (
	EVENT_RESOLVER_COUNT        = 5   // Number of event resolvers running in parallel
	MESSAGE_BACKLOG_SIZE        = 100 // Number of inbound messages that can be read and waiting to be processed
	FAILED_EVENT_BACKLOG_SIZE   = 100 // Number of failed events that can be waiting to publish
	RESOLVED_EVENT_BACKLOG_SIZE = 100 // Number of resolved events that can be waiting to publish

	// PUBLISH_WINDOW is how many resolved (or failed) events may be awaiting their broker
	// acknowledgement at once (messaging.OrderedWriter). Each publish is a round trip — on a
	// replicated stream, a quorum commit — and a loop that waits for each before sending the
	// next resolves at most one event per round trip however many resolvers it runs.
	// Chosen with BenchmarkResolvedPublishThroughput: the whole pipeline measured no faster
	// with a wider window, and a narrower one is fewer publishes a reconnect fails at once
	// and fewer sources held unacked in the pod.
	PUBLISH_WINDOW = 128
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
	event       dmodel.ResolvedEvent
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
	event       dmodel.FailedEvent
	src         messaging.Message
	correlation string
}

type InboundEventsProcessor struct {
	Microservice         *core.Microservice
	InboundEventsReader  messaging.MessageReader
	ResolvedEventsWriter messaging.OrderedWriter
	FailedEventsWriter   messaging.OrderedWriter
	Api                  dmodel.DeviceManagementApi
	AuthMode             string
	// MaxFutureSkew bounds how far a device-reported instant may lead the server's
	// own clock before resolution replaces it with the ceiling. It is the platform's
	// ONE event-time knob and it lives here because resolution is the one place the
	// event time is decided (see EventTimePolicy).
	MaxFutureSkew time.Duration

	messages  chan messaging.Message
	failed    chan failedItem
	resolved  chan resolvedItem
	resolvers []*EventResolver

	// resolverCount is the size of the resolver pool; 0 means EVENT_RESOLVER_COUNT. It is
	// set only by the throughput benchmark, which measures how the pool width interacts
	// with the publish stage.
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

	// Shutdown coordination (A5): procCancel stops the read loop; the WaitGroups
	// let ExecuteStop drain senders before closing the channels they feed, so a
	// resolver or the reader can never send on a closed channel at SIGTERM.
	procCtx    context.Context
	procCancel context.CancelFunc
	readerWG   sync.WaitGroup
	workerWG   sync.WaitGroup
	outboundWG sync.WaitGroup

	// readPacer spaces out the retries after a non-EOF read error and ends the loop once
	// the errors stop clearing. Built on first use by pacer(), because this struct is also
	// assembled by literal in tests that never run the constructor.
	readPacer *core.ReadPacer

	lifecycle core.LifecycleManager
}

// pacer returns the read loop's error pacer, building it on first use. It is touched only
// by the single read goroutine, which is the pacer's own contract.
func (iproc *InboundEventsProcessor) pacer() *core.ReadPacer {
	if iproc.readPacer == nil {
		iproc.readPacer = core.NewReadPacer(iproc.Microservice, "inbound events")
	}
	return iproc.readPacer
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
	return ResolveMetrics{
		red: ms.NewProcessorMetrics("resolve"),
		eventTimeBounded: ms.NewCounter(
			"resolve_event_time_bounded_total",
			"Reported event times refused for leading the server clock by more than the configured tolerance, and replaced with the ceiling"),
	}
}

// Create a new inbound events processor. authMode is the device authentication
// policy applied while resolving inbound events (transport security, ADR-014);
// maxFutureSkew bounds a device-reported event time against the server's own clock.
//
// metrics is built once in the initialize phase (see NewResolveMetrics) and shared by
// every processor this service constructs, because this constructor runs again on every
// start.
func NewInboundEventsProcessor(ms *core.Microservice, inbound messaging.MessageReader, resolved messaging.OrderedWriter,
	failed messaging.OrderedWriter, callbacks core.LifecycleCallbacks, api dmodel.DeviceManagementApi, authMode string,
	maxFutureSkew time.Duration, metrics ResolveMetrics) *InboundEventsProcessor {
	iproc := &InboundEventsProcessor{
		Microservice:         ms,
		InboundEventsReader:  inbound,
		ResolvedEventsWriter: resolved,
		FailedEventsWriter:   failed,
		Api:                  api,
		AuthMode:             authMode,
		MaxFutureSkew:        maxFutureSkew,
		metrics:              metrics,
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
func (iproc *InboundEventsProcessor) ProcessFailedEvent(ctx context.Context) bool {
	item, more := <-iproc.failed
	if !more {
		return true
	}
	log.Debug().Str("message", item.event.Message).Msg("received failed event")

	// Marshal event message to protobuf.
	bytes, err := proto.MarshalFailedEvent(&item.event)
	if err != nil {
		log.Error().Err(err).Msg("unable to marshal event to protobuf")
	}

	// Create and deliver message on the failed event's tenant subject.
	msg := messaging.Message{
		Key:   []byte(strconv.FormatInt(int64(item.event.Reason), 10)),
		Value: bytes,
	}.WithCorrelationID(item.correlation)
	src := item.src
	iproc.FailedEventsWriter.Publish(core.WithTenant(ctx, item.tenant), msg, func(err error) {
		if err == nil {
			_ = src.Ack()
		}
	})
	return false
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
	failed := dmodel.NewFailedEvent(uint(proto.FailureReason_Invalid), iproc.Microservice.FunctionalArea,
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
		log.Error().Err(err).Msg("unable to marshal unresolved event to protobuf")
		_ = src.Ack()
	} else {
		// Log the resolution failure REASON before dead-lettering. Without this the
		// cause travels only inside the dead-lettered FailedEvent payload — an
		// authentication rejection, a bad presence enum, a foreign device token all
		// looked identical from the logs (a "could not be resolved" debug line with no
		// error), which hid a real presence-auth bug for a long time. WARN, not ERROR:
		// a misbehaving producer can flood this, and each line is a single dropped event.
		log.Warn().Err(rezerr).Uint("reason", reason).Str("tenant", tenant).
			Str("source", unrez.Source).Str("device", unrez.Device).
			Str("eventType", unrez.EventType.String()).Str("correlation", correlation).
			Msg("event could not be resolved; dead-lettering")
		failed := dmodel.NewFailedEvent(reason, iproc.Microservice.FunctionalArea,
			"event could not be resolved", rezerr, bytes)
		iproc.failed <- failedItem{tenant: tenant, event: *failed, src: src, correlation: correlation}
	}
}

// Handle case where event was successfully resolved. The source message is acked
// only after the last resolved event it produced has been durably published, and
// left unacked if any publish fails so AckWait redelivers the whole message (A3).
//
// The publish is handed to the resolved writer, which keeps up to PUBLISH_WINDOW in
// flight and reports each outcome to settleResolved in the order submitted here.
func (iproc *InboundEventsProcessor) ProcessResolvedEvent(ctx context.Context) bool {
	item, more := <-iproc.resolved
	if !more {
		return true
	}
	coord := item.coord
	settle := func(err error) { iproc.settleResolved(coord, err) }
	bytes, err := proto.MarshalResolvedEvent(&item.event)
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
		// finishes events out of arrival order, so two events from one device can
		// reach this loop — and the stream — in either order. The writer then keeps
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

// resolvedDedupID names one resolved-event publish for the broker's duplicate window, so a
// publish that was stored but whose acknowledgement was lost — a timeout, a reconnect, a
// leader change — is not stored a second time when its source is redelivered and published
// again. resolved-events declares no window of its own, so the broker's default (two
// minutes) applies, and the first redelivery comes one AckWait (60 s) after the fetch.
//
// The id is built only from values that survive a redelivery unchanged and cannot be
// supplied by a device: the tenant, the source's inbound stream sequence and the event's
// position in the source's fan-out. The sequence alone is unique across tenants (one inbound
// stream carries them all); the tenant is there because a dedup id is stream-scoped and
// every id on a shared stream is kept tenant-scoped (see messaging.Message.DedupID).
//
// A source with no stream sequence (broker metadata unavailable) gets NO id, never a
// shared one: every such source would carry the same sequence, 0, and the second would
// be discarded as a duplicate of the first.
func resolvedDedupID(tenant string, coord *ackCoord, index int) string {
	if coord == nil || coord.src.StreamSeq == 0 {
		return ""
	}
	return fmt.Sprintf("resolved:%s:%d:%d", tenant, coord.src.StreamSeq, index)
}

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
func (iproc *InboundEventsProcessor) initializeEventResolvers(ctx context.Context) {
	// Make channels and workers for distributed processing.
	iproc.messages = make(chan messaging.Message, MESSAGE_BACKLOG_SIZE)
	iproc.resolvers = make([]*EventResolver, 0)
	// ONE memo for the whole pool (ADR-078). The workers all read the same channel, so
	// successive events from one device land on arbitrary workers; a per-worker memo
	// would bound the undeclared-position warning per worker and report the same
	// misconfiguration once per worker instead of once.
	locationMemo := newUndeclaredLocationMemo()
	// ONE counter for the whole pool, for the same reason as the memo above: the
	// workers share the inbound channel, so a per-worker counter would report a
	// fleet's clock skew as N unrelated series. It comes from the instruments built in
	// the initialize phase rather than being constructed here, because a counter belongs
	// to the process, while this processor — and so this method — is built anew every
	// time the connection-scoped oncreate callback runs.
	eventTime := EventTimePolicy{
		MaxFutureSkew: iproc.MaxFutureSkew,
		Bounded:       iproc.metrics.eventTimeBounded,
	}
	count := iproc.resolverCount
	if count == 0 {
		count = EVENT_RESOLVER_COUNT
	}
	for w := 1; w <= count; w++ {
		resolver := NewEventResolver(w, iproc.Api, iproc.AuthMode, eventTime, iproc.messages,
			iproc.OnInvalidEvent, iproc.OnResolvedEvent, iproc.OnUnresolvedEvent, iproc.metrics.red,
			locationMemo)
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
	iproc.initializeEventResolvers(ctx)

	// Initialize outbound processing channels.
	iproc.initializeOutboundProcessing(ctx)
	return nil
}

// Start component.
func (iproc *InboundEventsProcessor) Start(ctx context.Context) error {
	return iproc.lifecycle.Start(ctx)
}

// readLoop drains the inbound-events stream into the resolvers until the context is
// cancelled, the reader reports EOF, or a run of non-EOF read errors outlasts the pacer's
// budget — at which point the pacer has already ended the process, because a service that
// cannot read its own inbound stream is doing nothing while reporting healthy, and a
// restart is the remedy for most of what causes it.
//
// It is a named method rather than the body of the goroutine in ExecuteStart so the pacing
// tests can drive the SAME line production runs.
func (iproc *InboundEventsProcessor) readLoop(ctx context.Context) {
	messaging.RunConsumer(ctx, iproc.InboundEventsReader, iproc.pacer(), func(msg messaging.Message) bool {
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
	// Processing loop for inbound messages (runs under the cancelable context so
	// ExecuteStop can stop it before the channels are closed).
	iproc.readerWG.Add(1)
	go func() {
		defer iproc.readerWG.Done()
		iproc.readLoop(iproc.procCtx)
	}()
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
	if iproc.procCancel != nil {
		iproc.procCancel()
	}
	iproc.readerWG.Wait() // reader stopped: no more sends to messages
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
	// whole, and a source it cuts off is simply left unacked.
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
