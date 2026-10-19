// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package entityevent is the wire contract of the entity-deletion event device-management
// publishes when an edge entity is deleted. It lives in core so the services that reconcile
// their own references against it (event-management, event-processing, dashboard-management)
// do not import the producer to read it.
package entityevent

import (
	"time"

	"github.com/devicechain-io/dc-microservice/entity"
)

// EntityDeletedEvent is the envelope emitted when an edge entity is deleted
// (ADR-044): the cross-service signal that lets reference holders reconcile
// dangling rows keyed to an entity that no longer exists. The entity is named by
// its uniform (type, token) address (ADR-013/042); event-management's event_anchors
// key on the token (decision 4). EntityId is retained for local/log use only — it
// no longer crosses the seam as a reference. DeletedTime bounds the anchor cleanup
// (a reused token's newer anchors must survive a replayed deletion). The tenant is
// not a field: it travels on the per-tenant NATS subject.
type EntityDeletedEvent struct {
	EntityType  entity.Type
	EntityId    uint
	EntityToken string
	DeletedTime time.Time
}
