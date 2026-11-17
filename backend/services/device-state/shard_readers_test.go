// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-state/model"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/partition"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// Read through each processor's actual reader to prove its stream AND durable
// binding; counting consumers alone would miss two processors sharing one reader.
func TestResolvedEventsProcessorsCoverEveryGroupAndKeepLegacyDurable(t *testing.T) {
	for _, sharded := range []bool{true, false} {
		t.Run(fmt.Sprintf("sharded=%t", sharded), func(t *testing.T) {
			host, port := startEmbeddedNats(t)
			savedMs, savedCfg, savedApi := Microservice, Configuration, Api
			savedMetrics, savedSweep, savedDropped := StateMetrics, InactivitySweepMetrics, DroppedEvicted
			savedProcs, savedOwners := StateProcessors, OwnershipProcessor
			savedWriter, savedOwnerReader, savedStop := InboundEventsWriter, OwnershipReader, stopStreamSharding
			t.Cleanup(func() {
				Microservice, Configuration, Api = savedMs, savedCfg, savedApi
				StateMetrics, InactivitySweepMetrics, DroppedEvicted = savedMetrics, savedSweep, savedDropped
				StateProcessors, OwnershipProcessor = savedProcs, savedOwners
				InboundEventsWriter, OwnershipReader, stopStreamSharding = savedWriter, savedOwnerReader, savedStop
			})
			Microservice = &core.Microservice{InstanceId: "state-groups", FunctionalArea: "device-state", Readiness: core.NewReadinessGate()}
			Microservice.UseMetricsRegistry(prometheus.NewRegistry())
			Microservice.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port,
				StreamMaxBytes: 8 << 20, StreamMaxBytesCold: 1 << 20}
			if sharded {
				Microservice.InstanceConfiguration.Infrastructure.Nats.ShardGroups = &mscfg.NatsShardGroupsConfiguration{
					Count: 2, Suffixes: []string{streams.ResolvedEvents}}
			}
			Microservice.MicroserviceConfigurationRaw = []byte(`{}`)
			require.NoError(t, parseConfiguration())
			buildMetrics()
			// Only the emitter needs an Api here. Projection workers never receive
			// this binding probe's payload; we read it directly below.
			Api = &model.Api{}
			ctx := context.Background()
			mgr := messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), func(m *messaging.NatsManager) error {
				if sharded {
					kv, err := m.PartitionPlacementStore()
					if err != nil {
						return err
					}
					// P=1 puts every tenant, including T, in group 1.
					if err := partition.Publish(partition.FromKeyValue(kv), partition.Table{P: 1, Groups: []int{1}, Generation: 1}); err != nil {
						return err
					}
				}
				return createNatsComponents(m)
			})
			mgr.RecordMaxDeliveries(func(*messaging.NatsManager) (messaging.MaxDeliveryFunc, error) {
				return func(context.Context, messaging.MaxDelivery) (messaging.MaxDeliveryOutcome, error) {
					return messaging.MaxDeliveryLettered, nil
				}, nil
			})
			require.NoError(t, mgr.Initialize(ctx))
			StateProcessors, OwnershipProcessor, stopStreamSharding = nil, nil, nil
			t.Cleanup(func() {
				for _, sp := range StateProcessors {
					require.NoError(t, sp.Stop(ctx))
					require.NoError(t, sp.Terminate(ctx))
				}
				if OwnershipProcessor != nil {
					require.NoError(t, OwnershipProcessor.Stop(ctx))
					require.NoError(t, OwnershipProcessor.Terminate(ctx))
				}
				if stopStreamSharding != nil {
					stopStreamSharding()
				}
				require.NoError(t, mgr.Stop(ctx))
				require.NoError(t, mgr.Terminate(ctx))
			})
			require.NoError(t, mgr.Start(ctx), "production factory must accept sharded resolved-events")
			Microservice.Readiness.MarkReadyWithoutAuthSurface()
			count := 1
			if sharded {
				count = 2
			}
			require.Len(t, StateProcessors, count)
			js, err := mgr.Conn().JetStream()
			require.NoError(t, err)
			for group, sp := range StateProcessors {
				require.Equal(t, group == 0, sp.InactivityMonitorEnabled(),
					"only the first processor may run the all-tenant inactivity sweep")
				suffix := streams.ResolvedEvents
				if group > 0 {
					suffix += fmt.Sprintf("-g%d", group)
				}
				payload := fmt.Sprintf("group-%d", group)
				_, err := js.Publish("state-groups.T."+suffix, []byte(payload))
				require.NoError(t, err)
				readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				msg, err := sp.ResolvedEventsReader.ReadMessage(readCtx)
				cancel()
				require.NoError(t, err)
				require.Equal(t, payload, string(msg.Value), "processor must read its own group")
				// Literal names keep the assertion independent of the naming helper.
				durable := "state-groups_device-state_resolved-events"
				if group > 0 {
					durable += fmt.Sprintf("-g%d", group)
				}
				info, err := js.ConsumerInfo("state-groups_"+suffix, durable)
				require.NoError(t, err)
				require.EqualValues(t, 1, info.Delivered.Consumer)
			}
		})
	}
}
