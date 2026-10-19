// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// 🔴 A PLATFORM DURABLE CREATED AGAIN MUST NOT START ABOVE A COPY THE STORE STILL HOLDS.
// Deleting a consumer from the interest-retention store drops the copies only it wanted, but a
// copy another consumer still holds interest in stays, and its direct copy was skipped as carried
// by the store. A DeliverNew durable starts above it with its ack floor at the tail, so the copy
// is never read and a finish reads the kind as drained.
//
// GUARD: kills a re-created durable that starts at the tail while the store holds copies, and
// one that no longer drops the copies nobody holds (the control).
func TestARecreatedDurableReadsTheCopiesTheStoreStillHolds(t *testing.T) {
	b := startGuardBroker(t)
	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, true)
	_, err := nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	events := fcDurable(t, guardInstance, MqttFailClosedEvents)

	// Another consumer with interest in device publishes, as a gateway session consumer is.
	_, err = b.js.AddConsumer(MqttMessageStore, &nats.ConsumerConfig{
		Durable: "other-interest", AckPolicy: nats.AckExplicitPolicy, DeliverPolicy: nats.DeliverNewPolicy})
	require.NoError(t, err)

	c := fcDevice(t, b, "d1")
	topic := guardTopic(guardInstance, guardTenant, "d1")
	var published []string
	for i := 0; i < 20; i++ {
		p := fmt.Sprintf("m%d", i)
		fcPublish(t, c, topic, 1, p)
		published = append(published, p)
	}
	require.EqualValues(t, 20, b.storeInfo().State.Msgs, "premise: the durable holds an unread backlog")

	require.NoError(t, b.js.DeleteConsumer(MqttMessageStore, events.Name))
	require.EqualValues(t, 20, b.storeInfo().State.Msgs, "premise: the other consumer keeps the copies")

	rep, err := nmgr.checkMqttFailClosed(context.Background(), fcMetrics(guardInstance))
	require.NoError(t, err)
	require.Contains(t, rep.createdDurables, events.Name, "premise: the pass created the durable again")

	ci, err := b.js.ConsumerInfo(MqttMessageStore, events.Name)
	require.NoError(t, err)
	require.EqualValues(t, 20, ci.NumPending, "the re-created durable skipped the store's backlog")
	require.EqualValues(t, 0, ci.AckFloor.Stream)
	got := guardDrain(t, b.js, events.Name, 10*time.Second, func(got []string) bool { return len(got) >= 20 })
	require.Equal(t, published, got)
}

// The control: with nobody else holding the copies, deleting the durable drops them with it, so
// the re-created durable starts on an empty store and nothing is skipped by starting there.
func TestARecreatedDurableOnAnEmptiedStoreStartsAtItsTail(t *testing.T) {
	b := startGuardBroker(t)
	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, true)
	_, err := nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	events := fcDurable(t, guardInstance, MqttFailClosedEvents)
	c := fcDevice(t, b, "d1")
	for i := 0; i < 5; i++ {
		fcPublish(t, c, guardTopic(guardInstance, guardTenant, "d1"), 1, fmt.Sprintf("m%d", i))
	}
	require.NoError(t, b.js.DeleteConsumer(MqttMessageStore, events.Name))
	require.EqualValues(t, 0, b.storeInfo().State.Msgs, "premise: interest retention dropped them with the durable")
	_, err = nmgr.checkMqttFailClosed(context.Background(), fcMetrics(guardInstance))
	require.NoError(t, err)
	ci, err := b.js.ConsumerInfo(MqttMessageStore, events.Name)
	require.NoError(t, err)
	require.EqualValues(t, 0, ci.NumPending)
	require.EqualValues(t, 5, ci.AckFloor.Stream)
}
