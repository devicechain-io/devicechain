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

// DeviceRosterWriter is the concrete, NATS-backed implementation of
// model.DeviceRosterPublisher (ADR-051 slice 4c-2): it marshals a device-roster event
// and publishes it to the device-roster subject. Like the detection-rules and
// entity-deleted writers it lives in the processor layer (which owns the messaging
// writer) and is injected into the shared *Api at wiring time, keeping the model free
// of a messaging dependency.
//
// Publishing is best-effort: the tenant-scoped writer derives the subject from the
// tenant in the caller's context (the device write runs under the request's tenant), so
// no tenant plumbing is needed here. A marshal or publish failure is logged and
// swallowed — it must never fail or retry the device create/update, which is the source
// of truth. Emission is at-most-once (ADR-044 async-fact posture): a delivered fact is
// durably persisted by event-processing, but a fact that never reaches the stream is
// recovered by a later re-type or the planned reconcile, not by replay.
type DeviceRosterWriter struct {
	writer messaging.MessageWriter
}

// NewDeviceRosterWriter builds a device-roster publisher over the given writer.
func NewDeviceRosterWriter(writer messaging.MessageWriter) *DeviceRosterWriter {
	return &DeviceRosterWriter{writer: writer}
}

// PublishDeviceRoster marshals and publishes a device-roster event. It never returns
// an error (the interface is fire-and-forget); failures are logged.
func (w *DeviceRosterWriter) PublishDeviceRoster(ctx context.Context, event *model.DeviceRosterEvent) {
	bytes, err := proto.MarshalDeviceRosterEvent(event)
	if err != nil {
		log.Error().Err(err).Str("device", event.DeviceToken).Msg("Unable to marshal device-roster event")
		return
	}
	if err := w.writer.WriteMessages(ctx, messaging.Message{Value: bytes}); err != nil {
		log.Error().Err(err).Str("device", event.DeviceToken).Str("profile", event.ProfileToken).
			Msg("Unable to publish device-roster event")
	}
}
