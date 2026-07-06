// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	dmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	emtest "github.com/devicechain-io/dc-event-management/test"
	"github.com/devicechain-io/dc-microservice/entity"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// stubAck records Ack/Nak so a test can assert a message's disposition.
type stubAck struct{ acked, naked int }

func (s *stubAck) Ack() error { s.acked++; return nil }
func (s *stubAck) Nak() error { s.naked++; return nil }

func deletedMsg(t *testing.T, subject string, event *dmodel.EntityDeletedEvent, numDelivered int, ack messaging.Acknowledger) messaging.Message {
	t.Helper()
	b, err := dmproto.MarshalEntityDeletedEvent(event)
	assert.NoError(t, err)
	return messaging.NewConsumedMessage(subject, b, numDelivered, nil, ack)
}

const reconcilerSubject = "instance1.acme.entity-deleted"

// A device deletion cleans anchors by device token and acks.
func TestReconciler_DeletesAndAcks(t *testing.T) {
	api := new(emtest.MockApi)
	api.Mock.On("DeleteAnchorsForEntity", "device", "dev-4").Return(2, nil)
	r := &EntityAnchorReconciler{Api: api}
	ack := &stubAck{}

	r.handle(context.Background(), deletedMsg(t, reconcilerSubject,
		&dmodel.EntityDeletedEvent{EntityType: entity.TypeDevice, EntityId: 4, EntityToken: "dev-4", DeletedTime: time.Now().UTC()}, 1, ack))

	api.Mock.AssertCalled(t, "DeleteAnchorsForEntity", "device", "dev-4")
	assert.Equal(t, 1, ack.acked)
	assert.Equal(t, 0, ack.naked)
}

// An unparseable payload is dropped (acked) without a cleanup attempt.
func TestReconciler_PoisonDropped(t *testing.T) {
	api := new(emtest.MockApi)
	r := &EntityAnchorReconciler{Api: api}
	ack := &stubAck{}

	r.handle(context.Background(), messaging.NewConsumedMessage(reconcilerSubject, []byte("not-proto"), 1, nil, ack))

	assert.Equal(t, 1, ack.acked)
	api.Mock.AssertNotCalled(t, "DeleteAnchorsForEntity", mock.Anything, mock.Anything)
}

// A subject with no parseable tenant is dropped (acked) without a cleanup attempt —
// a tenantless delete must never run.
func TestReconciler_NoTenantDropped(t *testing.T) {
	api := new(emtest.MockApi)
	r := &EntityAnchorReconciler{Api: api}
	ack := &stubAck{}

	r.handle(context.Background(), deletedMsg(t, "no-tenant",
		&dmodel.EntityDeletedEvent{EntityType: entity.TypeDevice, EntityId: 4, DeletedTime: time.Now().UTC()}, 1, ack))

	assert.Equal(t, 1, ack.acked)
	api.Mock.AssertNotCalled(t, "DeleteAnchorsForEntity", mock.Anything, mock.Anything)
}

// A transient DB error naks for redelivery while below the poison ceiling.
func TestReconciler_TransientErrorNaks(t *testing.T) {
	api := new(emtest.MockApi)
	api.Mock.On("DeleteAnchorsForEntity", "customer", "cust-3").Return(0, errors.New("db down"))
	r := &EntityAnchorReconciler{Api: api}
	ack := &stubAck{}

	r.handle(context.Background(), deletedMsg(t, reconcilerSubject,
		&dmodel.EntityDeletedEvent{EntityType: entity.TypeCustomer, EntityId: 3, EntityToken: "cust-3", DeletedTime: time.Now().UTC()}, 1, ack))

	assert.Equal(t, 1, ack.naked)
	assert.Equal(t, 0, ack.acked)
}

// At the delivery ceiling a persistently failing message is dropped (acked) rather
// than naked forever.
func TestReconciler_PoisonCeilingDrops(t *testing.T) {
	api := new(emtest.MockApi)
	api.Mock.On("DeleteAnchorsForEntity", "customer", "cust-3").Return(0, errors.New("db down"))
	r := &EntityAnchorReconciler{Api: api}
	ack := &stubAck{}

	r.handle(context.Background(), deletedMsg(t, reconcilerSubject,
		&dmodel.EntityDeletedEvent{EntityType: entity.TypeCustomer, EntityId: 3, EntityToken: "cust-3", DeletedTime: time.Now().UTC()}, messaging.MaxDeliver, ack))

	assert.Equal(t, 1, ack.acked)
	assert.Equal(t, 0, ack.naked)
}
