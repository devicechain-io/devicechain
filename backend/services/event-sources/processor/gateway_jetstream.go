// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-event-sources/model"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
)

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
// Consuming a capture stream deletes the whole category. The broker persists the
// device's publish before it PUBACKs, so the message is durable BEFORE our code
// runs; there is no session, no client id, and no per-pod broker identity that
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

	lifecycle core.LifecycleManager
	cancel    context.CancelFunc
	// drained closes once the read loop has returned, so Stop does not race the
	// loop into a closed message channel.
	drained chan struct{}

	received func(string, []byte)
	decoded  func(string, string, *model.UnresolvedEvent, interface{}, uint64) error
	failed   func(string, string, []byte, error) error
	// allow meters an inbound message against its tenant's ingest ceiling before it
	// is queued for decode; a false return sheds the message. nil disables metering.
	allow RateGate
}

// NewGatewayJetStreamSource builds the capture-stream source. reader must be a
// durable reader over streams.DeviceEventsCapture.
func NewGatewayJetStreamSource(id string, reader messaging.MessageReader, decoder Decoder,
	received func(string, []byte),
	decoded func(string, string, *model.UnresolvedEvent, interface{}, uint64) error,
	failed func(string, string, []byte, error) error,
	allow RateGate) *GatewayJetStreamSource {
	es := &GatewayJetStreamSource{
		Id:       id,
		Decoder:  decoder,
		reader:   reader,
		received: received,
		decoded:  decoded,
		failed:   failed,
		allow:    allow,
	}
	es.lifecycle = core.NewLifecycleManager("gateway-jetstream-event-source", es, core.NewNoOpLifecycleCallbacks())
	return es
}

func (es *GatewayJetStreamSource) Initialize(ctx context.Context) error {
	return es.lifecycle.Initialize(ctx)
}

func (es *GatewayJetStreamSource) ExecuteInitialize(ctx context.Context) error { return nil }

func (es *GatewayJetStreamSource) Start(ctx context.Context) error {
	return es.lifecycle.Start(ctx)
}

// ExecuteStart brings up the decode workers and the read loop.
func (es *GatewayJetStreamSource) ExecuteStart(ctx context.Context) error {
	// Per-Start, not per-construction: the lifecycle explicitly permits Start after
	// Stop, and a channel closed by the previous ExecuteStop would make the next one
	// return instantly — racing a live read loop into a closed message channel.
	drained := make(chan struct{})
	es.drained = drained
	es.messages = make(chan rawMessage, DECODE_CHANNEL_DEPTH)
	es.workers = make([]*DecodeWorker, 0, DECODE_WORKER_COUNT)
	for w := 1; w <= DECODE_WORKER_COUNT; w++ {
		worker := NewDecodeWorker(w, es.Id, es.Decoder, es.messages, es.decoded, es.failed)
		es.workers = append(es.workers, worker)
		go worker.Process()
	}

	loopCtx, cancel := context.WithCancel(context.Background())
	es.cancel = cancel
	// The loop is handed its OWN drained channel rather than reading the field, so
	// a lingering loop from a previous Start cannot close the current one's.
	go es.readLoop(loopCtx, drained)
	log.Info().Str("source", es.Id).Msg("Gateway event source consuming the device-events capture stream.")
	return nil
}

func (es *GatewayJetStreamSource) Stop(ctx context.Context) error {
	return es.lifecycle.Stop(ctx)
}

// ExecuteStop stops the read loop and then the workers, in that order.
//
// The order is not cosmetic. Closing the message channel while the read loop
// could still enqueue would panic on a send to a closed channel, and the loop is
// the only writer — so it must be observed to have returned first. Messages
// already in flight are simply left UNACKED, which is the correct outcome: the
// capture stream redelivers them to whichever pod is running, and at-least-once
// is exactly the guarantee this source exists to provide.
func (es *GatewayJetStreamSource) ExecuteStop(ctx context.Context) error {
	if es.cancel != nil {
		es.cancel()
	}
	select {
	case <-es.drained:
	case <-ctx.Done():
		log.Warn().Str("source", es.Id).Msg("Gateway capture read loop did not drain before shutdown deadline.")
		return nil
	}
	if es.messages != nil {
		close(es.messages)
	}
	return nil
}

func (es *GatewayJetStreamSource) Terminate(ctx context.Context) error {
	return es.lifecycle.Terminate(ctx)
}

func (es *GatewayJetStreamSource) ExecuteTerminate(ctx context.Context) error { return nil }

// readLoop pulls from the capture stream until the context is cancelled or the
// reader reports EOF, then closes drained to release ExecuteStop.
func (es *GatewayJetStreamSource) readLoop(ctx context.Context, drained chan struct{}) {
	defer close(drained)
	for {
		msg, err := es.reader.ReadMessage(ctx)
		if err != nil {
			// io.EOF is the reader's TERMINAL signal — a closed connection, a drained
			// subscription, or a failed rebind — and every peer consumer
			// (command-delivery, outbound-connectors, event-processing) exits on it.
			// Continuing instead would be a hot spin: on a closed connection the fetch
			// returns instantly, so the loop would burn a core emitting one error log
			// per iteration. Today that is masked only by shutdown ordering, which is
			// not a property this loop should depend on.
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
				log.Info().Str("source", es.Id).Msg("Gateway capture read loop stopped.")
				return
			}
			// A read error is transport-level, not message-level: there is nothing to
			// ack or drop. The reader retries internally, so this is logged and the
			// loop continues rather than tearing the source down.
			es.reader.HandleResponse(err)
			continue
		}
		es.handle(msg)
	}
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
	// stream's own filter, where it cannot go stale. TestCaptureStreamCannotDeliver
	// CommandPlaneSubjects pins it.

	// Meter against the tenant's ingest ceiling before enqueue, so a tenant over its
	// limit spends no decode CPU.
	//
	// A shed message is ACKED, not left for redelivery. Leaving it unacked would
	// redeliver it into the same over-limit gate, which is not backpressure but a
	// spin — and on a SHARED consumer it would wedge the tenants behind it too.
	//
	// The message is metered at its CAPTURE APPEND TIME, not at now (ADR-030 I4).
	// The broker stamps that before it PUBACKs the device, so it is the device's own
	// send time, and metering against it is what makes a recovered backlog survive.
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
	if es.allow != nil && !es.allow(es.Id, tenant, msg.AppendTime) {
		ackDrop(msg, "over the tenant ingest rate limit")
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
	// counted as both inbound and rate-limited (matches the HTTP path).
	es.received(es.Id, msg.Value)

	es.messages <- rawMessage{
		tenant:     tenant,
		payload:    msg.Value,
		device:     deviceFromSubject(msg.Subject),
		captureSeq: msg.StreamSeq,
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

// ackDrop acknowledges a message the source is terminally, deliberately not going
// to process. It exists so that every drop site reads as a DECISION rather than as
// a bare return, because a bare return is the ACK TRAP.
func ackDrop(msg messaging.Message, reason string) {
	if err := msg.Ack(); err != nil {
		log.Warn().Err(err).Str("reason", reason).
			Msg("Failed to ack a dropped captured message; it will be redelivered and dropped again.")
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
