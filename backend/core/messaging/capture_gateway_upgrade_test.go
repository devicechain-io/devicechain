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
	"github.com/devicechain-io/dc-microservice/streams"
)

// UPGRADING A LIVE CAPTURE STREAM ONTO THE GATEWAY SHAPE.
//
// The previous release created the capture stream with ONE subject (a device's own telemetry)
// and its durable with that single filter. The new binary declares two subjects, and the stream
// and the consumer reconcile in different places: ensureStream rewrites the captured subjects,
// bind moves the durable from FilterSubject onto the FilterSubjects list. Both must happen, in
// that order, or the stream captures a gateway's publishes while the durable never delivers
// them: the broker acknowledges a gateway's PUBACKed publish, the platform never reads it.
// The reader now reads a durable of a NEW name with NO broker filter, handing out both subjects
// itself, and deletes the old-named one (NewReader, CaptureDurableName); what is held here is
// that the move happens, the old durable is gone, and both shapes arrive.
//
// This is the order on a manager that reconciles what it opens (a test's). On a deployed broker
// the stream half is the broker administrator's, not event-sources': see the test below.
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
	current := CaptureDurableName(instance, area, captureSuffix)
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
	moved, err := nmgr.js.ConsumerInfo(stream, current)
	require.NoError(t, err)
	require.Empty(t, moved.Config.FilterSubject, "the durable kept the old single filter")
	require.Empty(t, moved.Config.FilterSubjects, "the durable was given a broker filter list")
	_, err = nmgr.js.ConsumerInfo(stream, durable)
	require.ErrorIs(t, err, nats.ErrConsumerNotFound, "the old-named durable was left beside the new one")

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

// The same upgrade on a deployed broker, where the two halves have two owners: the broker
// administrator owns the stream's subject list (it is the catalog's, and a service only verifies
// it), while event-sources owns its durable and moves the filter when it binds. A deployed
// event-sources that meets the old one-subject stream is refused by name rather than reading on
// with a stream that drops a gateway's publishes; once the administrator has reconciled, it opens
// the reader, the durable is moved, and both shapes are delivered.
func TestOnADeployedBrokerTheAdministratorMovesTheCaptureSubjectsAndTheReaderMovesItsFilter(t *testing.T) {
	srv := startEmbeddedServer(t)
	admin := adminOn(t, srv)
	instance := admin.Microservice.InstanceId
	stream := captureStreamOn(instance)
	durable := DurableName(instance, "event-sources", captureSuffix)
	current := CaptureDurableName(instance, "event-sources", captureSuffix)
	oldSubject := DeviceEventsWildcard(instance)

	// Everything event-sources opens, as the administrator makes it today...
	_, err := admin.ReconcileBroker(context.Background(), []string{"event-sources"})
	require.NoError(t, err)
	// ...and then the capture stream and its durable put back to the previous release's shape.
	require.NoError(t, admin.js.DeleteStream(stream))
	_, err = admin.js.AddStream(&nats.StreamConfig{
		Name: stream, Subjects: []string{oldSubject}, Retention: nats.WorkQueuePolicy,
		Duplicates: time.Duration(streams.DuplicateWindowSecondsFor(captureSuffix)) * time.Second,
	})
	require.NoError(t, err)
	_, err = admin.js.AddConsumer(stream, &nats.ConsumerConfig{
		Durable: durable, AckPolicy: nats.AckExplicitPolicy,
		AckWait: AckWait, MaxDeliver: MaxDeliver, MaxAckPending: readerMaxAckPending,
		FilterSubject: oldSubject,
	})
	require.NoError(t, err)
	_, err = admin.js.Publish(DeviceEventsSubject(instance, "acme", "before"), []byte("before"))
	require.NoError(t, err)

	// The new build on the old stream: refused, by name, and nothing changed.
	_, err = deployedOn(t, srv, "event-sources").NewReader(captureSuffix)
	d := drift(t, err)
	require.Equal(t, stream, d.Name)
	require.Contains(t, d.Error(), "captures subjects")
	info, err := admin.js.StreamInfo(stream)
	require.NoError(t, err)
	require.Equal(t, []string{oldSubject}, info.Config.Subjects, "a verifying service changed the stream's subjects")

	// The hook: the administrator puts the catalog's subject list on the stream.
	report, err := admin.ReconcileBroker(context.Background(), []string{"event-sources"})
	require.NoError(t, err)
	require.Positive(t, report.Count("updated"))
	info, err = admin.js.StreamInfo(stream)
	require.NoError(t, err)
	require.ElementsMatch(t, DeviceEventsSubjects(instance), info.Config.Subjects)
	still, err := admin.js.ConsumerInfo(stream, durable)
	require.NoError(t, err)
	require.Equal(t, oldSubject, still.Config.FilterSubject, "the administrator touched a durable, which is its reader's")

	// The roll: event-sources verifies, binds, and moves its own durable off the old filter.
	svc := deployedOn(t, srv, "event-sources")
	reader, err := svc.NewReader(captureSuffix)
	require.NoError(t, err)
	moved, err := admin.js.ConsumerInfo(stream, current)
	require.NoError(t, err)
	require.Empty(t, moved.Config.FilterSubject, "the durable kept the old single filter")
	require.Empty(t, moved.Config.FilterSubjects, "the durable was given a broker filter list")
	_, err = admin.js.ConsumerInfo(stream, durable)
	require.ErrorIs(t, err, nats.ErrConsumerNotFound, "the old-named durable was left beside the new one")

	_, err = admin.js.Publish(GatewayEventsSubject(instance, "acme", "gw-1", "child"), []byte("gateway"))
	require.NoError(t, err)
	got := map[string]string{}
	readCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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
