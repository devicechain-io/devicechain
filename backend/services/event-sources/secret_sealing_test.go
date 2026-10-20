// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/eventtype"

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
	return &model.UnresolvedEvent{Device: "sensor-001", EventType: eventtype.Measurement,
		CredentialType: &ty, CredentialId: &id, CredentialSecret: &s}
}

// The message the inbound stream stores holds the presented secret only sealed: its bytes
// contain the secret nowhere, and the resolver's key opens it for this credential alone.
func TestThePublishedMessageNeverCarriesThePresentedSecret(t *testing.T) {
	tr := sealingFixture(t)
	const secret = "a-password-as-the-device-sent-it"

	_, msg, ok := inboundEventMessage("gw", "acme", presentingEvent(secret), &model.UnresolvedMeasurementsPayload{}, "acme.1")
	require.True(t, ok)
	require.NotContains(t, string(msg.Value), secret)

	decoded, err := esproto.UnmarshalUnresolvedEvent(msg.Value)
	require.NoError(t, err)
	require.Nil(t, decoded.CredentialSecret)
	require.NotNil(t, decoded.SealedCredentialSecret)
	opened, err := tr.Open("acme", "sensor-001", decoded.SealedCredentialSecret)
	require.NoError(t, err)
	require.Equal(t, secret, opened)
	_, err = tr.Open("other", "sensor-001", decoded.SealedCredentialSecret)
	require.Error(t, err, "sealed for one tenant, it opens for no other")
}

// Fail closed: with no key a secret is never published as sent.
func TestAnEventWithASecretIsDroppedWhenThereIsNoKeyToSealIt(t *testing.T) {
	sealingFixture(t)
	SecretTransit = nil
	_, _, ok := inboundEventMessage("gw", "acme", presentingEvent("pw-long-enough-to-matter"), &model.UnresolvedMeasurementsPayload{}, "acme.1")
	require.False(t, ok)
}

// A secret with no credential id authenticates nothing downstream, so it is discarded:
// nothing is published carrying it, sealed or not.
func TestASecretPresentedWithoutACredentialIdIsDiscarded(t *testing.T) {
	sealingFixture(t)
	ev := presentingEvent("pw-long-enough-to-matter")
	ev.CredentialId = nil
	_, msg, ok := inboundEventMessage("gw", "acme", ev, &model.UnresolvedMeasurementsPayload{}, "acme.1")
	require.True(t, ok)
	require.NotContains(t, string(msg.Value), "pw-long-enough")
	decoded, err := esproto.UnmarshalUnresolvedEvent(msg.Value)
	require.NoError(t, err)
	require.Nil(t, decoded.SealedCredentialSecret)
}

// A credential id presented with no credential type authenticates nothing downstream, so
// it is discarded: nothing is published carrying it, sealed or not. (An id of an unknown
// type is not discarded: it is looked up and matches nothing.)
func TestACredentialIdWithoutATypeIsDiscarded(t *testing.T) {
	sealingFixture(t)
	const token = "tok-typeless-0123456789"
	ev := presentingToken(token)
	ev.CredentialType = nil
	_, msg, ok := inboundEventMessage("http", "acme", ev, &model.UnresolvedMeasurementsPayload{}, "acme.1")
	require.True(t, ok)
	require.NotContains(t, string(msg.Value), token)
	decoded, err := esproto.UnmarshalUnresolvedEvent(msg.Value)
	require.NoError(t, err)
	require.Nil(t, decoded.CredentialId)
	require.Nil(t, decoded.SealedCredentialId)
}

// No usable root key, no start: the service refuses to initialize rather than run with
// nothing to seal a presented password with. Driven through the real initialize callback,
// which is what wires the key.
func TestTheServiceRefusesToInitializeWithoutARootKey(t *testing.T) {
	savedMs, savedCfg, savedTransit, savedGates := Microservice, Configuration, SecretTransit, safetyGates
	t.Cleanup(func() {
		Microservice, Configuration, SecretTransit, safetyGates = savedMs, savedCfg, savedTransit, savedGates
	})
	SecretTransit = nil

	for name, key := range map[string]string{"absent": "", "malformed": "not-base64!!", "wrong length": "AAAA"} {
		// Fresh each time: initialize registers its gauges once per Microservice.
		Microservice = &core.Microservice{InstanceId: "inst-1", FunctionalArea: "event-sources"}
		Microservice.UseMetricsRegistry(prometheus.NewRegistry())
		Microservice.MicroserviceConfigurationRaw = []byte(`{}`)
		Microservice.InstanceConfiguration.Infrastructure.Secrets.RootKey = key
		err := afterMicroserviceInitialized(context.Background())
		require.Error(t, err, name)
		require.Contains(t, err.Error(), "root key", name)
		require.Nil(t, SecretTransit, name)
	}
}

func presentingToken(token string) *model.UnresolvedEvent {
	ty := "ACCESS_TOKEN"
	return &model.UnresolvedEvent{Device: "sensor-001", EventType: eventtype.Measurement,
		CredentialType: &ty, CredentialId: &token}
}

// An ACCESS_TOKEN is the credential itself, so the message the inbound stream stores holds
// it only sealed: its bytes contain the token nowhere, and the resolver's key opens it
// for this tenant and credential type alone.
func TestThePublishedMessageNeverCarriesTheAccessToken(t *testing.T) {
	tr := sealingFixture(t)
	const token = "tok-as-the-device-sent-it-0123456789"

	_, msg, ok := inboundEventMessage("http", "acme", presentingToken(token), &model.UnresolvedMeasurementsPayload{}, "acme.1")
	require.True(t, ok)
	require.NotContains(t, string(msg.Value), token)

	decoded, err := esproto.UnmarshalUnresolvedEvent(msg.Value)
	require.NoError(t, err)
	require.Nil(t, decoded.CredentialId, "the clear id is not on the wire")
	require.NotNil(t, decoded.SealedCredentialId)
	opened, err := tr.OpenToken("acme", "ACCESS_TOKEN", decoded.SealedCredentialId)
	require.NoError(t, err)
	require.Equal(t, token, opened)
	_, err = tr.OpenToken("other", "ACCESS_TOKEN", decoded.SealedCredentialId)
	require.Error(t, err, "sealed for one tenant, it opens for no other")
	_, err = tr.OpenToken("acme", "MQTT_BASIC", decoded.SealedCredentialId)
	require.Error(t, err, "sealed for one credential type, it opens for no other")
	_, err = tr.Open("acme", token, decoded.SealedCredentialId)
	require.Error(t, err, "a sealed token is not a sealed password")
}

// Fail closed: with no key a token is never published as sent.
func TestAnEventWithAnAccessTokenIsDroppedWhenThereIsNoKeyToSealIt(t *testing.T) {
	sealingFixture(t)
	SecretTransit = nil
	_, _, ok := inboundEventMessage("http", "acme", presentingToken("tok-0123456789"), &model.UnresolvedMeasurementsPayload{}, "acme.1")
	require.False(t, ok)
}

// A credential type that is not a bearer type keeps its clear id (an MQTT username is
// not secret), and a type spelled in another case is still sealed.
func TestOnlyBearerCredentialIdsAreSealed(t *testing.T) {
	sealingFixture(t)
	_, msg, ok := inboundEventMessage("gw", "acme", presentingEvent("pw-long-enough-to-matter"), &model.UnresolvedMeasurementsPayload{}, "acme.1")
	require.True(t, ok)
	decoded, err := esproto.UnmarshalUnresolvedEvent(msg.Value)
	require.NoError(t, err)
	require.Equal(t, "sensor-001", *decoded.CredentialId)
	require.Nil(t, decoded.SealedCredentialId)

	lower := presentingToken("tok-lowercase-0123456789")
	ty := "access_token"
	lower.CredentialType = &ty
	_, msg, ok = inboundEventMessage("http", "acme", lower, &model.UnresolvedMeasurementsPayload{}, "acme.1")
	require.True(t, ok)
	require.NotContains(t, string(msg.Value), "tok-lowercase")
}
