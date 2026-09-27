// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/devicechain-io/dc-device-management/processor"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// resolution.workers in the service's configuration document reaches the processor it builds.
// Nothing else runs this wiring: a processor test builds the processor itself, so a service
// that stopped passing the setting would run the default while every other test stayed green.
//
// It builds the resolve metrics directly rather than through buildMetrics, which reads the
// core/service-built dead-letter producer this test has no service to build.
func TestConfiguredResolutionWorkersReachTheProcessor(t *testing.T) {
	savedMs, savedCfg, savedMetrics := Microservice, Configuration, ResolveMetrics
	t.Cleanup(func() { Microservice, Configuration, ResolveMetrics = savedMs, savedCfg, savedMetrics })

	for _, tc := range []struct {
		doc  string
		want int
	}{
		{`{"resolution":{"workers":3}}`, 3},
		// Counterweight: with nothing set, the processor gets the default, so a wiring that
		// always passed 3 (or anything else fixed) cannot pass both.
		{`{}`, 10},
	} {
		t.Run(tc.doc, func(t *testing.T) {
			Microservice = &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
			Microservice.UseMetricsRegistry(prometheus.NewRegistry())
			Microservice.MicroserviceConfigurationRaw = []byte(tc.doc)
			require.NoError(t, parseConfiguration())
			ResolveMetrics = processor.NewResolveMetrics(Microservice)

			require.Equal(t, tc.want, newInboundEventsProcessor(nil).Resolvers())
		})
	}
}
