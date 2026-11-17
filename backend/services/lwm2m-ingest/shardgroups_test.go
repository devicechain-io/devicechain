// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"testing"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/partition"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// Drive the service's own handle construction, rather than calling Enable in the test.
// Full initialization also needs projected PSK secrets and remote service endpoints.
func TestInboundWriterFollowsPlacement(t *testing.T) {
	for _, group := range []int{0, 1} {
		t.Run(messaging.GroupSuffix("group", group), func(t *testing.T) {
			startRunningManager(t)
			Microservice.FunctionalArea = "lwm2m-ingest"
			prevStop, prevWriter, prevReader, prevResponse := stopStreamSharding, Writer, CommandReader, ResponseWriter
			t.Cleanup(func() {
				stopStreamSharding, Writer, CommandReader, ResponseWriter = prevStop, prevWriter, prevReader, prevResponse
			})
			NatsManager.Microservice.InstanceConfiguration.Infrastructure.Nats.ShardGroups = &mscfg.NatsShardGroupsConfiguration{
				Count: 2, Suffixes: []string{streams.InboundEvents},
			}
			kv, err := NatsManager.PartitionPlacementStore()
			require.NoError(t, err)
			require.NoError(t, partition.Publish(partition.FromKeyValue(kv), partition.Table{P: 1, Groups: []int{group}, Generation: 1}))
			require.NoError(t, createNatsComponents(NatsManager))
			if stopStreamSharding != nil {
				t.Cleanup(stopStreamSharding)
			}
			require.NoError(t, Writer.WriteMessages(core.WithTenant(context.Background(), "tenant"), messaging.Message{Value: []byte("presence")}))
			js, err := NatsManager.Conn().JetStream()
			require.NoError(t, err)
			for g := 0; g < 2; g++ {
				info, err := js.StreamInfo(messaging.StreamName(Microservice.InstanceId, messaging.GroupSuffix(streams.InboundEvents, g)))
				if errors.Is(err, nats.ErrStreamNotFound) && g != group {
					continue // An absent other group also holds zero messages.
				}
				require.NoError(t, err)
				var want uint64
				if g == group {
					want = 1
				}
				require.Equal(t, want, info.State.Msgs, "group %d", g)
			}
		})
	}
}
