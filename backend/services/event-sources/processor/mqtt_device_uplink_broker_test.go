// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/integration"
	"github.com/devicechain-io/dc-event-sources/model"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// A DEVICE'S RAW PAYLOAD GOES THROUGH THE REAL CAPTURE STREAM AND DURABLE READER.
//
// The reader's hand-out is the stream's own subject list, so a stream that stored the uplink
// subject while the reader handed out only the events subjects would ack-and-drop every raw payload
// as foreign. Published straight to the uplink subject (the MQTT hop in front of it is the broker's,
// pinned in core/messaging), a payload goes through the real reader to the uplink route and nowhere
// else, and a telemetry message beside it still goes to inbound-events.
func TestTheCaptureReaderHandsARawPayloadToTheUplinkRoute(t *testing.T) {
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
	const instance = "uplink-e2e"
	ms := &core.Microservice{InstanceId: instance, FunctionalArea: "event-sources", Readiness: core.NewReadinessGate()}
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: u.Hostname(), Port: uint32(port), MqttDeviceUplinks: true}
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

	capture := messaging.StreamName(instance, streams.DeviceEventsCapture)
	info, err := js.StreamInfo(capture)
	require.NoError(t, err)
	require.Contains(t, info.Config.Subjects, messaging.DeviceUplinkWildcard(instance), "the stream stores the raw-payload subject")

	telemetry := []byte(`{"device":"own-1","eventType":"Measurement","payload":{"entries":[{"measurements":{"t":"1"}}]}}`)
	_, err = js.Publish(messaging.DeviceEventsSubject(instance, "acme", "own-1"), telemetry)
	require.NoError(t, err)
	const raw = 15
	for i := 0; i < raw; i++ {
		_, err = js.Publish(messaging.DeviceUplinkSubject(instance, "acme", "meter-1"), []byte{0x01, byte(i)})
		require.NoError(t, err)
	}

	var mu sync.Mutex
	var devices []string
	var marked int
	src := NewGatewayJetStreamSource(nil, "gw-e2e", NewJsonDecoder(map[string]string{}),
		func(string, []byte) {}, benchInboundMessage,
		func(_ string, _ string, _ []byte, err error) error {
			t.Errorf("unexpected failed-decode: %v", err)
			return nil
		}, nil, admitAllReadings, nil)
	require.NoError(t, src.UseDeviceUplinks(&DeviceUplinkRoute{
		Plans: integration.NewDeviceDecoders(&brokerPlanFake{plan: uplinkFramePlan()}, integration.DeviceDecoderConfig{}),
		Publish: func(_, tenant string, e *model.UnresolvedEvent, _ interface{}, dedupID string) error {
			mu.Lock()
			defer mu.Unlock()
			devices = append(devices, e.Device)
			if e.BrokerAsserted && tenant == "acme" && dedupID != "" {
				marked++
			}
			return nil
		},
	}))
	require.NoError(t, src.Initialize(ctx))
	src.SetReader(reader)
	src.SetWriter(writer)
	require.NoError(t, src.Start(ctx))

	inbound := messaging.StreamName(instance, streams.InboundEvents)
	require.Eventually(t, func() bool {
		mu.Lock()
		n := len(devices)
		mu.Unlock()
		info, err := js.StreamInfo(inbound)
		return n == raw && err == nil && info.State.Msgs >= 1
	}, 30*time.Second, 5*time.Millisecond, "the reader never handed every raw payload to the uplink route")
	require.NoError(t, src.Stop(ctx))

	mu.Lock()
	require.Equal(t, raw, marked, "every raw payload is broker-asserted, for the tenant, with a dedup id")
	for _, d := range devices {
		require.Equal(t, "meter-1", d)
	}
	mu.Unlock()
	info2, err := js.StreamInfo(inbound)
	require.NoError(t, err)
	require.Equal(t, uint64(1), info2.State.Msgs, "only the telemetry reached inbound-events; a raw payload never does")

	// Every message was acked: the work-queue stream holds nothing.
	require.Eventually(t, func() bool {
		info, err := js.StreamInfo(capture)
		return err == nil && info.State.Msgs == 0
	}, 10*time.Second, 10*time.Millisecond, "a captured message was never acked (work-queue stream still holds it)")
}
