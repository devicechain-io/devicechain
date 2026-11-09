// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/processor"
	"github.com/devicechain-io/dc-microservice/core"
)

// THE MODE SWITCH, through the code that wires it. resolveIngestMetering is tested on its own; this
// drives buildEventSources, as the service does, in each mode, with a source pointed at the platform
// broker (the gateway source over the capture stream, and its twin over the MQTT message store), and
// asks what was built.
//
//   - legacy (the default): no lag-credited gate exists, neither gateway source meters itself, and the
//     composed gate is the existing one, which refuses OriginPreMetered loudly;
//   - lagCredit: one gate, shared by both sources on their own streams, and the composed gate passes a
//     pre-metered message (the lifecycle refusal stays in front of it).
func buildInMeteringMode(t *testing.T, mode IngestMetering) {
	t.Helper()
	savedConfig, savedSources, savedMs := Configuration, EventSources, Microservice
	savedGateway, savedGatewayId, savedStore := GatewaySource, GatewaySourceId, StoreSource
	savedMode, savedLC := IngestMeteringMode, LagCredit
	savedBase, savedPrio, savedUnresolved := ingestBaseCeiling, ingestShedPriority, ingestUnresolved
	t.Cleanup(func() {
		Configuration, EventSources, Microservice = savedConfig, savedSources, savedMs
		GatewaySource, GatewaySourceId, StoreSource = savedGateway, savedGatewayId, savedStore
		IngestMeteringMode, LagCredit = savedMode, savedLC
		ingestBaseCeiling, ingestShedPriority, ingestUnresolved = savedBase, savedPrio, savedUnresolved
	})
	Microservice = &core.Microservice{InstanceId: "inst-1", FunctionalArea: "event-sources"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.InstanceConfiguration.Infrastructure.Nats.Hostname = "nats.platform"
	Microservice.InstanceConfiguration.Infrastructure.Nats.Port = 4222
	Configuration = &config.EventSourcesConfiguration{
		EventSources: []config.EventSource{{
			Id:            "mqtt1",
			Type:          processor.TYPE_MQTT,
			Configuration: map[string]string{"host": "nats.platform", "port": "1883"},
			Decoder:       config.EventDecoder{Type: processor.DECODER_TYPE_JSON},
		}},
	}
	Configuration.ApplyDefaults()
	GatewaySource, StoreSource, LagCredit = nil, nil, nil
	IngestMeteringMode = mode
	buildTestRateLimiters(t)
	require.NoError(t, buildEventSources())
	require.NotNil(t, GatewaySource, "a source pointed at the platform broker is the gateway source")
	require.NotNil(t, StoreSource, "with its twin over the MQTT message store")
}

func TestTheDefaultModeWiresNoLagCreditedGate(t *testing.T) {
	buildInMeteringMode(t, IngestMeteringLegacy)
	require.Nil(t, LagCredit, "legacy builds no lag-credited gate")
	lc, _ := GatewaySource.LagCredit()
	require.Nil(t, lc, "the capture source asks the composed gate for every decision")
	lc, _ = StoreSource.LagCredit()
	require.Nil(t, lc, "and so does the message-store source")
	require.Panics(t, func() { ingestGate("mqtt1", "acme", time.Time{}, false, processor.OriginPreMetered) },
		"the composed gate is the existing one, which refuses a pre-metered message loudly")
}

func TestTheLagCreditModeWiresOneGateIntoBothGatewaySources(t *testing.T) {
	buildInMeteringMode(t, IngestMeteringLagCredit)
	require.NotNil(t, LagCredit, "lagCredit builds the gate")
	lc, stream := GatewaySource.LagCredit()
	require.Same(t, LagCredit, lc, "the capture source meters itself in it")
	require.Equal(t, processor.StreamCapture, stream)
	lc, stream = StoreSource.LagCredit()
	require.Same(t, LagCredit, lc, "the message-store source shares it: one ceiling bucket per tenant")
	require.Equal(t, processor.StreamStore, stream)
	require.True(t, ingestGate("mqtt1", "acme", time.Time{}, false, processor.OriginPreMetered),
		"the composed gate passes a message its source metered itself")
}
