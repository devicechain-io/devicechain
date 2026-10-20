// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"github.com/devicechain-io/dc-microservice/presence"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/config"
	dmodel "github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/eventtype"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// deviceDisabledMetric is the counter a disabled device's refused event increments.
const deviceDisabledMetric = "devicechain_devicemanagement_resolve_device_disabled_total"

func disabledDevice(token string) *dmodel.Device {
	d := deviceWithToken(token)
	d.DisabledAt.Valid = true
	d.DisabledAt.Time = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	return d
}

// An event from a disabled device is refused through the REAL cached Api, on the trusted
// token path, and the refusal follows the device's state across its caches: the device is
// resolved (warming every cache), disabled through the Api, refused, re-enabled, and
// resolved again. Without the update's evictions the refusal would wait for the cached
// copy to expire, and without the check the disabled device would resolve.
func TestADisabledDeviceIsRefusedAtResolveAndFollowsItsState(t *testing.T) {
	rig := newCountingResolveRig(t, false, nil)
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_resolve_device_disabled_total"})
	rig.rez.deviceDisabled = counter

	rig.resolve(tempEvent("21.5")) // warms the device-by-token entry

	if _, err := rig.api.UpdateDevice(rig.ctx, "dev", &dmodel.DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	_, reason, err := rig.rez.ResolveEvent(rig.ctx, tempEvent("21.6"))
	require.Error(t, err, "a disabled device's event resolved")
	assert.Equal(t, uint(resolvedevent.FailureReason_DeviceDisabled), reason)
	assert.True(t, errors.Is(err, dmodel.ErrDeviceDisabled), "%v", err)
	assert.Equal(t, 1.0, testutil.ToFloat64(counter), "the refusal is counted once")

	if _, err := rig.api.UpdateDevice(rig.ctx, "dev", &dmodel.DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(true)}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	rig.resolve(tempEvent("21.7"))
	assert.Equal(t, 1.0, testutil.ToFloat64(counter), "an enabled device's event is not counted")
}

// The credential path refuses a disabled device the same way: an authenticated credential
// does not exempt the device it belongs to.
func (suite *EventResolverTestSuite) TestACredentialedEventFromADisabledDeviceIsRefused() {
	suite.API.Mock.On("AuthenticateDevice").Return(disabledDevice("TEST-123"), nil)
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_resolve_device_disabled_total"})
	rez := suite.resolver(config.AuthModeRequired)
	rez.deviceDisabled = counter

	_, reason, err := rez.ResolveEvent(credCtx(), eventWithCredential(suite.T(), "TEST-123"))
	require.Error(suite.T(), err)
	assert.Equal(suite.T(), uint(resolvedevent.FailureReason_DeviceDisabled), reason)
	assert.True(suite.T(), errors.Is(err, dmodel.ErrDeviceDisabled), "%v", err)
	assert.Equal(suite.T(), 1.0, testutil.ToFloat64(counter))
}

// A disabled device's event is settled on its FIRST delivery and discarded: acknowledged,
// counted by the wired counter, and never dead-lettered. Redelivery cannot change the
// answer while the device stays disabled, and a dead-letter copy of each event would carry
// a quarantined device's flood into the failed-events store.
func (suite *InboundEventsProcessorTestSuite) TestADisabledDevicesEventIsDiscardedOnItsFirstDelivery() {
	reg := suite.withRegistry()
	loc := buildLocationsEvent()
	loc.ProcessedTime = time.Now()
	loc.OccurredTime = loc.ProcessedTime // fresh, so the age floor does not refuse it first
	encoded, err := esproto.MarshalUnresolvedEvent(loc)
	require.NoError(suite.T(), err)
	ack := &atomicAck{}
	msg := messaging.NewConsumedMessage(testTenantSubject, encoded, 1, nil, ack)
	suite.Inbound.Mock.On("ReadMessage", mock.Anything).Return(msg, nil)
	suite.API.Mock.On("DevicesByToken", mock.Anything, mock.Anything).Return([]*dmodel.Device{disabledDevice(loc.Device)}, nil)

	readAndHandleOne(suite.IP, context.Background())
	require.Eventually(suite.T(), func() bool { return ack.n.Load() == 1 }, 10*time.Second, 10*time.Millisecond,
		"the event was not settled on its first delivery")
	select {
	case item := <-suite.IP.failed:
		suite.T().Fatalf("a disabled device's event was dead-lettered: %+v", item.event)
	case <-time.After(200 * time.Millisecond):
	}
	assert.Equal(suite.T(), 1.0, counterValue(suite, reg, deviceDisabledMetric), "the refusal is counted once")
}

// presenceEvent is a transport's presence transition for the rig's device, fresh so the
// age floor lets it through.
func presenceEvent(source string, state presence.State, session string) *esmodel.UnresolvedEvent {
	now := time.Now().UTC()
	return &esmodel.UnresolvedEvent{
		Source:        source,
		Device:        "dev",
		EventType:     eventtype.StateChange,
		OccurredTime:  now,
		ProcessedTime: now,
		Payload:       &esmodel.UnresolvedStateChangePayload{State: state, Reason: "test", SessionId: session},
	}
}

// A disabled device's connection is still the platform's to report. Disabling a device does
// not close a connection it already has, so the transition that ends it — from the broker,
// the LwM2M server or the Sparkplug host — must reach the presence projection, or the
// device reads CONNECTED for as long as it stays disabled. The transition is marked as a
// disabled device's, and the device's own data is still refused around it.
func TestADisabledDevicesPresenceTransitionsAreResolvedAndMarked(t *testing.T) {
	rig := newCountingResolveRig(t, false, nil)
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_resolve_device_disabled_total"})
	rig.rez.deviceDisabled = counter

	connected := rig.resolve(presenceEvent("mqtt", presence.StateConnected, "7"))
	assert.False(t, connected.SourceDeviceDisabled, "an enabled device's transition was marked disabled")

	if _, err := rig.api.UpdateDevice(rig.ctx, "dev", &dmodel.DeviceUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)}); err != nil {
		t.Fatalf("disable: %v", err)
	}

	for _, source := range []string{"mqtt", "lwm2m", "sparkplug"} {
		for _, state := range []presence.State{presence.StateDisconnected, presence.StateConnected} {
			results, reason, err := rig.rez.ResolveEvent(rig.ctx, presenceEvent(source, state, "8"))
			require.NoError(t, err, "%s %s from a disabled device was refused", source, state)
			require.Equal(t, uint(0), reason)
			require.Len(t, results, 1)
			resolved := results[0].Resolved
			payload, ok := resolved.Payload.(*resolvedevent.ResolvedStateChangePayload)
			require.True(t, ok, "%T", resolved.Payload)
			assert.Equal(t, string(state), payload.State)
			assert.True(t, resolved.SourceDeviceDisabled, "%s %s was not marked as a disabled device's", source, state)
		}
	}
	assert.Equal(t, 0.0, testutil.ToFloat64(counter), "a presence transition was counted as a refused event")

	// The device's own data is still refused: a measurement, a location and an alert.
	for _, event := range []*esmodel.UnresolvedEvent{tempEvent("21.6"), devLocationEvent(), {
		Device: "dev", EventType: eventtype.Alert,
		Payload: &esmodel.UnresolvedAlertsPayload{Entries: []esmodel.UnresolvedAlertEntry{{Type: "smoke"}}},
	}} {
		_, reason, err := rig.rez.ResolveEvent(rig.ctx, event)
		require.Error(t, err, "a disabled device's %s resolved", event.EventType)
		assert.True(t, errors.Is(err, dmodel.ErrDeviceDisabled), "%v", err)
		assert.Equal(t, uint(resolvedevent.FailureReason_DeviceDisabled), reason)
	}
	assert.Equal(t, 3.0, testutil.ToFloat64(counter), "each refused event is counted once")
}
