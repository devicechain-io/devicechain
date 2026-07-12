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

// AlarmEventWriter is the concrete, NATS-backed implementation of
// model.AlarmEventPublisher (ADR-041): it marshals an alarm state-change event and
// publishes it to the alarm-events subject. It lives in the processor layer (which
// owns the messaging writer) and is injected into the shared *Api at wiring time, so
// the model layer stays free of a messaging dependency (dependency inversion).
//
// Publishing is best-effort: the tenant-scoped writer derives the subject from the
// tenant already present in the caller's context (the raise-alarm consumer applies each
// edge under the event's tenant; an operator mutation runs under the request's tenant), so
// no tenant plumbing is needed here. A marshal or publish failure is logged and
// swallowed — the alarm row is the source of truth and a subscriber can re-query, so
// a dropped notification must never fail or retry the DB transition that produced it.
type AlarmEventWriter struct {
	writer messaging.MessageWriter
}

// NewAlarmEventWriter builds an alarm-event publisher over the given writer.
func NewAlarmEventWriter(writer messaging.MessageWriter) *AlarmEventWriter {
	return &AlarmEventWriter{writer: writer}
}

// PublishAlarmEvent marshals and publishes an alarm state-change event. It never
// returns an error (the interface is fire-and-forget); failures are logged.
func (w *AlarmEventWriter) PublishAlarmEvent(ctx context.Context, event *model.AlarmStateChangeEvent) {
	bytes, err := proto.MarshalAlarmStateChangeEvent(event)
	if err != nil {
		log.Error().Err(err).Str("alarm", event.AlarmToken).Msg("Unable to marshal alarm state-change event")
		return
	}
	if err := w.writer.WriteMessages(ctx, messaging.Message{Value: bytes}); err != nil {
		// Log with the alarm identity here (the writer's HandleResponse logs only the
		// subject) so a dropped transition on a specific alarm is diagnosable.
		log.Error().Err(err).Str("alarm", event.AlarmToken).Str("event", event.EventType.String()).
			Msg("Unable to publish alarm state-change event")
	}
}
