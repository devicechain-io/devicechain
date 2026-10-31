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

// Every consumer that letters and acks a message at its last delivery is built with a store
// hold. A processor test builds its own consumer, so a main.go that stopped passing one would
// leave every processor test green while a database outage lost acknowledged data again.
func TestTheInboundProcessorIsBuiltWithAStoreHold(t *testing.T) {
	Microservice = &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.MicroserviceConfigurationRaw = []byte(`{}`)
	require.NoError(t, parseConfiguration())
	ResolveMetrics = processor.NewResolveMetrics(Microservice)
	DeadLetters = deadletter.NewProducer(Microservice)
	savedTransit := SecretTransit
	t.Cleanup(func() { SecretTransit = savedTransit })
	SecretTransit = testTransit(t)

	require.NotNil(t, newInboundEventsProcessor().StoreHold())
}
