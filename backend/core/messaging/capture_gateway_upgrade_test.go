// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
)

// UPGRADING A LIVE CAPTURE STREAM ONTO THE GATEWAY SHAPE.
//
// The previous release created the capture stream with ONE subject (a device's own telemetry)
// and its durable with that single filter. The new binary declares two subjects, and the stream
// and the consumer reconcile in different places: ensureStream rewrites the captured subjects,
// bind moves the durable from FilterSubject onto the FilterSubjects list. Both must happen, in
// that order, or the stream captures a gateway's publishes while the durable never delivers
// them: the broker acknowledges a gateway's PUBACKed publish, the platform never reads it.
func TestTheCaptureStreamAndDurableUpgradeOntoTheGatewayShape(t *testing.T) {
	srv := startEmbeddedServer(t)
	area := uniqueArea("capture-upgrade")
	ms := testMicroservice(t, srv, area)
	nmgr := NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*NatsManager) error { return nil })
	ctx := context.Background()
	require.NoError(t, nmgr.Initialize(ctx))
	require.NoError(t, nmgr.Start(ctx))
	t.Cleanup(func() { _ = nmgr.Stop(ctx) })

	instance := ms.InstanceId
	stream := captureStreamOn(instance)
	durable := DurableName(instance, area, captureSuffix)
	oldSubject := DeviceEventsWildcard(instance)

	// The OLD shape, exactly as the previous release created it.
	_, err := nmgr.js.AddStream(&nats.StreamConfig{
		Name: stream, Subjects: []string{oldSubject}, Retention: nats.WorkQueuePolicy,
	})
	require.NoError(t, err)
	_, err = nmgr.js.AddConsumer(stream, &nats.ConsumerConfig{
		Durable: durable, AckPolicy: nats.AckExplicitPolicy,
		AckWait: AckWait, MaxDeliver: MaxDeliver, MaxAckPending: readerMaxAckPending,
		FilterSubject: oldSubject,
	})
	require.NoError(t, err)
	info, err := nmgr.js.ConsumerInfo(stream, durable)
	require.NoError(t, err)
	require.Equal(t, oldSubject, info.Config.FilterSubject, "the staged durable is not the old shape; the test would prove nothing")

	// A device's message stored BEFORE the upgrade must survive it.
	_, err = nmgr.js.Publish(DeviceEventsSubject(instance, "acme", "before"), []byte("before"))
	require.NoError(t, err)

	reader, err := nmgr.NewReader(captureSuffix)
	require.NoError(t, err, "the upgraded build could not open its capture reader")

	after, err := nmgr.js.StreamInfo(stream)
	require.NoError(t, err)
	require.ElementsMatch(t, DeviceEventsSubjects(instance), after.Config.Subjects)
	moved, err := nmgr.js.ConsumerInfo(stream, durable)
	require.NoError(t, err)
	require.ElementsMatch(t, DeviceEventsSubjects(instance), moved.Config.FilterSubjects)

	_, err = nmgr.js.Publish(GatewayEventsSubject(instance, "acme", "gw-1", "child"), []byte("gateway"))
	require.NoError(t, err)

	got := map[string]string{}
	readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for len(got) < 2 {
		msg, err := reader.ReadMessage(readCtx)
		require.NoError(t, err, "the upgraded reader stopped delivering after %v", got)
		got[string(msg.Value)] = msg.Subject
		require.NoError(t, msg.Ack())
	}
	require.Equal(t, DeviceEventsSubject(instance, "acme", "before"), got["before"])
	require.Equal(t, GatewayEventsSubject(instance, "acme", "gw-1", "child"), got["gateway"])
}
