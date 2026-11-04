// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/processor"
	"github.com/devicechain-io/dc-microservice/core"
)

// A message shed from an MQTT source was acknowledged to its device (PUBACK) before this service
// read it, so it is lost; a message shed from an HTTP source was answered 429 and is retried.
// MqttIngestShedAfterAcknowledgement alerts on the first, and only the first may feed it.
func TestOnlyMqttShedsCountAsShedAfterAcknowledgement(t *testing.T) {
	savedMs, savedConfig, savedSet := Microservice, Configuration, acknowledgedShedSources
	t.Cleanup(func() { Microservice, Configuration, acknowledgedShedSources = savedMs, savedConfig, savedSet })
	Microservice = &core.Microservice{InstanceId: "inst-1", FunctionalArea: "event-sources"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Configuration = &config.EventSourcesConfiguration{EventSources: []config.EventSource{
		{Id: "http1", Type: processor.TYPE_HTTP}, {Id: "mqtt1", Type: processor.TYPE_MQTT},
	}}
	initializeMetrics()

	// Exported at zero for the MQTT source, so the first shed is an increase and not a birth.
	require.Equal(t, 1, testutil.CollectAndCount(ShedAcknowledgedCounter))
	require.Zero(t, testutil.ToFloat64(ShedAcknowledgedCounter.WithLabelValues("mqtt1")))

	// The message stage and the readings stage each count a message once.
	onRateShed("mqtt1", "acme")
	onReadingShed("mqtt1", "acme", 7)
	require.Equal(t, 2.0, testutil.ToFloat64(ShedAcknowledgedCounter.WithLabelValues("mqtt1")),
		"a message shed from an MQTT source was already acknowledged and must be counted, once per message")

	onRateShed("http1", "acme")
	onReadingShed("http1", "acme", 7)
	require.Equal(t, 1, testutil.CollectAndCount(ShedAcknowledgedCounter),
		"an HTTP shed is answered 429 and retried; counting it would make the alert page for a loss that did not happen")
}
