// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
)

// The reader over the message store and the skip on the direct streams, against a real
// broker with its MQTT gateway on: what the platform takes delivery of with the fail-closed
// store in force, and from which copy.

// fcPublish sends one message at qos from a device client and waits for the broker's answer.
func fcPublish(t *testing.T, c *guardClient, topic string, qos byte, payload string) {
	t.Helper()
	tok := c.c.Publish(topic, qos, false, payload)
	require.True(t, tok.WaitTimeout(10*time.Second), "publishing %q", payload)
	require.NoError(t, tok.Error())
}

// fcRead reads one message within d, failing the test on anything but a message.
func fcRead(t *testing.T, r MessageReader, d time.Duration) Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	msg, err := r.ReadMessage(ctx)
	require.NoError(t, err, "no message within %v", d)
	return msg
}

// fcNothing asserts r hands nothing out within d.
func fcNothing(t *testing.T, r MessageReader, d time.Duration, what string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	msg, err := r.ReadMessage(ctx)
	require.ErrorIsf(t, err, io.EOF, "%s: handed out %q", what, string(msg.Value))
}

func fcDevice(t *testing.T, b *guardBroker, device string) *guardClient {
	t.Helper()
	clientID, err := DeviceClientID(guardInstance, guardTenant, device)
	require.NoError(t, err)
	return connectGuardClient(t, b.mqttURL(), clientID, mqtt.NewMemoryStore(), false)
}

// A store message is handed out as the device published it: its own subject, without the
// store's prefix and without the gateway's header, carrying the epoch of the store it came
// from. A QoS 2 publish, whose direct copy is the one the platform forwards, is acked and
// not handed out; a QoS 0 one is never stored at all.
//
// GUARD: kills a reader that hands out the "$MQTT.msgs." subject (the tenant parse would read
// "msgs" as the tenant), one that leaves the gateway header on what it forwards, one with no
// epoch (no dedup id), and one that forwards QoS 2 store copies (each would arrive twice).
func TestTheStoreReaderHandsOutWhatTheDevicePublished(t *testing.T) {
	b := startGuardBroker(t)
	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, true)
	state, err := nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	r, err := fcEventsReader(t, nmgr)
	require.NoError(t, err)
	t.Cleanup(r.Close)

	c := fcDevice(t, b, "d1")
	topic := guardTopic(guardInstance, guardTenant, "d1")
	fcPublish(t, c, topic, 0, "qos0")
	fcPublish(t, c, topic, 2, "qos2")
	fcPublish(t, c, topic, 1, "qos1")

	msg := fcRead(t, r, 10*time.Second)
	require.Equal(t, "qos1", string(msg.Value), "the QoS 2 store copy was handed out, or the QoS 0 one was stored")
	require.Equal(t, fmt.Sprintf("%s.%s.devices.d1.events", guardInstance, guardTenant), msg.Subject)
	_, has := msg.Headers[mqttGatewayHeader]
	require.False(t, has, "the gateway header is still on the message")
	require.NotZero(t, msg.StreamSeq)
	require.Equal(t, MqttMessageStoreDedupID(guardTenant, state.Epoch, msg.StreamSeq), msg.DedupID,
		"the message carries the dedup id of its tenant, its store's epoch and its sequence")
	require.NoError(t, msg.Ack())
	fcNothing(t, r, time.Second, "after the QoS 1 message")
}

// 🔑 THE READER NEVER CREATES ITS DURABLE: the fail-closed pass owns them, and a released
// store must not have one put back by a reader's self-heal. Built before the durable exists it
// waits, quietly, and binds once the pass has created it; when the durable is removed again it
// waits again, and does not recreate it.
//
// GUARD: kills a store reader that creates its durable (on build, on its first read, or on
// re-binding after the durable went).
func TestTheStoreReaderNeverCreatesItsDurable(t *testing.T) {
	saved := mqttStoreBindRetry
	mqttStoreBindRetry = 100 * time.Millisecond
	t.Cleanup(func() { mqttStoreBindRetry = saved })
	b := startGuardBroker(t)
	_, err := b.js.AddStream(guardStoreConfig(nats.DiscardOld, 0, 1))
	require.NoError(t, err)
	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, true)
	events := fcDurable(t, guardInstance, MqttFailClosedEvents)

	r, err := fcEventsReader(t, nmgr)
	require.NoError(t, err, "a reader is built before its durable exists")
	t.Cleanup(r.Close)
	fcNothing(t, r, time.Second, "before the durable exists")
	_, err = b.js.ConsumerInfo(MqttMessageStore, events.Name)
	require.ErrorIs(t, err, nats.ErrConsumerNotFound, "the reader created its durable")

	_, err = nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	c := fcDevice(t, b, "d1")
	fcPublish(t, c, guardTopic(guardInstance, guardTenant, "d1"), 1, "after")
	msg := fcRead(t, r, 10*time.Second)
	require.Equal(t, "after", string(msg.Value))
	require.NoError(t, msg.Ack())

	require.NoError(t, b.js.DeleteConsumer(MqttMessageStore, events.Name))
	fcNothing(t, r, 3*time.Second, "after the durable was removed")
	_, err = b.js.ConsumerInfo(MqttMessageStore, events.Name)
	require.ErrorIs(t, err, nats.ErrConsumerNotFound, "the reader recreated its durable after it was removed")
}

// 🔑 M1: THE EPOCH IS THE STORE THE MESSAGE CAME FROM, even across a recreation the watch
// makes while the reader holds a fetched batch. Store 1 has messages at sequences 1..n; the
// reader takes the first and has the rest buffered when store 1 is deleted and the watch
// creates store 2, whose messages are at the SAME sequences. No message of store 1 is handed
// out with store 2's epoch, and none of store 2 with store 1's: each id is the store's own, or
// there is none (a duplicate at worst, never a suppression).
//
// GUARD: kills a reader that stamps the epoch it last read rather than the message's own (the
// first store-2 message would carry store 1's epoch, or a buffered store-1 message store 2's,
// and its id would equal a real id of the other store inside the duplicate window).
func TestEachStoreMessageCarriesTheEpochOfTheStoreItCameFrom(t *testing.T) {
	saved := mqttStoreBindRetry
	mqttStoreBindRetry = 100 * time.Millisecond
	t.Cleanup(func() { mqttStoreBindRetry = saved })
	b := startGuardBroker(t)
	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, true)
	first, err := nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	r, err := fcEventsReader(t, nmgr)
	require.NoError(t, err)
	t.Cleanup(r.Close)
	c := fcDevice(t, b, "d1")
	topic := guardTopic(guardInstance, guardTenant, "d1")
	const n = 5
	for i := 0; i < n; i++ {
		fcPublish(t, c, topic, 1, fmt.Sprintf("one-%d", i))
	}
	msg := fcRead(t, r, 10*time.Second)
	require.Equal(t, MqttMessageStoreDedupID(guardTenant, first.Epoch, msg.StreamSeq), msg.DedupID)
	require.Equal(t, "one-0", string(msg.Value))
	require.NotEmpty(t, r.pending, "premise: the rest of store 1's messages are buffered in the reader")

	require.NoError(t, b.js.DeleteStream(MqttMessageStore))
	time.Sleep(5 * time.Millisecond)
	_, err = nmgr.checkMqttFailClosed(context.Background(), fcMetrics(guardInstance))
	require.NoError(t, err)
	second := b.storeInfo().Created
	require.True(t, second.After(first.Epoch), "premise: the watch made a new store")
	c2 := fcDevice(t, b, "d2")
	for i := 0; i < n; i++ {
		fcPublish(t, c2, guardTopic(guardInstance, guardTenant, "d2"), 1, fmt.Sprintf("two-%d", i))
	}

	seenTwo := map[uint64]bool{}
	deadline := time.Now().Add(20 * time.Second)
	for len(seenTwo) < n && time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		msg, err := r.ReadMessage(ctx)
		cancel()
		if errors.Is(err, io.EOF) {
			continue
		}
		require.NoError(t, err)
		switch v := string(msg.Value); {
		case v[:3] == "one":
			require.NotEqualf(t, MqttMessageStoreDedupID(guardTenant, second, msg.StreamSeq), msg.DedupID,
				"%s, from store 1, carries store 2's epoch", v)
		case v[:3] == "two":
			require.Equalf(t, MqttMessageStoreDedupID(guardTenant, second, msg.StreamSeq), msg.DedupID,
				"%s, from store 2, does not carry store 2's id (store 1's epoch, or none)", v)
			seenTwo[msg.StreamSeq] = true
			require.NoError(t, msg.Ack())
		default:
			t.Fatalf("unexpected message %q", v)
		}
	}
	require.Len(t, seenTwo, n, "store 2's messages were not all read")
	for seq := uint64(1); seq <= n; seq++ {
		require.Truef(t, seenTwo[seq], "premise: store 2 reused sequence %d", seq)
	}
}

// fcEventsReader is the reader over the events durable, with inbound-events in place: it
// forwards into that stream, and parks while it cannot measure it, as in the service.
func fcEventsReader(t *testing.T, nmgr *NatsManager) (*MqttStoreReader, error) {
	t.Helper()
	_, err := nmgr.NewWriter(streams.InboundEvents)
	require.NoError(t, err)
	return nmgr.NewMqttMessageStoreReader(MqttFailClosedEvents)
}

// fcDirectReader is a reader of device-reports (a direct stream whose consumers each have
// their own durable) for a manager whose functional area is area.
func fcDirectReader(t *testing.T, b *guardBroker, area string, on bool) (*NatsManager, MessageReader) {
	t.Helper()
	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, on)
	nmgr.Microservice.FunctionalArea = area
	r, err := nmgr.NewReader(streams.DeviceReports)
	require.NoError(t, err)
	return nmgr, r
}

// 🔑 THE SKIP ON A DIRECT STREAM. A device QoS 1 publish lands both in the stream its subject
// names and in the store; with the fail-closed store in force the store copy is the one
// forwarded, so a reader of the direct stream skips a QoS 1 copy above the store's from mark.
// Everything else is handed out: a copy at or below the mark (its store copy may predate the
// durable), a QoS 0 publish (never stored), anything while the store is not fail-closed. A pod
// with the setting off skips nothing until it has seen the store marked for this instance, and
// once the store is released no pod skips.
//
// GUARD: kills a skip that ignores the from mark (a publish from before the durable existed
// would be lost), one that skips QoS 0, one in a never-marked pod with the setting off (a
// change to every install that never turned it on), and one that outlives the release.
func TestADirectCopyIsSkippedOnlyWhileTheStoreHoldsIt(t *testing.T) {
	savedRefresh := mqttStoreViewRefresh
	mqttStoreViewRefresh = 0
	t.Cleanup(func() { mqttStoreViewRefresh = savedRefresh })
	fastMqttStoreLookup(t)
	b := startGuardBroker(t)
	onMgr, onReader := fcDirectReader(t, b, "device-management", true)
	offMgr, offReader := fcDirectReader(t, b, "update-management", false)
	c := fcDevice(t, b, "d1")
	topic := fmt.Sprintf("%s/%s/%s/d1", guardInstance, guardTenant, SubjectDeviceReports)
	expect := func(r MessageReader, who string, want ...string) {
		t.Helper()
		for _, w := range want {
			msg := fcRead(t, r, 10*time.Second)
			require.Equalf(t, w, string(msg.Value), "%s", who)
			require.NoError(t, msg.Ack())
		}
		fcNothing(t, r, 500*time.Millisecond, who)
	}

	fcPublish(t, c, topic, 1, "before")
	_, err := onMgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	fcSettleFromMarks(t, onMgr)
	fcPublish(t, c, topic, 1, "held")
	fcPublish(t, c, topic, 0, "qos0")

	expect(onReader, "a pod with the setting on", "before", "qos0")
	expect(offReader, "a never-marked pod with the setting off", "before", "held", "qos0")
	require.EqualValues(t, 1, onMgr.mqttSkipped.Load())

	// The pod with the setting off sees the store marked (its start or its watch does): from
	// then on it skips too.
	n := offMgr.Microservice.InstanceConfiguration.Infrastructure.Nats
	require.NoError(t, offMgr.ReconcileMqttStores(context.Background(), n.MqttMessageStoreMaxBytes(), n.MqttQoS2StoreMaxBytes))
	require.True(t, offMgr.MqttMessageStoreMarked())
	fcPublish(t, c, topic, 1, "held-2")
	expect(onReader, "a pod with the setting on")
	expect(offReader, "a marked pod with the setting off")

	// Released: no pod skips.
	offMgr.mqttClock = func() time.Time { return time.Now().Add(time.Hour) }
	require.NoError(t, offMgr.ReconcileMqttStores(context.Background(), n.MqttMessageStoreMaxBytes(), n.MqttQoS2StoreMaxBytes))
	require.Equal(t, guardInstance, b.storeInfo().Config.Metadata[mqttFailClosedReleasedKey], "premise: released")
	fcPublish(t, c, topic, 1, "after-release")
	expect(onReader, "a pod with the setting on, after the release", "after-release")
	expect(offReader, "a pod with the setting off, after the release", "after-release")
}

// A look that is stale and cannot be refreshed skips nothing: being unsure costs a duplicate,
// never a loss.
//
// GUARD: kills a skip decided on a look older than mqttStoreViewTTL.
func TestAStaleLookAtTheStoreSkipsNothing(t *testing.T) {
	ms := &core.Microservice{InstanceId: guardInstance}
	ms.InstanceConfiguration.Infrastructure.Nats.MqttFailClosed = true
	nmgr := &NatsManager{Microservice: ms}
	v := nmgr.mqttView()
	fresh := mqttStoreSnapshot{at: time.Now(), present: true, owned: true, marked: true,
		from: map[string]uint64{streams.DeviceEventsCapture: 10}}
	v.snap = fresh
	require.True(t, v.carriedByStore(streams.DeviceEventsCapture, "1", 11), "control: a fresh look skips")
	require.False(t, v.carriedByStore(streams.DeviceEventsCapture, "1", 10), "at the mark is not above it")
	require.False(t, v.carriedByStore(streams.DeviceEventsCapture, "0", 11), "QoS 0 is never stored")
	require.False(t, v.carriedByStore(streams.DeviceEventsCapture, "", 11), "a NATS publish has no store copy")
	stale := fresh
	stale.at = time.Now().Add(-mqttStoreViewTTL)
	v.snap = stale
	require.False(t, v.carriedByStore(streams.DeviceEventsCapture, "1", 11), "a stale look skipped a copy")
}

// The subject check an unfiltered store reader runs on every message it is delivered: single
// token wildcards only, every token present, nothing more and nothing less.
//
// GUARD: kills a matcher that accepts a prefix (a longer subject of another kind), a shorter
// subject, an empty token, or a different literal.
func TestStoreSubjectMatchesIsATokenMatch(t *testing.T) {
	events := mqttMsgsSubjectPrefix + DeviceEventsWildcard("inst")
	for subj, want := range map[string]bool{
		mqttMsgsSubjectPrefix + DeviceEventsSubject("inst", "t1", "d1"):                          true,
		mqttMsgsSubjectPrefix + DeviceEventsSubject("inst", "t2", "d-9"):                         true,
		mqttMsgsSubjectPrefix + DeviceEventsSubject("other", "t1", "d1"):                         false,
		mqttMsgsSubjectPrefix + DeviceEventsSubject("inst", "t1", "d1") + ".x":                   false,
		mqttMsgsSubjectPrefix + "inst.t1.devices.d1":                                             false,
		mqttMsgsSubjectPrefix + "inst..devices.d1.events":                                        false,
		mqttMsgsSubjectPrefix + DeviceScopedSubject("inst", "t1", SubjectCommandResponses, "d1"): false,
		"inst.t1.devices.d1.events":                                                              false,
		"":                                                                                       false,
	} {
		require.Equalf(t, want, storeSubjectMatches(events, subj), "%q against %q", subj, events)
	}
	require.True(t, storeSubjectMatches("a.*.c", "a.b.c"))
	require.False(t, storeSubjectMatches("a.*.c", "a.b"))
	require.False(t, storeSubjectMatches("a.*", "a.b.c"))
}

// 🔴 THE EVENTS DURABLE IS UNFILTERED, and a store whose events durable still has the filter an
// earlier build gave it is moved onto the new one by the reconcile, in place (same durable,
// same position). A wildcard filter over the store makes the broker visit every device's
// subject for each message it delivers to a reader that has fallen a block behind
// (MqttFailClosedDurable.Filter); measured, it caps the reader at a tenth to a quarter of
// its unfiltered rate on a three-node cluster, which is the throughput cliff a backlog then
// does not recover from.
//
// GUARD: kills a durable set that filters events again, and a reconcile that leaves an existing
// durable on its old filter.
func TestTheEventsDurableIsUnfilteredAndAnOldFilterIsMoved(t *testing.T) {
	ev := fcDurable(t, "inst", MqttFailClosedEvents)
	require.Empty(t, ev.Filter, "the events durable is unfiltered")
	require.Equal(t, mqttMsgsSubjectPrefix+DeviceEventsWildcard("inst"), ev.Forwards)
	for _, d := range MqttFailClosedDurables("inst") {
		if d.Kind != MqttFailClosedEvents {
			require.Equalf(t, d.Forwards, d.Filter, "the %s durable is filtered to its kind", d.Kind)
		}
	}

	b := startGuardBroker(t)
	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, true)
	_, err := nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	name := fcDurable(t, guardInstance, MqttFailClosedEvents).Name
	ci, err := b.js.ConsumerInfo(MqttMessageStore, name)
	require.NoError(t, err)
	cfg := ci.Config
	cfg.FilterSubject = mqttMsgsSubjectPrefix + DeviceEventsWildcard(guardInstance)
	_, err = b.js.UpdateConsumer(MqttMessageStore, &cfg)
	require.NoError(t, err, "premise: a durable carrying the earlier filter")

	_, err = nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	after, err := b.js.ConsumerInfo(MqttMessageStore, name)
	require.NoError(t, err)
	require.Empty(t, after.Config.FilterSubject, "the reconcile moved the durable onto no filter")
	require.Equal(t, ci.Created, after.Created, "moved in place, not recreated")
}
