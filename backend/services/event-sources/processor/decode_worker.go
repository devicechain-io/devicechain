// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"errors"
	"fmt"

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
	tenant  string
	payload []byte
	// device is the device token the TRANSPORT addressed, when the transport
	// carries one (the MQTT events topic does; HTTP does not). It is the
	// broker-authorized identity — a device may only publish to its own events
	// topic — so it is checked against the device the PAYLOAD claims to be.
	device string
	// captureSeq is the JetStream sequence of the capture-stream message this
	// payload came from, and 0 for a transport that has none (HTTP, and the
	// external-broker MQTT source). It becomes the publish dedup id, so a
	// redelivery of the same captured message is stored once — see
	// processor.DedupID (ADR-030 amendment).
	captureSeq uint64
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
// failed-decode stream is unavailable is routed there once by the worker, naked,
// redelivered, routed again on every attempt, and then routed a final time by the
// terminal step — six publishes of one payload, most of them to a stream that is
// already failing. Marking the failure lets the source skip a route it knows has
// just failed and leave the message unacked instead.
var ErrDeadLetterUnavailable = errors.New("failed-decode route unavailable")

// Worker used to decode event payloads.
type DecodeWorker struct {
	WorkerId    int
	SourceId    string
	Decoder     Decoder
	RawMessages <-chan rawMessage
	// Callback publishes a decoded event and reports whether the publish DURABLY
	// succeeded. It returns an error so the worker can settle the source's message
	// on the real outcome: a transport that acknowledges its broker must not do so
	// on the strength of having merely attempted the publish.
	Callback func(string, string, *model.UnresolvedEvent, interface{}, uint64) error
	// Failed routes an undecodable payload to the failed-decode path, likewise
	// reporting whether that routing itself succeeded.
	Failed func(string, string, []byte, error) error
}

// Create a new decode worker.
func NewDecodeWorker(workerId int, sourceId string, decoder Decoder, rawMessages <-chan rawMessage,
	callback func(string, string, *model.UnresolvedEvent, interface{}, uint64) error,
	failed func(string, string, []byte, error) error) *DecodeWorker {
	worker := &DecodeWorker{
		WorkerId:    workerId,
		SourceId:    sourceId,
		Decoder:     decoder,
		RawMessages: rawMessages,
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
			event, payload, err := wrk.Decoder.Decode(raw.payload)
			if err == nil {
				err = checkDeviceMatchesTransport(raw, event)
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
			} else {
				raw.settle(wrk.Callback(wrk.SourceId, raw.tenant, event, payload, raw.captureSeq))
			}
		} else {
			log.Debug().Msg("Decode worker received shutdown signal.")
			return
		}
	}
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
