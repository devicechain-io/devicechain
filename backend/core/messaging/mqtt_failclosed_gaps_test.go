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

// A durable the pass has to create again (it was deleted out of band) gets its direct
// stream's from mark READ AGAIN. While it was missing the store kept no copy of what the
// device published, so a direct copy from then is the only one; keeping the old, lower mark
// would have every reader skip those copies too.
//
// GUARD: kills a pass that keeps a from mark across the re-creation of its durable.
func TestARecreatedDurableReadsItsFromMarkAgain(t *testing.T) {
	fastMqttStoreLookup(t)
	b := startGuardBroker(t)
	onMgr, _ := fcDirectReader(t, b, "device-management", true)
	_, err := onMgr.ReconcileMqttFailClosed(context.Background())
	require.NoError(t, err)
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
	require.Equal(t, strconv.FormatUint(ds.State.LastSeq, 10),
		b.storeInfo().Config.Metadata[mqttFailClosedFromKeyPrefix+SubjectDeviceReports],
		"the from mark predates the re-created durable: the unheld direct copies would be skipped")
}
