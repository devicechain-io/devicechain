// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/processor"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// platformAndExternalConfig is one source on the platform broker (its host is the NATS
// hostname, so buildEventSources makes it the gateway pair) and one on an operator's own broker.
func platformAndExternalConfig(bodyCredential string) *config.EventSourcesConfiguration {
	mqtt := func(id, host string) config.EventSource {
		return config.EventSource{
			Id:            id,
			Type:          processor.TYPE_MQTT,
			Configuration: map[string]string{"host": host, "port": "1883", "topic": "+/#"},
			Decoder:       config.EventDecoder{Type: processor.DECODER_TYPE_JSON},
		}
	}
	cfg := &config.EventSourcesConfiguration{
		EventSources: []config.EventSource{mqtt("platform", "nats.platform"), mqtt("ext", "broker.example")},
	}
	cfg.Capture.BodyCredential = bodyCredential
	cfg.ApplyDefaults()
	return cfg
}

func buildStripPod(t *testing.T, bodyCredential string) {
	t.Helper()
	savedConfig, savedSources, savedMs := Configuration, EventSources, Microservice
	savedGateway, savedStore, savedId := GatewaySource, StoreSource, GatewaySourceId
	savedCounter, savedGauge := CredentialInMqttBodyCounter, BodyCredentialStripGauge
	t.Cleanup(func() {
		Configuration, EventSources, Microservice = savedConfig, savedSources, savedMs
		GatewaySource, StoreSource, GatewaySourceId = savedGateway, savedStore, savedId
		CredentialInMqttBodyCounter, BodyCredentialStripGauge = savedCounter, savedGauge
	})
	t.Setenv("HOSTNAME", "pod-a")
	Microservice = &core.Microservice{InstanceId: "inst-1"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.InstanceConfiguration.Infrastructure.Nats.Hostname = "nats.platform"
	initializeMetrics()
	GatewaySource, StoreSource = nil, nil
	Configuration = platformAndExternalConfig(bodyCredential)
	buildTestRateLimiters(t)
	require.NoError(t, buildEventSources())
}

// THE WIRING, through main's constructor. capture.bodyCredential: strip must reach BOTH
// sources over the platform broker (capture and the message-store backup, which also asserts),
// and move the gauge; the default must reach neither. A wire missed here would turn a
// documented upgrade bridge back into a data gap, silently, so it fails this test.
func TestBodyCredentialStripIsWiredOntoBothPlatformBrokerSources(t *testing.T) {
	buildStripPod(t, config.BodyCredentialStrip)
	require.NotNil(t, GatewaySource)
	require.NotNil(t, StoreSource)
	require.True(t, GatewaySource.StrippingBodyCredential(), "the capture source must strip")
	require.True(t, StoreSource.StrippingBodyCredential(), "the message-store source must strip")
	require.Equal(t, 1.0, testutil.ToFloat64(BodyCredentialStripGauge))
}

func TestBodyCredentialIsRefusedByDefaultAndTheGaugeSaysSo(t *testing.T) {
	buildStripPod(t, "")
	require.NotNil(t, GatewaySource)
	require.False(t, GatewaySource.StrippingBodyCredential())
	require.False(t, StoreSource.StrippingBodyCredential())
	require.Equal(t, 0.0, testutil.ToFloat64(BodyCredentialStripGauge))
}

// A strip counts on the SAME series a refusal does, by source, never by tenant.
func TestAStripCountsOnTheRefusalCounter(t *testing.T) {
	buildStripPod(t, config.BodyCredentialStrip)
	before := testutil.ToFloat64(CredentialInMqttBodyCounter.WithLabelValues("platform"))
	onBodyCredentialStripped(newBodyCredentialWarner())("platform", "acme", "sensor-001", true)
	require.Equal(t, before+1, testutil.ToFloat64(CredentialInMqttBodyCounter.WithLabelValues("platform")))
}

func TestTheStripWarningIsOncePerDevicePerHourAndBounded(t *testing.T) {
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	w := newBodyCredentialWarner()
	w.now = func() time.Time { return now }
	w.cap = 3

	require.True(t, w.shouldWarn("acme", "d1"))
	require.False(t, w.shouldWarn("acme", "d1"), "the same device is quiet inside the window")
	require.True(t, w.shouldWarn("other", "d1"), "the same token in another tenant is another device")
	require.True(t, w.shouldWarn("acme", "d2"))
	require.False(t, w.shouldWarn("acme", "d3"), "past the cap a device is counted, not named")

	now = now.Add(time.Hour)
	require.True(t, w.shouldWarn("acme", "d1"), "the window ends and the set is cleared")
	require.True(t, w.shouldWarn("acme", "d3"))
}
