// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// alwaysOwns is the ownership gate of a source a test runs on its own, outside any lease.
func alwaysOwns() bool { return true }

func TestExternalMqttClientIDNamesInstanceSourceAndReplica(t *testing.T) {
	base, err := ExternalMqttClientID("inst-1", "mqtt1", "pod-a")
	require.NoError(t, err)
	assert.Equal(t, "devicechain:inst-1:mqtt1:pod-a", base)

	for _, tc := range []struct {
		name                      string
		instance, source, replica string
		want                      string
	}{
		{"another instance", "inst-2", "mqtt1", "pod-a", "devicechain:inst-2:mqtt1:pod-a"},
		{"another source", "inst-1", "mqtt2", "pod-a", "devicechain:inst-1:mqtt2:pod-a"},
		{"another pod", "inst-1", "mqtt1", "pod-b", "devicechain:inst-1:mqtt1:pod-b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExternalMqttClientID(tc.instance, tc.source, tc.replica)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.NotEqual(t, base, got)
		})
	}
}

// nats-server refuses a client id carrying ".", "*", ">" or whitespace with CONNACK
// "identifier rejected" (its isValidName). A source whose id carried one would fail at
// start on every NATS broker; the broker test proves the composed id end to end.
func TestExternalMqttClientIDIsOneANatsServerAccepts(t *testing.T) {
	for _, source := range []string{"mqtt1", "a.b", "a*b", "a>b", "a b", "acme:line3"} {
		id, err := ExternalMqttClientID("inst-1", source, "pod-a")
		require.NoError(t, err)
		assert.False(t, strings.ContainsAny(id, ". \t\r\n\f*>"), "source %q gave %q", source, id)
	}
}

func TestExternalMqttClientIDRefusesAnEmptyPart(t *testing.T) {
	for _, tc := range []struct{ instance, source, replica string }{
		{"", "mqtt1", "pod-a"}, {"inst-1", "", "pod-a"}, {"inst-1", "mqtt1", ""},
	} {
		_, err := ExternalMqttClientID(tc.instance, tc.source, tc.replica)
		assert.ErrorIs(t, err, errEmptyClientIDPart, "%+v", tc)
	}
}

// The separator must not occur inside the instance or the replica, or two different
// inputs could compose one id.
func TestExternalMqttClientIDRefusesASeparatorInTheInstanceOrReplica(t *testing.T) {
	_, err := ExternalMqttClientID("inst:1", "mqtt1", "pod-a")
	assert.Error(t, err)
	_, err = ExternalMqttClientID("inst-1", "mqtt1", "pod:a")
	assert.Error(t, err)
}

// A source id is free text in the configuration. Reduced to the token grammar, "a:b" and
// "a-b" would meet; the hash suffix keeps them apart, and is added only when the id needed
// reducing.
func TestSourceIdsThatReduceAlikeKeepDistinctClientIDs(t *testing.T) {
	plain, err := ExternalMqttClientID("inst-1", "a-b", "pod-a")
	require.NoError(t, err)
	assert.Equal(t, "devicechain:inst-1:a-b:pod-a", plain)

	sum := sha256.Sum256([]byte("a:b"))
	reduced, err := ExternalMqttClientID("inst-1", "a:b", "pod-a")
	require.NoError(t, err)
	assert.Equal(t, "devicechain:inst-1:a-b-"+hex.EncodeToString(sum[:])[:8]+":pod-a", reduced)
	assert.NotEqual(t, plain, reduced)
}

// The id the source reports is the id paho connects with.
func TestMqttEventSourceConnectsWithItsClientID(t *testing.T) {
	es, err := NewMqttEventSource("ext", "cid-under-test", map[string]string{"host": "h", "port": "1883", "topic": "t"},
		nil, "", "", NewJsonDecoder(map[string]string{}),
		func(string, []byte) {},
		func(string, string, *model.UnresolvedEvent, interface{}, uint64) error { return nil },
		func(string, string, []byte, error) error { return nil },
		nil, admitAllReadings, admitAll, alwaysOwns, func(error) {})
	require.NoError(t, err)
	require.NoError(t, es.Initialize(context.Background()))
	opts := es.Client.OptionsReader()
	assert.Equal(t, "cid-under-test", opts.ClientID())
	assert.Equal(t, "cid-under-test", es.ClientID())
}

func TestNewMqttEventSourceRefusesAnEmptyClientIDOrNoOwnerGate(t *testing.T) {
	build := func(clientID string, owns func() bool) error {
		_, err := NewMqttEventSource("ext", clientID, map[string]string{"host": "h", "port": "1883", "topic": "t"},
			nil, "", "", NewJsonDecoder(map[string]string{}),
			func(string, []byte) {},
			func(string, string, *model.UnresolvedEvent, interface{}, uint64) error { return nil },
			func(string, string, []byte, error) error { return nil },
			nil, admitAllReadings, admitAll, owns, func(error) {})
		return err
	}
	assert.ErrorIs(t, build("", alwaysOwns), errNoClientID)
	assert.ErrorIs(t, build("cid", nil), errNoOwnerGate)
	assert.NoError(t, build("cid", alwaysOwns))
}

// A message delivered to a pod that no longer owns the source is dropped before anything
// is metered, counted or queued.
func TestAMessageDeliveredAfterTheSourceWasLostIsDropped(t *testing.T) {
	metered, received := 0, 0
	owned := true
	es, err := NewMqttEventSource("ext", "cid", map[string]string{"host": "h", "port": "1883", "topic": "t"},
		nil, "", "", NewJsonDecoder(map[string]string{}),
		func(string, []byte) { received++ },
		func(string, string, *model.UnresolvedEvent, interface{}, uint64) error { return nil },
		func(string, string, []byte, error) error { return nil },
		func(string, string, time.Time, bool, Origin) bool { metered++; return true },
		admitAllReadings, admitAll, func() bool { return owned }, func(error) {})
	require.NoError(t, err)
	es.messages = make(chan rawMessage, 8)

	msg := &fakeMqttMessage{topic: "inst-1/acme/devices/d1/events", payload: []byte(`{"device":"d1"}`)}
	es.onMessage(nil, msg)
	require.Len(t, es.messages, 1, "an owned source queues the message")

	owned = false
	es.onMessage(nil, msg)
	assert.Len(t, es.messages, 1, "a source that has lost ownership queued a message")
	assert.Equal(t, 1, metered, "a dropped message was metered against its tenant")
	assert.Equal(t, 1, received, "a dropped message was counted as received")
}
