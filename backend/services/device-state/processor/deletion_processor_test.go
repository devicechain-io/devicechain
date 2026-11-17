// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/entityevent"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
)

type deletedApplier struct {
	calls         int
	tenant, token string
	id            uint64
	at            time.Time
	scoped        bool
	err           error
}

func (a *deletedApplier) ApplyDeviceDeleted(ctx context.Context, token string, id uint64, at time.Time) (bool, error) {
	a.calls++
	a.tenant, _ = core.TenantFromContext(ctx)
	_, a.scoped = auth.ScopeFromContext(ctx)
	a.token, a.id, a.at = token, id, at
	return a.err == nil, a.err
}

func TestDeletionConsumerUsesSubjectTenantAndDisposesFacts(t *testing.T) {
	at := time.Now().UTC()
	for _, tc := range []struct {
		name                    string
		kind                    entity.Type
		token                   string
		at                      time.Time
		poison                  bool
		err                     error
		delivered, calls, acked int
	}{
		{name: "device", kind: entity.TypeDevice, token: "truck-7", at: at, delivered: 1, calls: 1, acked: 1},
		{name: "customer", kind: entity.TypeCustomer, token: "truck-7", at: at, delivered: 1, acked: 1},
		{name: "poison", poison: true, delivered: 1, acked: 1},
		{name: "empty token", kind: entity.TypeDevice, at: at, delivered: 1, acked: 1},
		{name: "zero time", kind: entity.TypeDevice, token: "truck-7", delivered: 1, acked: 1},
		{name: "retry", kind: entity.TypeDevice, token: "truck-7", at: at, delivered: 1, err: errors.New("unavailable"), calls: 1},
		{name: "exhausted", kind: entity.TypeDevice, token: "truck-7", at: at, delivered: messaging.MaxDeliver, err: errors.New("unavailable"), calls: 1, acked: 1},
		{name: "tenant erased", kind: entity.TypeDevice, token: "truck-7", at: at, delivered: 1, err: rdb.ErrTenantPurged, calls: 1, acked: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := entityevent.MarshalEntityDeletedEvent(&entityevent.EntityDeletedEvent{EntityType: tc.kind, EntityToken: tc.token, EntityId: 17, DeletedTime: tc.at})
			require.NoError(t, err)
			if tc.poison {
				b = []byte{0xff}
			}
			ack := &factAck{}
			api := &deletedApplier{err: tc.err}
			msg := messaging.NewConsumedMessage("instance.tenant-a.entity-deleted", b, tc.delivered, nil, ack)
			(&DeletionProcessor{Api: api}).handle(core.WithTenant(context.Background(), "wrong"), msg)
			require.Equal(t, tc.calls, api.calls)
			require.Equal(t, tc.acked, ack.acked)
			if api.calls > 0 {
				require.Equal(t, "tenant-a", api.tenant)
				require.Equal(t, "truck-7", api.token)
				require.Equal(t, uint64(17), api.id)
				require.True(t, api.at.Equal(at))
				require.False(t, api.scoped)
			}
		})
	}
}
