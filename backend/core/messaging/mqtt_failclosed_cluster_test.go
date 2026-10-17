// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package messaging

import (
	"context"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	dctest "github.com/devicechain-io/dc-microservice/test"
)

// On a cluster the store and its durables take the configured replica count: a fresh store
// is created with it, and a store the gateway made at one replica is raised to it in place.
// (On a single server the count is 1 whether or not it is set, so the single-server tests
// cannot see it.)
//
// GUARD: kills a create without the replica count, and a conform that does not raise it.
func TestTheFailClosedStoreTakesTheConfiguredReplicasOnACluster(t *testing.T) {
	servers := dctest.StartJetStreamCluster(t, 3)
	nc, err := nats.Connect(servers[0].ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	nmgr := failClosedManager(t, nc, js, guardInstance, true)
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.StreamReplicas = 3
	require.Eventually(t, func() bool { return nmgr.brokerIsClustered() }, 10*time.Second, 50*time.Millisecond,
		"premise: the manager sees a cluster")

	state, err := nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, state.Replicas)
	si, err := js.StreamInfo(MqttMessageStore)
	require.NoError(t, err)
	require.Equal(t, 3, si.Config.Replicas, "a fresh store is created at the configured replicas")
	for _, d := range MqttFailClosedDurables(guardInstance) {
		ci, err := js.ConsumerInfo(MqttMessageStore, d.Name)
		require.NoError(t, err)
		require.Equalf(t, 3, ci.Config.Replicas, "the %s durable's replicas", d.Kind)
	}

	// A store the gateway made first, at one replica.
	require.NoError(t, js.DeleteStream(MqttMessageStore))
	gw, err := js.AddStream(guardStoreConfig(nats.DiscardOld, 0, 1))
	require.NoError(t, err)
	// Through the retrying entry point: a durable created at three replicas straight after
	// the store's own scale-up can be refused for "insufficient resources" until the new
	// peers have caught up, which the startup retry schedule is there to absorb.
	_, err = nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	si, err = js.StreamInfo(MqttMessageStore)
	require.NoError(t, err)
	require.Equal(t, 3, si.Config.Replicas, "a one-replica store is raised in place")
	require.Equal(t, gw.Created, si.Created, "raised in place, not recreated")
}
