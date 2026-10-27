// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"github.com/devicechain-io/dc-microservice/presence"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/eventtype"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/eventtime"
	"github.com/stretchr/testify/assert"
)

func strptr(s string) *string { return &s }

// The presented credential must survive the marshal/unmarshal round-trip so it
// reaches the resolver across the messaging hop (ADR-014).
func TestMarshalRoundTripCarriesCredential(t *testing.T) {
	event := &model.UnresolvedEvent{
		Source:                 "mqtt1",
		Device:                 "TEST-123",
		OccurredTime:           time.Now().UTC().Truncate(time.Second),
		ProcessedTime:          time.Now().UTC().Truncate(time.Second),
		EventType:              eventtype.Location,
		Payload:                &model.UnresolvedLocationsPayload{Entries: []model.UnresolvedLocationEntry{}},
		CredentialType:         strptr(string("MQTT_BASIC")),
		CredentialId:           strptr("device-user"),
		SealedCredentialSecret: []byte("\xd2sealed-form"),
	}

	bytes, err := MarshalUnresolvedEventAs(event, eventtime.WireCompact)
	assert.NoError(t, err)

	got, err := UnmarshalUnresolvedEvent(bytes)
	assert.NoError(t, err)
	assert.NotNil(t, got.CredentialType)
	assert.Equal(t, "MQTT_BASIC", *got.CredentialType)
	assert.NotNil(t, got.CredentialId)
	assert.Equal(t, "device-user", *got.CredentialId)
	assert.Nil(t, got.CredentialSecret, "the plaintext field never crosses the wire")
	assert.NotNil(t, got.SealedCredentialSecret)
	assert.Equal(t, []byte("\xd2sealed-form"), got.SealedCredentialSecret)
}

// The stream keeps what is marshalled, so an event that still carries the secret as the
// device sent it is refused rather than written, and the secret is in no byte of the
// message.
func TestMarshalRefusesAnUnsealedCredentialSecret(t *testing.T) {
	event := &model.UnresolvedEvent{
		Source:           "mqtt1",
		Device:           "TEST-123",
		OccurredTime:     time.Now().UTC(),
		ProcessedTime:    time.Now().UTC(),
		EventType:        eventtype.Location,
		Payload:          &model.UnresolvedLocationsPayload{Entries: []model.UnresolvedLocationEntry{}},
		CredentialType:   strptr("MQTT_BASIC"),
		CredentialId:     strptr("device-user"),
		CredentialSecret: strptr("device-pass-as-sent"),
	}
	bytes, err := MarshalUnresolvedEventAs(event, eventtime.WireCompact)
	assert.ErrorIs(t, err, ErrUnsealedCredentialSecret)
	assert.Nil(t, bytes)
}

// An event with no credential round-trips with nil credential fields (the common
// case under disabled/optional auth).
func TestMarshalRoundTripWithoutCredential(t *testing.T) {
	event := &model.UnresolvedEvent{
		Source:        "mqtt1",
		Device:        "TEST-123",
		OccurredTime:  time.Now().UTC().Truncate(time.Second),
		ProcessedTime: time.Now().UTC().Truncate(time.Second),
		EventType:     eventtype.Location,
		Payload:       &model.UnresolvedLocationsPayload{Entries: []model.UnresolvedLocationEntry{}},
	}

	bytes, err := MarshalUnresolvedEventAs(event, eventtime.WireCompact)
	assert.NoError(t, err)

	got, err := UnmarshalUnresolvedEvent(bytes)
	assert.NoError(t, err)
	assert.Nil(t, got.CredentialType)
	assert.Nil(t, got.CredentialId)
	assert.Nil(t, got.CredentialSecret)
}

// The inbound event's OccurredTime/ProcessedTime must survive the marshal/unmarshal
// round-trip with SUB-SECOND precision. event-sources marshals every device POST here
// on the first pipeline hop, and the persisted event's natural key is
// (tenant, device, event_type, occurred_time): truncating occurred_time to the whole
// second silently collapses two distinct sub-second readings from one device into one
// persisted event. The distinct nanoseconds below fail an Equal() assertion if the
// marshal ever truncated to RFC3339 again — the check that catches the regression.
func TestMarshalUnresolvedEventCarriesSubSecondTimestamps(t *testing.T) {
	occurred := time.Date(2026, 7, 20, 10, 30, 15, 123456789, time.UTC)
	processed := time.Date(2026, 7, 20, 10, 30, 15, 987654321, time.UTC)
	event := &model.UnresolvedEvent{
		Source:        "http1",
		Device:        "TEST-123",
		OccurredTime:  occurred,
		ProcessedTime: processed,
		EventType:     eventtype.Location,
		Payload:       &model.UnresolvedLocationsPayload{Entries: []model.UnresolvedLocationEntry{}},
	}

	bytes, err := MarshalUnresolvedEventAs(event, eventtime.WireCompact)
	assert.NoError(t, err)

	got, err := UnmarshalUnresolvedEvent(bytes)
	assert.NoError(t, err)
	assert.True(t, got.OccurredTime.Equal(occurred), "occurred time sub-second round-trip: got %s want %s", got.OccurredTime, occurred)
	assert.True(t, got.ProcessedTime.Equal(processed), "processed time sub-second round-trip: got %s want %s", got.ProcessedTime, processed)
}

// SF-3: the AuthenticatedTransport marker must survive the marshal/unmarshal round-trip
// (it crosses the messaging hop to the resolver, and a dropped marker would silently
// re-break transport-authenticated presence). proto3 bool defaults false, so an event
// that never set it round-trips false — there is no path where old bytes set it true.
func TestMarshalRoundTripCarriesAuthenticatedTransport(t *testing.T) {
	for _, marked := range []bool{true, false} {
		event := &model.UnresolvedEvent{
			Source:                 "lwm2m",
			Device:                 "lw-dev-1",
			OccurredTime:           time.Now().UTC().Truncate(time.Second),
			ProcessedTime:          time.Now().UTC().Truncate(time.Second),
			EventType:              eventtype.StateChange,
			Payload:                &model.UnresolvedStateChangePayload{State: presence.StateConnected, SessionId: "1"},
			AuthenticatedTransport: marked,
		}
		bytes, err := MarshalUnresolvedEventAs(event, eventtime.WireCompact)
		assert.NoError(t, err)
		got, err := UnmarshalUnresolvedEvent(bytes)
		assert.NoError(t, err)
		assert.Equal(t, marked, got.AuthenticatedTransport)
	}
}

// The stream keeps what is marshalled, so an access token as the device sent it is
// refused rather than written; its sealed form round-trips.
func TestMarshalRefusesAnUnsealedAccessToken(t *testing.T) {
	event := &model.UnresolvedEvent{
		Source:         "http1",
		Device:         "TEST-123",
		OccurredTime:   time.Now().UTC(),
		ProcessedTime:  time.Now().UTC(),
		EventType:      eventtype.Location,
		Payload:        &model.UnresolvedLocationsPayload{Entries: []model.UnresolvedLocationEntry{}},
		CredentialType: strptr("ACCESS_TOKEN"),
		CredentialId:   strptr("tok-as-sent"),
	}
	bytes, err := MarshalUnresolvedEventAs(event, eventtime.WireCompact)
	assert.ErrorIs(t, err, ErrUnsealedCredentialId)
	assert.Nil(t, bytes)

	event.CredentialId = nil
	event.SealedCredentialId = []byte("\xd2sealed-token")
	bytes, err = MarshalUnresolvedEventAs(event, eventtime.WireCompact)
	assert.NoError(t, err)
	got, err := UnmarshalUnresolvedEvent(bytes)
	assert.NoError(t, err)
	assert.Nil(t, got.CredentialId)
	assert.Equal(t, []byte("\xd2sealed-token"), got.SealedCredentialId)
	assert.NotContains(t, string(bytes), "tok-as-sent")
}

// The broker-asserted marker crosses the messaging hop to the resolver; a dropped marker
// would refuse every platform-MQTT event under `required` now that the body carries no
// credential. proto3 bool defaults false, so bytes written before the field existed read
// as unasserted: there is no path where old bytes assert.
func TestMarshalRoundTripCarriesBrokerAsserted(t *testing.T) {
	for _, marked := range []bool{true, false} {
		event := &model.UnresolvedEvent{
			Source:         "gw",
			Device:         "dev-1",
			OccurredTime:   time.Now().UTC().Truncate(time.Second),
			ProcessedTime:  time.Now().UTC().Truncate(time.Second),
			EventType:      eventtype.StateChange,
			Payload:        &model.UnresolvedStateChangePayload{State: presence.StateConnected, SessionId: "1"},
			BrokerAsserted: marked,
		}
		bytes, err := MarshalUnresolvedEventAs(event, eventtime.WireCompact)
		assert.NoError(t, err)
		got, err := UnmarshalUnresolvedEvent(bytes)
		assert.NoError(t, err)
		assert.Equal(t, marked, got.BrokerAsserted)
		assert.False(t, got.AuthenticatedTransport, "the two markers are independent")
	}
}

// The gateway crosses the messaging hop with the assertion: a dropped gateway would turn every
// gateway event into a plain broker-asserted device event for the child named in the subject,
// which is exactly the forgery the assignment check exists to stop. proto3 string defaults empty,
// so bytes written before the field existed read as "no gateway" (a device's own publish).
func TestMarshalRoundTripCarriesTheGateway(t *testing.T) {
	for _, gateway := range []string{"gw-1", ""} {
		event := &model.UnresolvedEvent{
			Source:         "gw",
			Device:         "child-1",
			OccurredTime:   time.Now().UTC().Truncate(time.Second),
			ProcessedTime:  time.Now().UTC().Truncate(time.Second),
			EventType:      eventtype.StateChange,
			Payload:        &model.UnresolvedStateChangePayload{State: presence.StateConnected, SessionId: "1"},
			BrokerAsserted: true,
			Gateway:        gateway,
		}
		bytes, err := MarshalUnresolvedEventAs(event, eventtime.WireCompact)
		assert.NoError(t, err)
		got, err := UnmarshalUnresolvedEvent(bytes)
		assert.NoError(t, err)
		assert.Equal(t, gateway, got.Gateway)
		assert.Equal(t, "child-1", got.Device)
	}
}

// The integration and its sealed token cross the messaging hop: a dropped Integration would turn
// an integration's event into a plain one, trusted (or refused) by the credential policy instead
// of by the integration's own authentication and device type scope. Both wire formats carry them.
func TestMarshalRoundTripCarriesTheIntegration(t *testing.T) {
	for _, format := range []eventtime.WireFormat{eventtime.WireCompact, eventtime.WireLegacy} {
		event := &model.UnresolvedEvent{
			Source:                 "integration:chirp-1",
			Device:                 "dev-1",
			OccurredTime:           time.Now().UTC().Truncate(time.Second),
			ProcessedTime:          time.Now().UTC().Truncate(time.Second),
			EventType:              eventtype.StateChange,
			Payload:                &model.UnresolvedStateChangePayload{State: presence.StateConnected, SessionId: "1"},
			Integration:            "chirp-1",
			SealedIntegrationToken: []byte{1, 2, 3, 4},
		}
		bytes, err := MarshalUnresolvedEventAs(event, format)
		assert.NoError(t, err)
		got, err := UnmarshalUnresolvedEvent(bytes)
		assert.NoError(t, err)
		assert.Equal(t, "chirp-1", got.Integration)
		assert.Equal(t, []byte{1, 2, 3, 4}, got.SealedIntegrationToken)
	}
	plain := &model.UnresolvedEvent{
		Source: "s", Device: "d", OccurredTime: time.Now().UTC().Truncate(time.Second),
		ProcessedTime: time.Now().UTC().Truncate(time.Second), EventType: eventtype.StateChange,
		Payload: &model.UnresolvedStateChangePayload{State: presence.StateConnected, SessionId: "1"},
	}
	bytes, err := MarshalUnresolvedEventAs(plain, eventtime.WireCompact)
	assert.NoError(t, err)
	got, err := UnmarshalUnresolvedEvent(bytes)
	assert.NoError(t, err)
	assert.Empty(t, got.Integration, "an event that is not an integration's reads as none")
	assert.Nil(t, got.SealedIntegrationToken)
}
