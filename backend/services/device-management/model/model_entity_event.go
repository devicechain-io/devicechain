// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"github.com/devicechain-io/dc-microservice/entityevent"
)

// EntityEventPublisher acknowledges lifecycle facts from the transactional deletion outbox.
// A failed publish must return its error so the drain retains the fact for retry.
// Implementations must be safe for concurrent use.
type EntityEventPublisher interface {
	PublishEntityDeleted(ctx context.Context, event *entityevent.EntityDeletedEvent) error
}
