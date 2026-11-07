// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-event-sources/processor"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/stretchr/testify/require"
)

// A DEVICE'S OWN UPLINK IS WRITTEN TO THE UPLINK STREAM ONLY, SEALED, AND WITH NO AUTHORITY BUT ITS
// OWN CREDENTIAL. This asserts the publish the device route is wired to; the route's own tests hand
// it a publisher of their own.

func deviceUplinkEvent() *model.UnresolvedEvent {
	typ, id, secret := "MQTT_BASIC", "meter-1", "hunter2"
	now := time.Now().UTC()
	return &model.UnresolvedEvent{
		Device: "meter-1", OccurredTime: now, ProcessedTime: now, EventType: eventtype.IntegrationUplink,
		CredentialType: &typ, CredentialId: &id, CredentialSecret: &secret,
	}
}

func TestADeviceUplinkIsWrittenToTheUplinkStreamSealedAndUnmarked(t *testing.T) {
	sealingFixture(t)
	savedInbound, savedUplinks := InboundEventsWriter, IntegrationUplinksWriter
	t.Cleanup(func() { InboundEventsWriter, IntegrationUplinksWriter = savedInbound, savedUplinks })
	inbound, uplinks := &recordingWriter{}, &recordingWriter{}
	InboundEventsWriter, IntegrationUplinksWriter = inbound, uplinks

	e := deviceUplinkEvent()
	// A producer that tried to smuggle authority in: every one is cleared.
	e.Integration, e.SealedIntegrationToken, e.IntegrationIdentifier = "cs", []byte{1}, "dev"
	e.AuthenticatedTransport = true

	require.NoError(t, onDeviceUplinkDecoded("http", "acme", e, &model.UnresolvedIntegrationUplinkPayload{Frame: []byte{1, 2}, MaxReadings: 3}))
	require.Zero(t, inbound.count(), "a device uplink must never reach inbound-events")
	require.Equal(t, 1, uplinks.count())

	got, err := esproto.UnmarshalUnresolvedEvent(uplinks.msgs[0].Value)
	require.NoError(t, err)
	require.Equal(t, eventtype.IntegrationUplink, got.EventType)
	require.Equal(t, "http", got.Source)
	require.Empty(t, got.Integration)
	require.Nil(t, got.SealedIntegrationToken)
	require.Empty(t, got.IntegrationIdentifier)
	require.False(t, got.AuthenticatedTransport)
	require.Nil(t, got.CredentialSecret, "the secret as sent never crosses the stream")
	require.NotEmpty(t, got.SealedCredentialSecret)
	require.NotContains(t, string(uplinks.msgs[0].Value), "hunter2")
}

// The device-facing publish for every other path still refuses the carrier type: the new function
// is the only door.
func TestTheGenericDevicePublishStillRefusesTheCarrier(t *testing.T) {
	sealingFixture(t)
	_, _, ok := inboundEventMessage("http", "acme", deviceUplinkEvent(), &model.UnresolvedIntegrationUplinkPayload{}, "")
	require.False(t, ok)
}

func TestADeviceUplinkWriteFailureIsReturnedNotSwallowed(t *testing.T) {
	sealingFixture(t)
	saved := IntegrationUplinksWriter
	t.Cleanup(func() { IntegrationUplinksWriter = saved })
	payload := &model.UnresolvedIntegrationUplinkPayload{MaxReadings: 1}

	IntegrationUplinksWriter = nil
	require.Error(t, onDeviceUplinkDecoded("http", "acme", deviceUplinkEvent(), payload))

	IntegrationUplinksWriter = &recordingWriter{err: messaging.ErrStreamBackpressure}
	require.ErrorIs(t, onDeviceUplinkDecoded("http", "acme", deviceUplinkEvent(), payload), messaging.ErrStreamBackpressure)

	// A message that cannot be built is the route's 500, not a success.
	IntegrationUplinksWriter = &recordingWriter{}
	require.ErrorIs(t, onDeviceUplinkDecoded("http", "", deviceUplinkEvent(), payload), processor.ErrEventNotBuilt)
}
