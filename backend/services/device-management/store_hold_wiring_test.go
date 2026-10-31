// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-device-management/processor"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/rdb"
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

// The raise-alarm and device-reports consumers this service builds carry a store hold, for the
// reason on the test above.
func TestTheStreamConsumersAreBuiltWithAStoreHold(t *testing.T) {
	savedMs, savedApi, savedRdb, savedMetrics, savedReports, savedCfg :=
		Microservice, Api, RdbManager, RaiseAlarmMetrics, DeviceReportsMetrics, Configuration
	t.Cleanup(func() {
		Microservice, Api, RdbManager, RaiseAlarmMetrics, DeviceReportsMetrics, Configuration =
			savedMs, savedApi, savedRdb, savedMetrics, savedReports, savedCfg
	})
	Microservice = &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	RdbManager = &rdb.RdbManager{}
	Api = model.NewApi(RdbManager)
	Configuration = config.NewDeviceManagementConfiguration()

	require.NotNil(t, newRaiseAlarmConsumer(nil, nil).Hold, "raise-alarm consumer has no store hold")
	require.NotNil(t, newDeviceReportsConsumer(nil, nil).Hold, "device-reports consumer has no store hold")
}
