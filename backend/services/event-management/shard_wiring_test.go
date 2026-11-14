// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-management/processor"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/partition"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// Exercise the service's production handles against JetStream, including the
// legacy durable and subjects and startup with an empty placement mirror.
func TestShardGroupServiceWiring(t *testing.T) {
	savedMs, savedCfg, savedStop := Microservice, Configuration, stopStreamSharding
	t.Cleanup(func() { Microservice, Configuration, stopStreamSharding = savedMs, savedCfg, savedStop })
	for _, mode := range []string{"off", "on", "unplaced"} {
		t.Run(mode, func(t *testing.T) {
			srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, NoLog: true, NoSigs: true, StoreDir: dctest.JetStreamStoreDir(t), JetStreamMaxStore: dctest.JetStreamMaxStore})
			require.NoError(t, err)
			go srv.Start()
			require.True(t, srv.ReadyForConnections(10*time.Second))
			t.Cleanup(srv.Shutdown)
			u, err := url.Parse(srv.ClientURL())
			require.NoError(t, err)
			port, err := strconv.Atoi(u.Port())
			require.NoError(t, err)
			Microservice = &core.Microservice{InstanceId: "n2", FunctionalArea: "event-management", Readiness: core.NewReadinessGate()}
			Microservice.UseMetricsRegistry(prometheus.NewRegistry())
			Microservice.Readiness.MarkReadyWithoutAuthSurface()
			Microservice.InstanceConfiguration.Infrastructure.Nats.Hostname = u.Hostname()
			Microservice.InstanceConfiguration.Infrastructure.Nats.Port = uint32(port)
			Microservice.InstanceConfiguration.Infrastructure.Nats.StreamMaxBytes = 8 << 20
			Microservice.InstanceConfiguration.Infrastructure.Nats.StreamMaxBytesCold = 1 << 20
			if mode != "off" {
				Microservice.InstanceConfiguration.Infrastructure.Nats.ShardGroups = &mscfg.NatsShardGroupsConfiguration{Count: 2, Suffixes: []string{streams.InboundEvents, streams.ResolvedEvents}}
			}
			Microservice.MicroserviceConfigurationRaw = []byte(`{}`)
			require.NoError(t, parseConfiguration())
			savedProc, savedExtra, savedReader, savedWriter := EventPersistenceProcessor, AdditionalPersistenceProcessors, ResolvedEventsReader, FailedEventsWriter
			savedMetrics := PersistMetrics
			buildMetrics()
			t.Cleanup(func() {
				EventPersistenceProcessor, AdditionalPersistenceProcessors, ResolvedEventsReader, FailedEventsWriter = savedProc, savedExtra, savedReader, savedWriter
				PersistMetrics = savedMetrics
			})
			nmgr := messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), func(*messaging.NatsManager) error { return nil })
			require.NoError(t, nmgr.Initialize(context.Background()))
			t.Cleanup(func() { _ = nmgr.Terminate(context.Background()) })
			js, err := nmgr.Conn().JetStream()
			require.NoError(t, err)
			if mode == "on" {
				kv, err := nmgr.PartitionPlacementStore()
				require.NoError(t, err)
				table := partition.Table{P: 2, Groups: []int{0, 1}, Overrides: map[string]int{"tenant-a": 0, "tenant-b": 1}, Generation: 1}
				require.NoError(t, partition.Publish(partition.FromKeyValue(kv), table))
			}
			require.NoError(t, createNatsComponents(nmgr))
			t.Cleanup(stopStreamSharding)
			readers := []messaging.MessageReader{EventPersistenceProcessor.ResolvedEventsReader}
			for _, p := range AdditionalPersistenceProcessors {
				readers = append(readers, p.(*processor.EventPersistenceProcessor).ResolvedEventsReader)
			}
			writer, err := nmgr.NewWriter(streams.ResolvedEvents)
			require.NoError(t, err)
			if mode == "unplaced" {
				require.ErrorIs(t, writer.WriteMessages(core.WithTenant(context.Background(), "tenant-b"), messaging.Message{Value: []byte(`{"event":"unplaced"}`)}), messaging.ErrUnplacedTenant)
				for _, suffix := range []string{"resolved-events", "resolved-events" + "-g1"} {
					info, err := js.StreamInfo("n2_" + suffix)
					require.NoError(t, err)
					require.Zero(t, info.State.Msgs, "unplaced event reached a stream")
				}
				return
			}
			expectedReaders := 1
			if mode == "on" {
				expectedReaders = 2
			}
			require.Len(t, readers, expectedReaders)
			for g, tenant := range []string{"tenant-a", "tenant-b"} {
				group := g
				if mode == "off" {
					group = 0
				}
				suffix := "resolved-events"
				if group == 1 {
					suffix += "-g1"
				}
				payload := []byte(fmt.Sprintf(`{"event":%q}`, tenant))
				ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), tenant), 5*time.Second)
				defer cancel()
				require.NoError(t, writer.WriteMessages(ctx, messaging.Message{Value: payload}))
				msg, err := readers[group].ReadMessage(ctx)
				require.NoError(t, err)
				require.Equal(t, "n2."+tenant+"."+suffix, msg.Subject)
				require.Equal(t, payload, msg.Value)

				require.NoError(t, msg.Ack())
				info, err := js.ConsumerInfo("n2_"+suffix, "n2_event-management_"+suffix)
				require.NoError(t, err, "legacy/group durable name")
				require.Equal(t, "n2.*."+suffix, info.Config.FilterSubject)
			}
			if mode == "off" {
				_, err := js.StreamInfo("n2_" + "resolved-events" + "-g1")
				require.ErrorIs(t, err, nats.ErrStreamNotFound)
			}
		})
	}
}
