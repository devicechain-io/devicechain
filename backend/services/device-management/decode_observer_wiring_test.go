// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/devicechain-io/dc-device-management/processor"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// builtInboundProcessor builds the inbound processor the way the service does, with a decode
// accountant in place (the service refuses to build one without it).
func builtInboundProcessor(t *testing.T) *processor.InboundEventsProcessor {
	t.Helper()
	saved := DecodeAccountant
	t.Cleanup(func() { DecodeAccountant = saved })
	DecodeAccountant = processor.NewDecodeAccountant(nil, "test", nil)
	return newInboundEventsProcessor()
}

func decodeWiringFixture(t *testing.T) {
	t.Helper()
	Microservice = &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.MicroserviceConfigurationRaw = []byte(`{}`)
	require.NoError(t, parseConfiguration())
	ResolveMetrics = processor.NewResolveMetrics(Microservice)
	DeadLetters = deadletter.NewProducer(Microservice)
	savedTransit := SecretTransit
	t.Cleanup(func() { SecretTransit = savedTransit })
	SecretTransit = testTransit(t)
}

// The processor's resolvers tell the SERVICE's accountant their decode outcomes. A processor test
// builds its own processor, so a main.go that stopped passing the observer would leave every one
// green while a broken decoder wrote a failed-events record for every uplink it saw.
func TestTheInboundProcessorIsBuiltWithADecodeObserver(t *testing.T) {
	decodeWiringFixture(t)
	saved := DecodeAccountant
	t.Cleanup(func() { DecodeAccountant = saved })
	DecodeAccountant = processor.NewDecodeAccountant(nil, "test", nil)

	require.Same(t, DecodeAccountant, newInboundEventsProcessor().DecodeObserver())
}

// And a service with no accountant refuses to build the processor at all, rather than run decoders
// without their budget.
func TestTheInboundProcessorRefusesToBeBuiltWithoutADecodeAccountant(t *testing.T) {
	decodeWiringFixture(t)
	saved := DecodeAccountant
	t.Cleanup(func() { DecodeAccountant = saved })
	DecodeAccountant = nil

	require.Panics(t, func() { newInboundEventsProcessor() })
}
