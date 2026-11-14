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

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/partition"
	"github.com/devicechain-io/dc-microservice/shardgroups"
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
			Microservice = &core.Microservice{InstanceId: "n2", FunctionalArea: "event-sources", Readiness: core.NewReadinessGate()}
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
			savedGW, savedPub, savedPresence, savedInbound, savedGC := GatewaySource, ExternalPublishers, PresenceWriter, InboundEventsWriter, HTTPGroupCommit
			GatewaySource, ExternalPublishers = nil, nil
			initializeMetrics()
			t.Cleanup(func() {
				stopHTTPGroupCommit()
				if PresenceWriter != nil {
					PresenceWriter.Close()
				}
				GatewaySource, ExternalPublishers, PresenceWriter, InboundEventsWriter, HTTPGroupCommit = savedGW, savedPub, savedPresence, savedInbound, savedGC
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
			peerMs := &core.Microservice{InstanceId: "n2", FunctionalArea: "device-management", InstanceConfiguration: Microservice.InstanceConfiguration}
			peerMs.UseMetricsRegistry(prometheus.NewRegistry())
			peer := messaging.NewNatsManager(peerMs, core.NewNoOpLifecycleCallbacks(), func(*messaging.NatsManager) error { return nil })
			require.NoError(t, peer.Initialize(context.Background()))
			t.Cleanup(func() { _ = peer.Terminate(context.Background()) })
			stopPeer, err := shardgroups.Enable(peer)
			require.NoError(t, err)
			t.Cleanup(stopPeer)
			_, err = peer.NewWriter(streams.ResolvedEvents)
			require.NoError(t, err)
			peerReaders, err := shardgroups.NewReaders(peer, streams.InboundEvents)
			require.NoError(t, err)
			stopGatewayFeedback()
			t.Cleanup(stopGatewayFeedback)
			require.NoError(t, createNatsComponents(nmgr))
			t.Cleanup(stopStreamSharding)
			readers := peerReaders
			ordered, err := newGatewayWriter(nmgr)
			require.NoError(t, err)
			defer ordered.Close()
			rounds, err := nmgr.NewRoundPublisher(streams.InboundEvents)
			require.NoError(t, err)
			publishers := []func(context.Context, messaging.Message) error{
				func(ctx context.Context, msg messaging.Message) error {
					return InboundEventsWriter.WriteMessages(ctx, msg)
				},
				func(ctx context.Context, msg messaging.Message) error {
					done := make(chan error, 1)
					ordered.Publish(ctx, msg, func(err error) { done <- err })
					return <-done
				},
				func(ctx context.Context, msg messaging.Message) error {
					_, err := rounds.Publish(ctx, msg)
					return err
				},
			}
			if mode == "unplaced" {
				for _, publish := range publishers {
					require.ErrorIs(t, publish(core.WithTenant(context.Background(), "tenant-b"), messaging.Message{Value: []byte(`{"event":"unplaced"}`)}), messaging.ErrUnplacedTenant)
				}
				for _, suffix := range []string{"inbound-events", "inbound-events" + "-g1"} {
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
				suffix := "inbound-events"
				if group == 1 {
					suffix += "-g1"
				}
				payload := []byte(fmt.Sprintf(`{"event":%q}`, tenant))
				ctx, cancel := context.WithTimeout(core.WithTenant(context.Background(), tenant), 5*time.Second)
				defer cancel()
				for _, publish := range publishers {
					require.NoError(t, publish(ctx, messaging.Message{Value: payload}))
					msg, err := readers[group].ReadMessage(ctx)
					require.NoError(t, err)
					require.Equal(t, "n2."+tenant+"."+suffix, msg.Subject)
					require.Equal(t, payload, msg.Value)

					require.NoError(t, msg.Ack())
				}
				info, err := js.ConsumerInfo("n2_"+suffix, "n2_device-management_"+suffix)
				require.NoError(t, err, "legacy/group durable name")
				require.Equal(t, "n2.*."+suffix, info.Config.FilterSubject)
			}
			if mode == "off" {
				_, err := js.StreamInfo("n2_" + "inbound-events" + "-g1")
				require.ErrorIs(t, err, nats.ErrStreamNotFound)
			}
		})
	}
}
