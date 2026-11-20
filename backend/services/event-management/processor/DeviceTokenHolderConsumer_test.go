// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/contract"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	emtest "github.com/devicechain-io/dc-event-management/test"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const rosterSubject = "instance1.acme.device-roster"

func rosterMsg(t *testing.T, subject string, ev *contract.DeviceRosterEvent, delivered int, ack messaging.Acknowledger) messaging.Message {
	t.Helper()
	b, err := dmproto.MarshalDeviceRosterEvent(ev)
	require.NoError(t, err)
	return messaging.NewConsumedMessage(subject, b, delivered, nil, ack)
}

// A roster fact records its token as held since the fact's ExpectedSince, under the subject's
// tenant, and is acked.
func TestHolderConsumer_RecordsTheHolderAndAcks(t *testing.T) {
	since := time.Date(2026, 11, 10, 13, 0, 0, 0, time.UTC)
	api := new(emtest.MockApi)
	api.Mock.On("RecordDeviceTokenHeld", "pump-1", mock.MatchedBy(since.Equal)).Return(nil)
	c := &DeviceTokenHolderConsumer{Api: api}
	ack := &stubAck{}
	c.handle(context.Background(), rosterMsg(t, rosterSubject,
		&contract.DeviceRosterEvent{DeviceToken: "pump-1", ProfileToken: "pumps", ExpectedSince: since}, 1, ack))
	api.Mock.AssertExpectations(t)
	assert.Equal(t, 1, ack.acked)
}

// A DISABLED device still holds its token: a disable fact records it like any other.
func TestHolderConsumer_ADisabledDeviceStillHoldsItsToken(t *testing.T) {
	since := time.Date(2026, 11, 10, 13, 0, 0, 0, time.UTC)
	api := new(emtest.MockApi)
	api.Mock.On("RecordDeviceTokenHeld", "pump-1", mock.Anything).Return(nil)
	c := &DeviceTokenHolderConsumer{Api: api}
	c.handle(context.Background(), rosterMsg(t, rosterSubject,
		&contract.DeviceRosterEvent{DeviceToken: "pump-1", ExpectedSince: since, Disabled: true}, 1, &stubAck{}))
	api.Mock.AssertExpectations(t)
}

// Poison, a fact with no tenant, and a fact with no instant are acked and record nothing.
func TestHolderConsumer_DropsWhatNamesNoHolder(t *testing.T) {
	for name, msg := range map[string]func(*stubAck) messaging.Message{
		"poison": func(a *stubAck) messaging.Message {
			return messaging.NewConsumedMessage(rosterSubject, []byte("not-proto"), 1, nil, a)
		},
		"no tenant": func(a *stubAck) messaging.Message {
			return rosterMsg(t, "no-tenant", &contract.DeviceRosterEvent{DeviceToken: "pump-1", ExpectedSince: time.Now()}, 1, a)
		},
		"no instant": func(a *stubAck) messaging.Message {
			return rosterMsg(t, rosterSubject, &contract.DeviceRosterEvent{DeviceToken: "pump-1"}, 1, a)
		},
		"no device": func(a *stubAck) messaging.Message {
			return rosterMsg(t, rosterSubject, &contract.DeviceRosterEvent{ExpectedSince: time.Now()}, 1, a)
		},
	} {
		t.Run(name, func(t *testing.T) {
			api := new(emtest.MockApi)
			ack := &stubAck{}
			(&DeviceTokenHolderConsumer{Api: api}).handle(context.Background(), msg(ack))
			assert.Equal(t, 1, ack.acked)
			api.Mock.AssertNotCalled(t, "RecordDeviceTokenHeld", mock.Anything, mock.Anything)
		})
	}
}

// A transient failure is left unacked for redelivery; past the cap it is acked (the sweep
// repairs the token).
func TestHolderConsumer_TransientThenExhausted(t *testing.T) {
	for _, tc := range []struct {
		delivered, acked int
	}{{1, 0}, {messaging.MaxDeliver, 1}} {
		api := new(emtest.MockApi)
		api.Mock.On("RecordDeviceTokenHeld", "pump-1", mock.Anything).Return(errors.New("db down"))
		ack := &stubAck{}
		(&DeviceTokenHolderConsumer{Api: api}).handle(context.Background(), rosterMsg(t, rosterSubject,
			&contract.DeviceRosterEvent{DeviceToken: "pump-1", ExpectedSince: time.Now()}, tc.delivered, ack))
		api.Mock.AssertCalled(t, "RecordDeviceTokenHeld", "pump-1", mock.Anything)
		assert.Equalf(t, tc.acked, ack.acked, "delivery %d", tc.delivered)
	}
}
