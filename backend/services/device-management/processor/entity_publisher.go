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

// EntityDeletedWriter is the concrete, NATS-backed implementation of
// model.EntityEventPublisher (ADR-044): it marshals an entity-deletion event and
// publishes it to the entity-deleted subject. Like the alarm writer it lives in the
// processor layer (which owns the messaging writer) and is injected into the shared
// *Api at wiring time, keeping the model free of a messaging dependency.
//
// Publishing is best-effort: the tenant-scoped writer derives the subject from the
// tenant in the caller's context (the delete runs under the request's tenant), so no
// tenant plumbing is needed here. A marshal or publish failure is logged and
// swallowed — the delete is the source of truth and the planned reconciliation sweep
// (ADR-044 decision 3) will catch a missed event, so a dropped publish must never
// fail or retry the delete.
type EntityDeletedWriter struct {
	writer messaging.MessageWriter
}

// NewEntityDeletedWriter builds an entity-event publisher over the given writer.
func NewEntityDeletedWriter(writer messaging.MessageWriter) *EntityDeletedWriter {
	return &EntityDeletedWriter{writer: writer}
}

// PublishEntityDeleted marshals and publishes an entity-deletion event. It never
// returns an error (the interface is fire-and-forget); failures are logged.
func (w *EntityDeletedWriter) PublishEntityDeleted(ctx context.Context, event *model.EntityDeletedEvent) {
	bytes, err := proto.MarshalEntityDeletedEvent(event)
	if err != nil {
		log.Error().Err(err).Str("entity", event.EntityToken).Msg("Unable to marshal entity-deletion event")
		return
	}
	if err := w.writer.WriteMessages(ctx, messaging.Message{Value: bytes}); err != nil {
		log.Error().Err(err).Str("entityType", string(event.EntityType)).Str("entity", event.EntityToken).
			Msg("Unable to publish entity-deletion event")
	}
}
