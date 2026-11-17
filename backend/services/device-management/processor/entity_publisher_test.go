// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/entityevent"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/require"
)

type deletionMessageWriter struct {
	messaging.MessageWriter
	messages []messaging.Message
	err      error
}

func (w *deletionMessageWriter) WriteMessages(_ context.Context, messages ...messaging.Message) error {
	w.messages = append(w.messages, messages...)
	return w.err
}

// Pins unstable or cross-tenant deduplication IDs that would duplicate or suppress facts.
func TestEntityDeletedDedupID(t *testing.T) {
	require.Equal(t, "entdel/acme/device/42", EntityDeletedDedupID("acme", entity.TypeDevice, 42))
	require.NotEqual(t, EntityDeletedDedupID("acme", entity.TypeDevice, 42), EntityDeletedDedupID("other", entity.TypeDevice, 42))
	require.NotEqual(t, EntityDeletedDedupID("acme", entity.TypeDevice, 42), EntityDeletedDedupID("acme", entity.TypeAsset, 42))
	require.NotEqual(t, EntityDeletedDedupID("acme", entity.TypeDevice, 42), EntityDeletedDedupID("acme", entity.TypeDevice, 43))
}

// Pins swallowed broker errors, missing deduplication, and publishing without a tenant.
func TestTheEntityDeletionWriterAcknowledgesOnlyTenantScopedPublishes(t *testing.T) {
	broker := &deletionMessageWriter{err: errors.New("broker unavailable")}
	w := NewEntityDeletedWriter(broker)
	event := &entityevent.EntityDeletedEvent{EntityType: entity.TypeDevice, EntityId: 42, EntityToken: "device"}
	require.ErrorIs(t, w.PublishEntityDeleted(core.WithTenant(context.Background(), "acme"), event), broker.err)
	require.Len(t, broker.messages, 1)
	require.Equal(t, "entdel/acme/device/42", broker.messages[0].DedupID)
	require.Error(t, w.PublishEntityDeleted(context.Background(), event))
	require.Len(t, broker.messages, 1)
	broker.err = nil
	require.NoError(t, w.PublishEntityDeleted(core.WithTenant(context.Background(), "acme"), event))
	require.Len(t, broker.messages, 2)
	require.Equal(t, broker.messages[0].DedupID, broker.messages[1].DedupID)
}
