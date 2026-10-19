// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// A held finish is quiet until it has lasted mqttFinishHeldWarnAfter, and loud from then on.
//
// GUARD: kills a hold that never warns, and one that warns on its first pass.
func TestAHeldFinishWarnsOnlyOnceItHasLastedTheThreshold(t *testing.T) {
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.TraceLevel)
	t.Cleanup(func() { zerolog.SetGlobalLevel(prev) })
	logs := captureLogs(t)

	nmgr := &NatsManager{}
	nmgr.noteFinishHeld("rev1753 first held pass", "a replica is down")
	nmgr.mqttFinish.mu.Lock()
	nmgr.mqttFinish.since = time.Now().Add(-mqttFinishHeldWarnAfter - time.Minute)
	nmgr.mqttFinish.mu.Unlock()
	nmgr.noteFinishHeld("rev1753 long held pass", "a replica is down")

	require.Equal(t, "debug", levelOf(t, logs.String(), "rev1753 first held pass"), "the first held pass warned")
	rec := findLog(logs, "rev1753 long held pass")
	require.NotNil(t, rec)
	require.Equal(t, "warn", rec["level"], "a finish held past the threshold did not warn")
}

// A pass that is not held by the cluster ends the hold: one that finds a durable not drained
// yet (the cluster answered; the reader is behind) and one that finishes. A gauge left at the
// age of an earlier hold would page for a release that is merely busy, or already done.
//
// GUARD: kills a not-drained verdict that leaves the hold standing (or is counted as one), and
// a finish that leaves it standing.
func TestAPassTheClusterAnsweredEndsTheHold(t *testing.T) {
	fastMqttStoreLookup(t)
	b := startGuardBroker(t)
	clock := newFcClock()
	on := failClosedManager(t, b.nc, b.js, guardInstance, true)
	on.mqttClock = clock.now
	_, err := on.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	clientID, err := DeviceClientID(guardInstance, guardTenant, "d1")
	require.NoError(t, err)
	c := connectGuardClient(t, b.mqttURL(), clientID, mqtt.NewMemoryStore(), false)
	const n = 3
	for i := 0; i < n; i++ {
		require.True(t, c.publish(guardTopic(guardInstance, guardTenant, "d1"), fmt.Sprintf("p%d", i), 10*time.Second))
	}

	off := failClosedManager(t, b.nc, b.js, guardInstance, false)
	off.mqttClock = clock.now
	clock.advance(mqttFailClosedLeaseTTL)
	rep, err := off.checkMqttFailClosed(context.Background(), nil)
	require.NoError(t, err)
	require.True(t, rep.released, "premise: the store was released")
	clock.advance(mqttFailClosedReleaseGrace)

	hold := func() {
		off.mqttFinish.mu.Lock()
		off.mqttFinish.since = time.Now().Add(-time.Hour)
		off.mqttFinish.last = time.Now()
		off.mqttFinish.mu.Unlock()
	}
	hold()
	rep, err = off.checkMqttFailClosed(context.Background(), nil)
	require.NoError(t, err)
	require.False(t, rep.finished, "premise: nothing was read, so the release cannot finish")
	require.Zero(t, off.MqttFinishHeldFor(), "a durable the cluster showed not drained kept the hold standing")

	ev := fcDurable(t, guardInstance, MqttFailClosedEvents)
	sub, err := b.js.PullSubscribe(ev.Filter, ev.Name, nats.Bind(MqttMessageStore, ev.Name))
	require.NoError(t, err)
	defer func() { _ = sub.Unsubscribe() }()
	for read := 0; read < n; {
		msgs, err := sub.Fetch(n, nats.MaxWait(5*time.Second))
		require.NoError(t, err)
		for _, m := range msgs {
			require.NoError(t, m.AckSync())
			read++
		}
	}
	hold()
	var finished bool
	for i := 0; i < 20 && !finished; i++ {
		rep, err = off.checkMqttFailClosed(context.Background(), nil)
		require.NoError(t, err)
		finished = rep.finished
		if !finished {
			time.Sleep(250 * time.Millisecond)
		}
	}
	require.True(t, finished, "premise: everything was read, so the release finishes")
	require.Zero(t, off.MqttFinishHeldFor(), "a finished release kept the hold standing")
}

// 🔴 A PENDING STORE WHOSE AGE CANNOT BE READ IS LEFT ALONE. Giving the gateway's subjects to a
// store a pass may still be finishing is loss; leaving an abandoned one pending a little longer
// only refuses QoS 1 publishes until the next look. A failed age read is the second, at the
// start and in the watch.
//
// GUARD: kills a failed age read taken as "abandoned", in either place.
func TestAPendingStoreWhoseAgeCannotBeReadIsLeftAlone(t *testing.T) {
	fastMqttStoreLookup(t)
	b := startGuardBroker(t)
	saved := mqttFailClosedPendingAbandon
	mqttFailClosedPendingAbandon = 0
	t.Cleanup(func() { mqttFailClosedPendingAbandon = saved })
	_, err := b.js.AddStream(&nats.StreamConfig{Name: MqttMessageStore, Subjects: []string{mqttFailClosedPendingSubject},
		Storage: nats.FileStorage, Retention: nats.InterestPolicy, MaxBytes: fcHotBytes})
	require.NoError(t, err)

	// The age is a raw request on the manager's connection; the stream reads go through its
	// JetStream context. A closed connection fails the first and not the second.
	closed, err := nats.Connect(b.srv.ClientURL())
	require.NoError(t, err)
	closed.Close()
	off := failClosedManager(t, closed, b.js, guardInstance, false)
	n := off.Microservice.InstanceConfiguration.Infrastructure.Nats

	_, err = off.isAbandonedPendingMqttStore(context.Background(), b.storeInfo())
	require.Error(t, err, "premise: the age cannot be read")
	_ = off.ReconcileMqttStores(context.Background(), n.MqttStoreMaxBytes, n.MqttQoS2StoreMaxBytes)
	require.Equal(t, []string{mqttFailClosedPendingSubject}, b.storeInfo().Config.Subjects,
		"a start that could not read the store's age handed it the gateway's subjects")
	off.checkMqttStoreBounds(context.Background(), nil, mqttWatchBounds(n.MqttStoreMaxBytes, n.MqttQoS2StoreMaxBytes))
	require.Equal(t, []string{mqttFailClosedPendingSubject}, b.storeInfo().Config.Subjects,
		"the bound watch handed a store whose age it could not read the gateway's subjects")

	// The control: with the age readable, the same store is abandoned (the threshold is 0 here).
	live := failClosedManager(t, b.nc, b.js, guardInstance, false)
	require.NoError(t, live.ReconcileMqttStores(context.Background(), n.MqttStoreMaxBytes, n.MqttQoS2StoreMaxBytes))
	require.NotEqual(t, []string{mqttFailClosedPendingSubject}, b.storeInfo().Config.Subjects,
		"control: a readable age past the threshold gives the store the gateway's subjects")
}

// An answer with no creation time has no age to show, so it is not old: a store or durable the
// broker cannot date is never taken as abandoned or settled. An error answer is an error.
//
// GUARD: kills a missing creation time read as old, and an error answer read as an age.
func TestBrokerAgeWithoutACreationTimeIsNotOld(t *testing.T) {
	b := startGuardBroker(t)
	reply := func(body string) func(*nats.Msg) {
		return func(m *nats.Msg) { _ = m.Respond([]byte(body)) }
	}
	sub, err := b.nc.Subscribe("test.age.undated", reply(`{"ts":"2030-01-01T12:00:00Z"}`))
	require.NoError(t, err)
	defer func() { _ = sub.Unsubscribe() }()
	sub2, err := b.nc.Subscribe("test.age.refused", reply(`{"error":{"code":503,"description":"no"},"created":"2000-01-01T00:00:00Z","ts":"2030-01-01T12:00:00Z"}`))
	require.NoError(t, err)
	defer func() { _ = sub2.Unsubscribe() }()
	require.NoError(t, b.nc.Flush(), "the replies are registered before they are asked for")
	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, false)

	age, err := nmgr.brokerAge(context.Background(), "test.age.undated", "x")
	require.NoError(t, err)
	require.Zero(t, age, "an undated answer was given an age")
	_, err = nmgr.brokerAge(context.Background(), "test.age.refused", "x")
	require.Error(t, err, "an error answer was read as an age")
}
