// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/entity"
)

// EntityDeletedEvent is the envelope emitted when an edge entity is deleted
// (ADR-044): the cross-service signal that lets reference holders reconcile
// dangling rows keyed to an entity that no longer exists. The entity is named by
// its uniform (type, token) address (ADR-013/042) and its raw row id — event
// holders currently key on the id (event-management's event_anchors); the token is
// carried for logging and the future token-reference reshape (decision 4). The
// tenant is not a field: it travels on the per-tenant NATS subject.
type EntityDeletedEvent struct {
	EntityType  entity.Type
	EntityId    uint
	EntityToken string
	DeletedTime time.Time
}

// EntityEventPublisher publishes entity-lifecycle events (ADR-044). Like the alarm
// publisher it is best-effort and side-band to the delete: the delete is the source
// of truth and a missed event will be caught by the planned reconciliation sweep
// (ADR-044 decision 3), so a failed publish is logged by the implementation, never
// surfaced to the caller (a NATS hiccup must not fail or retry the delete).
// Implementations must be safe for concurrent use.
type EntityEventPublisher interface {
	PublishEntityDeleted(ctx context.Context, event *EntityDeletedEvent)
}
