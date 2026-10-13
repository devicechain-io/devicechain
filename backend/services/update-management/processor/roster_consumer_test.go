// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/require"
)

type fakeRosterApi struct {
	calls []rosterCall
	err   error
}

type rosterCall struct {
	tenant, device string
	enabled        bool
}

func (f *fakeRosterApi) DeviceRosterChanged(ctx context.Context, device string, enabled bool) error {
	tenant, _ := core.TenantFromContext(ctx)
	f.calls = append(f.calls, rosterCall{tenant, device, enabled})
	return f.err
}

func rosterConsumerOver(api RosterApi) *RosterConsumer {
	ms := &core.Microservice{InstanceId: "inst", FunctionalArea: "update-management"}
	return NewRosterConsumer(ms, nil, core.NewNoOpLifecycleCallbacks(), api, nil)
}

func rosterFact(t *testing.T, device string, enabled bool) []byte {
	t.Helper()
	b, err := dmproto.MarshalDeviceRosterEvent(&dmmodel.DeviceRosterEvent{DeviceToken: device, Enabled: enabled, Disabled: !enabled})
	require.NoError(t, err)
	return b
}

// The enabled flag (and only it) on the real roster fact reaches the API, on the tenant of the subject.
func TestTheRosterConsumerPassesTheEnabledFlagOnTheSubjectsTenant(t *testing.T) {
	api := &fakeRosterApi{}
	c := rosterConsumerOver(api)
	for _, enabled := range []bool{true, false} {
		ack := &ackRecorder{}
		c.handle(context.Background(), messaging.NewConsumedMessage("inst.acme.device-roster", rosterFact(t, "dev-1", enabled), 1, nil, ack))
		require.Equal(t, 1, ack.acks)
	}
	require.Equal(t, []rosterCall{{"acme", "dev-1", true}, {"acme", "dev-1", false}}, api.calls)
}

// A fact that is not usable is dropped and acked; a transient failure is left for redelivery.
func TestTheRosterConsumerDropsWhatItCannotUseAndRetriesWhatFailed(t *testing.T) {
	api := &fakeRosterApi{}
	c := rosterConsumerOver(api)

	ack := &ackRecorder{}
	c.handle(context.Background(), messaging.NewConsumedMessage("inst.acme.device-roster", []byte("not-proto\xff"), 1, nil, ack))
	require.Equal(t, 1, ack.acks)
	ack = &ackRecorder{}
	c.handle(context.Background(), messaging.NewConsumedMessage("inst.acme.device-roster", rosterFact(t, "", false), 1, nil, ack))
	require.Equal(t, 1, ack.acks, "a fact naming no device")
	require.Empty(t, api.calls)

	api.err = errors.New("device-management is down")
	ack = &ackRecorder{}
	c.handle(context.Background(), messaging.NewConsumedMessage("inst.acme.device-roster", rosterFact(t, "dev-1", false), 1, nil, ack))
	require.Zero(t, ack.acks, "a failed re-offer must be left for redelivery")
}
