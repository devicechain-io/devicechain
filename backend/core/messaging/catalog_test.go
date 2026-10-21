// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

// deployedManager is a manager over a Deployed microservice of the given area and NO broker:
// the catalog is asked before the broker is touched, so a refusal needs none.
func deployedManager(area string) *NatsManager {
	return &NatsManager{Microservice: &core.Microservice{InstanceId: "test", FunctionalArea: area, Deployed: true}}
}

// An area that uses a stream, bucket or consumer it does not declare is refused, at the
// manager, before any broker call. Each case is the NEGATIVE CONTROL for its row of the
// declaration: event-sources is a real area and a real writer, and the stream it is refused
// is a real one.
func TestUndeclaredUseIsRefused(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		area string
		call func(*NatsManager) error
		want string // what the refusal must name
	}{
		{"writer on another area's stream", "event-sources", func(m *NatsManager) error {
			_, err := m.NewWriter(streams.DeviceCommands)
			return err
		}, streams.DeviceCommands},
		{"ordered writer on a stream the area only reads", "device-management", func(m *NatsManager) error {
			_, err := m.NewOrderedWriter(streams.InboundEvents, WriterResolution, 1)
			return err
		}, streams.InboundEvents},
		{"writer on a broker-written stream", "event-sources", func(m *NatsManager) error {
			_, err := m.NewWriter(streams.DeviceEventsCapture)
			return err
		}, streams.DeviceEventsCapture},
		{"reader on an undeclared stream", "update-management", func(m *NatsManager) error {
			_, err := m.NewReader(streams.DeviceCommands)
			return err
		}, streams.DeviceCommands},
		{"replay without being declared to replay", "device-state", func(m *NatsManager) error {
			_, _, err := m.NewReplayReader(streams.ResolvedEvents, 1)
			return err
		}, streams.ResolvedEvents},
		{"replay by time", "device-state", func(m *NatsManager) error {
			_, _, err := m.NewReplayReaderFromTime(streams.ResolvedEvents, timeZero())
			return err
		}, streams.ResolvedEvents},
		{"range read", "device-state", func(m *NatsManager) error {
			_, err := m.NewRangeReader(streams.ResolvedEvents, 1, 2)
			return err
		}, streams.ResolvedEvents},
		{"live feed of a stream the area does not follow", "device-state", func(m *NatsManager) error {
			_, err := m.SubscribeLive(ctx, "acme", streams.ResolvedEvents)
			return err
		}, streams.ResolvedEvents},
		{"bucket of another area", "event-sources", func(m *NatsManager) error {
			_, err := m.KeyValueStore(kv.BucketDeviceByToken, "any", 0)
			return err
		}, kv.BucketDeviceByToken},
		{"undeclared bucket", "user-management", func(m *NatsManager) error {
			_, err := m.KeyValueStore("a-bucket-nobody-declared", "any", 0)
			return err
		}, "a-bucket-nobody-declared"},
		{"an area nobody declares", "no-such-area", func(m *NatsManager) error {
			_, err := m.NewWriter(streams.InboundEvents)
			return err
		}, "no-such-area"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call(deployedManager(tc.area))
			require.Error(t, err, "an undeclared use must fail at the manager")
			require.True(t, strings.Contains(err.Error(), tc.want), "the refusal must name %q: %v", tc.want, err)
			require.Contains(t, err.Error(), "declare", "the refusal must say where the fix is")
		})
	}
}

// The control for the control: the same checks accept what the declaration says. Without it
// the refusals above would pass for a check that refuses everything.
func TestDeclaredUseIsAccepted(t *testing.T) {
	for _, tc := range []struct {
		area string
		call func(*NatsManager) error
	}{
		{"event-sources", func(m *NatsManager) error { return m.requireWriter(streams.InboundEvents) }},
		{"event-sources", func(m *NatsManager) error { return m.requireAdmin(streams.DeviceEventsCapture) }},
		{"device-management", func(m *NatsManager) error { return m.requireReader(streams.InboundEvents, streams.ReadDurable) }},
		{"event-processing", func(m *NatsManager) error { return m.requireReader(streams.ResolvedEvents, streams.ReadEphemeral) }},
		{"event-management", func(m *NatsManager) error { return m.requireReader(streams.ResolvedEvents, streams.ReadLive) }},
		{"device-management", func(m *NatsManager) error { return m.requireBucket(kv.BucketDeviceByToken) }},
		{"dashboard-management", func(m *NatsManager) error { return m.requireWriter(streams.DeadLetters) }},
	} {
		require.NoError(t, tc.call(deployedManager(tc.area)), "%s", tc.area)
	}
}

// A fixture is not held to the declaration (see core.Microservice.Deployed).
func TestFixtureManagerIsNotHeldToTheCatalog(t *testing.T) {
	m := &NatsManager{Microservice: &core.Microservice{InstanceId: "test", FunctionalArea: "test-area"}}
	require.NoError(t, m.requireWriter(streams.DeviceCommands))
	require.NoError(t, m.requireAdmin(streams.DeviceCommands))
	require.NoError(t, m.requireBucket("anything"))
	require.NoError(t, m.requireConsumer(streams.DeviceCommands, "anything"))
}

// A reader's consumer NAME is part of what it declares. An ack-floor reader is a different
// consumer from a durable one on the same stream, so declaring one does not declare the other,
// and event-processing - which declares both names on resolved-events - owns exactly those two.
func TestConsumerNamesAreDeclared(t *testing.T) {
	ep := deployedManager("event-processing")
	floor := AckFloorDurableName("test", "event-processing", streams.ResolvedEvents)
	legacy := DurableName("test", "event-processing", streams.ResolvedEvents)
	require.NoError(t, ep.requireConsumer(streams.ResolvedEvents, floor))
	require.NoError(t, ep.requireConsumer(streams.ResolvedEvents, legacy), "the legacy durable is deleted at term start")
	require.Error(t, ep.requireConsumer(streams.ResolvedEvents, DurableName("test", "device-state", streams.ResolvedEvents)),
		"another area's durable is not this area's to create, fetch from, acknowledge or delete")
	require.Error(t, ep.requireConsumer(streams.ResolvedEvents, "test_event-processing_resolved-events_extra"))

	// command-delivery reads device-roster as an ordinary durable; the ack-floor name is not its own.
	cd := deployedManager("command-delivery")
	require.NoError(t, cd.requireConsumer(streams.DeviceRoster, DurableName("test", "command-delivery", streams.DeviceRoster)))
	require.Error(t, cd.requireConsumer(streams.DeviceRoster, AckFloorDurableName("test", "command-delivery", streams.DeviceRoster)))
}

func TestDeclaredDurablesByMode(t *testing.T) {
	require.Equal(t, []string{DurableName("i", "a", "s")},
		DeclaredDurables("i", "s", streams.Reader{Area: "a", Mode: streams.ReadDurable}))
	require.Len(t, DeclaredDurables("i", "s", streams.Reader{Area: "a", Mode: streams.ReadAckFloor}), 2)
	require.Empty(t, DeclaredDurables("i", "s", streams.Reader{Area: "a", Mode: streams.ReadEphemeral}))
	require.Empty(t, DeclaredDurables("i", "s", streams.Reader{Area: "a", Mode: streams.ReadLive}))
}

func timeZero() (t time.Time) { return }
