// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/processor"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// The persistence settings in the service's configuration document reach the writers the
// processor starts, and so do the defaults when the document sets none. Nothing else runs
// this wiring: a processor test builds the processor itself, so a service that stopped
// passing its configuration would run the defaults while every other test stayed green.
// Each row states the settings as literals, never through config.PersistenceConfiguration,
// so a default changed in one place cannot agree with itself.
func TestConfiguredPersistenceReachesTheWriters(t *testing.T) {
	for _, tc := range []struct {
		name, doc   string
		wantWriters int
		want        processor.WriterSettings
	}{
		{"configured", `{"persistence":{"writers":3,"maxBatch":4,"lingerMillis":9}}`, 3,
			processor.WriterSettings{MaxBatch: 4, Linger: 9 * time.Millisecond}},
		{"defaults", `{}`, 10, processor.WriterSettings{MaxBatch: 64, Linger: 10 * time.Millisecond}},
		{"linger turned off", `{"persistence":{"lingerMillis":0}}`, 10, processor.WriterSettings{MaxBatch: 64, Linger: 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			savedMs, savedCfg, savedMetrics := Microservice, Configuration, PersistMetrics
			t.Cleanup(func() { Microservice, Configuration, PersistMetrics = savedMs, savedCfg, savedMetrics })

			Microservice = &core.Microservice{InstanceId: "test", FunctionalArea: "event-management"}
			Microservice.UseMetricsRegistry(prometheus.NewRegistry())
			Microservice.MicroserviceConfigurationRaw = []byte(tc.doc)
			require.NoError(t, parseConfiguration())
			buildMetrics()

			proc := newEventPersistenceProcessor(nil, nil)
			require.NoError(t, proc.Initialize(context.Background()))
			t.Cleanup(func() { _ = proc.ExecuteStop(context.Background()) })

			writers := proc.Writers()
			require.Len(t, writers, tc.wantWriters, "writers started")
			for i, w := range writers {
				require.Equal(t, tc.want, w, "writer %d", i)
			}
		})
	}
}
