// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"testing"

	"github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func sealingFixture(t *testing.T) *credential.SecretTransit {
	t.Helper()
	savedMs, savedTransit := Microservice, SecretTransit
	t.Cleanup(func() { Microservice, SecretTransit = savedMs, savedTransit })
	Microservice = &core.Microservice{InstanceId: "inst-1", FunctionalArea: "event-sources"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	initializeMetrics()
	tr, err := credential.DeriveSecretTransit(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	SecretTransit = tr
	return tr
}

func presentingEvent(secret string) *model.UnresolvedEvent {
	s, id, ty := secret, "sensor-001", "MQTT_BASIC"
	return &model.UnresolvedEvent{Device: "sensor-001", EventType: model.Measurement,
		CredentialType: &ty, CredentialId: &id, CredentialSecret: &s}
}

// The message the inbound stream stores holds the presented secret only sealed: its bytes
// contain the secret nowhere, and the resolver's key opens it for this credential alone.
func TestThePublishedMessageNeverCarriesThePresentedSecret(t *testing.T) {
	tr := sealingFixture(t)
	const secret = "a-password-as-the-device-sent-it"

	_, msg, ok := inboundEventMessage("gw", "acme", presentingEvent(secret), &model.UnresolvedMeasurementsPayload{}, 1)
	require.True(t, ok)
	require.NotContains(t, string(msg.Value), secret)

	decoded, err := esproto.UnmarshalUnresolvedEvent(msg.Value)
	require.NoError(t, err)
	require.Nil(t, decoded.CredentialSecret)
	require.NotNil(t, decoded.SealedCredentialSecret)
	opened, err := tr.Open("acme", "sensor-001", *decoded.SealedCredentialSecret)
	require.NoError(t, err)
	require.Equal(t, secret, opened)
	_, err = tr.Open("other", "sensor-001", *decoded.SealedCredentialSecret)
	require.Error(t, err, "sealed for one tenant, it opens for no other")
}

// Fail closed: with no key a secret is never published as sent.
func TestAnEventWithASecretIsDroppedWhenThereIsNoKeyToSealIt(t *testing.T) {
	sealingFixture(t)
	SecretTransit = nil
	_, _, ok := inboundEventMessage("gw", "acme", presentingEvent("pw-long-enough-to-matter"), &model.UnresolvedMeasurementsPayload{}, 1)
	require.False(t, ok)
}

// A secret with no credential id authenticates nothing downstream, so it is discarded:
// nothing is published carrying it, sealed or not.
func TestASecretPresentedWithoutACredentialIdIsDiscarded(t *testing.T) {
	sealingFixture(t)
	ev := presentingEvent("pw-long-enough-to-matter")
	ev.CredentialId = nil
	_, msg, ok := inboundEventMessage("gw", "acme", ev, &model.UnresolvedMeasurementsPayload{}, 1)
	require.True(t, ok)
	require.NotContains(t, string(msg.Value), "pw-long-enough")
	decoded, err := esproto.UnmarshalUnresolvedEvent(msg.Value)
	require.NoError(t, err)
	require.Nil(t, decoded.SealedCredentialSecret)
}
