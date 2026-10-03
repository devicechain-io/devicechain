// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/processor"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// externalSourcesConfig is two sources on an operator's own broker. The host is not the
// platform broker's, so both take the external (paho) branch of buildEventSources.
func externalSourcesConfig() *config.EventSourcesConfiguration {
	source := func(id string) config.EventSource {
		return config.EventSource{
			Id:            id,
			Type:          processor.TYPE_MQTT,
			Configuration: map[string]string{"host": "broker.example", "port": "1883", "topic": "+/#"},
			Decoder:       config.EventDecoder{Type: processor.DECODER_TYPE_JSON},
		}
	}
	return &config.EventSourcesConfiguration{
		EventSources: []config.EventSource{source("ext-a"), source("ext-b")},
	}
}

// buildAsPod builds the event sources exactly as main does, in a pod named pod of
// instance inst-1. Each pod is its own process, so each gets its own metrics registry.
func buildAsPod(t *testing.T, pod string) []core.LifecycleComponent {
	t.Helper()
	t.Setenv("HOSTNAME", pod)
	Microservice = &core.Microservice{InstanceId: "inst-1"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Configuration = externalSourcesConfig()
	buildTestRateLimiters(t)
	require.NoError(t, buildEventSources())
	require.Len(t, EventSources, 2)
	return EventSources
}

// clientIDOf reads the MQTT client id a built source would connect with.
//
// The first arm reads the id from paho's own options: MqttEventSource.ClientID returns the
// id in the options it hands paho, and an owned source's ClientID is that of a term source
// its factory built, so a source that connected under any other id fails here (the owned
// source's constructor refuses it).
//
// The second arm exists so that this file compiles and RUNS against a tree where the
// source has no ClientID method, and reads the id from paho's own options instead: that
// is how this test was shown to fail on the code it replaced, where every source on every
// pod connected as the literal "devicechain".
func clientIDOf(t *testing.T, src core.LifecycleComponent) string {
	t.Helper()
	switch s := src.(type) {
	case interface{ ClientID() string }:
		return s.ClientID()
	case *processor.MqttEventSource:
		require.NoError(t, s.Initialize(context.Background()))
		opts := s.Client.OptionsReader()
		return opts.ClientID()
	default:
		t.Fatalf("built a %T, which is not an external MQTT source", src)
		return ""
	}
}

// 🔴 THE DEFECT THIS PINS. Every external-broker source connected with the client id
// "devicechain", whatever instance, source or pod it belonged to. A broker keeps one session
// per client id and closes the older connection when a second arrives, so two pods (which
// every rolling update runs side by side), two sources on one broker, or two instances
// reading one broker took the session from each other in a loop, and lost what arrived
// while each reconnected.
//
// It builds the sources through main's own wiring, once as pod-a and once as pod-b, and
// requires each source on each pod to connect under an id naming the instance, the source
// and the pod.
func TestEachPodsExternalSourceHasItsOwnClientID(t *testing.T) {
	savedConfig, savedSources, savedMs := Configuration, EventSources, Microservice
	t.Cleanup(func() { Configuration, EventSources, Microservice = savedConfig, savedSources, savedMs })

	podA := buildAsPod(t, "pod-a")
	aOnA, bOnA := clientIDOf(t, podA[0]), clientIDOf(t, podA[1])
	podB := buildAsPod(t, "pod-b")
	aOnB, bOnB := clientIDOf(t, podB[0]), clientIDOf(t, podB[1])

	assert.Equal(t, "devicechain:inst-1:ext-a:pod-a", aOnA)
	assert.Equal(t, "devicechain:inst-1:ext-b:pod-a", bOnA)
	assert.Equal(t, "devicechain:inst-1:ext-a:pod-b", aOnB)
	assert.Equal(t, "devicechain:inst-1:ext-b:pod-b", bOnB)

	seen := map[string]bool{}
	for _, id := range []string{aOnA, bOnA, aOnB, bOnB} {
		seen[id] = true
	}
	assert.Len(t, seen, 4, "two sources on two pods must connect under four distinct ids, got %v", seen)
}
