// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/entityevent"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/require"
)

type deletionCall struct {
	tenant, token string
	incarnation   uint64
	deleted       time.Time
}
type fakeDeletionApi struct {
	calls []deletionCall
	err   error
}

func (f *fakeDeletionApi) DeviceDeleted(ctx context.Context, token string, incarnation uint64, deleted time.Time) error {
	tenant, _ := core.TenantFromContext(ctx)
	f.calls = append(f.calls, deletionCall{tenant, token, incarnation, deleted})
	return f.err
}

func TestDeletionConsumerUsesSubjectTenantAndDeviceIdentity(t *testing.T) {
	api := &fakeDeletionApi{}
	ms := &core.Microservice{InstanceId: "inst", FunctionalArea: "update-management"}
	c := NewDeletionConsumer(ms, nil, core.NewNoOpLifecycleCallbacks(), api, nil)
	deleted := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	payload, err := entityevent.MarshalEntityDeletedEvent(&entityevent.EntityDeletedEvent{
		EntityType: entity.TypeDevice, EntityId: 17, EntityToken: "dev-1", DeletedTime: deleted,
	})
	require.NoError(t, err)
	ack := &ackRecorder{}
	c.handle(context.Background(), messaging.NewConsumedMessage("inst.acme.entity-deleted", payload, 1, nil, ack))
	require.Equal(t, 1, ack.acks)
	require.Equal(t, []deletionCall{{"acme", "dev-1", 17, deleted}}, api.calls)
	api.err = errors.New("store unavailable")
	ack = &ackRecorder{}
	c.handle(context.Background(), messaging.NewConsumedMessage("inst.acme.entity-deleted", payload, 1, nil, ack))
	require.Zero(t, ack.acks, "a failed write is retried")
	require.Len(t, api.calls, 2)
}

func TestDeletionConsumerAcksNonDevicesAndPoisonWithoutCallingTheApi(t *testing.T) {
	api := &fakeDeletionApi{}
	c := NewDeletionConsumer(&core.Microservice{InstanceId: "inst", FunctionalArea: "update-management"}, nil, core.NewNoOpLifecycleCallbacks(), api, nil)
	for _, ev := range []entityevent.EntityDeletedEvent{
		{EntityType: entity.TypeCustomer, EntityToken: "customer", EntityId: 17, DeletedTime: time.Now()},
		{EntityType: entity.TypeDevice, EntityToken: "", DeletedTime: time.Now()},
		{EntityType: entity.TypeDevice, EntityToken: "dev-1"},
	} {
		payload, err := entityevent.MarshalEntityDeletedEvent(&ev)
		require.NoError(t, err)
		ack := &ackRecorder{}
		c.handle(context.Background(), messaging.NewConsumedMessage("inst.acme.entity-deleted", payload, 1, nil, ack))
		require.Equal(t, 1, ack.acks)
	}
	ack := &ackRecorder{}
	c.handle(context.Background(), messaging.NewConsumedMessage("inst.acme.entity-deleted", []byte("not-proto\xff"), 1, nil, ack))
	require.Equal(t, 1, ack.acks)
	require.Empty(t, api.calls)
}
