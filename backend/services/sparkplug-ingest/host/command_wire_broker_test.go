// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"strings"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/devicechain-io/dc-sparkplug-ingest/codec"
	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
)

// These tests drive the REAL Client over a REAL paho connection to the embedded broker, and read
// what a Sparkplug node would receive: the topic, the QoS and retain flag the BROKER delivers,
// and the payload bytes. publishCommand is tested through the caller that uses it
// (SendMetricCommand, publishRebirth), not as a bare function.

type wireMsg struct {
	topic    string
	qos      byte
	retained bool
	payload  []byte
}

// wireWatcher subscribes (QoS 1, so a QoS-0 publish is not masked by a downgrade) to filter and
// records what arrives.
type wireWatcher struct {
	mu   sync.Mutex
	msgs []wireMsg
}

func (w *wireWatcher) all() []wireMsg {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]wireMsg(nil), w.msgs...)
}

func watchWire(t *testing.T, url, clientID, filter string) *wireWatcher {
	t.Helper()
	cl := mqtt.NewClient(mqtt.NewClientOptions().AddBroker(url).SetClientID(clientID))
	tok := cl.Connect()
	require.True(t, tok.WaitTimeout(15*time.Second), "watcher connect timed out")
	require.NoError(t, tok.Error())
	t.Cleanup(func() { cl.Disconnect(100) })
	w := &wireWatcher{}
	//subconfirm:ok the watcher's subscription is confirmed on the next line
	sub := cl.Subscribe(filter, 1, func(_ mqtt.Client, m mqtt.Message) {
		if !strings.Contains(m.Topic(), "/NCMD/") && !strings.Contains(m.Topic(), "/DCMD/") {
			return // births and data the feeder publishes are not what this watches
		}
		w.mu.Lock()
		w.msgs = append(w.msgs, wireMsg{m.Topic(), m.Qos(), m.Retained(), append([]byte(nil), m.Payload()...)})
		w.mu.Unlock()
	})
	require.True(t, sub.WaitTimeout(15*time.Second), "watcher subscribe timed out")
	require.NoError(t, sub.Error())
	return w
}

const (
	wireNode   = "node-3"
	wireNBirth = "spBv1.0/plant-a/NBIRTH/" + wireNode
	wireDBirth = "spBv1.0/plant-a/DBIRTH/" + wireNode + "/pump-1"
)

var (
	wireNodeT = CommandTarget{Group: "plant-a", Node: wireNode}
	wireDevT  = CommandTarget{Group: "plant-a", Node: wireNode, Device: "pump-1"}
)

func encodeT(t *testing.T, p *sppb.Payload) []byte {
	t.Helper()
	b, err := codec.Encode(p)
	require.NoError(t, err)
	return b
}

// nbirthWire declares an aliased Int8 "Setpoint", an un-aliased String "Mode", and the reserved
// Rebirth metric (which a node must not alias).
func nbirthWire(t *testing.T, setpointAlias uint64) []byte {
	return encodeT(t, &sppb.Payload{Seq: proto.Uint64(0), Metrics: []*sppb.Payload_Metric{
		bdSeqM(7),
		{Name: proto.String("Setpoint"), Alias: proto.Uint64(setpointAlias), Datatype: proto.Uint32(datatypeInt8),
			Value: &sppb.Payload_Metric_IntValue{IntValue: 1}},
		{Name: proto.String("Mode"), Datatype: proto.Uint32(datatypeString), Value: &sppb.Payload_Metric_StringValue{StringValue: "auto"}},
		{Name: proto.String("Node Control/Rebirth"), Datatype: proto.Uint32(datatypeBoolean), Value: &sppb.Payload_Metric_BooleanValue{BooleanValue: false}},
	}})
}

func dbirthWire(t *testing.T) []byte {
	return encodeT(t, &sppb.Payload{Seq: proto.Uint64(1), Metrics: []*sppb.Payload_Metric{
		{Name: proto.String("Run"), Alias: proto.Uint64(50), Datatype: proto.Uint32(datatypeBoolean), Value: &sppb.Payload_Metric_BooleanValue{BooleanValue: false}},
	}})
}

// onlineWithBirths brings a Client up and feeds it the node's and device's births, returning it
// once the births are in the metric table.
func onlineWithBirths(t *testing.T, rig *spRig, setpointAlias uint64) (*Client, *mqttFeeder) {
	t.Helper()
	c := rig.client(rig.writer, newSpMetrics())
	rig.online(c)
	t.Cleanup(func() { stopWithin(t, c, 30*time.Second) })
	feed := rig.feeder()
	feed.publish(wireNBirth, nbirthWire(t, setpointAlias))
	feed.publish(wireDBirth, dbirthWire(t))
	require.Eventually(t, func() bool {
		_, e1 := c.sessions.LookupMetric(wireNodeT, "Setpoint")
		_, e2 := c.sessions.LookupMetric(wireDevT, "Run")
		return e1 == nil && e2 == nil
	}, 20*time.Second, 5*time.Millisecond, "the births never reached the metric table")
	return c, feed
}

func awaitMsgs(t *testing.T, w *wireWatcher, n int) []wireMsg {
	t.Helper()
	require.Eventually(t, func() bool { return len(w.all()) >= n }, 20*time.Second, 5*time.Millisecond,
		"expected %d command message(s) on the wire, saw %d", n, len(w.all()))
	return w.all()
}

// writes is what the watcher saw that is not a Rebirth request: a gap the test provokes makes the
// Host ask for one, and that is not the command under test.
func (w *wireWatcher) writes(t *testing.T) []wireMsg {
	var out []wireMsg
	for _, m := range w.all() {
		p, err := codec.Decode(m.payload)
		require.NoError(t, err)
		if p.GetMetrics()[0].GetName() != nodeControlRebirthMetric {
			out = append(out, m)
		}
	}
	return out
}

func payloadOf(t *testing.T, m wireMsg) *sppb.Payload {
	t.Helper()
	p, err := codec.Decode(m.payload)
	require.NoError(t, err)
	return p
}

// 🔴 What a node RECEIVES, off a real broker: the topic for node vs device, QoS 0 and not retained
// as DELIVERED (the watcher subscribes at QoS 1, so a QoS-1 publish would arrive as 1), a payload
// timestamp, no seq, the datatype set, and the alias-only / name-only addressing.
//
// Cites: [tck-id-topics-ncmd-topic] [tck-id-topics-dcmd-topic] [tck-id-topics-ncmd-mqtt]
// [tck-id-topics-dcmd-mqtt] [tck-id-payloads-ncmd-qos] [tck-id-payloads-ncmd-retain]
// [tck-id-payloads-dcmd-qos] [tck-id-payloads-dcmd-retain] [tck-id-payloads-ncmd-timestamp]
// [tck-id-payloads-dcmd-timestamp] [tck-id-payloads-ncmd-seq] [tck-id-payloads-dcmd-seq]
// [tck-id-payloads-alias-data-cmd-requirement]
// [tck-id-operational-behavior-data-commands-rebirth-name-aliases].
func TestCommandsOnTheWire(t *testing.T) {
	rig := newSpRig(t, spWindow, 0)
	c, _ := onlineWithBirths(t, rig, 11)
	w := watchWire(t, rig.mqttURL, "cmd-watch", "spBv1.0/plant-a/#")

	// Aliased node metric: alias only, no name.
	ref, err := c.sessions.LookupMetric(wireNodeT, "Setpoint")
	require.NoError(t, err)
	_, _, err = c.SendMetricCommand(ref, -2)
	require.NoError(t, err)
	// Un-aliased node metric: name only.
	ref, err = c.sessions.LookupMetric(wireNodeT, "Mode")
	require.NoError(t, err)
	_, _, err = c.SendMetricCommand(ref, "manual")
	require.NoError(t, err)
	// Aliased device metric on the DCMD topic.
	ref, err = c.sessions.LookupMetric(wireDevT, "Run")
	require.NoError(t, err)
	_, _, err = c.SendMetricCommand(ref, true)
	require.NoError(t, err)
	// Rebirth: by name, even though this node aliases other metrics.
	require.True(t, c.publishRebirth(wireNodeT.Group, wireNodeT.Node))

	msgs := awaitMsgs(t, w, 4)
	byTopic := map[string][]wireMsg{}
	for _, m := range msgs {
		byTopic[m.topic] = append(byTopic[m.topic], m)
		assert.Equal(t, byte(0), m.qos, "QoS 0 as delivered: %s", m.topic)
		assert.False(t, m.retained, "retain false: %s", m.topic)
		p := payloadOf(t, m)
		assert.NotNil(t, p.Timestamp, "payload timestamp: %s", m.topic)
		assert.Nil(t, p.Seq, "no seq: %s", m.topic)
		require.Len(t, p.GetMetrics(), 1)
		assert.NotNil(t, p.GetMetrics()[0].Datatype, "datatype always set (recorded deviation): %s", m.topic)
	}
	require.Len(t, byTopic["spBv1.0/plant-a/NCMD/node-3"], 3, "setpoint, mode, rebirth on the node topic")
	require.Len(t, byTopic["spBv1.0/plant-a/DCMD/node-3/pump-1"], 1, "the device write on the DCMD topic")

	var sawSetpoint, sawMode, sawRebirth bool
	for _, m := range byTopic["spBv1.0/plant-a/NCMD/node-3"] {
		met := payloadOf(t, m).GetMetrics()[0]
		switch {
		case met.Alias != nil && met.GetAlias() == 11:
			sawSetpoint = true
			assert.Nil(t, met.Name, "alias-only: the name is excluded when the BIRTH aliased the metric")
			assert.Equal(t, uint32(0xFFFFFFFE), met.GetIntValue(), "Int8 -2, sign-extended")
		case met.GetName() == "Mode":
			sawMode = true
			assert.Nil(t, met.Alias, "a node that does not alias is addressed by name")
			assert.Equal(t, "manual", met.GetStringValue())
		case met.GetName() == "Node Control/Rebirth":
			sawRebirth = true
			assert.Nil(t, met.Alias)
			assert.True(t, met.GetBooleanValue())
		}
	}
	assert.True(t, sawSetpoint && sawMode && sawRebirth, "setpoint=%v mode=%v rebirth=%v", sawSetpoint, sawMode, sawRebirth)
	dev := payloadOf(t, byTopic["spBv1.0/plant-a/DCMD/node-3/pump-1"][0]).GetMetrics()[0]
	assert.Equal(t, uint64(50), dev.GetAlias())
	assert.Nil(t, dev.Name)
	assert.True(t, dev.GetBooleanValue())

	// Retain, read the other way: a subscriber that arrives AFTER the publishes gets nothing, so
	// the broker kept nothing. The control below proves the watcher can see a retained message at
	// all - without it, "nothing arrived" would be a reading of a broker that retains nothing.
	late := watchWire(t, rig.mqttURL, "cmd-late", "spBv1.0/plant-a/#")
	time.Sleep(500 * time.Millisecond)
	assert.Empty(t, late.all(), "a late subscriber receives nothing: the commands were not retained")

	cl := mqtt.NewClient(mqtt.NewClientOptions().AddBroker(rig.mqttURL).SetClientID("retain-control"))
	require.True(t, cl.Connect().WaitTimeout(15*time.Second))
	t.Cleanup(func() { cl.Disconnect(100) })
	// QoS 1: the broker has PROCESSED it (and retained it) before the late subscriber arrives.
	pub := cl.Publish("spBv1.0/plant-a/NCMD/retained-control", 1, true, []byte{0x08, 0x01})
	require.True(t, pub.WaitTimeout(15*time.Second))
	ctl := watchWire(t, rig.mqttURL, "cmd-late-2", "spBv1.0/plant-a/NCMD/retained-control")
	got := awaitMsgs(t, ctl, 1)
	assert.True(t, got[0].retained, "control: a retained publish IS seen as retained by a late subscriber")
}

// 🔴 EPOCH CONTROL THROUGH THE CALLER. A command is validated against the table, then a rebirth
// lands and the node remaps the alias, then the command is published: the wire must carry the NEW
// alias. The paired mutant (SendMetricCommand skipping RevalidateMetric) puts alias 11 on the wire.
func TestSendMetricCommandAfterARebirthUsesTheNewAlias(t *testing.T) {
	rig := newSpRig(t, spWindow, 0)
	c, feed := onlineWithBirths(t, rig, 11)
	w := watchWire(t, rig.mqttURL, "cmd-watch", "spBv1.0/plant-a/#")

	stale, err := c.sessions.LookupMetric(wireNodeT, "Setpoint") // captured at "validation"
	require.NoError(t, err)
	require.Equal(t, uint64(11), stale.Alias)

	// The node rebirths with Setpoint remapped 11 -> 12 (a gap-commanded rebirth keeps the session,
	// so only the generation distinguishes the tables).
	c.sessions.Observe(Topic{GroupID: "plant-a", EdgeNodeID: wireNode, MessageType: NDATA}, &sppb.Payload{Seq: proto.Uint64(200)})
	feed.publish(wireNBirth, nbirthWire(t, 12))
	require.Eventually(t, func() bool {
		r, err := c.sessions.LookupMetric(wireNodeT, "Setpoint")
		return err == nil && r.Alias == 12
	}, 20*time.Second, 5*time.Millisecond)

	used, _, err := c.SendMetricCommand(stale, 3)
	require.NoError(t, err)
	assert.Equal(t, uint64(12), used.Alias)

	require.Eventually(t, func() bool { return len(w.writes(t)) >= 1 }, 20*time.Second, 5*time.Millisecond)
	msgs := w.writes(t)
	require.Len(t, msgs, 1)
	met := payloadOf(t, msgs[0]).GetMetrics()[0]
	assert.Equal(t, uint64(12), met.GetAlias(), "the stale alias 11 must never reach the wire")
	assert.Nil(t, met.Name)
}

// A refused value publishes nothing: the encoder's refusal is before the wire.
func TestRefusedValueIsNotPublished(t *testing.T) {
	rig := newSpRig(t, spWindow, 0)
	c, _ := onlineWithBirths(t, rig, 11)
	w := watchWire(t, rig.mqttURL, "cmd-watch", "spBv1.0/plant-a/#")

	ref, err := c.sessions.LookupMetric(wireNodeT, "Setpoint")
	require.NoError(t, err)
	_, payload, err := c.SendMetricCommand(ref, 300) // 300 into the BIRTH-declared Int8
	require.ErrorIs(t, err, ErrValueRefused)
	assert.Nil(t, payload)

	// Control: the same call with an in-range value does publish, so silence above is the refusal.
	_, _, err = c.SendMetricCommand(ref, 100)
	require.NoError(t, err)
	msgs := awaitMsgs(t, w, 1)
	time.Sleep(300 * time.Millisecond)
	assert.Len(t, w.all(), 1, "only the in-range command reached the wire")
	assert.Equal(t, uint32(100), payloadOf(t, msgs[0]).GetMetrics()[0].GetIntValue())
}

// With no live broker session there is nothing to publish on, and no table to address against.
func TestSendMetricCommandWithoutASession(t *testing.T) {
	rig := newSpRig(t, spWindow, 0)
	c, _ := onlineWithBirths(t, rig, 11)
	ref, err := c.sessions.LookupMetric(wireNodeT, "Setpoint")
	require.NoError(t, err)
	c.sessions.EndHostSession()
	_, _, err = c.SendMetricCommand(ref, 1)
	assert.ErrorIs(t, err, ErrNotBorn, "the Host session's tables are gone: park and wait for the rebuild")
}

// 🔴 THE HOST SESSION ENDING CLEARS THE TABLES, through the real teardown path (clearSession is
// what runLoop calls when the broker connection is abandoned), not by calling the tracker.
func TestHostSessionEndClearsTheTables(t *testing.T) {
	rig := newSpRig(t, spWindow, 0)
	c, _ := onlineWithBirths(t, rig, 11)
	c.mu.Lock()
	live := c.mc
	c.mu.Unlock()
	require.NotNil(t, live)

	// Control: a clearSession for some OTHER client is not this session's end.
	c.clearSession(mqtt.NewClient(mqtt.NewClientOptions()))
	_, err := c.sessions.LookupMetric(wireNodeT, "Setpoint")
	require.NoError(t, err, "control: an unrelated client's teardown leaves the table")

	c.clearSession(live)
	_, err = c.sessions.LookupMetric(wireNodeT, "Setpoint")
	assert.ErrorIs(t, err, ErrNotBorn)
}
