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
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type deletionCall struct {
	tenant, token string
	at            time.Time
}
type deletionFake struct {
	calls []deletionCall
	err   error
}

func (f *deletionFake) CancelDeletedDeviceCommands(ctx context.Context, token string, at time.Time) (int64, error) {
	tenant, _ := core.TenantFromContext(ctx)
	f.calls = append(f.calls, deletionCall{tenant, token, at})
	return 3, f.err
}

func TestDeletionConsumerAppliesDeviceFactsAndSettlesPoison(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name, subject string
		kind          entity.Type
		token         string
		at            time.Time
		poison, fail  bool
		calls, acks   int
	}{
		{name: "device", subject: "inst.acme.entity-deleted", kind: entity.TypeDevice, token: "valve-7", at: at, calls: 1, acks: 1},
		{name: "customer", subject: "inst.acme.entity-deleted", kind: entity.TypeCustomer, token: "customer", at: at, acks: 1},
		{name: "no tenant", subject: "no-tenant", acks: 1},
		{name: "bad payload", subject: "inst.acme.entity-deleted", poison: true, acks: 1},
		{name: "empty token", subject: "inst.acme.entity-deleted", kind: entity.TypeDevice, at: at, acks: 1},
		{name: "zero time", subject: "inst.acme.entity-deleted", kind: entity.TypeDevice, token: "valve-7", acks: 1},
		{name: "retry", subject: "inst.acme.entity-deleted", kind: entity.TypeDevice, token: "valve-7", at: at, fail: true, calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &deletionFake{}
			if tc.fail {
				api.err = errors.New("database is down")
			}
			rc := NewDeletionConsumer(&core.Microservice{InstanceId: "inst", FunctionalArea: "command-delivery"}, nil, core.NewNoOpLifecycleCallbacks(), api, nil)
			payload, err := entityevent.MarshalEntityDeletedEvent(&entityevent.EntityDeletedEvent{EntityType: tc.kind, EntityToken: tc.token, DeletedTime: tc.at})
			require.NoError(t, err)
			if tc.poison {
				payload = []byte("not-proto\xff")
			}
			ack := &gateAck{}
			rc.handle(context.Background(), messaging.NewConsumedMessage(tc.subject, payload, 1, nil, ack))
			assert.Equal(t, tc.acks, ack.n)
			require.Len(t, api.calls, tc.calls)
			if tc.calls > 0 {
				assert.Equal(t, deletionCall{"acme", tc.token, tc.at}, api.calls[0])
			}
		})
	}
}

func TestDeletionConsumerUsesTheSharedFailureDisposition(t *testing.T) {
	for _, tc := range []struct {
		name          string
		err           error
		acks, letters int
	}{
		{"exhausted", errors.New("write failed"), 1, 1},
		{"erased", rdb.ErrTenantPurged, 1, 0},
		{"hold released", messaging.ErrHoldReleased, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &deletionFake{err: tc.err}
			dead := &deadRecorder{}
			rc := NewDeletionConsumer(&core.Microservice{InstanceId: "inst", FunctionalArea: "command-delivery"}, nil, core.NewNoOpLifecycleCallbacks(), api, testDeadSink(dead))
			payload, err := entityevent.MarshalEntityDeletedEvent(&entityevent.EntityDeletedEvent{
				EntityType: entity.TypeDevice, EntityToken: "valve-7", DeletedTime: time.Now().UTC(),
			})
			require.NoError(t, err)
			ack := &gateAck{}
			msg := messaging.NewConsumedMessage("inst.acme.entity-deleted", payload, messaging.MaxDeliver, nil, ack).
				WithOrigin(messaging.Origin{Suffix: streams.EntityDeleted, Stream: "inst_entity-deleted", Consumer: "inst_command-delivery_entity-deleted", Seq: 5})
			rc.handle(context.Background(), msg)
			assert.Equal(t, tc.acks, ack.n)
			assert.Len(t, dead.msgs, tc.letters)
		})
	}
}
