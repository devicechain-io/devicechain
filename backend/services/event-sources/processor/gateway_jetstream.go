// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-event-sources/model"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
)

// CAPTURE_PUBLISH_WINDOW is how many inbound-events publishes the gateway source keeps
// awaiting their PubAck at once (messaging.OrderedWriter), where it used to keep one per
// decode worker (DECODE_WORKER_COUNT). It is the same window device-management uses, and
// the curve that confirmed it is BenchmarkCaptureIngestThroughput's: in-process over
// loopback with 20,000 captured events (one tenant, 1,000 devices), median of three, in
// events per second,
//
//	      none     sync     w=1    w=128    w=256
//	R1  78,451   10,424   2,611   64,085   58,929
//	R3  39,705    4,704     828   23,062   21,574
//
// where none builds each message and publishes nothing — the stage's ceiling without a
// publish — and sync is the five synchronous publishes this replaced. The rule, fixed
// before measuring, was that the publish is a ceiling if sync falls below half of none at
// R3 (it is under an eighth), and that a window ships only if it at least doubles sync at
// R3 (128 is about 4.9x) and a wider one gains nothing (256 is within noise, and lower).
// The absolute numbers are a floor — a real network adds a round trip to every publish —
// and the ratios are the result.
const CAPTURE_PUBLISH_WINDOW = 128

// POISON_ROUTE_LIMIT bounds how many poison messages are routed to failed-decode at once.
// The route is a synchronous publish of up to 5 s, and it used to run on the decode worker
// that settled the message; it now runs off the ordered writer's settle goroutine, whose
// done callbacks must be quick. See settler.
const POISON_ROUTE_LIMIT = 4

// InboundMessageFunc builds the inbound-events message for one decoded event, with the
// context it is published under (it carries the tenant). ok=false is a terminal,
// deliberate drop (no tenant, an event that cannot be marshalled): the source message is
// settled nil and nothing is published — the ACK TRAP rule, as for every other drop.
type InboundMessageFunc func(source, tenant string, event *model.UnresolvedEvent, payload interface{},
	captureSeq uint64) (ctx context.Context, msg messaging.Message, ok bool)

// poisonRoutes bounds the failed-decode routes running at once (slots, POISON_ROUTE_LIMIT)
// and counts them (running), so a stop can wait for them before the connection drains.
//
// It is per start, like the rest of the pipeline, and each message's settler holds the one
// that was current when the message was taken. A stop that gives up at its deadline leaves
// that start's writer settling on its own, and those outcomes can still start routes; were
// the count shared, they would Add to a WaitGroup the next start's stop is waiting on, which
// the WaitGroup forbids. They count against their own start's instead, which nobody waits
// for any more.
type poisonRoutes struct {
	slots   chan struct{}
	running sync.WaitGroup
}

// NewInboundWriter builds the gateway source's ordered writer to inbound-events, with the
// CAPTURE_PUBLISH_WINDOW it was measured at. It lives here rather than in main.go so that
// the window the source actually runs with is the one a test in this package observes.
func NewInboundWriter(nmgr *messaging.NatsManager) (messaging.OrderedWriter, error) {
	return nmgr.NewOrderedWriter(streams.InboundEvents, CAPTURE_PUBLISH_WINDOW)
}

// pendingPublish is one built message on its way from a decode worker to the submitter.
type pendingPublish struct {
	ctx  context.Context
	msg  messaging.Message
	done func(error)
}

// GatewayJetStreamSource ingests device telemetry by consuming the durable
// capture stream the broker writes every device publish into, rather than by
// subscribing to the broker over MQTT (ADR-030 amendment).
//
// # Why this is not an MQTT client
//
// The MQTT source acknowledged the device as soon as the payload was on an
// in-memory channel — before anything durable had happened — so everything
// buffered at SIGKILL was silently lost, and the device had already been told the
// message was safe. That could not be fixed by acking later, for two independent
// reasons. paho defaults CleanSession, and NATS implements [MQTT-3.1.2-6] by
// discarding the session and every unacked message on disconnect, which is
// precisely the SIGKILL case. And NATS speaks MQTT 3.1.1 only, where shared
// subscriptions do not exist: a "$share" subscribe is ACCEPTED and then delivers
// nothing, and distinct client ids deliver every message to every replica — so
// the MQTT-client design could never exceed one replica at all.
//
// Consuming a capture stream deletes the whole category. The broker stores the
// device's publish in the capture stream itself, so the message is in a stream BEFORE
// our code runs (the PUBACK does not wait for that store; see
// streams.DeviceEventsCapture); there is no session, no client id, and no per-pod broker identity that
// scale-down could strand. It is the pattern command-delivery already runs in
// production (NewReader(streams.CommandResponses)).
//
// # Scope
//
// The gateway path only. An operator-configured EXTERNAL broker keeps the MQTT
// client and stays at-most-once by decision: on a broker we do not own, session
// and retention are the operator's configuration, so a durability claim there
// would be unenforceable.
type GatewayJetStreamSource struct {
	Id      string
	Decoder Decoder

	reader   messaging.MessageReader
	messages chan rawMessage
	workers  []*DecodeWorker

	// writer publishes to inbound-events with CAPTURE_PUBLISH_WINDOW publishes in flight.
	// Its ONE submitter is the goroutine submitLoop runs; the decode workers hand it
	// their built messages over publishes, which is UNBUFFERED on purpose: the window is
	// already the buffer, and a queue in front of it would only hold captured messages
	// here while the broker's AckWait clock runs on them — during a failure episode, when
	// the writer admits one publish per backoff, long enough to be redelivered unattempted.
	writer    messaging.OrderedWriter
	publishes chan pendingPublish
	// workersDone and submitterDone close when every decode worker, and the submitter,
	// of the current start have returned.
	workersDone   chan struct{}
	submitterDone chan struct{}
	// poison is the current start's failed-decode routes; see poisonRoutes.
	poison *poisonRoutes

	lifecycle core.LifecycleManager
	cancel    context.CancelFunc
	// drained closes once the read loop has returned, so Stop does not race the
	// loop into a closed message channel.
	drained chan struct{}

	received func(string, []byte)
	build    InboundMessageFunc
	failed   func(string, string, []byte, error) error
	// allow meters an inbound message against its tenant's ingest ceiling before it
	// is queued for decode; a false return sheds the message. nil disables metering.
	allow RateGate
	// readings charges a decoded message's readings against its tenant's ingest ceiling
	// (see ReadingGate). Never nil (the constructor refuses one).
	readings ReadingGate

	// readPacer spaces out the retries after a non-EOF read error and ends the loop once
	// the errors stop clearing. It carries the microservice a give-up is reported to, so
	// it is built in the constructor rather than lazily. Nil-safe by construction: a
	// source built with no microservice (as tests do) still paces, and its give-up stops
	// the loop and logs instead of ending a process there is none of.
	readPacer *core.ReadPacer
}

// NewGatewayJetStreamSource builds the capture-stream source. The reader and the
// inbound-events writer are supplied separately by SetReader and SetWriter, because at
// the point sources are built neither exists yet — see SetReader. A nil readings panics:
// a source built without the reading stage would charge every message one unit however
// many readings it carried.
func NewGatewayJetStreamSource(ms *core.Microservice, id string, decoder Decoder,
	received func(string, []byte),
	build InboundMessageFunc,
	failed func(string, string, []byte, error) error,
	allow RateGate, readings ReadingGate) *GatewayJetStreamSource {
	if readings == nil {
		panic("processor.NewGatewayJetStreamSource: a reading gate is required")
	}
	es := &GatewayJetStreamSource{
		Id:        id,
		Decoder:   decoder,
		received:  received,
		build:     build,
		failed:    failed,
		allow:     allow,
		readings:  readings,
		readPacer: core.NewReadPacer(ms, "gateway capture"),
	}
	es.lifecycle = core.NewLifecycleManager("gateway-jetstream-event-source", es, core.NewNoOpLifecycleCallbacks())
	return es
}

// SetReader supplies the durable reader over streams.DeviceEventsCapture. It must
// be called before Start.
//
// The reader is NOT a constructor argument, and that is deliberate. Sources are
// built during the microservice's INITIALIZE phase, while every NATS reader is
// created during START — so at construction time the reader genuinely does not
// exist yet and any value passed for it can only be nil. Taking it as a parameter
// invited exactly that: the service passed a package-level reader variable that
// was still nil, built a source that looked correctly wired, and segfaulted in the
// read loop on the first startup after the capture path went live. Peer consumers
// (command-delivery, event-processing) already assign their readers inside the
// NATS oncreate callback for this reason; this makes that the only way to do it.
func (es *GatewayJetStreamSource) SetReader(reader messaging.MessageReader) {
	es.reader = reader
}

// SetWriter supplies the ordered writer to inbound-events. Like the reader it exists only
// from NATS component creation (START), after sources are built (INITIALIZE), so it is a
// setter, called beside SetReader. The source owns the writer from here: its ExecuteStop
// calls Draining and Close in the order the writer's contract requires, so the whole stop
// order lives in one place a test can run rather than in main.go, which none does.
func (es *GatewayJetStreamSource) SetWriter(writer messaging.OrderedWriter) {
	es.writer = writer
}

func (es *GatewayJetStreamSource) Initialize(ctx context.Context) error {
	return es.lifecycle.Initialize(ctx)
}

func (es *GatewayJetStreamSource) ExecuteInitialize(ctx context.Context) error { return nil }

func (es *GatewayJetStreamSource) Start(ctx context.Context) error {
	return es.lifecycle.Start(ctx)
}

// ExecuteStart brings up the publish pipeline (decode workers and the submitter) and the
// read loop.
func (es *GatewayJetStreamSource) ExecuteStart(ctx context.Context) error {
	// Refuse to start unwired. Without this the nil reader is not touched until the
	// read loop's first fetch, on a goroutine, where it panics the process with a
	// bare SIGSEGV that names neither the source nor the missing dependency. This
	// is the point of consumption, and it is the only place the wiring can be
	// checked at all: the service's own wiring lives in main.go, which no test runs.
	// The reader is checked first; the writer, wired beside it, next.
	if es.reader == nil {
		return fmt.Errorf("event source %q was started without a capture-stream reader: "+
			"SetReader must be called during NATS component creation, before start", es.Id)
	}
	if es.writer == nil {
		return fmt.Errorf("event source %q was started without an inbound-events writer: "+
			"SetWriter must be called during NATS component creation, before start", es.Id)
	}
	// Per-Start, not per-construction. These belong to the read loop this start is
	// about to spawn and to nothing else: ExecuteStop cancels that loop and closes the
	// message channel, so a set built once at construction would be a set the first
	// stop destroys. Building them here means a second entry into ExecuteStart — which
	// a start retried after a failed one produces — gets its own rather than a channel
	// the previous attempt already closed, which would make the read loop return
	// instantly and race a live loop into a closed channel.
	drained := make(chan struct{})
	es.drained = drained
	es.startPipeline(DECODE_WORKER_COUNT)

	loopCtx, cancel := context.WithCancel(context.Background())
	es.cancel = cancel
	// The loop is handed its OWN drained channel rather than reading the field, so
	// a lingering loop from a previous start attempt cannot close the current one's.
	go es.readLoop(loopCtx, drained)
	log.Info().Str("source", es.Id).Msg("Gateway event source consuming the device-events capture stream.")
	return nil
}

func (es *GatewayJetStreamSource) Stop(ctx context.Context) error {
	return es.lifecycle.Stop(ctx)
}

// startPipeline brings up, for one start, the decode workers and the ONE submitter that
// hands their messages to the writer. Everything it builds is per start, for the reason
// ExecuteStart gives, and the workers are handed the start's own publishes channel
// rather than reading the field, so a worker lingering from a stop cut short by its
// deadline cannot send into a later start's channel.
func (es *GatewayJetStreamSource) startPipeline(workers int) {
	messages := make(chan rawMessage, DECODE_CHANNEL_DEPTH)
	publishes := make(chan pendingPublish)
	workersDone := make(chan struct{})
	submitterDone := make(chan struct{})
	es.messages, es.publishes = messages, publishes
	es.workersDone, es.submitterDone = workersDone, submitterDone
	es.poison = &poisonRoutes{slots: make(chan struct{}, POISON_ROUTE_LIMIT)}

	submit := es.submitter(publishes)
	var running sync.WaitGroup
	es.workers = make([]*DecodeWorker, 0, workers)
	for w := 1; w <= workers; w++ {
		worker := NewDecodeWorker(w, es.Id, es.Decoder, messages, es.readings, submit, es.failed)
		es.workers = append(es.workers, worker)
		running.Add(1)
		go func() {
			defer running.Done()
			worker.Process()
		}()
	}
	go func() {
		running.Wait()
		close(workersDone)
	}()
	go submitLoop(publishes, es.writer, submitterDone)
}

// submitter returns the workers' DecodedFunc for one start: build the message and hand it
// to the ONE submitter. A terminal drop settles nil here, on the worker. done goes to the
// writer as it is: the writer's settle loop already logs a failed publish, and the settler
// logs it again with the tenant and capture sequence.
func (es *GatewayJetStreamSource) submitter(publishes chan<- pendingPublish) DecodedFunc {
	return func(source, tenant string, event *model.UnresolvedEvent, payload interface{},
		captureSeq uint64, done func(error)) {
		ctx, msg, ok := es.build(source, tenant, event, payload, captureSeq)
		if !ok {
			done(nil)
			return
		}
		publishes <- pendingPublish{ctx: ctx, msg: msg, done: done}
	}
}

// submitLoop is the writer's ONE submitter (the messaging.OrderedWriter contract). It
// returns when publishes is closed and drained, then closes done.
func submitLoop(publishes <-chan pendingPublish, writer messaging.OrderedWriter, done chan<- struct{}) {
	defer close(done)
	for p := range publishes {
		writer.Publish(p.ctx, p.msg, p.done)
	}
}

// ExecuteStop stops the read loop, then the decode workers, then the submitter, and
// returns once every message it took has been settled.
//
// The order is not cosmetic. Closing a channel while the goroutine ahead of it could still
// send would panic on a send to a closed channel, so each stage is observed to have
// returned before the channel behind it is closed: the read loop before the message
// channel, every decode worker before the publish channel, and the submitter before the
// writer's Close, which the writer's contract forbids while a submission is in progress.
// Close returns once every publish has settled — acked on its PubAck, or left UNACKED on a
// failure — and the poison routes those outcomes started are waited for last, so all of it
// happens before the service drains the NATS connection. The writer is told it is
// draining as soon as the read loop is cancelled, so a failing stream no longer holds the
// shutdown in backoff.
//
// Every wait is bounded by ctx. Past it the stop logs which stage it gave up on and
// returns, leaving what is unsettled UNACKED, which is the correct outcome: the capture
// stream redelivers it to whichever pod is running, the capture dedup id stores it once,
// and at-least-once is exactly the guarantee this source exists to provide.
func (es *GatewayJetStreamSource) ExecuteStop(ctx context.Context) error {
	if es.cancel != nil {
		es.cancel()
	}
	// A source stopped without ever having started has nothing to stop. The lifecycle
	// permits that stop, and without this it would wait out the whole deadline on a nil
	// channel.
	if es.drained == nil {
		return nil
	}
	// Draining before the read loop is waited for, not after: the loop can be blocked
	// handing a message into a full pipeline, and while a failing stream holds the
	// writer in backoff, room frees only one backoff at a time. Draining ends that
	// backoff, so the loop's last hand-off does not wait out one first. Nothing more is
	// fetched once the loop sees the cancel.
	es.writer.Draining()
	if !es.await(ctx, es.drained, "read loop") {
		return nil
	}
	es.stopPipeline(ctx)
	return nil
}

// stopPipeline stops what startPipeline started; see ExecuteStop. It reports whether
// every stage finished inside ctx. It calls Draining itself (a second call is a no-op) so
// that it is complete on its own, as the tests that run the pipeline without a read loop
// use it.
func (es *GatewayJetStreamSource) stopPipeline(ctx context.Context) bool {
	es.writer.Draining()
	close(es.messages)
	if !es.await(ctx, es.workersDone, "decode workers") {
		return false
	}
	close(es.publishes)
	if !es.await(ctx, es.submitterDone, "publish submitter") {
		return false
	}
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		es.writer.Close()
	}()
	if !es.await(ctx, closed, "inbound-events publishes") {
		return false
	}
	// Every done has run, so every poison route that will ever start has started: the
	// wait below cannot miss one.
	routed := make(chan struct{})
	poison := es.poison
	go func() {
		defer close(routed)
		poison.running.Wait()
	}()
	return es.await(ctx, routed, "failed-decode routes")
}

// await waits for done or ctx, logging the stage it gave up on.
func (es *GatewayJetStreamSource) await(ctx context.Context, done <-chan struct{}, stage string) bool {
	select {
	case <-done:
		return true
	case <-ctx.Done():
		log.Warn().Str("source", es.Id).Str("stage", stage).
			Msg("Gateway capture source did not stop before the shutdown deadline; what it has not settled is left unacked for redelivery.")
		return false
	}
}

func (es *GatewayJetStreamSource) Terminate(ctx context.Context) error {
	return es.lifecycle.Terminate(ctx)
}

func (es *GatewayJetStreamSource) ExecuteTerminate(ctx context.Context) error { return nil }

// readLoop pulls from the capture stream until the context is cancelled, the reader reports
// EOF, or a run of non-EOF read errors outlasts the pacer's budget, then closes drained to
// release ExecuteStop.
func (es *GatewayJetStreamSource) readLoop(ctx context.Context, drained chan struct{}) {
	defer close(drained)
	messaging.RunConsumer(ctx, es.reader, es.readPacer, func(msg messaging.Message) bool {
		es.handle(msg)
		return true
	})
	log.Info().Str("source", es.Id).Msg("Gateway capture read loop stopped.")
}

// handle admits one captured message into the decode pipeline, or terminally
// drops it.
//
// # THE ACK TRAP
//
// Every early return here must ACK. Under the previous MQTT source these were
// bare returns and that was correct, because paho had already auto-acked before
// the callback ran. Consuming a durable stream inverts it: a message that is not
// acked is REDELIVERED, so a fail-closed drop that simply returns becomes an
// infinite redelivery loop — the same message, dropped for the same reason,
// forever, at whatever rate the consumer can fetch. It melts the broker and no
// error is logged anywhere, because from the code's point of view each pass is
// working exactly as designed.
//
// This is the defect that reviews walk straight past, because every one of these
// returns is individually, obviously correct.
func (es *GatewayJetStreamSource) handle(msg messaging.Message) {
	// The tenant is the second subject segment. A message whose subject carries no
	// parseable tenant cannot be published to a tenant-scoped subject, so it is
	// dropped fail-closed rather than published unscoped.
	//
	// Note this should be UNREACHABLE: the capture stream's subject filter is the
	// device-events shape, so a delivered message has five segments by construction.
	// It is kept because "the filter guarantees it" is exactly the kind of invariant
	// that a later subject change breaks silently, and the cost of the check is a
	// string scan.
	tenant, ok := tenantFromSubject(msg.Subject)
	if !ok {
		log.Warn().Str("subject", msg.Subject).Msg("Dropping captured message with no parseable tenant.")
		ackDrop(msg, "no parseable tenant")
		return
	}
	// Validate the tenant token grammar before it is used as a rate-limiter key
	// (fail-closed, mirroring the HTTP path): the parse above only checks the
	// segment is non-empty, so without this an arbitrary segment could seed an
	// unbounded set of limiter buckets.
	if err := core.ValidateToken(tenant); err != nil {
		log.Warn().Err(err).Str("tenant", tenant).Msg("Dropping captured message with invalid tenant.")
		ackDrop(msg, "invalid tenant")
		return
	}

	// NOTE: there is deliberately no command-plane check here, and its absence is a
	// property rather than an omission. The MQTT source needed one because its
	// subscription could match command topics; this source consumes a stream whose
	// subject filter IS the device-events shape, which a command subject cannot
	// match. The exclusion moved from a denylist that had to name every internal
	// suffix — and had already gone stale once, naming two of them — into the
	// stream's own filter, where it cannot go stale. TestCaptureStreamFilterCannot
	// MatchInternalSubjects pins our side of it, and TestGatewaySubscriptionExcludes
	// InternalSubjects pins that a REAL broker agrees — the first checks the shape
	// arithmetic with a test-local matcher, so on its own it would only confirm that
	// our idea of subject matching agrees with itself.

	// Meter against the tenant's ingest ceiling before enqueue, so a tenant over its
	// limit spends no decode CPU.
	//
	// A shed message is ACKED, not left for redelivery. Leaving it unacked would
	// redeliver it into the same over-limit gate, which is not backpressure but a
	// spin — and on a SHARED consumer it would wedge the tenants behind it too.
	//
	// The message is metered at its CAPTURE APPEND TIME, not at now (ADR-030 I4).
	//
	// That is the BROKER's timestamp, assigned when it wrote the message — not a
	// value the device supplies. The distinction cuts both ways and both are wanted:
	// nothing device-controlled reaches the gate, so there is no device-clock
	// exploit; and a device that buffers locally for an hour then dumps on reconnect
	// gets append times of NOW and is metered as the burst it actually is. What is
	// recovered is a backlog that built up on OUR side, which is the failure this
	// source exists to survive.
	// Metered at now, an hour of a tenant's perfectly compliant traffic all "arrives"
	// within the seconds it takes to drain and is almost entirely shed — the platform
	// discarding messages it had already told the devices were safe, which would make
	// this source's whole reason for existing false. Metered at append time the same
	// backlog replays against the timeline it was produced on and is admitted in
	// full, while a tenant who genuinely exceeded their ceiling carries that burst in
	// their timestamps and is shed by exactly the amount they would have been had
	// there been no outage at all. Nothing is traded away for the recovery.
	//
	// A message with no append time (metadata unavailable) meters at now, which is
	// the pre-I4 behaviour — degraded to over-shedding a backlog, never to unmetered.
	//
	// 🔴 THE GATE IS CONSULTED ON EVERY DELIVERY, and the redelivery exemption is
	// passed IN rather than applied here. It used to be a `msg.NumDelivered <= 1 &&`
	// guard on this line, which was correct for the only refusal the gate carried at
	// the time and became wrong the moment a second one was composed in front of it:
	// the tenant-deleted refusal stopped being consulted on redeliveries, so a tenant
	// deleted between delivery 1 and a retry had that retry admitted. The exemption
	// belongs to the metering layer, which is the only layer it is true of.
	//
	// Readings are charged after decode, in the decode worker, on the same timeline
	// (meterAt, decided once here so both stages route the message to the same limiter)
	// and with the same redelivery exemption. A message over its tenant's reading ceiling
	// is ack-dropped there, as a message over the message ceiling is here.
	meterAt := meterTime(msg.AppendTime)
	redelivery := msg.NumDelivered > 1
	if es.allow != nil && !es.allow(es.Id, tenant, meterAt, redelivery, OriginAuthenticated) {
		ackDrop(msg, "refused at the tenant ingest gate")
		return
	}

	// A captured message with no stream sequence would publish with NO dedup id
	// (DedupID fails safe to ""), silently disabling ingest idempotency while every
	// unit test still passes — the dedup tests exercise DedupID directly, so none of
	// them sees the wiring. The sequence comes from broker metadata, which the
	// reader reports as 0 when it is unavailable, so this is the one place the
	// failure is observable at all. It is a warning rather than a drop: publishing
	// without dedup is degraded, not wrong.
	if msg.StreamSeq == 0 {
		log.Warn().Str("tenant", tenant).Str("subject", msg.Subject).
			Msg("Captured message carries no stream sequence; it will publish with NO dedup id " +
				"and a redelivery would be stored twice.")
	}

	// Count the arrival only once it clears the gate, so a shed message is not
	// counted as both inbound and rate-limited (matches the HTTP path). A message the
	// reading stage sheds after decode WAS received: it is counted as inbound and on the
	// reading-shed counters, never on the message stage's rate-limited counter.
	es.received(es.Id, msg.Value)

	es.messages <- rawMessage{
		tenant:     tenant,
		payload:    msg.Value,
		device:     deviceFromSubject(msg.Subject),
		captureSeq: msg.StreamSeq,
		receivedAt: msg.AppendTime,
		meterAt:    meterAt,
		redelivery: redelivery,
		origin:     OriginAuthenticated,
		done:       es.settler(msg, tenant),
	}
}

// settler returns the completion handler for a captured message: it acknowledges
// the capture stream only once the payload has been durably forwarded.
//
// A failure is retried by NOT acking, up to messaging.MaxDeliver attempts, after
// which the message is poison rather than unlucky and is routed to failed-decode
// and acked. Without that terminal step a message that can never be published
// would be redelivered until the broker gave up on it, taking the diagnostic with
// it — the same shape as the drop trap above, one level down.
func (es *GatewayJetStreamSource) settler(msg messaging.Message, tenant string) func(error) {
	poison := es.poison
	return func(handleErr error) {
		if handleErr == nil {
			if err := msg.Ack(); err != nil {
				// The publish DID succeed, so the event is not lost — but the ack did
				// not land, so the capture stream will redeliver. The publish carries a
				// dedup id keyed on this message's capture sequence, so the redelivery
				// is suppressed at the stream rather than duplicated (ADR-030 I2).
				log.Warn().Err(err).Str("tenant", tenant).Uint64("captureSeq", msg.StreamSeq).
					Msg("Failed to ack a durably-forwarded captured message; it will redeliver and be deduped.")
			}
			return
		}
		if msg.NumDelivered >= messaging.MaxDeliver {
			// The worker may have ALREADY tried the failed-decode route and failed —
			// that is what ErrDeadLetterUnavailable marks. Re-attempting a route that
			// has just failed would publish the same payload again to a stream that is
			// already unavailable, so the message is left unacked instead.
			if errors.Is(handleErr, ErrDeadLetterUnavailable) {
				log.Error().Err(handleErr).Str("tenant", tenant).Uint64("captureSeq", msg.StreamSeq).
					Int("attempts", msg.NumDelivered).
					Msg("Captured message is poison and the failed-decode route is unavailable; leaving it unacked.")
				return
			}
			// The route is a synchronous publish of up to 5 s, and this runs on the
			// inbound-events writer's settle goroutine, which reports every other
			// outcome in order behind it. So it runs on its own goroutine, at most
			// POISON_ROUTE_LIMIT at once. With every slot busy the message is left
			// UNACKED: this is its last delivery, so the broker terminates it and the
			// max-delivery recorder letters it — recorded, loudly, rather than holding
			// back every other message's ack.
			select {
			case poison.slots <- struct{}{}:
			default:
				log.Error().Err(handleErr).Str("tenant", tenant).Uint64("captureSeq", msg.StreamSeq).
					Int("attempts", msg.NumDelivered).
					Msg("Captured message is poison and every failed-decode route is busy; leaving it unacked for the max-delivery record.")
				return
			}
			poison.running.Add(1)
			go func() {
				defer func() {
					<-poison.slots
					poison.running.Done()
				}()
				es.routePoison(msg, tenant, handleErr)
			}()
			return
		}
		// LEFT UNACKED ON PURPOSE — deliberately NOT naked.
		//
		// Nak means redeliver IMMEDIATELY, and that turns MaxDeliver from a fuse into
		// a millisecond-scale one: measured against a real broker, all five delivery
		// attempts burn roughly 1.4ms after the first Nak, because each redelivery
		// arrives in about one round trip. The failure that matters here is a
		// DOWNSTREAM outage — JetStream refusing appends — which is message-
		// independent and lasts minutes, so retrying at RTT speed exhausts the fuse
		// INSIDE the outage and then strands or mass-dead-letters everything that
		// flowed during it. The capture stream's whole hour-long recovery envelope
		// would never engage, which is the guarantee this slice exists to provide.
		//
		// Saying nothing lets AckWait pace the retry instead: redelivery comes after
		// 60s, so MaxDeliver spans five MINUTES rather than milliseconds, and an
		// outage shorter than that is ridden out with no message lost and none
		// diverted. The consumer's unacked messages also stop being fetched once
		// MaxAckPending fills, which is exactly the right backpressure while the
		// downstream is refusing writes — better than spinning against it.
		//
		// The cost is that a genuinely one-off, message-specific failure now waits
		// 60s instead of retrying at once. That is an error path, and paying latency
		// there to keep the outage path correct is the right trade.
		log.Warn().Err(handleErr).Str("tenant", tenant).Uint64("captureSeq", msg.StreamSeq).
			Int("attempt", msg.NumDelivered).
			Msg("Captured message not durably forwarded; leaving it unacked for AckWait-paced redelivery.")
	}
}

// routePoison routes a message that failed every delivery attempt to failed-decode, and
// acks it once it is there.
func (es *GatewayJetStreamSource) routePoison(msg messaging.Message, tenant string, handleErr error) {
	log.Error().Err(handleErr).Str("tenant", tenant).Uint64("captureSeq", msg.StreamSeq).
		Int("attempts", msg.NumDelivered).
		Msg("Captured message failed every delivery attempt; routing to failed-decode.")
	if err := es.failed(es.Id, tenant, msg.Value, handleErr); err != nil {
		// Even the dead-letter route failed. Leave it UNACKED: the broker's own
		// MaxDeliver terminates it eventually, and losing it loudly later beats
		// discarding it silently now.
		log.Error().Err(err).Str("tenant", tenant).Uint64("captureSeq", msg.StreamSeq).
			Msg("Failed to route a poison captured message to failed-decode; leaving it unacked.")
		return
	}
	ackDrop(msg, "poison after MaxDeliver attempts")
}

// ackDrop acknowledges a message the source is terminally, deliberately not going
// to process. It exists so that every drop site reads as a DECISION rather than as
// a bare return, because a bare return is the ACK TRAP.
// It does not predict what happens on the redelivery, because that now depends on
// WHY it was dropped: a lifecycle refusal applies on every delivery and drops it
// again, while a rate shed is exempt from metering on a redelivery and would admit
// it. Naming one of those as the outcome would be wrong half the time.
func ackDrop(msg messaging.Message, reason string) {
	if err := msg.Ack(); err != nil {
		log.Warn().Err(err).Str("reason", reason).
			Msg("Failed to ack a dropped captured message; the broker will redeliver it.")
	}
}

// tenantFromSubject derives the tenant from a captured subject of the form
// "{instance}.{tenant}.devices.{token}.events": the tenant is the second of at
// least three non-empty dot-separated segments. Only the second segment is read,
// so this is agnostic to the instance-id prefix. It mirrors tenantFromTopic, which
// does the same for the MQTT topic form still used by the external-broker source.
func tenantFromSubject(subject string) (string, bool) {
	parts := strings.SplitN(subject, ".", 3)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", false
	}
	return parts[1], true
}

// deviceFromSubject returns the device token a captured events subject addresses,
// for the shape "{instance}.{tenant}.devices.{token}.events". The broker grant
// confines a device to its OWN such subject, so this token is broker-authorized
// rather than merely asserted — which is what makes it worth checking the payload
// against (see checkDeviceMatchesTransport).
//
// The segment literals come from core/streams, the same declaration the grant, the
// capture stream's subject filter and the MQTT topic form are all built from, so
// this parser cannot recognise a shape the stream no longer captures or miss one
// it does.
func deviceFromSubject(subject string) string {
	parts := strings.Split(subject, ".")
	if len(parts) != messaging.DeviceEventsSegmentCount ||
		parts[messaging.DeviceEventsDevicesIndex] != messaging.SegmentDevices ||
		parts[messaging.DeviceEventsEventsIndex] != messaging.SegmentEvents {
		return ""
	}
	return parts[messaging.DeviceEventsTokenIndex]
}

// String identifies the source in lifecycle logging.
func (es *GatewayJetStreamSource) String() string {
	return fmt.Sprintf("GatewayJetStreamSource[%s]", es.Id)
}
