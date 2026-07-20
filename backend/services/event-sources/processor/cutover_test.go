// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// CUTOVER CHOREOGRAPHY (ADR-030 I5)
//
// Moving an instance off the MQTT-client ingest path and onto the capture stream
// is a one-time transition with two failure modes, and which one you get is
// decided entirely by the ORDER in which the old pod dies and the stream is
// created. Both are proven below against a real broker, because both are broker
// behaviour rather than ours:
//
//   - create the stream AFTER the last MQTT client dies → every message the broker
//     PUBACKs in between is accepted and written nowhere (TestCaptureBeginsOnly...);
//   - create the stream BEFORE it dies → the overlap is ingested twice, once down
//     each path (TestRolloutOverlapIsIngestedTwice).
//
// There is no third ordering: some window is unavoidable. DeviceChain takes the
// duplicate, because ADR-030 exists to stop the platform discarding messages it
// already told a device were safe — a duplicate measurement is visible and
// correctable, a lost one is neither.
//
// The default RollingUpdate strategy produces the safe ordering for free, which is
// why event-sources must NOT be switched to Recreate. See the guard and the long
// note in deploy/helm/devicechain/templates/deployment.yaml.

// publishAsDevice publishes one QoS-1 message the way a device does and waits for
// the broker's PUBACK, so the test only proceeds once the broker has taken
// responsibility for the message.
func publishAsDevice(t *testing.T, mqttPort int, clientID, topic, body string) {
	t.Helper()

	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://127.0.0.1:%d", mqttPort))
	opts.SetClientID(clientID)
	cl := mqtt.NewClient(opts)
	tok := cl.Connect()
	require.True(t, tok.WaitTimeout(15*time.Second), "mqtt connect timed out")
	require.NoError(t, tok.Error())
	t.Cleanup(func() { cl.Disconnect(100) })

	pub := cl.Publish(topic, 1, false, []byte(body))
	require.True(t, pub.WaitTimeout(10*time.Second), "publish timed out")
	require.NoError(t, pub.Error())
}

// addCaptureStream creates a stream over the production device-events subject.
//
// The stream NAME is test-local; the SUBJECT is the real one, and the subject is
// the whole claim under test — what the broker will and will not write into a
// stream is decided by subject matching, not by the name.
func addCaptureStream(t *testing.T, nc *nats.Conn) (nats.JetStreamContext, string) {
	t.Helper()

	js, err := nc.JetStream()
	require.NoError(t, err)
	const name = "cutover-capture"
	_, err = js.AddStream(&nats.StreamConfig{
		Name:     name,
		Subjects: []string{messaging.DeviceEventsWildcard(testInstance)},
		Storage:  nats.FileStorage,
	})
	require.NoError(t, err)
	return js, name
}

func streamMsgs(t *testing.T, js nats.JetStreamContext, name string) uint64 {
	t.Helper()
	info, err := js.StreamInfo(name)
	require.NoError(t, err)
	return info.State.Msgs
}

// A JetStream stream captures nothing that was published before it existed, even
// though the broker PUBACKed it — the MQTT gateway's own persistence does not
// replay into a stream that appears later.
//
// This is the entire reason the capture stream must be ensured while the OLD
// ingest path is still running. If it were false, the rollout could tear the old
// pod down first and let the new one create the stream on the way up, which is
// exactly what strategy: Recreate would do.
func TestCaptureBeginsOnlyWhenTheStreamExists(t *testing.T) {
	nc, mqttPort := startGateway(t)

	topic := messaging.SubjectToMqttTopic(
		messaging.DeviceEventsSubject(testInstance, "acme", "sensor-001"))

	// PUBACKed with no capture stream in existence: the broker accepted
	// responsibility for this message and has nowhere to put it.
	publishAsDevice(t, mqttPort, "cutover-before", topic, `{"before":true}`)

	js, name := addCaptureStream(t, nc)
	time.Sleep(500 * time.Millisecond)

	require.Equal(t, uint64(0), streamMsgs(t, js, name),
		"a message PUBACKed before the stream existed was captured after all — if this "+
			"ever becomes true the pre-create ordering is no longer load-bearing, but until "+
			"then tearing the old pod down first silently loses this message")

	// The counterweight: capture must actually work once the stream is there, or
	// the assertion above would pass on a broker that captures nothing at all.
	publishAsDevice(t, mqttPort, "cutover-after", topic, `{"after":true}`)
	require.Eventually(t, func() bool {
		return streamMsgs(t, js, name) == 1
	}, 10*time.Second, 100*time.Millisecond,
		"a message published WITH the stream present was not captured — capture is broken")
}

// While the old MQTT-client pod and the new capture stream coexist, one device
// publish lands in BOTH paths and is therefore ingested twice.
//
// This pins the accepted cost of the cutover rather than a defect. It is worth
// keeping because it is the premise of the duplicate-window note in the upgrade
// docs: if a broker change ever made the gateway deliver to only one of the two,
// the cutover would become clean and that note could be dropped. Today it does
// not, and the window is real.
func TestRolloutOverlapIsIngestedTwice(t *testing.T) {
	nc, mqttPort := startGateway(t)

	// The OLD path: the previous release's MQTT client, still subscribed.
	delivered := subscribeGateway(t, mqttPort)
	// The NEW path: the capture stream the incoming release ensures at startup.
	js, name := addCaptureStream(t, nc)

	topic := messaging.SubjectToMqttTopic(
		messaging.DeviceEventsSubject(testInstance, "acme", "sensor-001"))
	publishAsDevice(t, mqttPort, "cutover-overlap", topic, `{"overlap":true}`)

	viaMqtt := 0
	select {
	case <-delivered:
		viaMqtt = 1
	case <-time.After(10 * time.Second):
	}
	require.Equal(t, 1, viaMqtt, "the old MQTT path did not receive the publish")

	require.Eventually(t, func() bool {
		return streamMsgs(t, js, name) == 1
	}, 10*time.Second, 100*time.Millisecond,
		"the capture stream did not receive the publish")
}
