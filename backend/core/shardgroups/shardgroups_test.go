// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package shardgroups

import (
	"context"
	"net/url"
	"strconv"
	"testing"
	"time"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/partition"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestConfiguredShardingFollowsAndRefusesInvalidPlacement(t *testing.T) {
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
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ms.InstanceConfiguration.Infrastructure.Nats.Hostname = u.Hostname()
	ms.InstanceConfiguration.Infrastructure.Nats.Port = uint32(port)
	ms.InstanceConfiguration.Infrastructure.Nats.StreamMaxBytes = 8 << 20
	ms.InstanceConfiguration.Infrastructure.Nats.StreamMaxBytesCold = 1 << 20
	m := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*messaging.NatsManager) error { return nil })
	require.NoError(t, m.Initialize(context.Background()))
	t.Cleanup(func() { _ = m.Terminate(context.Background()) })
	m.Microservice.InstanceConfiguration.Infrastructure.Nats.ShardGroups = &mscfg.NatsShardGroupsConfiguration{Count: 2, Suffixes: []string{streams.InboundEvents, streams.ResolvedEvents}}
	kv, err := m.PartitionPlacementStore()
	require.NoError(t, err)
	stop, err := Enable(m)
	require.NoError(t, err)
	t.Cleanup(stop)
	w, err := m.NewWriter(streams.InboundEvents)
	require.NoError(t, err)
	require.ErrorIs(t, w.WriteMessages(core.WithTenant(context.Background(), "tenant"), messaging.Message{Value: []byte("refused")}), messaging.ErrUnplacedTenant)
	table := partition.Table{P: 1, Groups: []int{1}, Generation: 1}
	require.NoError(t, partition.Publish(partition.FromKeyValue(kv), table))
	require.Eventually(t, func() bool {
		return w.WriteMessages(core.WithTenant(context.Background(), "tenant"), messaging.Message{Value: []byte("placed")}) == nil
	}, 5*time.Second, 10*time.Millisecond)
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
}

// Probe the same tenant routing used by the live-feed service without publishing.
func placementError(m *messaging.NatsManager) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := m.SubscribeLive(ctx, "tenant", streams.ResolvedEvents)
	return err
}
