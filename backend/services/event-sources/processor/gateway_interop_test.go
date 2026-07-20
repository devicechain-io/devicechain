// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// MQTT → JETSTREAM INTEROP PIN (ADR-030 I6)
//
// Ingest now rests on a property of the broker rather than of our code: an MQTT
// QoS-1 publish from a device must land in a JetStream stream, on the subject the
// topic maps to, with its payload untouched. Every one of those is nats-server's
// behaviour, and none of it is covered by the tests around it — the existing
// gateway tests assert the OUTBOUND direction (a NATS subject delivered to an MQTT
// subscriber), which is the direction the capture stream does NOT use.
//
// So a nats-server bump that changed the topic→subject mapping, framed the payload,
// or stopped routing gateway publishes into user streams would leave every unit
// test green and end ingest in production. That is the failure this pins: it runs
// against a real embedded gateway in CI, so the bump fails here, loudly, first.
//
// What is deliberately NOT pinned here is "PUBACK means persisted". That the broker
// writes the capture stream before acknowledging the device is an artifact of how
// the gateway is implemented, not a contract it publishes, so asserting it would
// pin something nats-server never promised and may reasonably change.

// A device's QoS-1 publish must arrive in the capture stream on the mapped subject
// with byte-identical payload.
//
// The payload is deliberately not JSON. It carries a NUL, a byte that is invalid
// UTF-8 on its own, and a trailing newline — so any framing, re-encoding or
// string round-trip anywhere on the path changes it and fails here. A JSON body
// would survive several transformations that a protobuf or CBOR device payload
// would not, and devices send both.
func TestDevicePublishReachesTheCaptureStreamUnaltered(t *testing.T) {
	nc, mqttPort := startGateway(t)
	js, name := addCaptureStream(t, nc)

	const tenant = "acme"
	const device = "sensor-001"
	subject := messaging.DeviceEventsSubject(testInstance, tenant, device)
	topic := messaging.SubjectToMqttTopic(subject)

	payload := []byte{0x00, 0x01, 0xff, 0xfe, 'd', 'c', 0x7f, '\n'}

	sub, err := js.SubscribeSync(subject, nats.BindStream(name))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	publishAsDevice(t, mqttPort, "interop-pub", topic, string(payload))

	msg, err := sub.NextMsg(15 * time.Second)
	require.NoError(t, err,
		"a QoS-1 device publish never reached the capture stream — the MQTT gateway no "+
			"longer routes device telemetry into a user stream, which ends ingest entirely")

	require.Equal(t, subject, msg.Subject,
		"the gateway mapped MQTT topic %q to a different subject than SubjectToMqttTopic "+
			"expects; the capture stream's subject filter and the device grant are both "+
			"built from that mapping, so ingest would silently stop", topic)

	require.Equal(t, payload, msg.Data,
		"the payload changed in transit; the decoder is handed raw device bytes and would "+
			"fail to decode every event")

	// The device token is recovered from the DELIVERED subject and is what the
	// decode worker checks the payload's own claim against. If the mapping ever
	// produced a subject this parser cannot read, that check would silently stop
	// running rather than fail.
	require.Equal(t, device, deviceFromSubject(msg.Subject),
		"the device token could not be recovered from the delivered subject")
}
