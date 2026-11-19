// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/require"
)

func TestTenantShardGroupForFactRouting(t *testing.T) {
	m := &NatsManager{Microservice: &core.Microservice{}}
	g, err := m.TenantShardGroup("unplaced", streams.ResolvedEvents)
	require.NoError(t, err)
	require.Zero(t, g)
	cfg := n1Config(streams.ResolvedEvents)
	m.Microservice.InstanceConfiguration.Infrastructure.Nats.ShardGroups = cfg
	_, err = m.TenantShardGroup("tenant-b", streams.ResolvedEvents)
	require.ErrorIs(t, err, ErrShardRoutingNotEnabled)
	require.NoError(t, m.EnableStreamSharding(shardRouterFunc(n1Router), cfg))
	for _, tenant := range []string{"tenant-a", "tenant-b"} {
		want, _ := n1Router(tenant)
		got, err := m.TenantShardGroup(tenant, streams.ResolvedEvents)
		require.NoError(t, err)
		require.Equal(t, want, got)
		physical, err := m.tenantSuffix(tenant, streams.ResolvedEvents)
		require.NoError(t, err)
		require.Equal(t, GroupSuffix(streams.ResolvedEvents, got), physical)
	}
	for _, tenant := range []string{"missing", "negative", "too-high"} {
		_, err := m.TenantShardGroup(tenant, streams.ResolvedEvents)
		require.ErrorIs(t, err, ErrUnplacedTenant)
	}
	g, err = m.TenantShardGroup("unplaced", streams.InboundEvents)
	require.NoError(t, err)
	require.Zero(t, g)
}
