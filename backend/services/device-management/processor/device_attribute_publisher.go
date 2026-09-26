// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

// DeviceAttributeWriter is the concrete, NATS-backed implementation of
// model.DeviceAttributePublisher (ADR-051 slice 4c-3): it marshals a device-attribute
// event and publishes it to the device-attribute subject. Like the roster,
// detection-rules, and entity-deleted writers it lives in the processor layer (which
// owns the messaging writer) and is injected into the shared *Api at wiring time,
// keeping the model free of a messaging dependency.
//
// Publishing is best-effort: the tenant-scoped writer derives the subject from the
// tenant in the caller's context (the attribute write runs under the request's tenant),
// so no tenant plumbing is needed here. A marshal or publish failure is logged and
// COUNTED, never surfaced — it must never fail or retry the attribute set/delete, which is
// the source of truth. Emission is at-most-once (ADR-044 async-fact posture): a delivered
// fact is durably persisted by event-processing, and a fact that never reaches the stream
// is repaired by event-processing's reconcile against this service (at the start of its
// leadership term and every five minutes); it is not replayed.
type DeviceAttributeWriter struct {
	writer messaging.MessageWriter
	// failures counts facts this writer could not put on the wire, for any reason. Nil-safe.
	failures prometheus.Counter
}

// NewDeviceAttributeWriter builds a device-attribute publisher over the given writer,
// counting every fact it fails to publish on failures (nil disables the count).
func NewDeviceAttributeWriter(writer messaging.MessageWriter, failures prometheus.Counter) *DeviceAttributeWriter {
	return &DeviceAttributeWriter{writer: writer, failures: failures}
}

// PublishDeviceAttribute marshals and publishes a device-attribute event. It never
// returns an error (the interface is fire-and-forget); failures are logged and counted.
func (w *DeviceAttributeWriter) PublishDeviceAttribute(ctx context.Context, event *model.DeviceAttributeEvent) {
	bytes, err := proto.MarshalDeviceAttributeEvent(event)
	if err != nil {
		log.Error().Err(err).Str("device", event.DeviceToken).Str("attr", event.AttrKey).
			Msg("Unable to marshal device-attribute event")
		countFactFailure(w.failures)
		return
	}
	if err := w.writer.WriteMessages(ctx, messaging.Message{Value: bytes}); err != nil {
		log.Error().Err(err).Str("device", event.DeviceToken).Str("attr", event.AttrKey).
			Msg("Unable to publish device-attribute event; the detection engine picks the change up at its next reconcile against this service.")
		countFactFailure(w.failures)
	}
}
