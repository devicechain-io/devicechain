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

	"github.com/stretchr/testify/require"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// The whole pipelined hop against a real broker: captured messages read by the real
// durable reader, decoded, published through the REAL ordered writer at the shipped window,
// and every capture message acked from the writer's outcome. It pins what the in-process
// doubles cannot: that each event is stored in inbound-events exactly once, carrying the
// dedup id built from its tenant and its OWN capture sequence, and that the settle path
// really acks the broker — nothing is left pending on the capture consumer.
func TestPipelinedCaptureReachesInboundEventsOnceEndToEnd(t *testing.T) {
	const events = 500
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dctest.JetStreamStoreDir(t),
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "embedded nats server not ready")
	t.Cleanup(srv.Shutdown)

	u, err := url.Parse(srv.ClientURL())
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	const instance = "pipeline-e2e"
	ms := &core.Microservice{InstanceId: instance, FunctionalArea: "event-sources", Readiness: core.NewReadinessGate()}
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: u.Hostname(), Port: uint32(port)}
	ms.Readiness.MarkReadyWithoutAuthSurface()
	var reader messaging.MessageReader
	var writer messaging.OrderedWriter
	nmgr := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(n *messaging.NatsManager) error {
		r, err := n.NewReader(streams.DeviceEventsCapture)
		if err != nil {
			return err
		}
		reader = r
		writer, err = n.NewOrderedWriter(streams.InboundEvents, CAPTURE_PUBLISH_WINDOW)
		return err
	})
	nmgr.RecordMaxDeliveries(func(*messaging.NatsManager) (messaging.MaxDeliveryFunc, error) {
		return func(context.Context, messaging.MaxDelivery) (messaging.MaxDeliveryOutcome, error) {
			t.Error("a capture message ran out of deliveries")
			return messaging.MaxDeliveryLettered, nil
		}, nil
	})
	ctx := context.Background()
	require.NoError(t, nmgr.Initialize(ctx))
	require.NoError(t, nmgr.Start(ctx))
	t.Cleanup(func() { _ = nmgr.Stop(context.Background()) })

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	// Published straight to the capture subjects: the MQTT hop in front of them is pinned
	// by TestCaptureSurvivesASourceRestartEndToEnd.
	for i := 0; i < events; i++ {
		device := i % 50
		_, err := js.Publish(fmt.Sprintf("%s.acme.devices.dev-%d.events", instance, device),
			[]byte(fmt.Sprintf(`{"device":"dev-%d","eventType":"Measurement",`+
				`"payload":{"entries":[{"measurements":{"t":"%d"}}]}}`, device, i)))
		require.NoError(t, err)
	}

	src := NewGatewayJetStreamSource(nil, "gw-e2e", NewJsonDecoder(map[string]string{}, 0),
		func(string, []byte) {}, benchInboundMessage,
		func(_ string, _ string, _ []byte, err error) error {
			t.Errorf("unexpected failed-decode: %v", err)
			return nil
		}, nil)
	require.NoError(t, src.Initialize(ctx))
	src.SetReader(reader)
	src.SetWriter(writer)
	require.NoError(t, src.Start(ctx))

	inbound := messaging.StreamName(instance, streams.InboundEvents)
	require.Eventually(t, func() bool {
		info, err := js.StreamInfo(inbound)
		return err == nil && info.State.Msgs >= events
	}, 30*time.Second, 5*time.Millisecond, "inbound-events never held every captured event")
	require.NoError(t, src.Stop(ctx))

	info, err := js.StreamInfo(inbound)
	require.NoError(t, err)
	require.Equal(t, uint64(events), info.State.Msgs, "each captured event must be stored exactly once")

	ids := map[string]bool{}
	for seq := info.State.FirstSeq; seq <= info.State.LastSeq; seq++ {
		m, err := js.GetMsg(inbound, seq)
		require.NoError(t, err)
		ids[m.Header.Get(nats.MsgIdHdr)] = true
	}
	want := map[string]bool{}
	for seq := 1; seq <= events; seq++ {
		want[fmt.Sprintf("acme:%d", seq)] = true
	}
	require.Equal(t, want, ids,
		"every stored event must carry the dedup id of its own capture message, or a redelivery is stored twice")

	capture := messaging.StreamName(instance, streams.DeviceEventsCapture)
	require.Eventually(t, func() bool {
		for ci := range js.ConsumersInfo(capture) {
			if ci.NumAckPending == 0 && ci.AckFloor.Stream == events {
				return true
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond, "the capture consumer still has messages awaiting an ack")
}
