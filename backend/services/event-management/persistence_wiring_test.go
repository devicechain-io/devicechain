// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-event-management/config"
	"github.com/devicechain-io/dc-event-management/processor"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// The persistence settings in the service's configuration document reach the writers the
// processor starts. Nothing else runs this wiring: a processor test builds the processor
// itself, so a service that stopped passing its configuration would run the defaults while
// every other test stayed green.
func TestConfiguredPersistenceReachesTheWriters(t *testing.T) {
	savedMs, savedCfg, savedMetrics := Microservice, Configuration, PersistMetrics
	t.Cleanup(func() { Microservice, Configuration, PersistMetrics = savedMs, savedCfg, savedMetrics })

	Microservice = &core.Microservice{InstanceId: "test", FunctionalArea: "event-management"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.MicroserviceConfigurationRaw = []byte(`{"persistence":{"writers":3,"maxBatch":4,"lingerMillis":9}}`)
	require.NoError(t, parseConfiguration())
	buildMetrics()

	proc := newEventPersistenceProcessor(nil, nil)
	require.NoError(t, proc.Initialize(context.Background()))
	t.Cleanup(func() { _ = proc.ExecuteStop(context.Background()) })

	want := config.PersistenceConfiguration{Writers: 3, MaxBatch: 4, LingerMillis: 9}
	writers := proc.Writers()
	require.Len(t, writers, want.Writers, "writers started")
	for i, w := range writers {
		require.Equal(t, processor.WriterSettings{MaxBatch: want.MaxBatch, Linger: want.Linger()}, w, "writer %d", i)
	}
}
