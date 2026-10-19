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

	// Another consumer with interest in every device publish, as an operator's would have. (The
	// platform's own case is TestARecreatedReportsDurableReadsWhatTheEventsDurableStillHolds.)
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

// 🔴 THE PLATFORM HOLDS THE OTHER INTEREST ITSELF. The events durable is unfiltered, so every
// device report it has not acked keeps its store copy alive after the reports durable is
// deleted. Those copies' direct copies were skipped under the from mark; a reports durable
// created again at the tail would never read them, with no consumer but the platform's own.
//
// GUARD: kills a re-created durable that starts at the tail (DeliverNew) or at the store's last
// sequence, with no foreign consumer in the test.
func TestARecreatedReportsDurableReadsWhatTheEventsDurableStillHolds(t *testing.T) {
	b := startGuardBroker(t)
	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, true)
	_, err := nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	reports := fcDurable(t, guardInstance, MqttFailClosedReports)

	c := fcDevice(t, b, "d1")
	topic := fmt.Sprintf("%s/%s/%s/d1", guardInstance, guardTenant, SubjectDeviceReports)
	var published []string
	for i := 0; i < 5; i++ {
		p := fmt.Sprintf("r%d", i)
		fcPublish(t, c, topic, 1, p)
		published = append(published, p)
	}
	require.NoError(t, b.js.DeleteConsumer(MqttMessageStore, reports.Name))
	require.EqualValues(t, 5, b.storeInfo().State.Msgs, "premise: the unfiltered events durable keeps the copies")

	rep, err := nmgr.checkMqttFailClosed(context.Background(), fcMetrics(guardInstance))
	require.NoError(t, err)
	require.Contains(t, rep.createdDurables, reports.Name, "premise: the pass created the durable again")

	ci, err := b.js.ConsumerInfo(MqttMessageStore, reports.Name)
	require.NoError(t, err)
	require.EqualValues(t, 5, ci.NumPending, "the re-created reports durable skipped copies only the store holds")
	got := guardDrain(t, b.js, reports.Name, 10*time.Second, func(got []string) bool { return len(got) >= 5 })
	require.Equal(t, published, got)
}

// A FIRST TAKE-OVER STARTS AT THE TAIL. A store with no mark of this instance may hold copies
// another consumer keeps; the platform never skipped their direct copies, so reading them from
// the store would forward each a second time, under a dedup id no direct copy shares.
//
// GUARD: kills a durable created from the start of the store when the store carries no mark.
func TestAFirstTakeOverDoesNotReadWhatTheStoreAlreadyHeld(t *testing.T) {
	b := startGuardBroker(t)
	c := fcDevice(t, b, "d1")
	_, err := b.js.AddConsumer(MqttMessageStore, &nats.ConsumerConfig{
		Durable: "other-interest", AckPolicy: nats.AckExplicitPolicy})
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		fcPublish(t, c, guardTopic(guardInstance, guardTenant, "d1"), 1, fmt.Sprintf("m%d", i))
	}
	require.EqualValues(t, 5, b.storeInfo().State.Msgs, "premise: the other consumer keeps the copies")
	require.NotContains(t, b.storeInfo().Config.Metadata, mqttFailClosedOwnerKey, "premise: no mark yet")

	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, true)
	_, err = nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	for _, d := range MqttFailClosedDurables(guardInstance) {
		ci, err := b.js.ConsumerInfo(MqttMessageStore, d.Name)
		require.NoError(t, err)
		require.EqualValues(t, 0, ci.NumPending, "%s read what the store held before the platform took it over", d.Name)
	}
}

// A RELEASED STORE IS THIS INSTANCE'S TOO. Its durables still hold copies whose direct copies
// were skipped before the release; a reports durable deleted then, and created again when a pod
// with the setting on takes the store back, must read the copies the events durable keeps alive.
//
// GUARD: kills a re-creation from the start of the store keyed on the owner mark alone.
func TestARecreatedDurableOnAReleasedStoreReadsWhatItStillHolds(t *testing.T) {
	b := startGuardBroker(t)
	clock := newFcClock()
	on := failClosedManager(t, b.nc, b.js, guardInstance, true)
	on.mqttClock = clock.now
	_, err := on.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	reports := fcDurable(t, guardInstance, MqttFailClosedReports)

	c := fcDevice(t, b, "d1")
	topic := fmt.Sprintf("%s/%s/%s/d1", guardInstance, guardTenant, SubjectDeviceReports)
	var published []string
	for i := 0; i < 5; i++ {
		p := fmt.Sprintf("r%d", i)
		fcPublish(t, c, topic, 1, p)
		published = append(published, p)
	}

	off := failClosedManager(t, b.nc, b.js, guardInstance, false)
	off.mqttClock = clock.now
	clock.advance(mqttFailClosedLeaseTTL)
	rep, err := off.checkMqttFailClosed(context.Background(), nil)
	require.NoError(t, err)
	require.True(t, rep.released, "premise: the store was released")
	md := b.storeInfo().Config.Metadata
	require.NotContains(t, md, mqttFailClosedOwnerKey, "premise: a released store carries no owner mark")
	require.Equal(t, guardInstance, md[mqttFailClosedReleasedKey], "premise: it carries the released mark")

	require.NoError(t, b.js.DeleteConsumer(MqttMessageStore, reports.Name))
	require.EqualValues(t, 5, b.storeInfo().State.Msgs, "premise: the unfiltered events durable keeps the copies")

	rep, err = on.checkMqttFailClosed(context.Background(), fcMetrics(guardInstance))
	require.NoError(t, err)
	require.Contains(t, rep.createdDurables, reports.Name, "premise: the take-back created the durable again")
	ci, err := b.js.ConsumerInfo(MqttMessageStore, reports.Name)
	require.NoError(t, err)
	require.EqualValues(t, 5, ci.NumPending, "the re-created reports durable skipped copies only the store holds")
	got := guardDrain(t, b.js, reports.Name, 10*time.Second, func(got []string) bool { return len(got) >= 5 })
	require.Equal(t, published, got)
}
