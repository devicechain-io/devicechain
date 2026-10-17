// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// The store-recreate window and the platform durables' backlog: the two residuals of the
// fail-closed reader closed before the setting stopped being refused.

// 🔴 A STORE THE WATCH CREATES AGAIN TAKES NOTHING UNTIL ITS DURABLES EXIST. Created with the
// gateway's subjects, it would take the device's store copy with no durable interested in it,
// skip it, and the gateway would PUBACK a publish whose direct copy a reader with a look from
// before the deletion skips too. With only the pending subject, the gateway's store write is
// not answered: no PUBACK, the connection closed, and the device's resend is held by the durable
// once the pass has finished.
//
// GUARD: kills a re-creation that gives the store the gateway's subjects before the durables
// exist (the publish made between the two is PUBACKed), and one that leaves the pending subject
// in place after them (the resend is never stored). Shown to fail with the store created on
// $MQTT.msgs.>: "during" was PUBACKed.
func TestARecreatedStoreTakesNothingUntilItsDurablesExist(t *testing.T) {
	b := startGuardBroker(t)
	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, true)
	_, err := nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)

	clientID, err := DeviceClientID(guardInstance, guardTenant, "dev1")
	require.NoError(t, err)
	session := mqtt.NewMemoryStore()
	g := connectGuardClient(t, b.mqttURL(), clientID, session, false)
	topic := guardTopic(guardInstance, guardTenant, "dev1")
	require.True(t, g.publish(topic, "before", 10*time.Second), "premise: the declared store acknowledges")

	require.NoError(t, b.js.DeleteStream(MqttMessageStore))
	window, during := false, true
	nmgr.mqttPassHook = func(point string) {
		if point != "store-created" {
			return
		}
		window = true
		require.Equal(t, []string{mqttFailClosedPendingSubject}, b.storeInfo().Config.Subjects,
			"the store was created on the gateway's subjects")
		during = g.publish(topic, "during", 15*time.Second)
	}
	m := fcMetrics(guardInstance)
	_, err = nmgr.checkMqttFailClosed(context.Background(), m)
	nmgr.mqttPassHook = nil
	require.NoError(t, err)
	require.True(t, window, "premise: the watch created the store again")
	require.False(t, during, "a store with no durable yet acknowledged a device publish")

	info := b.storeInfo()
	require.Equal(t, []string{mqttMsgsSubjectPrefix + ">"}, info.Config.Subjects,
		"the pass left the store on the pending subject")
	require.Equal(t, nats.DiscardNew, info.Config.Discard)
	require.EqualValues(t, 1, testutil.ToFloat64(m.storeRepaired.WithLabelValues("store")))
	require.EqualValues(t, 0, testutil.ToFloat64(m.storeRepaired.WithLabelValues("subjects")),
		"the subjects a pass created the store with were counted as a change made out of band")

	// The device's session sends the packet it had no PUBACK for again, and the durable has it.
	connectGuardClient(t, b.mqttURL(), clientID, session, false)
	events := fcDurable(t, guardInstance, MqttFailClosedEvents)
	got := guardDrain(t, b.js, events.Name, 20*time.Second, func(got []string) bool { return containsString(got, "during") })
	require.Contains(t, got, "during", "the resend after the window was not stored")
}

// A store left on the pending subject by a pass that stopped before finishing refuses every QoS 1
// publish, and with no mark on it nobody would finish it. A pod with the setting off gives it the
// gateway's subjects back, but only once it is older than any pass could still be running for.
//
// GUARD: kills an off pod that repairs a store a pass is still finishing (the fresh store is
// changed), and one that never repairs an abandoned one.
func TestAnAbandonedPendingStoreGetsTheGatewaysSubjectsBack(t *testing.T) {
	fastMqttStoreLookup(t)
	b := startGuardBroker(t)
	_, err := b.js.AddStream(&nats.StreamConfig{Name: MqttMessageStore, Subjects: []string{mqttFailClosedPendingSubject},
		Storage: nats.FileStorage, Retention: nats.InterestPolicy, MaxBytes: fcHotBytes})
	require.NoError(t, err)
	off := failClosedManager(t, b.nc, b.js, guardInstance, false)
	n := off.Microservice.InstanceConfiguration.Infrastructure.Nats

	require.NoError(t, off.ReconcileMqttStores(context.Background(), n.MqttStoreMaxBytes, n.MqttQoS2StoreMaxBytes))
	require.Equal(t, []string{mqttFailClosedPendingSubject}, b.storeInfo().Config.Subjects,
		"a store a pass may still be finishing was changed")
	off.checkMqttStoreBounds(context.Background(), nil, mqttWatchBounds(n.MqttStoreMaxBytes, n.MqttQoS2StoreMaxBytes))
	require.Equal(t, []string{mqttFailClosedPendingSubject}, b.storeInfo().Config.Subjects,
		"the bound watch changed a store a pass may still be finishing")

	saved := mqttFailClosedPendingAbandon
	mqttFailClosedPendingAbandon = 0
	t.Cleanup(func() { mqttFailClosedPendingAbandon = saved })
	off.checkMqttStoreBounds(context.Background(), nil, mqttWatchBounds(n.MqttStoreMaxBytes, n.MqttQoS2StoreMaxBytes))
	require.Equal(t, []string{mqttMsgsSubjectPrefix + ">"}, b.storeInfo().Config.Subjects,
		"the bound watch left an abandoned store refusing every publish")

	cfg := b.storeInfo().Config
	cfg.Subjects = []string{mqttFailClosedPendingSubject}
	_, err = b.js.UpdateStream(&cfg)
	require.NoError(t, err)
	require.NoError(t, off.ReconcileMqttStores(context.Background(), n.MqttStoreMaxBytes, n.MqttQoS2StoreMaxBytes))
	require.Equal(t, []string{mqttMsgsSubjectPrefix + ">"}, b.storeInfo().Config.Subjects,
		"a start left an abandoned store refusing every publish")
}

// Each platform durable's backlog is exported by the watch: what of its kind it has not been
// handed, and what it holds unacknowledged. A durable that cannot be read has its series
// withdrawn rather than left at its last value.
//
// GUARD: kills a sampler that reports the store's whole backlog for every durable (the response
// and report durables hold none of the events), one that does not sample, and one that keeps a
// deleted durable's backlog.
func TestTheWatchExportsEachPlatformDurablesBacklog(t *testing.T) {
	b := startGuardBroker(t)
	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, true)
	_, err := nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	clientID, err := DeviceClientID(guardInstance, guardTenant, "dev1")
	require.NoError(t, err)
	g := connectGuardClient(t, b.mqttURL(), clientID, mqtt.NewMemoryStore(), false)
	for _, p := range []string{"a", "b", "c"} {
		require.True(t, g.publish(guardTopic(guardInstance, guardTenant, "dev1"), p, 10*time.Second))
	}

	m := fcMetrics(guardInstance)
	_, err = nmgr.checkMqttFailClosed(context.Background(), m)
	require.NoError(t, err)
	events := fcDurable(t, guardInstance, MqttFailClosedEvents)
	require.EqualValues(t, 3, testutil.ToFloat64(m.durablePending.WithLabelValues(events.Name)))
	require.EqualValues(t, 0, testutil.ToFloat64(m.durableAckPending.WithLabelValues(events.Name)))
	for _, d := range MqttFailClosedDurables(guardInstance) {
		if d.Kind != MqttFailClosedEvents {
			require.EqualValuesf(t, 0, testutil.ToFloat64(m.durablePending.WithLabelValues(d.Name)),
				"the %s durable was given the events' backlog", d.Kind)
		}
	}

	// Deleted between the pass and the sample: the pass recreates it on its next run, but this
	// sample cannot read it, so its series go. Driven directly, so no pass recreates it first.
	require.NoError(t, b.js.DeleteConsumer(MqttMessageStore, events.Name))
	nmgr.sampleMqttFailClosedDurables(context.Background(), m)
	require.Equal(t, len(MqttFailClosedDurables(guardInstance))-1, testutil.CollectAndCount(m.durablePending),
		"a durable that could not be read kept its backlog")
	require.Equal(t, len(MqttFailClosedDurables(guardInstance))-1, testutil.CollectAndCount(m.durableAckPending))
}
