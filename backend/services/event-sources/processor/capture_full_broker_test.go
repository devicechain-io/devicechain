// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	dctest "github.com/devicechain-io/dc-microservice/test"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// WHAT A FULL CAPTURE STREAM DOES TO AN MQTT DEVICE, measured on a real broker.
//
// The obvious way to make a full capture stream "fail closed" is to have it discard NEW: the
// broker would then refuse the store, the device would get no PUBACK, and a QoS 1 device
// would retry. GKE perf round 12 lost 7.2% of 2.16M PUBACKed events to the capture stream's
// ceiling and that was the follow-up it proposed. This test is why it was not taken: the
// broker's MQTT gateway PUBACKs a QoS 1 publish once ITS OWN session store has it, and hands
// the message to this stream as a publish with no reply subject, so a refusal has nobody to
// go to. Under either policy every publish is PUBACKed and the overflow is lost. What the
// policy decides is only WHICH messages go and whether anything can see it:
//
//   - discard-old evicts the OLDEST. Their sequences are a hole the capture durable's cursor
//     steps over, and jetstream_consumer_unread_skipped_total counts exactly that
//     (JetStreamDurableLostUnread).
//   - discard-new refuses the NEWEST. They never get a sequence, so there is no hole, and
//     nothing in the platform counts them; the broker logs it at debug level.
//
// So discard-new would lose the same events invisibly, and the platform keeps discard-old
// (messaging.applyStreamDiscard puts an out-of-band edit back). Preventing the loss has to
// come from draining faster than devices publish and a ceiling sized for the gap, which is
// what capture.publishWindow and the capture stream's ceiling are about.
//
// If a nats-server upgrade ever makes the gateway wait for this stream (the PUBACK-after-
// capture property ADR-030 hoped for), the PUBACKed counts below change and this fails:
// that is the moment to revisit discard-new, not before.
func TestAFullCaptureStreamNeverRefusesTheMqttDevice(t *testing.T) {
	const published = 20
	cases := []struct {
		name     string
		discard  nats.DiscardPolicy
		maxMsgs  int64
		wantHeld uint64
		// firstBody/lastBody are the device messages the stream still holds at each end.
		firstBody, lastBody string
		// markerRefused: the end marker is a JetStream publish, which DOES carry a reply
		// subject, so a discard-new stream refuses it to its face.
		markerRefused bool
	}{
		// NEGATIVE CONTROL: a ceiling above the load keeps everything, so the counts below are
		// the stream's doing and not the harness's.
		{name: "ceiling above the load", discard: nats.DiscardOld, maxMsgs: 1000, wantHeld: published + 1,
			firstBody: "m00", lastBody: "marker"},
		{name: "discard old", discard: nats.DiscardOld, maxMsgs: 10, wantHeld: 10,
			firstBody: "m11", lastBody: "marker"},
		{name: "discard new", discard: nats.DiscardNew, maxMsgs: 10, wantHeld: 10,
			firstBody: "m00", lastBody: "m09", markerRefused: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true,
				StoreDir: dctest.JetStreamStoreDir(t), ServerName: "capture-full",
				MQTT: natsserver.MQTTOpts{Host: "127.0.0.1", Port: -1}})
			require.NoError(t, err)
			go srv.Start()
			require.True(t, srv.ReadyForConnections(15*time.Second), "embedded nats server not ready")
			t.Cleanup(srv.Shutdown)
			mqttPort := dctest.MQTTPort(t, srv)
			nc, err := nats.Connect(srv.ClientURL())
			require.NoError(t, err)
			t.Cleanup(nc.Close)
			js, err := nc.JetStream()
			require.NoError(t, err)

			// The production subject; the name and the ceiling are the test's.
			const stream = "capture-full"
			subject := messaging.DeviceEventsSubject(testInstance, "acme", "sensor-001")
			_, err = js.AddStream(&nats.StreamConfig{Name: stream, Subjects: []string{messaging.DeviceEventsWildcard(testInstance)},
				Retention: nats.WorkQueuePolicy, Discard: tc.discard, MaxMsgs: tc.maxMsgs, Storage: nats.FileStorage})
			require.NoError(t, err)

			opts := mqtt.NewClientOptions().AddBroker(fmt.Sprintf("tcp://127.0.0.1:%d", mqttPort)).
				SetClientID("capture-full-device").SetAutoReconnect(false)
			cl := mqtt.NewClient(opts)
			tok := cl.Connect()
			require.True(t, tok.WaitTimeout(15*time.Second), "mqtt connect timed out")
			require.NoError(t, tok.Error())
			t.Cleanup(func() { cl.Disconnect(100) })

			topic := messaging.SubjectToMqttTopic(subject)
			pubacked := 0
			for i := 0; i < published; i++ {
				p := cl.Publish(topic, 1, false, []byte(fmt.Sprintf("m%02d", i)))
				if p.WaitTimeout(10*time.Second) && p.Error() == nil {
					pubacked++
				}
			}
			require.Equal(t, published, pubacked, "every QoS 1 publish is PUBACKed, whatever the capture stream does with it")
			require.True(t, cl.IsConnectionOpen(), "the gateway did not close the device's connection")

			// END MARKER. The gateway hands each publish to the stream before it stores its own
			// copy and PUBACKs, so once the last PUBACK is in, every device message is queued
			// on the stream ahead of this publish, and its answer means they have all been
			// stored or refused.
			ack, err := js.Publish(subject, []byte("marker"))
			if tc.markerRefused {
				require.Error(t, err, "a discard-new stream refuses a publisher that asks for an answer")
			} else {
				require.NoError(t, err)
				require.EqualValues(t, published+1, ack.Sequence, "every device message was given a sequence before the marker")
			}

			info, err := js.StreamInfo(stream)
			require.NoError(t, err)
			require.Equal(t, tc.wantHeld, info.State.Msgs)
			first, err := js.GetMsg(stream, info.State.FirstSeq)
			require.NoError(t, err)
			last, err := js.GetMsg(stream, info.State.LastSeq)
			require.NoError(t, err)
			require.Equal(t, tc.firstBody, string(first.Data), "oldest message held")
			require.Equal(t, tc.lastBody, string(last.Data), "newest message held")

			lost := published - int(info.State.Msgs)
			if !tc.markerRefused {
				lost++ // the marker is held too
			}
			switch tc.discard {
			case nats.DiscardOld:
				// What was evicted left a hole of sequences below FirstSeq: what the capture
				// durable's unread counter is computed from.
				require.EqualValues(t, lost, info.State.FirstSeq-1, "every PUBACKed message lost is a sequence a reader would step over")
			case nats.DiscardNew:
				// What was refused never got a sequence: no hole, nothing for a reader to count.
				require.EqualValues(t, info.State.Msgs, info.State.LastSeq, "the refused messages left no sequence behind")
				require.EqualValues(t, 1, info.State.FirstSeq)
			}
			t.Logf("%s: %d PUBACKed, %d held, %d PUBACKed messages lost", tc.name, pubacked, info.State.Msgs, lost)
		})
	}
}
