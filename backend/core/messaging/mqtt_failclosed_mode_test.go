// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func TestWALWriterModeHandoffWaitsForTheHolderAndItsPendingAcks(t *testing.T) {
	for _, envelopes := range []bool{false, true} {
		t.Run(fmt.Sprintf("to-envelopes=%t", envelopes), func(t *testing.T) {
			b := startGuardBroker(t)
			clock := newFcClock()
			mode := func(on bool) MqttStoreWriterMode {
				if on {
					return MqttStoreWriterEnvelopes
				}
				return MqttStoreWriterPerMessage
			}
			old := failClosedManager(t, b.nc, b.js, guardInstance, true)
			require.NoError(t, old.SetMqttMessageStoreWriterMode(mode(!envelopes)))
			old.mqttClock = clock.now
			_, err := old.ReconcileMqttFailClosed(context.Background())
			require.NoError(t, err)
			d := fcDurable(t, guardInstance, MqttFailClosedEvents)
			sub, err := b.js.PullSubscribe("", d.Name, nats.Bind(MqttMessageStore, d.Name))
			require.NoError(t, err)
			defer sub.Unsubscribe()
			subject := mqttMsgsSubjectPrefix + guardInstance + "." + guardTenant + ".devices.mode-device.events"
			_, err = b.js.Publish(subject, []byte("already-in-flight"))
			require.NoError(t, err)
			ms, err := sub.Fetch(1, nats.MaxWait(time.Second))
			require.NoError(t, err)
			_, err = b.js.Publish(subject, []byte("still-unread"))
			require.NoError(t, err)

			next := failClosedManager(t, b.nc, b.js, guardInstance, true)
			require.NoError(t, next.SetMqttMessageStoreWriterMode(mode(envelopes)))
			next.mqttClock = clock.now
			state, err := next.ReconcileMqttFailClosed(context.Background())
			require.NoError(t, err)
			require.Equal(t, mqttStandingStandby, state.Standing, "a live holder with another mode must exclude this pod")
			reader, err := fcEventsReader(t, next)
			require.NoError(t, err)
			defer reader.Close()
			fcNothing(t, reader, 80*time.Millisecond, "a different-mode standby must not fetch the WAL")
			ci, err := b.js.ConsumerInfo(MqttMessageStore, d.Name)
			require.NoError(t, err)
			require.EqualValues(t, 1, ci.NumAckPending)
			require.EqualValues(t, 1, ci.NumPending)

			clock.advance(mqttFailClosedLeaseTTL + time.Second)
			state, err = next.ReconcileMqttFailClosed(context.Background())
			require.NoError(t, err)
			require.Equal(t, mqttStandingStandby, state.Standing, "a stale holder still has an in-flight publication")
			fcNothing(t, reader, 80*time.Millisecond, "handoff must wait for ack-pending to reach zero")
			require.NoError(t, ms[0].AckSync())
			state, err = next.ReconcileMqttFailClosed(context.Background())
			require.NoError(t, err)
			require.Equal(t, mqttStandingHolder, state.Standing)
			got := fcRead(t, reader, 5*time.Second)
			require.Equal(t, "still-unread", string(got.Value))
			require.NoError(t, got.Ack())
			require.Eventually(t, func() bool {
				ci, err := b.js.ConsumerInfo(MqttMessageStore, d.Name)
				return err == nil && ci.NumAckPending == 0
			}, time.Second, time.Millisecond)

			// A restarted old-mode pod must stand by, even with no in-flight work.
			state, err = old.ReconcileMqttFailClosed(context.Background())
			require.NoError(t, err)
			require.Equal(t, mqttStandingStandby, state.Standing)
			oldReader, err := fcEventsReader(t, old)
			require.NoError(t, err)
			defer oldReader.Close()
			_, err = b.js.Publish(subject, []byte("new-mode-only"))
			require.NoError(t, err)
			fcNothing(t, oldReader, 80*time.Millisecond, "the previous mode cannot join the new holder")
			got = fcRead(t, reader, 5*time.Second)
			require.Equal(t, "new-mode-only", string(got.Value))
			require.NoError(t, got.Ack())
		})
	}
}

func TestWALSameWriterModeMayReadBesideADifferentBound(t *testing.T) {
	b := startGuardBroker(t)
	first := failClosedManager(t, b.nc, b.js, guardInstance, true)
	_, err := first.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	next := failClosedManager(t, b.nc, b.js, guardInstance, true)
	next.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamMaxBytes = 2 * fcHotBytes
	state, err := next.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	require.Equal(t, mqttStandingStandby, state.Standing)
	reader, err := fcEventsReader(t, next)
	require.NoError(t, err)
	defer reader.Close()
	_, err = b.js.Publish(mqttMsgsSubjectPrefix+guardInstance+"."+guardTenant+".devices.same-mode.events", []byte("same-mode"))
	require.NoError(t, err)
	got := fcRead(t, reader, 5*time.Second)
	require.Equal(t, "same-mode", string(got.Value))
	require.NoError(t, got.Ack())
}

func TestWALWriterModeRejectsAnUnknownMode(t *testing.T) {
	nmgr := &NatsManager{}
	require.Error(t, nmgr.SetMqttMessageStoreWriterMode("unknown"))
}

// A flag-off pod can drain a released store, but releasing it must observe the
// same mode boundary as an on-to-on handoff.
func TestWALModeHandoffToAFlagOffDrainerWaitsForPendingAcks(t *testing.T) {
	for _, envelopes := range []bool{false, true} {
		t.Run(fmt.Sprintf("to-envelopes=%t", envelopes), func(t *testing.T) {
			b := startGuardBroker(t)
			clock := newFcClock()
			oldMode, nextMode := MqttStoreWriterEnvelopes, MqttStoreWriterPerMessage
			if envelopes {
				oldMode, nextMode = nextMode, oldMode
			}
			old := failClosedManager(t, b.nc, b.js, guardInstance, true)
			require.NoError(t, old.SetMqttMessageStoreWriterMode(oldMode))
			old.mqttClock = clock.now
			_, err := old.ReconcileMqttFailClosed(context.Background())
			require.NoError(t, err)
			d := fcDurable(t, guardInstance, MqttFailClosedEvents)
			sub, err := b.js.PullSubscribe("", d.Name, nats.Bind(MqttMessageStore, d.Name))
			require.NoError(t, err)
			defer sub.Unsubscribe()
			subject := mqttMsgsSubjectPrefix + guardInstance + "." + guardTenant + ".devices.flag-off.events"
			_, err = b.js.Publish(subject, []byte("in-flight"))
			require.NoError(t, err)
			ms, err := sub.Fetch(1, nats.MaxWait(time.Second))
			require.NoError(t, err)
			_, err = b.js.Publish(subject, []byte("drain-me"))
			require.NoError(t, err)
			next := failClosedManager(t, b.nc, b.js, guardInstance, false)
			require.NoError(t, next.SetMqttMessageStoreWriterMode(nextMode))
			next.mqttClock = clock.now
			rep, err := next.mqttFailClosedPass(context.Background(), false)
			require.NoError(t, err)
			require.Equal(t, mqttStandingHeldByOn, rep.standing)
			reader, err := fcEventsReader(t, next)
			require.NoError(t, err)
			defer reader.Close()
			fcNothing(t, reader, 80*time.Millisecond, "a flag-off reader cannot bypass the live writer mode")
			clock.advance(mqttFailClosedLeaseTTL + time.Second)
			rep, err = next.mqttFailClosedPass(context.Background(), false)
			require.NoError(t, err)
			require.Equal(t, mqttStandingStandby, rep.standing)
			fcNothing(t, reader, 80*time.Millisecond, "release must wait for old-mode pending acks")
			require.NoError(t, ms[0].AckSync())
			rep, err = next.mqttFailClosedPass(context.Background(), false)
			require.NoError(t, err)
			require.Equal(t, mqttStandingReleased, rep.standing)
			got := fcRead(t, reader, 5*time.Second)
			require.Equal(t, "drain-me", string(got.Value))
			require.NoError(t, got.Ack())
		})
	}
}

// An earlier pass can release the store before a drainer's mode is configured.
// Its next pass must transfer that released drain at zero ack-pending.
func TestAReleasedStoreCanAdoptTheDrainersConfiguredWriterMode(t *testing.T) {
	b := startGuardBroker(t)
	clock := newFcClock()
	old := failClosedManager(t, b.nc, b.js, guardInstance, true)
	old.mqttClock = clock.now
	_, err := old.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	clock.advance(mqttFailClosedLeaseTTL + time.Second)
	next := failClosedManager(t, b.nc, b.js, guardInstance, false)
	next.mqttClock = clock.now
	rep, err := next.mqttFailClosedPass(context.Background(), false)
	require.NoError(t, err)
	require.Equal(t, mqttStandingReleased, rep.standing)
	require.NoError(t, next.SetMqttMessageStoreWriterMode(MqttStoreWriterEnvelopes))
	rep, err = next.mqttFailClosedPass(context.Background(), false)
	require.NoError(t, err)
	require.Equal(t, mqttStandingReleased, rep.standing)
	si, err := b.js.StreamInfo(MqttMessageStore)
	require.NoError(t, err)
	require.Equal(t, MqttStoreWriterEnvelopes, mqttFailClosedWriterMode(si.Config.Metadata[mqttFailClosedHolderKey]))
	reader, err := fcEventsReader(t, next)
	require.NoError(t, err)
	defer reader.Close()
	_, err = b.js.Publish(mqttMsgsSubjectPrefix+guardInstance+"."+guardTenant+".devices.released.events", []byte("released-drain"))
	require.NoError(t, err)
	got := fcRead(t, reader, 5*time.Second)
	require.Equal(t, "released-drain", string(got.Value))
	require.NoError(t, got.Ack())
}
