// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// FULL-HOP DURABILITY (ADR-030 rig)
//
// The other event-sources broker tests each cover ONE hop and stop:
//
//   - gateway_interop_test / cutover_test drive a real device publish through the
//     embedded MQTT gateway and assert it lands in the capture STREAM — then read
//     the stream directly and never run our consumer;
//   - gateway_jetstream_test drives GatewayJetStreamSource.handle with hand-built
//     messages and a fake ack — the real decode path, but no broker and no read loop.
//
// Neither runs the whole chain, and neither runs the read loop at all — which is
// the exact code the I3 wiring bug lived in and reached a live cluster through,
// because handle() is reachable from a test and readLoop() was not.
//
// This test runs the entire path end to end against a real embedded gateway: a
// device's QoS-1 MQTT publish is PUBACKed by the broker, captured, and pulled off
// the durable capture stream by a real GatewayJetStreamSource — its read loop, its
// handle, its settler, its decode workers — until the decoded callback fires. Then
// it asserts the property the capture stream exists for: a source that stops and is
// replaced delivers everything published while it was down.
//
// What it does and does NOT pin about duplicates: it pins that the replacement
// RESUMES the durable rather than replaying it from the start — a fresh consumer or
// a recreated durable would re-deliver the already-settled `before` here and fail.
// It does NOT pin per-message ack timing: a single un-acked message redelivers only
// after AckWait (60s), past this test's horizon, and that behaviour is pinned fast
// and deterministically by TestASuccessfulPublishIsAckedAndCarriesTheCaptureSequence
// (acks == 1). The two are complementary — that one owns the ack, this one owns the
// live chain and the durable resume.

// captureReader builds a durable reader over the real DeviceEventsCapture stream,
// pointed at the already-running embedded gateway. It is a NatsManager struct
// literal driven through ExecuteInitialize + NewReader rather than NewNatsManager,
// for two reasons: the constructor registers stream metrics against the global
// promauto registry (so building two managers on one area panics), and the source
// owns the read loop, so none of the manager's Start machinery is wanted here. The
// area is shared across the two readers on purpose — the durable name is built from
// instance+area+suffix, so the replacement must reuse it to resume the same durable.
func captureReader(t *testing.T, nc *nats.Conn, area string) messaging.MessageReader {
	t.Helper()

	u, err := url.Parse(nc.ConnectedUrl())
	require.NoError(t, err, "parse embedded server url")
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err, "parse embedded server port")

	ms := &core.Microservice{
		InstanceId:     testInstance,
		FunctionalArea: area,
		Readiness:      core.NewReadinessGate(),
	}
	ms.InstanceConfiguration.Infrastructure.Nats.Hostname = u.Hostname()
	ms.InstanceConfiguration.Infrastructure.Nats.Port = uint32(port)
	// Reads gate on auth being live; nothing here exercises auth, so open the gate
	// rather than block every fetch on a validator this test never installs.
	ms.Readiness.MarkReady(nil)

	nmgr := &messaging.NatsManager{Microservice: ms}
	require.NoError(t, nmgr.ExecuteInitialize(context.Background()),
		"connect a capture reader to the embedded gateway")
	t.Cleanup(func() {
		if c := nmgr.Conn(); c != nil {
			c.Close()
		}
	})

	// NewReader ensures the capture stream on first call — the same stream, with the
	// same device-events subject filter, that event-sources ensures at startup — so
	// this is also what makes the stream exist before the device publishes.
	r, err := nmgr.NewReader(streams.DeviceEventsCapture)
	require.NoError(t, err, "bind the durable capture reader")
	return r
}

// decodedEvent is one delivery observed at the far end of the whole chain.
type decodedEvent struct {
	device string
	seq    uint64
}

// startCaptureSource wires a real GatewayJetStreamSource to the given reader and
// starts its lifecycle, returning a channel that receives one decodedEvent per
// message that survives the full path to the decode callback. allow is nil, so
// metering is disabled and the test isolates the durability hop.
func startCaptureSource(t *testing.T, id string, reader messaging.MessageReader) (*GatewayJetStreamSource, <-chan decodedEvent) {
	t.Helper()

	out := make(chan decodedEvent, 32)
	src := NewGatewayJetStreamSource(id, NewJsonDecoder(map[string]string{}),
		func(string, []byte) {},
		// The decoded callback is (sourceId, tenant, event, payload, captureSeq); the
		// device token the payload claims is on the event, not a positional argument.
		func(_ string, _ string, event *model.UnresolvedEvent, _ interface{}, seq uint64) error {
			out <- decodedEvent{device: event.Device, seq: seq}
			return nil
		},
		func(_ string, _ string, _ []byte, decodeErr error) error {
			// A failed decode on a payload the test controls is a test bug, not a
			// path under test — surface it rather than let it look like a lost message.
			t.Errorf("unexpected failed-decode on the full hop: %v", decodeErr)
			return nil
		},
		nil)

	ctx := context.Background()
	require.NoError(t, src.Initialize(ctx))
	src.SetReader(reader)
	require.NoError(t, src.Start(ctx))
	return src, out
}

func TestCaptureSurvivesASourceRestartEndToEnd(t *testing.T) {
	nc, mqttPort := startGateway(t)

	const (
		tenant       = "acme"
		device       = "sensor-001"
		consumerArea = "fullhop-consumer"
	)
	subject := messaging.DeviceEventsSubject(testInstance, tenant, device)
	topic := messaging.SubjectToMqttTopic(subject)

	// A device event the JSON decoder accepts. The device it claims must match the
	// one the subject addresses, or checkDeviceMatchesTransport would fail-decode it.
	event := func(seq int) string {
		return fmt.Sprintf(
			`{"device":%q,"eventType":"Location","occurredTime":"2026-07-20T10:30:%02dZ","latitude":1,"longitude":2,"elevation":3}`,
			device, seq)
	}
	recv := func(t *testing.T, out <-chan decodedEvent, who string) decodedEvent {
		t.Helper()
		select {
		case e := <-out:
			return e
		case <-time.After(20 * time.Second):
			t.Fatalf("%s: no event reached the decode callback; the full MQTT→capture→"+
				"consumer chain is broken", who)
			return decodedEvent{}
		}
	}

	// The reader ensures the capture stream, so it exists before the device ever
	// publishes — the load-bearing ordering the cutover depends on.
	reader := captureReader(t, nc, consumerArea)
	src, out := startCaptureSource(t, "gw-fullhop", reader)

	// A device publish, PUBACKed by the broker, must traverse the entire chain and
	// reach the decode callback.
	publishAsDevice(t, mqttPort, "fullhop-before", topic, event(1))
	before := recv(t, out, "first source")
	require.Equal(t, device, before.device,
		"the device recovered at the end of the chain is not the one that published")
	require.NotZero(t, before.seq,
		"the capture sequence must reach the decode callback — it is the dedup id, and a "+
			"zero one silently disables ingest idempotency")

	// The source goes down: the rollout / crash / outage window.
	require.NoError(t, src.Stop(context.Background()))

	// Traffic the broker PUBACKs while nothing is consuming. Before the capture
	// stream this is exactly what vanished — acknowledged to the device, stored
	// nowhere.
	const gap = 4
	for i := 0; i < gap; i++ {
		publishAsDevice(t, mqttPort, fmt.Sprintf("fullhop-gap-%d", i), topic, event(10+i))
	}

	// A replacement source binds the SAME durable, the way a replacement pod does,
	// and drains the gap.
	replacement := captureReader(t, nc, consumerArea)
	src2, out2 := startCaptureSource(t, "gw-fullhop-2", replacement)
	t.Cleanup(func() { _ = src2.Stop(context.Background()) })

	seen := map[uint64]bool{before.seq: true}
	for i := 0; i < gap; i++ {
		e := recv(t, out2, fmt.Sprintf("replacement gap-%d", i))
		require.Equal(t, device, e.device)
		require.False(t, seen[e.seq],
			"the replacement re-decoded a message already settled before the restart (seq %d); "+
				"it did not RESUME the durable but replayed the stream, so a rollout would double "+
				"every reading published before it", e.seq)
		seen[e.seq] = true
	}

	// One more publish must be the very next thing decoded — not a redelivery of
	// already-settled traffic. Without this the test would also pass on a replacement
	// that replays the whole stream from the start, which loses nothing but
	// duplicates everything before the gap.
	publishAsDevice(t, mqttPort, "fullhop-after", topic, event(20))
	after := recv(t, out2, "replacement after")
	require.False(t, seen[after.seq],
		"a redelivery arrived where a fresh message was expected; the replacement replayed "+
			"the stream instead of resuming the durable")
}
