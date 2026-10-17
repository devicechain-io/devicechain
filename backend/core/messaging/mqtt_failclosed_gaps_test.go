// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// 🔑 A STALE LOOK NAMES NO EPOCH FOR A MESSAGE STORED AFTER IT. The reader's last look at the
// store can be of an incarnation that has since been deleted and created again (the watch has
// not passed yet, or another pod made the new one). A message the new incarnation stored is
// newer than that look's last message, so the look cannot vouch for it: a new look is taken,
// and the message carries the new incarnation's epoch. Taking the old look's creation time
// instead would give a new sequence the OLD epoch, an id the old incarnation's message of the
// same sequence already used, and the dedup window would drop it without trace.
//
// GUARD: kills an epoch window with no upper bound (a look vouching for messages stored
// after it).
func TestAStaleLookDoesNotLendItsEpochToANewerIncarnation(t *testing.T) {
	b := startGuardBroker(t)
	nmgr := failClosedManager(t, b.nc, b.js, guardInstance, true)
	_, err := nmgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	c := fcDevice(t, b, "d1")
	fcPublish(t, c, guardTopic(guardInstance, guardTenant, "d1"), 1, "old")
	old, err := nmgr.mqttView().refresh(time.Now())
	require.NoError(t, err)
	require.False(t, old.lastTime.IsZero(), "premise: the look saw the old incarnation's message")

	// A new incarnation the view has not looked at, with a message of its own.
	require.NoError(t, b.js.DeleteStream(MqttMessageStore))
	time.Sleep(5 * time.Millisecond)
	_, err = b.js.AddStream(guardStoreConfig(nats.DiscardOld, 0, 1))
	require.NoError(t, err)
	require.NoError(t, guardAddDurable(b.js, "review-epoch", guardFilter(guardInstance), 0))
	c2 := fcDevice(t, b, "d2")
	fcPublish(t, c2, guardTopic(guardInstance, guardTenant, "d2"), 1, "new")
	si := b.storeInfo()
	require.True(t, si.Created.After(old.created), "premise: a new incarnation")
	m, err := b.js.GetMsg(MqttMessageStore, si.State.LastSeq)
	require.NoError(t, err)
	require.Equal(t, old.created, nmgr.mqttView().current().created, "premise: the view's look is still the old one")

	require.Equal(t, si.Created, nmgr.mqttView().epochOf(m.Time),
		"a message of the new incarnation was given the stale look's epoch")
}

// A QoS 2 publish is delivered after its PUBREL down a path that does not wait on the store,
// so its direct copy is the one the platform forwards (the store reader drops the store copy
// of it). A reader of a direct stream therefore hands a QoS 2 copy out even while the store is
// held fail-closed.
//
// GUARD: kills a direct-copy skip that takes QoS 2 for QoS 1: the publish would reach the
// platform from neither copy.
func TestADirectQoS2CopyIsNeverSkipped(t *testing.T) {
	savedRefresh := mqttStoreViewRefresh
	mqttStoreViewRefresh = 0
	t.Cleanup(func() { mqttStoreViewRefresh = savedRefresh })
	fastMqttStoreLookup(t)
	b := startGuardBroker(t)
	onMgr, onReader := fcDirectReader(t, b, "device-management", true)
	_, err := onMgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	c := fcDevice(t, b, "d1")
	topic := fmt.Sprintf("%s/%s/%s/d1", guardInstance, guardTenant, SubjectDeviceReports)
	fcPublish(t, c, topic, 2, "qos2")
	msg := fcRead(t, onReader, 10*time.Second)
	require.Equal(t, "qos2", string(msg.Value), "the QoS 2 direct copy was skipped")
	require.NoError(t, msg.Ack())
	require.Zero(t, onMgr.mqttSkipped.Load(), "a QoS 2 direct copy was skipped")
}

// fcSettleFromMarks lets every platform durable count as settled, for the rest of the test,
// and runs the holding pass that then records the from marks a take-over leaves out.
func fcSettleFromMarks(t *testing.T, nmgr *NatsManager) {
	t.Helper()
	saved := mqttFailClosedFromSettle
	mqttFailClosedFromSettle = 0
	t.Cleanup(func() { mqttFailClosedFromSettle = saved })
	rep, err := nmgr.mqttFailClosedPass(context.Background(), false)
	require.NoError(t, err)
	require.Empty(t, rep.pendingFrom, "premise: every kind has its from mark")
	info, err := nmgr.js.StreamInfo(MqttMessageStore)
	require.NoError(t, err)
	require.Len(t, mqttFailClosedFromMarks(info.Config.Metadata), len(mqttFailClosedDirectSuffix))
}

// A durable the pass has to create again (it was deleted out of band) loses its kind's from
// mark until it has settled. While it was missing the store kept no copy of what the device
// published, so a direct copy from then is the only one; keeping the old, lower mark would have
// every reader skip those copies, and so would a mark read in the same pass that re-created it
// (see TestADirectCopyInFlightAcrossTheDurablesCreationIsNotSkipped). Once settled, the next
// pass reads the mark afresh.
//
// GUARD: kills a pass that keeps a from mark across the re-creation of its durable, and one
// that never records it again.
func TestARecreatedDurableReadsItsFromMarkAgain(t *testing.T) {
	fastMqttStoreLookup(t)
	b := startGuardBroker(t)
	onMgr, _ := fcDirectReader(t, b, "device-management", true)
	_, err := onMgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	fcSettleFromMarks(t, onMgr)
	reports := fcDurable(t, guardInstance, MqttFailClosedReports)
	require.NoError(t, b.js.DeleteConsumer(MqttMessageStore, reports.Name))
	c := fcDevice(t, b, "d1")
	topic := fmt.Sprintf("%s/%s/%s/d1", guardInstance, guardTenant, SubjectDeviceReports)
	for i := 0; i < 3; i++ {
		fcPublish(t, c, topic, 1, fmt.Sprintf("unheld-%d", i))
	}
	ds, err := b.js.StreamInfo(StreamName(guardInstance, SubjectDeviceReports))
	require.NoError(t, err)
	require.EqualValues(t, 3, ds.State.LastSeq, "premise: the direct copies landed")

	rep, err := onMgr.checkMqttFailClosed(context.Background(), fcMetrics(guardInstance))
	require.NoError(t, err)
	require.Contains(t, rep.createdDurables, reports.Name, "premise: the pass created the durable again")
	require.Contains(t, rep.pendingFrom, reports.Name)
	require.NotContains(t, b.storeInfo().Config.Metadata, mqttFailClosedFromKeyPrefix+SubjectDeviceReports,
		"the re-created durable's kind kept a from mark: the unheld direct copies would be skipped")

	rep, err = onMgr.checkMqttFailClosed(context.Background(), fcMetrics(guardInstance))
	require.NoError(t, err)
	require.Empty(t, rep.pendingFrom)
	require.Equal(t, strconv.FormatUint(ds.State.LastSeq, 10),
		b.storeInfo().Config.Metadata[mqttFailClosedFromKeyPrefix+SubjectDeviceReports],
		"the settled durable's from mark was not read afresh")
}

// 🔴 A DIRECT COPY IN FLIGHT ACROSS THE DURABLE'S CREATION. The gateway sends a publish's direct
// copy before its store copy, to a different stream, so the store copy can be skipped for no
// interest (the durable not yet applied) while the direct copy, not yet given a sequence, lands
// AFTER the pass read the direct stream's last sequence. That copy is the only one. Here it is
// the gateway-shaped message published straight onto the direct stream once the durables exist,
// with no store copy at all.
//
// A take-over therefore leaves each kind without a from mark until its durable has existed for
// mqttFailClosedFromSettle (by the broker's clock), and a reader skips nothing of a kind without
// one. A pass made before then, by this pod or any other, records nothing either.
//
// GUARD: kills a from mark read in the pass that creates the durables (the in-flight copy is
// above it and skipped: lost), and an age read off anything but the durable itself.
func TestADirectCopyInFlightAcrossTheDurablesCreationIsNotSkipped(t *testing.T) {
	savedRefresh := mqttStoreViewRefresh
	mqttStoreViewRefresh = 0
	t.Cleanup(func() { mqttStoreViewRefresh = savedRefresh })
	fastMqttStoreLookup(t)
	b := startGuardBroker(t)
	onMgr, onReader := fcDirectReader(t, b, "device-management", true)
	state, err := onMgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
	require.Equal(t, mqttStandingHolder, state.Standing)
	require.Empty(t, mqttFailClosedFromMarks(b.storeInfo().Config.Metadata),
		"the take-over recorded from marks in the pass that created the durables")

	// A second pass at once, as another pod's start would make: the durables are seconds old.
	rep, err := onMgr.mqttFailClosedPass(context.Background(), false)
	require.NoError(t, err)
	require.Len(t, rep.pendingFrom, len(mqttFailClosedDirectSuffix), "an unsettled durable's kind got a from mark")
	age, err := onMgr.mqttDurableAge(context.Background(), fcDurable(t, guardInstance, MqttFailClosedReports).Name)
	require.NoError(t, err)
	require.GreaterOrEqual(t, age, time.Duration(0))
	require.Less(t, age, mqttFailClosedFromSettle, "premise: the broker reports the durable as just created")

	// The in-flight direct copy: gateway header, QoS 1, no store copy.
	subject := DeviceScopedSubject(guardInstance, guardTenant, SubjectDeviceReports, "d1")
	inflight := nats.NewMsg(subject)
	inflight.Header.Set(mqttGatewayHeader, "1")
	inflight.Data = []byte("in-flight")
	_, err = b.js.PublishMsg(inflight)
	require.NoError(t, err)
	msg := fcRead(t, onReader, 10*time.Second)
	require.Equal(t, "in-flight", string(msg.Value), "the only copy of an in-flight publish was skipped")
	require.NoError(t, msg.Ack())
	require.Zero(t, onMgr.mqttSkipped.Load())

	// Settled: the next pass records the marks, and from then on a QoS 1 direct copy is skipped
	// (its store copy is forwarded instead).
	fcSettleFromMarks(t, onMgr)
	c := fcDevice(t, b, "d1")
	fcPublish(t, c, fmt.Sprintf("%s/%s/%s/d1", guardInstance, guardTenant, SubjectDeviceReports), 1, "held")
	fcNothing(t, onReader, time.Second, "a settled kind")
	require.EqualValues(t, 1, onMgr.mqttSkipped.Load(), "control: once settled, the direct copy is skipped")
}
