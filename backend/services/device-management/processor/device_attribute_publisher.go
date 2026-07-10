// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-microservice/messaging"
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
// swallowed — it must never fail or retry the attribute set/delete, which is the source
// of truth. Emission is at-most-once (ADR-044 async-fact posture): a delivered fact is
// durably persisted by event-processing, but a fact that never reaches the stream is
// recovered by a later write to the same attribute or the planned reconcile, not replay.
type DeviceAttributeWriter struct {
	writer messaging.MessageWriter
}

// NewDeviceAttributeWriter builds a device-attribute publisher over the given writer.
func NewDeviceAttributeWriter(writer messaging.MessageWriter) *DeviceAttributeWriter {
	return &DeviceAttributeWriter{writer: writer}
}

// PublishDeviceAttribute marshals and publishes a device-attribute event. It never
// returns an error (the interface is fire-and-forget); failures are logged.
func (w *DeviceAttributeWriter) PublishDeviceAttribute(ctx context.Context, event *model.DeviceAttributeEvent) {
	bytes, err := proto.MarshalDeviceAttributeEvent(event)
	if err != nil {
		log.Error().Err(err).Str("device", event.DeviceToken).Str("attr", event.AttrKey).
			Msg("Unable to marshal device-attribute event")
		return
	}
	if err := w.writer.WriteMessages(ctx, messaging.Message{Value: bytes}); err != nil {
		log.Error().Err(err).Str("device", event.DeviceToken).Str("attr", event.AttrKey).
			Msg("Unable to publish device-attribute event")
	}
}
