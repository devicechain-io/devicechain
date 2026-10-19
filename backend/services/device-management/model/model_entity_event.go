// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"github.com/devicechain-io/dc-microservice/entityevent"
)

// EntityEventPublisher publishes entity-lifecycle events (ADR-044). Like the alarm
// publisher it is best-effort and side-band to the delete: the delete is the source
// of truth and a missed event is repaired by each holder's own sweep against this
// service (ADR-044 decision 3) — event-management's anchor reconciliation sweep, and
// event-processing's reconcile of its device roster and threshold attributes — so a
// failed publish is logged by the implementation, never surfaced to the caller (a
// NATS hiccup must not fail or retry the delete).
// Implementations must be safe for concurrent use.
type EntityEventPublisher interface {
	PublishEntityDeleted(ctx context.Context, event *entityevent.EntityDeletedEvent)
}
