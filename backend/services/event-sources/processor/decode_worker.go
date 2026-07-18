// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
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
}

// Worker used to decode event payloads.
type DecodeWorker struct {
	WorkerId    int
	SourceId    string
	Decoder     Decoder
	RawMessages <-chan rawMessage
	Callback    func(string, string, *model.UnresolvedEvent, interface{})
	Failed      func(string, string, []byte, error)
}

// Create a new decode worker.
func NewDecodeWorker(workerId int, sourceId string, decoder Decoder, rawMessages <-chan rawMessage,
	callback func(string, string, *model.UnresolvedEvent, interface{}),
	failed func(string, string, []byte, error)) *DecodeWorker {
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
			if err != nil {
				wrk.Failed(wrk.SourceId, raw.tenant, raw.payload, err)
			} else if err := checkDeviceMatchesTransport(raw, event); err != nil {
				wrk.Failed(wrk.SourceId, raw.tenant, raw.payload, err)
			} else {
				wrk.Callback(wrk.SourceId, raw.tenant, event, payload)
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
