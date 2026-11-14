// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/require"
)

func TestBrokerAdminCreatesConfiguredGroupStreams(t *testing.T) {
	srv := startEmbeddedServer(t)
	admin := adminOn(t, srv)
	admin.Microservice.InstanceConfiguration.Infrastructure.Nats.ShardGroups = &config.NatsShardGroupsConfiguration{Count: 2, Suffixes: []string{"inbound-events", "resolved-events"}}
	_, err := admin.ReconcileBroker(t.Context(), []string{"event-sources", "device-management", "event-management"})
	require.NoError(t, err)
	for _, suffix := range []string{"inbound-events", "resolved-events"} {
		base, err := admin.js.StreamInfo(StreamName("test", suffix))
		require.NoError(t, err)
		group, err := admin.js.StreamInfo(StreamName("test", GroupSuffix(suffix, 1)))
		require.NoError(t, err)
		require.Equal(t, base.Config.MaxBytes, group.Config.MaxBytes)
		require.Equal(t, []string{"test.*." + suffix + "-g1"}, group.Config.Subjects)
	}
	_, err = admin.ReconcileBroker(t.Context(), []string{"event-sources", "device-management", "event-management"})
	require.NoError(t, err)
}

func TestGroupZeroOptionalPlacement(t *testing.T) {
	admin := adminOn(t, startEmbeddedServer(t))
	cfg := &config.NatsShardGroupsConfiguration{Count: 2, Suffixes: []string{"inbound-events"}, PlacementTags: map[int][]string{0: {"dc-nats-0"}, 1: {"dc-nats-1"}}}
	require.NoError(t, admin.EnableStreamSharding(brokerAdminRouter{}, cfg))
	require.Equal(t, []string{"dc-nats-0"}, admin.newStreamConfig("inbound-events").Placement.Tags)
	require.Equal(t, []string{"dc-nats-1"}, admin.newStreamConfig("inbound-events-g1").Placement.Tags)
}

func TestPerServerPlacementTagExpansion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nats.conf")
	require.NoError(t, os.WriteFile(path, []byte("server_name: $SERVER_NAME\nserver_tags: [$SERVER_NAME]\n"), 0o600))
	for _, name := range []string{"dc-nats-0", "dc-nats-1", "dc-nats-2"} {
		t.Setenv("SERVER_NAME", name)
		opts, err := natsserver.ProcessConfigFile(path)
		require.NoError(t, err)
		require.Equal(t, []string{name}, opts.Tags)
	}
}
