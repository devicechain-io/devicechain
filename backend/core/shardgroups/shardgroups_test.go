// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package shardgroups

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestConfiguredShardingFollowsAndRefusesInvalidPlacement(t *testing.T) {
	m, registry := newReadersManagerWithRegistry(t)
	m.Microservice.InstanceConfiguration.Infrastructure.Nats.ShardGroups = &mscfg.NatsShardGroupsConfiguration{Count: 2, Suffixes: []string{streams.InboundEvents, streams.ResolvedEvents}}
	kv, err := m.PartitionPlacementStore()
	require.NoError(t, err)
	stop, err := Enable(m)
	require.NoError(t, err)
	t.Cleanup(stop)
	placementUnavailable := func() float64 {
		families, err := registry.Gather()
		require.NoError(t, err)
		for _, family := range families {
			if strings.HasSuffix(family.GetName(), "jetstream_shard_placement_unavailable") {
				return family.Metric[0].Gauge.GetValue()
			}
		}
		t.Fatal("placement availability gauge was not registered")
		return -1
	}
	require.Equal(t, 1.0, placementUnavailable(), "empty initial snapshot must publish unavailable")
	// The second enable is rejected before registering another copy of the gauge.
	_, err = Enable(m)
	require.Error(t, err)
	w, err := m.NewWriter(streams.InboundEvents)
	require.NoError(t, err)
	require.ErrorIs(t, w.WriteMessages(core.WithTenant(context.Background(), "tenant"), messaging.Message{Value: []byte("refused")}), messaging.ErrUnplacedTenant)
	table := partition.Table{P: 1, Groups: []int{1}, Generation: 1}
	require.NoError(t, partition.Publish(partition.FromKeyValue(kv), table))
	require.Eventually(t, func() bool {
		return w.WriteMessages(core.WithTenant(context.Background(), "tenant"), messaging.Message{Value: []byte("placed")}) == nil
	}, 5*time.Second, 10*time.Millisecond)
	require.Zero(t, placementUnavailable())
	js, err := m.Conn().JetStream()
	require.NoError(t, err)
	for suffix, count := range map[string]uint64{"inbound-events": 0, "inbound-events-g1": 1} {
		info, err := js.StreamInfo("test_" + suffix)
		require.NoError(t, err)
		require.Equal(t, count, info.State.Msgs)
	}
	_, err = kv.Put(partition.TableKey, []byte(`{"format":999}`))
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		err := placementError(m)
		return err != nil
	}, 5*time.Second, 10*time.Millisecond)
	require.ErrorIs(t, w.WriteMessages(core.WithTenant(context.Background(), "tenant"), messaging.Message{Value: []byte("invalid")}), messaging.ErrUnplacedTenant)
	raw, err := partition.Encode(table)
	require.NoError(t, err)
	_, err = kv.Put(partition.TableKey, raw)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		err := placementError(m)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, kv.Delete(partition.TableKey))
	require.Eventually(t, func() bool {
		err := placementError(m)
		return err != nil
	}, 5*time.Second, 10*time.Millisecond)
	require.ErrorIs(t, w.WriteMessages(core.WithTenant(context.Background(), "tenant"), messaging.Message{Value: []byte("deleted")}), messaging.ErrUnplacedTenant)
	require.Equal(t, 1.0, placementUnavailable())

	// A valid placement keeps answering through ShardGroup until the watch is cancelled.
	_, err = kv.Put(partition.TableKey, raw)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return placementError(m) == nil }, 5*time.Second, 10*time.Millisecond)
	require.Never(t, func() bool { return placementError(m) != nil }, 100*time.Millisecond, 10*time.Millisecond)
	stop()
	require.Eventually(t, func() bool {
		return errors.Is(placementError(m), messaging.ErrUnplacedTenant)
	}, 5*time.Second, 10*time.Millisecond)
}

// Probe the same tenant routing used by the live-feed service without publishing.
func placementError(m *messaging.NatsManager) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := m.SubscribeLive(ctx, "tenant", streams.ResolvedEvents)
	return err
}

func TestNewReadersWithIndependentGroupOptions(t *testing.T) {
	for _, selected := range []bool{false, true} {
		t.Run(strconv.FormatBool(selected), func(t *testing.T) {
			m := newReadersManager(t)
			m.Microservice.FunctionalArea = "event-processing"
			n := 1
			if selected {
				cfg := &mscfg.NatsShardGroupsConfiguration{Count: 2, Suffixes: []string{streams.ResolvedEvents}}
				m.Microservice.InstanceConfiguration.Infrastructure.Nats.ShardGroups = cfg
				require.NoError(t, m.EnableStreamSharding(partition.NewRouter("test"), cfg))
				n = 2
			}
			js, err := m.Conn().JetStream()
			require.NoError(t, err)
			var calls []int
			readers, err := NewReadersWith(m, streams.ResolvedEvents, func(group int) []messaging.ReaderOption {
				calls = append(calls, group)
				return []messaging.ReaderOption{messaging.ReaderWithAckFloor(func(context.Context) (uint64, error) {
					return uint64(group + 2), nil
				})}
			})
			require.NoError(t, err)
			require.Len(t, readers, n)
			for group, reader := range readers {
				require.Equal(t, group, calls[group])
				physical := messaging.GroupSuffix(streams.ResolvedEvents, group)
				for seq := 1; seq <= 4; seq++ {
					_, err := js.Publish(messaging.ScopedSubject("test", "tenant", physical), []byte(strconv.Itoa(seq)))
					require.NoError(t, err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				msg, err := reader.ReadMessage(ctx)
				cancel()
				require.NoError(t, err)
				require.EqualValues(t, group+2, msg.StreamSeq)
				require.Equal(t, strconv.Itoa(group+2), string(msg.Value))
				require.ErrorIs(t, msg.Ack(), messaging.ErrAckFloorReader)
				info, err := js.ConsumerInfo(messaging.StreamName("test", physical), messaging.AckFloorDurableName("test", "event-processing", physical))
				require.NoError(t, err)
				require.Equal(t, nats.AckAllPolicy, info.Config.AckPolicy)
				require.EqualValues(t, group+2, info.Config.OptStartSeq)
			}
		})
	}
}

func newReadersManager(t *testing.T) *messaging.NatsManager {
	t.Helper()
	m, _ := newReadersManagerWithRegistry(t)
	return m
}

func newReadersManagerWithRegistry(t *testing.T) (*messaging.NatsManager, *prometheus.Registry) {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, NoLog: true, NoSigs: true, StoreDir: dctest.JetStreamStoreDir(t), JetStreamMaxStore: dctest.JetStreamMaxStore})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second))
	t.Cleanup(srv.Shutdown)
	u, err := url.Parse(srv.ClientURL())
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "event-sources"}
	registry := prometheus.NewRegistry()
	ms.UseMetricsRegistry(registry)
	ms.InstanceConfiguration.Infrastructure.Nats.Hostname = u.Hostname()
	ms.InstanceConfiguration.Infrastructure.Nats.Port = uint32(port)
	ms.InstanceConfiguration.Infrastructure.Nats.StreamMaxBytes = 8 << 20
	ms.InstanceConfiguration.Infrastructure.Nats.StreamMaxBytesCold = 1 << 20
	m := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*messaging.NatsManager) error { return nil })
	require.NoError(t, m.Initialize(context.Background()))
	t.Cleanup(func() { _ = m.Terminate(context.Background()) })
	return m, registry
}
