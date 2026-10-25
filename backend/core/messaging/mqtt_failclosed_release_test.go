// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// THE RELEASE AND ITS FINISH, AND THE LOCK THEY ARE MADE UNDER, against the real broker.

// 🔴 A RELEASE EVICTS NOTHING. The durables of a released store hold the only copies of the
// direct copies pods with the setting on skipped. Discard old, or the setting-off bound (256
// MiB by default, below the hot ceiling a fail-closed store takes), would evict the oldest of
// them the moment the release is written: messages the devices were PUBACKed on. The control
// is the default bound, which the backlog here does not reach.
//
// GUARD: kills a release that puts discard old or the lower bound back before the durables
// are drained.
func TestAReleaseKeepsEveryStoreCopyTheDurablesHold(t *testing.T) {
	for _, tc := range []struct {
		name    string
		offCap  int64
		control bool
	}{
		{"control: the default off bound", 0, true},
		{"off bound below the backlog", 4 << 10, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			pad := strings.Repeat("x", 900)
			const n = 12
			for i := 0; i < n; i++ {
				require.True(t, c.publish(guardTopic(guardInstance, guardTenant, "d1"), fmt.Sprintf("%02d%s", i, pad), 10*time.Second))
			}
			ev := fcDurable(t, guardInstance, MqttFailClosedEvents).Name
			requireStoreHolds(t, b, ev, n, "premise: every PUBACKed copy is held")

			off := failClosedManager(t, b.nc, b.js, guardInstance, false)
			off.mqttClock = clock.now
			if tc.offCap != 0 {
				off.Microservice.InstanceConfiguration.Infrastructure.Nats.MqttStoreMaxBytes = tc.offCap
			}
			clock.advance(mqttFailClosedLeaseTTL)
			rep, err := off.checkMqttFailClosed(context.Background(), nil)
			require.NoError(t, err)
			require.True(t, rep.released, "premise: the store was released")

			requireStoreHolds(t, b, ev, n, fmt.Sprintf(
				"the release evicted PUBACKed store copies the durable still held (store now %d bytes, cap %d)",
				b.storeInfo().State.Bytes, b.storeInfo().Config.MaxBytes))
		})
	}
}

// requireStoreHolds fails unless the message store holds n messages and the durable counts
// them all as its own to deliver.
//
// 🔑 THE STORE IS ASKED FIRST, AND WITHOUT WAITING; THE DURABLE'S COUNT IS WAITED FOR. A
// PUBACK is sent once the store has the message, so the store's own message count is exact
// when the last publish returns, and it is the promise under test: an eviction (a discard
// old, a lower bound) changes it at once. The durable's NumPending is not that: the broker
// replies to the publish BEFORE it tells the stream's consumers of the new message, and does
// so on a goroutine of its own (nats-server 2.15 stream.go: the PubAck, then sigq.push, drained
// by signalConsumersLoop), so a count read right after the last PUBACK can be behind by the
// messages that goroutine has not reached. CI read 9 of 12 that way, on a run that had not
// released anything yet. The wait below is for that goroutine only: a message the store does not
// hold is failed by the first check, and the wait is bounded by far more than the lag.
func requireStoreHolds(t *testing.T, b *guardBroker, durable string, n int, msg string) {
	t.Helper()
	require.EqualValues(t, n, b.storeInfo().State.Msgs, "the store itself: "+msg)
	deadline := time.Now().Add(10 * time.Second)
	for {
		ci, err := b.js.ConsumerInfo(MqttMessageStore, durable)
		if err == nil && ci.NumPending == uint64(n) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the durable never counted all %d held copies (last: %+v, %v): %s", n, ci, err, msg)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 🔑 THE LOCK IS FENCED. The locks bucket's entry lifetime is whatever the service that created
// the bucket chose, and in an install that is user-management, at 5 s: shorter than a pass
// that retries. Here a pod with the setting off decides, under the lock, to release a store
// whose holder's heartbeat is stale, and then stalls past the entry's lifetime. The holder's
// pass takes the expired lock and renews its heartbeat. The stalled pass must then write
// nothing: its renewal before the write fails. The control runs the same interleaving in a
// bucket this file created (30 s), where the holder cannot take the lock and the release goes
// through: the scenario is one that writes.
//
// GUARD: kills a pass that writes on a lock it no longer holds (a release made over a holder
// that is alive, or durables deleted under a take-over).
func TestTheFailClosedLockOutlivesTheLocksBucketTTL(t *testing.T) {
	for _, tc := range []struct {
		name      string
		preCreate bool
	}{
		{"bucket created by user-management first", true},
		{"control: bucket created by the fail-closed lock", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFcFleet(t)
			if tc.preCreate {
				_, err := failClosedManager(t, f.b.nc, f.b.js, guardInstance, false).NewDistributedLock(5 * time.Second)
				require.NoError(t, err)
			}
			holder := f.start("A(on)", true, 0)
			off := f.start("B(off)", false, 0)
			f.clock.advance(mqttFailClosedLeaseTTL)
			renewed := false
			off.nmgr.mqttPassHook = func(point string) {
				if point != "locked" || renewed {
					return
				}
				for deadline := time.Now().Add(9 * time.Second); time.Now().Before(deadline) && !renewed; {
					rep, err := holder.nmgr.mqttFailClosedPass(context.Background(), false)
					require.NoError(t, err)
					renewed = rep.standing == mqttStandingHolder
					time.Sleep(250 * time.Millisecond)
				}
			}
			rep, err := off.nmgr.mqttFailClosedPass(context.Background(), false)
			si := f.store()
			if tc.preCreate {
				require.True(t, renewed, "premise: the holder took the expired lock and renewed its heartbeat")
				require.ErrorIs(t, err, errMqttFailClosedLockLost)
				require.Equal(t, guardInstance, si.Config.Metadata[mqttFailClosedOwnerKey],
					"a pass whose lock had expired released the store over its live holder")
			} else {
				require.False(t, renewed, "control: the holder could not take a lock that was held")
				require.NoError(t, err)
				require.True(t, rep.released, "control: the release is made")
			}
		})
	}
}

// A released store's durables are removed while devices keep publishing. A durable devices
// publish into is never empty for long at once, however well its reader keeps up, so "drained"
// is acked past the drain cut, not "holds nothing": otherwise every QoS 1 publish would be
// forwarded twice (the direct copy, no longer skipped, and the store copy) for as long as the
// traffic never paused.
//
// GUARD: kills a finish that waits for a durable to hold nothing.
func TestAReleasedStoreFinishesUnderSteadyLoad(t *testing.T) {
	fastMqttStoreLookup(t)
	b := startGuardBroker(t)
	clock := newFcClock()
	on := failClosedManager(t, b.nc, b.js, guardInstance, true)
	on.mqttClock = clock.now
	_, err := on.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	off := failClosedManager(t, b.nc, b.js, guardInstance, false)
	off.mqttClock = clock.now
	clock.advance(mqttFailClosedLeaseTTL)
	rep, err := off.checkMqttFailClosed(context.Background(), nil)
	require.NoError(t, err)
	require.True(t, rep.released)
	clock.advance(mqttFailClosedReleaseGrace)

	// A reader that keeps up with steady traffic, as a marked off pod's store ingest does.
	ev := fcDurable(t, guardInstance, MqttFailClosedEvents)
	sub, err := b.js.PullSubscribe(ev.Filter, ev.Name, nats.Bind(MqttMessageStore, ev.Name))
	require.NoError(t, err)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var read atomic.Int64
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			msgs, _ := sub.Fetch(64, nats.MaxWait(200*time.Millisecond))
			for _, m := range msgs {
				_ = m.AckSync()
				read.Add(1)
			}
		}
	}()
	clientID, err := DeviceClientID(guardInstance, guardTenant, "d1")
	require.NoError(t, err)
	c := connectGuardClient(t, b.mqttURL(), clientID, mqtt.NewMemoryStore(), false)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			tok := c.c.Publish(guardTopic(guardInstance, guardTenant, "d1"), 1, false, fmt.Sprintf("p%d", i))
			tok.WaitTimeout(5 * time.Second)
		}
	}()
	defer func() { close(stop); wg.Wait(); _ = sub.Unsubscribe() }()

	finished := false
	for i := 0; i < 40 && !finished; i++ {
		time.Sleep(250 * time.Millisecond)
		rep, err := off.checkMqttFailClosed(context.Background(), nil)
		require.NoError(t, err)
		finished = rep.finished
	}
	t.Logf("read %d store copies during the check", read.Load())
	require.True(t, finished, "a released store under steady traffic never finished: its durables stay, and "+
		"every QoS 1 publish is forwarded from both copies")
}
