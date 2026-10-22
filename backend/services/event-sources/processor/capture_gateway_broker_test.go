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

	esproto "github.com/devicechain-io/dc-event-sources/proto"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// THE CAPTURE STREAM STORES TWO SUBJECTS, AND THE REAL DURABLE READER DRAINS BOTH.
//
// The reader filters on the exact list (a work-queue stream with a single wildcard filter would
// leave the other shape unread forever, the broker holding messages the platform believes it
// never received). Published straight to the capture subjects, as the MQTT hop in front of them
// is pinned elsewhere: a device's own telemetry and a gateway's for a child go through the real
// reader, decode and ordered writer, and what lands in inbound-events carries the assertion each
// subject earns: the device's own sets no gateway, the gateway's sets the gateway and the child.
func TestTheCaptureReaderDrainsBothSubjectShapesAndMarksTheGateway(t *testing.T) {
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
	const instance = "gateway-e2e"
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
		writer, err = NewInboundWriter(n, pinnedWindow)
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

	// The stream holds both declared subjects, and the durable filters on exactly those.
	capture := messaging.StreamName(instance, streams.DeviceEventsCapture)
	info, err := js.StreamInfo(capture)
	require.NoError(t, err)
	require.ElementsMatch(t, messaging.DeviceEventsSubjects(instance), info.Config.Subjects)

	body := func(device string, i int) []byte {
		return []byte(fmt.Sprintf(`{"device":%q,"eventType":"Measurement",`+
			`"payload":{"entries":[{"measurements":{"t":"%d"}}]}}`, device, i))
	}
	const each = 20
	for i := 0; i < each; i++ {
		_, err := js.Publish(messaging.DeviceEventsSubject(instance, "acme", fmt.Sprintf("own-%d", i)), body(fmt.Sprintf("own-%d", i), i))
		require.NoError(t, err)
		_, err = js.Publish(messaging.GatewayEventsSubject(instance, "acme", "gw-1", fmt.Sprintf("child-%d", i)), body(fmt.Sprintf("child-%d", i), i))
		require.NoError(t, err)
	}

	src := NewGatewayJetStreamSource(nil, "gw-e2e", NewJsonDecoder(map[string]string{}),
		func(string, []byte) {}, benchInboundMessage,
		func(_ string, _ string, _ []byte, err error) error {
			t.Errorf("unexpected failed-decode: %v", err)
			return nil
		}, nil, admitAllReadings)
	require.NoError(t, src.Initialize(ctx))
	src.SetReader(reader)
	src.SetWriter(writer)
	require.NoError(t, src.Start(ctx))

	inbound := messaging.StreamName(instance, streams.InboundEvents)
	require.Eventually(t, func() bool {
		info, err := js.StreamInfo(inbound)
		return err == nil && info.State.Msgs >= 2*each
	}, 30*time.Second, 5*time.Millisecond, "inbound-events never held both shapes: the reader's filter missed one")
	require.NoError(t, src.Stop(ctx))

	info, err = js.StreamInfo(inbound)
	require.NoError(t, err)
	require.Equal(t, uint64(2*each), info.State.Msgs)
	gateways, own := 0, 0
	for seq := info.State.FirstSeq; seq <= info.State.LastSeq; seq++ {
		m, err := js.GetMsg(inbound, seq)
		require.NoError(t, err)
		event, err := esproto.UnmarshalUnresolvedEvent(m.Data)
		require.NoError(t, err)
		require.True(t, event.BrokerAsserted)
		if event.Gateway != "" {
			gateways++
			require.Equal(t, "gw-1", event.Gateway)
			require.Contains(t, event.Device, "child-")
		} else {
			own++
			require.Contains(t, event.Device, "own-")
		}
	}
	require.Equal(t, each, gateways)
	require.Equal(t, each, own)

	// Nothing is left pending on the capture consumer: every message of both shapes was acked.
	require.Eventually(t, func() bool {
		info, err := js.StreamInfo(capture)
		return err == nil && info.State.Msgs == 0
	}, 10*time.Second, 10*time.Millisecond, "a capture message of one shape was never acked (work-queue stream still holds it)")
}
