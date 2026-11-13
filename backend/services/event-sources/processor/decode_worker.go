// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"github.com/devicechain-io/dc-microservice/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/rs/zerolog/log"
)

// rawMessage pairs an inbound transport message's payload with the tenant it
// belongs to. The tenant is derived by the source from its own addressing (an
// MQTT topic "{instanceId}/{tenant}/...", an HTTP path "/{instanceId}/{tenant}/...",
// ADR-006/ADR-048) before
// the payload is enqueued, so it travels with the payload to the producer, which
// publishes to the tenant-scoped subject.
type rawMessage struct {
	// remote is the trace context a device-originated message carried. It is untrusted: the ingest
	// span links to it and never adopts it (tracing.StartEdge).
	remote  trace.SpanContext
	tenant  string
	payload []byte
	// device is the device token the TRANSPORT addressed, when the transport
	// carries one (the MQTT events topic does; HTTP does not). It is the
	// broker-authorized identity — a device may only publish to its own events
	// topic — so it is checked against the device the PAYLOAD claims to be.
	device string
	// dedupID is the inbound-events dedup id the source names this payload by: built from
	// the capture-stream sequence (processor.DedupID) or from the MQTT message store's
	// epoch and sequence (messaging.MqttMessageStoreDedupID), so a redelivery of the same
	// message is stored once (ADR-030 amendment). "" for a transport that has none (HTTP,
	// and the external-broker MQTT source), which publishes no dedup header. The SOURCE
	// builds it because only the source knows which stream, and which incarnation of it,
	// the sequence belongs to.
	dedupID string
	// receivedAt is the broker time the capture stream stored the message: when the
	// platform received it. It becomes the event's ProcessedTime, and the OccurredTime
	// of an event that reports none, and it is identical on every redelivery. Zero for
	// a transport with none (the external-broker MQTT source), read as now.
	receivedAt time.Time
	// meterAt is the send time the message stage metered this message at (see meterTimeAt):
	// the capture append time when the message was BACKLOG, zero (now) when it was live.
	// The reading stage is charged at the same time, so both stages route one message to
	// the same limiter. Zero on HTTP and external MQTT, which have no backlog.
	meterAt time.Time
	// metering is the lag-credited gate's decision for this message (route, stamp, the time of the
	// message stage), carried so the reading stage follows it; the zero value when the message was not
	// metered there (legacy mode, HTTP, external MQTT under legacy).
	metering ingestMeter
	// redelivery is true when the broker re-offers a message it delivered before (a capture
	// message with NumDelivered > 1). The reading charge exempts it for the reason RateGate
	// does: delivery 1 paid. One gap is accepted: a pod stopped after delivery 1 was charged
	// as a message but before it was decoded leaves it unacked, and its redelivery is then
	// exempt from both stages. That is bounded by the decode queue per restart.
	redelivery bool
	// origin is the tenant's provenance, which decides the reading stage's limiter (see
	// Origin). The zero value is treated as untrusted.
	origin Origin
	// assertion says whether the platform's own broker authenticated this message's
	// device. Only the capture source can make it true; see brokerAssertion.
	assertion brokerAssertion
	// uplink is true for a raw payload a device published to its own uplink topic: not a DeviceChain
	// envelope, so it is never decoded by Decoder but carried to the profile decoder (see
	// mqtt_device_uplink.go). Only the capture source sets it, from the subject.
	uplink bool
	// done reports the outcome of handling this message back to the source that
	// produced it, so the source can acknowledge the broker only once the payload
	// is durably forwarded. nil for a fire-and-forget transport, which is why every
	// call site must go through settle rather than invoking it directly.
	//
	// This is the seam ADR-030 turns on. Before it, the MQTT source acked the
	// device as soon as the payload was on this channel — well before the decode
	// worker published anything — so everything buffered here at SIGKILL was
	// silently lost.
	done func(error)
	// shed, when set, is called just before settle(nil) for a message the reading stage shed
	// at the tenant's ceiling, so a transport that tells its sender about a shed can say so
	// and does not report the message as accepted. nil for a transport that does not.
	shed func()
}

// settle reports the handling outcome for a raw message, tolerating a transport
// that has no acknowledgement (HTTP, external MQTT).
//
// A nil error means "durably handled, do not send it again"; a non-nil error means
// the message was NOT durably handled and the source decides whether to retry it.
// A DROP is a nil error: the message is terminally, deliberately not wanted, and
// reporting it as a failure would ask the broker to redeliver something that will
// be dropped identically every time — the ACK TRAP that turns a fail-closed drop
// into an infinite redelivery loop.
func (r rawMessage) settle(err error) {
	if r.done != nil {
		r.done(err)
	}
}

// ErrDeadLetterUnavailable marks a handling failure whose payload the worker
// ALREADY tried and failed to route to the failed-decode path.
//
// The distinction matters because the source's terminal step is itself "route to
// failed-decode and ack". Without it, a payload that fails to DECODE while the
// failed-decode stream is unavailable is routed there once by the worker, left unacked,
// redelivered, routed again on every attempt, and then routed a final time by the
// terminal step — six publishes of one payload, most of them to a stream that is
// already failing. Marking the failure lets the source skip a route it knows has
// just failed and leave the message unacked instead.
var ErrDeadLetterUnavailable = errors.New("failed-decode route unavailable")

// DecodedFunc hands a decoded event on for publishing and reports the outcome to done
// exactly once: nil ONLY once the event is durably stored (or deliberately dropped — see
// rawMessage.settle), non-nil otherwise, so a transport that acknowledges its broker does
// so on the real outcome and never on the strength of having merely attempted the
// publish. It may call done before returning (a synchronous publish, a terminal drop) or
// later, from another goroutine (a pipelined publish). done is never nil: the worker
// passes rawMessage.settle, which tolerates a transport with nothing to acknowledge.
type DecodedFunc func(source, tenant string, event *model.UnresolvedEvent, payload interface{},
	dedupID string, done func(error))

// Inline adapts a synchronous publish callback to a DecodedFunc: it publishes on the
// calling worker and reports the result to done before returning.
func Inline(decoded func(string, string, *model.UnresolvedEvent, interface{}, string) error) DecodedFunc {
	return func(source, tenant string, event *model.UnresolvedEvent, payload interface{},
		dedupID string, done func(error)) {
		done(decoded(source, tenant, event, payload, dedupID))
	}
}

// Worker used to decode event payloads.
type DecodeWorker struct {
	WorkerId    int
	SourceId    string
	Decoder     Decoder
	RawMessages <-chan rawMessage
	// Readings charges a decoded message's readings against its tenant's ingest ceiling
	// before it is handed on; see ReadingGate. Never nil.
	Readings ReadingGate
	// Callback hands a decoded event on for publishing; see DecodedFunc.
	Callback DecodedFunc
	// Failed routes an undecodable payload to the failed-decode path, likewise
	// reporting whether that routing itself succeeded.
	Failed func(string, string, []byte, error) error
	// Uplinks carries a device's raw payload (rawMessage.uplink) to its type's decoder. nil on a
	// worker of a source that has no such route: such a payload is then recorded on failed-decode
	// with its cause, never decoded as an envelope and never dropped without a word.
	Uplinks *uplinkRunner
}

// Create a new decode worker. A nil readings panics: a worker built without the reading
// stage would hand on every message uncharged, and nothing at the call site would show it.
func NewDecodeWorker(workerId int, sourceId string, decoder Decoder, rawMessages <-chan rawMessage,
	readings ReadingGate,
	callback DecodedFunc,
	failed func(string, string, []byte, error) error) *DecodeWorker {
	if readings == nil {
		panic("processor.NewDecodeWorker: a reading gate is required")
	}
	worker := &DecodeWorker{
		WorkerId:    workerId,
		SourceId:    sourceId,
		Decoder:     decoder,
		RawMessages: rawMessages,
		Readings:    readings,
		Callback:    callback,
		Failed:      failed,
	}
	return worker
}

// Processes raw payloads into decoded events.
func (wrk *DecodeWorker) Process() {
	for {
		raw, more := <-wrk.RawMessages
		if more {
			log.Debug().Msg(fmt.Sprintf("Decode handled by worker id %d", wrk.WorkerId))
			if raw.uplink {
				u := wrk.Uplinks
				if u == nil {
					// No route behind this worker: the payload is recorded with that cause.
					u = &uplinkRunner{failed: wrk.Failed}
				}
				// Only a message the platform's broker asserted may be carried: the assertion is read here,
				// where it is allowed to be, and handed in as the mark.
				if !raw.assertion.asserted || raw.assertion.gateway != "" {
					u.recordNow(wrk.SourceId, raw, DeviceUplinkInternalError, ErrUplinkNotAsserted)
					continue
				}
				u.run(wrk.SourceId, raw, raw.assertion.mark)
				continue
			}
			traceCtx, span := tracing.StartEdge(context.Background(), "ingest.mqtt", "mqtt", raw.remote)
			if span != nil {
				span.SetAttributes(attribute.String("tenant.id", raw.tenant))
			}
			if span != nil {
				settle := raw.done
				raw.done = func(err error) {
					defer span.End()
					if settle != nil {
						settle(err)
					}
				}
			}
			event, payload, err := wrk.Decoder.Decode(raw.payload, raw.receivedAt)
			if event != nil && span != nil {
				event.TraceContext = traceCtx
			}
			if err == nil {
				err = checkDeviceMatchesTransport(raw, event)
			}
			if err == nil && raw.assertion.asserted {
				// The topic IS the identity here, so a body credential is not needed and
				// is refused rather than ignored: ignoring it would keep a leaking device
				// working, silently, forever. The error is static, so the secret is never
				// in a log line or a counter.
				if credentialInBody(event) {
					err = model.ErrCredentialInMqttBody
				} else {
					raw.assertion.mark(event)
				}
			}
			if err != nil {
				// A payload that cannot be decoded, or whose claimed device contradicts
				// the transport it was authorized on, is TERMINAL — the same input
				// produces the same rejection every time. So the message is settled on
				// whether it reached the FAILED-DECODE path, not on the rejection
				// itself: settling on the rejection would ask the broker to redeliver a
				// payload that can never succeed, until it gave up and the diagnostic
				// was lost with it.
				//
				// When that route ITSELF fails the message is still unhandled, but it is
				// marked so the source does not re-attempt a route that has just failed.
				if routeErr := wrk.Failed(wrk.SourceId, raw.tenant, raw.payload, err); routeErr != nil {
					raw.settle(fmt.Errorf("%w: %w", ErrDeadLetterUnavailable, routeErr))
				} else {
					raw.settle(nil)
				}
			} else if !wrk.chargeReadings(raw, readingsOf(payload)) {
				// Shed at the tenant's ceiling for the readings it carries. ACKED, like a
				// message the message stage sheds: left unacked it would be redelivered into
				// the same over-limit gate (the ACK TRAP). Workers can take a backlog slightly
				// out of order; the limiter charges an older time at its mark, so that can
				// over-shed by milliseconds and can never mint tokens.
				if raw.shed != nil {
					raw.shed()
				}
				raw.settle(nil)
			} else {
				wrk.Callback(wrk.SourceId, raw.tenant, event, payload, raw.dedupID, raw.settle)
			}
		} else {
			log.Debug().Msg("Decode worker received shutdown signal.")
			return
		}
	}
}

// chargeReadings is the reading stage's verdict for one decoded message: the lag-credited gate when the
// message stage metered it there (the decision rides in raw.metering, so both stages follow one route),
// the Readings gate otherwise.
func (wrk *DecodeWorker) chargeReadings(raw rawMessage, readings int) bool {
	if raw.metering.active() {
		return raw.metering.readings(wrk.SourceId, raw.tenant, readings)
	}
	return wrk.Readings(wrk.SourceId, raw.tenant, raw.meterAt, raw.redelivery, raw.origin, readings)
}

// checkDeviceMatchesTransport rejects an event whose payload claims to be from a
// different device than the transport was authorized to publish as.
//
// The MQTT grant confines a device to "{instance}.{tenant}.devices.{token}.events",
// so the token in the topic is broker-verified. Without this check that segment is
// decorative: nothing downstream reads it, and attribution comes from the body's
// `device` field alone. Under deviceAuthMode `optional` or `disabled` the body is
// trusted verbatim, so device A publishing on ITS OWN topic with a body naming
// device B is persisted as B's event — it updates B's state and fires B's rules.
// The narrowed grant reads as though it prevents that; on its own it does not.
//
// Fails closed as a decode failure so the message lands on the existing
// failed-decode path (counted, logged, dead-lettered) rather than being silently
// dropped.
//
// Transports that carry no device identity (HTTP, whose path has only instance and
// tenant) leave raw.device empty and are unaffected — there the credential in the
// body remains the only attribution, which is what deviceAuthMode governs.
func checkDeviceMatchesTransport(raw rawMessage, event *model.UnresolvedEvent) error {
	if raw.device == "" || event == nil || event.Device == "" {
		return nil
	}
	if event.Device != raw.device {
		return fmt.Errorf("event claims device %q but was published on the topic for device %q",
			event.Device, raw.device)
	}
	return nil
}
