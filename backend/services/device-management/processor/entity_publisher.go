// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/entityevent"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/rs/zerolog/log"
)

// EntityDeletedWriter is the concrete, NATS-backed implementation of
// model.EntityEventPublisher (ADR-044): it marshals an entity-deletion event and
// publishes it to the entity-deleted subject. Like the alarm writer it lives in the
// processor layer (which owns the messaging writer) and is injected into the shared
// *Api at wiring time, keeping the model free of a messaging dependency.
//
// The drain deletes a row only after an acknowledged publish. Errors must be returned
// so a broker outage leaves the fact queued for retry under its own tenant.
type EntityDeletedWriter struct {
	writer messaging.MessageWriter
}

// NewEntityDeletedWriter builds an entity-event publisher over the given writer.
func NewEntityDeletedWriter(writer messaging.MessageWriter) *EntityDeletedWriter {
	return &EntityDeletedWriter{writer: writer}
}

// EntityDeletedDedupID is stable across retries and scoped to a tenant and entity identity.
func EntityDeletedDedupID(tenant string, etype entity.Type, id uint) string {
	return fmt.Sprintf("entdel/%s/%s/%d", tenant, etype, id)
}

// PublishEntityDeleted returns marshal or writer failures to the outbox drain.
func (w *EntityDeletedWriter) PublishEntityDeleted(ctx context.Context, event *entityevent.EntityDeletedEvent) error {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok || tenant == "" {
		return fmt.Errorf("publishing entity deletion requires a tenant")
	}
	bytes, err := entityevent.MarshalEntityDeletedEvent(event)
	if err != nil {
		log.Error().Err(err).Str("entity", event.EntityToken).Msg("Unable to marshal entity-deletion event")
		return err
	}
	err = w.writer.WriteMessages(ctx, messaging.Message{Value: bytes, DedupID: EntityDeletedDedupID(tenant, event.EntityType, event.EntityId)})
	if err != nil {
		log.Error().Err(err).Str("entityType", string(event.EntityType)).Str("entity", event.EntityToken).Msg("Unable to publish entity-deletion event")
	}
	return err
}
