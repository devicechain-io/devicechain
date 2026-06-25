// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/stretchr/testify/assert"
)

func strptr(s string) *string { return &s }

// The presented credential must survive the marshal/unmarshal round-trip so it
// reaches the resolver across the messaging hop (ADR-014).
func TestMarshalRoundTripCarriesCredential(t *testing.T) {
	event := &model.UnresolvedEvent{
		Source:           "mqtt1",
		Device:           "TEST-123",
		OccurredTime:     time.Now().UTC().Truncate(time.Second),
		ProcessedTime:    time.Now().UTC().Truncate(time.Second),
		EventType:        model.Location,
		Payload:          &model.UnresolvedLocationsPayload{Entries: []model.UnresolvedLocationEntry{}},
		CredentialType:   strptr(string("MQTT_BASIC")),
		CredentialId:     strptr("device-user"),
		CredentialSecret: strptr("device-pass"),
	}

	bytes, err := MarshalUnresolvedEvent(event)
	assert.NoError(t, err)

	got, err := UnmarshalUnresolvedEvent(bytes)
	assert.NoError(t, err)
	assert.NotNil(t, got.CredentialType)
	assert.Equal(t, "MQTT_BASIC", *got.CredentialType)
	assert.NotNil(t, got.CredentialId)
	assert.Equal(t, "device-user", *got.CredentialId)
	assert.NotNil(t, got.CredentialSecret)
	assert.Equal(t, "device-pass", *got.CredentialSecret)
}

// An event with no credential round-trips with nil credential fields (the common
// case under disabled/optional auth).
func TestMarshalRoundTripWithoutCredential(t *testing.T) {
	event := &model.UnresolvedEvent{
		Source:        "mqtt1",
		Device:        "TEST-123",
		OccurredTime:  time.Now().UTC().Truncate(time.Second),
		ProcessedTime: time.Now().UTC().Truncate(time.Second),
		EventType:     model.Location,
		Payload:       &model.UnresolvedLocationsPayload{Entries: []model.UnresolvedLocationEntry{}},
	}

	bytes, err := MarshalUnresolvedEvent(event)
	assert.NoError(t, err)

	got, err := UnmarshalUnresolvedEvent(bytes)
	assert.NoError(t, err)
	assert.Nil(t, got.CredentialType)
	assert.Nil(t, got.CredentialId)
	assert.Nil(t, got.CredentialSecret)
}
