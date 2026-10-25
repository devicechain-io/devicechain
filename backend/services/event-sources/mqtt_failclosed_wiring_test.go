// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/processor"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
)

// fcWiringBroker starts a JetStream server with the MQTT gateway on, and points a fresh
// Microservice at it with the fail-closed setting as given. Nothing has connected over MQTT,
// so the gateway has created none of its streams: whatever exists afterwards, the service made.
func fcWiringBroker(t *testing.T, on bool) (nats.JetStreamContext, *messaging.NatsManager) {
	js, nmgr, _ := fcWiringBrokerWith(t, on, false)
	return js, nmgr
}

// fcWiringBrokerWith is fcWiringBroker that can configure the gateway source (a source pointed
// at the platform broker, which builds GatewaySource and StoreSource as the service does) and
// returns the broker's MQTT URL. Without it, StoreSource is a bare source, enough for the
// declaration tests that never read the store.
func fcWiringBrokerWith(t *testing.T, on, gateway bool) (nats.JetStreamContext, *messaging.NatsManager, string) {
	t.Helper()
	savedConfig, savedSources, savedMs := Configuration, EventSources, Microservice
	savedGateway, savedPublishers := GatewaySource, ExternalPublishers
	savedInbound, savedFailed, savedCapture := InboundEventsWriter, FailedDecodeWriter, CaptureReader
	savedStore, savedStoreSource := MqttMessageStore, StoreSource
	t.Cleanup(func() {
		Configuration, EventSources, Microservice = savedConfig, savedSources, savedMs
		GatewaySource, ExternalPublishers = savedGateway, savedPublishers
		InboundEventsWriter, FailedDecodeWriter, CaptureReader = savedInbound, savedFailed, savedCapture
		MqttMessageStore, StoreSource = savedStore, savedStoreSource
	})

	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, NoLog: true, NoSigs: true,
		StoreDir: dctest.JetStreamStoreDir(t),
		MQTT:     natsserver.MQTTOpts{Host: "127.0.0.1", Port: -1},
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second))
	t.Cleanup(srv.Shutdown)
	mqttPort := dctest.MQTTPort(t, srv)
	u, err := url.Parse(srv.ClientURL())
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	Microservice = &core.Microservice{InstanceId: "inst-1", FunctionalArea: "event-sources", Readiness: core.NewReadinessGate()}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.InstanceConfiguration.Infrastructure.Nats.Hostname = u.Hostname()
	Microservice.InstanceConfiguration.Infrastructure.Nats.Port = uint32(port)
	Microservice.InstanceConfiguration.Infrastructure.Nats.MqttFailClosed = on
	Microservice.InstanceConfiguration.ApplyDefaults()
	Microservice.Readiness.MarkReadyWithoutAuthSurface()
	Configuration = &config.EventSourcesConfiguration{}
	if gateway {
		Configuration.EventSources = []config.EventSource{{
			Id:            "mqtt1",
			Type:          processor.TYPE_MQTT,
			Configuration: map[string]string{"host": u.Hostname(), "port": strconv.Itoa(mqttPort)},
			Decoder:       config.EventDecoder{Type: processor.DECODER_TYPE_JSON},
		}}
	}
	Configuration.ApplyDefaults()
	EventSources, GatewaySource, ExternalPublishers, StoreSource = nil, nil, nil, nil
	MqttMessageStore = messaging.MqttMessageStoreState{}
	if gateway {
		initializeMetrics()
		buildTestRateLimiters(t)
		require.NoError(t, buildEventSources())
		require.NotNil(t, StoreSource, "the gateway source's store twin was not built")
	} else if on {
		// The declaration tests never read the store; a bare source satisfies the start's
		// refusal of the setting with nothing to read device events from the store.
		StoreSource = processor.NewGatewayJetStreamSource(nil, "bare", nil, nil, nil, nil, nil,
			func(string, string, time.Time, bool, processor.Origin, int) bool { return true })
	}

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)

	nmgr := messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), createNatsComponents)
	nmgr.RecordMaxDeliveries(func(*messaging.NatsManager) (messaging.MaxDeliveryFunc, error) {
		return func(context.Context, messaging.MaxDelivery) (messaging.MaxDeliveryOutcome, error) {
			return messaging.MaxDeliveryLettered, nil
		}, nil
	})
	require.NoError(t, nmgr.Initialize(context.Background()))
	return js, nmgr, fmt.Sprintf("tcp://127.0.0.1:%d", mqttPort)
}

// THE WIRING, through createNatsComponents. With the setting on, starting the broker manager
// declares the fail-closed store and its durables, and does so before the readers it builds
// (the capture durable exists only after); with it off, the start creates no gateway stream at
// all, which is what it did before the setting existed.
//
// GUARD: kills a service that never calls the reconcile (no store), one that calls it after
// its readers, and a flag-off start that touches the gateway's store.
func TestCreateNatsComponentsDeclaresTheFailClosedStoreOnlyWithTheSettingOn(t *testing.T) {
	t.Run("on", func(t *testing.T) {
		js, nmgr := fcWiringBroker(t, true)
		require.NoError(t, nmgr.Start(context.Background()))
		t.Cleanup(func() { _ = nmgr.Stop(context.Background()) })

		si, err := js.StreamInfo(messaging.MqttMessageStore)
		require.NoError(t, err, "the start declared no fail-closed store")
		require.Equal(t, nats.DiscardNew, si.Config.Discard)
		require.Equal(t, si.Created, MqttMessageStore.Epoch, "the service holds the store's epoch for its dedup ids")
		for _, d := range messaging.MqttFailClosedDurables("inst-1") {
			ci, err := js.ConsumerInfo(messaging.MqttMessageStore, d.Name)
			require.NoErrorf(t, err, "the %s durable", d.Kind)
			capture, err := js.ConsumerInfo(messaging.StreamName("inst-1", streams.DeviceEventsCapture),
				messaging.CaptureDurableName("inst-1", "event-sources", streams.DeviceEventsCapture))
			require.NoError(t, err)
			require.True(t, !ci.Created.After(capture.Created),
				"the %s durable was created after the capture reader: the store comes first", d.Kind)
		}
	})
	t.Run("off", func(t *testing.T) {
		js, nmgr := fcWiringBroker(t, false)
		require.NoError(t, nmgr.Start(context.Background()))
		t.Cleanup(func() { _ = nmgr.Stop(context.Background()) })
		_, err := js.StreamInfo(messaging.MqttMessageStore)
		require.ErrorIs(t, err, nats.ErrStreamNotFound, "with the setting off the start must not create the gateway's store")
		require.True(t, MqttMessageStore.Epoch.IsZero())
	})
}

// A broker that serves another instance stops the start with the reason, rather than leaving
// ingest on a store the reconcile refused to own.
func TestTheStartFailsOnASharedBrokerWithTheSettingOn(t *testing.T) {
	js, nmgr := fcWiringBroker(t, true)
	_, err := js.AddStream(&nats.StreamConfig{Name: messaging.StreamName("otherinst", streams.InboundEvents),
		Subjects: []string{"otherinst.*." + streams.InboundEvents}, Storage: nats.MemoryStorage})
	require.NoError(t, err)
	err = nmgr.Start(context.Background())
	t.Cleanup(func() { _ = nmgr.Stop(context.Background()) })
	require.ErrorIs(t, err, messaging.ErrMqttFailClosedSharedBroker)
}

// With the setting on and no source pointed at the platform broker, nothing would read device
// events from the store, which would fill and then refuse every QoS 1 publish: the start fails
// saying so.
//
// GUARD: kills a start that declares a store it has no reader for.
func TestTheStartFailsWithTheSettingOnAndNoGatewaySource(t *testing.T) {
	_, nmgr := fcWiringBroker(t, true)
	StoreSource = nil
	err := nmgr.Start(context.Background())
	t.Cleanup(func() { _ = nmgr.Stop(context.Background()) })
	require.ErrorIs(t, err, errNoGatewaySourceForStore)
}

// THE READERS, through the code that starts them. With the setting on, mqttStoreIngest builds
// the store source over the events durable and the forwarders for command responses and device
// reports, and a device's QoS 1 event published over MQTT reaches inbound-events through it;
// the capture source, started with the service, skips the direct copy, so it arrives once.
//
// GUARD: kills a start that never builds the store readers (the store would fill and refuse),
// one that hands the store source no writer, and a capture path that forwards the direct copy
// too.
func TestTheServiceReadsTheMessageStoreWithTheSettingOn(t *testing.T) {
	js, nmgr, mqttURL := fcWiringBrokerWith(t, true, true)
	ctx := context.Background()
	require.NoError(t, nmgr.Start(ctx))
	t.Cleanup(func() { _ = nmgr.Stop(context.Background()) })
	// The start's pass created the durables, so it left the direct streams without from marks
	// (nothing is skipped until the durables have settled). A second pass, with the settle
	// shortened, records them, as the watch's first pass after the settle does.
	messaging.SetMqttFailClosedFromSettleForTesting(t, 0)
	_, err := nmgr.ReconcileMqttFailClosed(ctx)
	require.NoError(t, err)
	require.NoError(t, GatewaySource.Initialize(ctx))
	require.NoError(t, GatewaySource.Start(ctx))
	t.Cleanup(func() { _ = GatewaySource.Stop(context.Background()) })
	saved := mqttStoreIngest
	mqttStoreIngest = &storeIngest{}
	t.Cleanup(func() {
		mqttStoreIngest.stop(context.Background())
		mqttStoreIngest = saved
	})
	require.NoError(t, mqttStoreIngest.start(ctx, nmgr))
	require.True(t, mqttStoreIngest.running())

	clientID, err := messaging.DeviceClientID("inst-1", "acme", "sensor-001")
	require.NoError(t, err)
	opts := mqtt.NewClientOptions().AddBroker(mqttURL).SetClientID(clientID).SetConnectTimeout(5 * time.Second)
	c := mqtt.NewClient(opts)
	tok := c.Connect()
	require.True(t, tok.WaitTimeout(10*time.Second))
	require.NoError(t, tok.Error())
	t.Cleanup(func() { c.Disconnect(100) })
	pub := c.Publish("inst-1/acme/devices/sensor-001/events", 1, false,
		`{"device":"sensor-001","eventType":"Measurement","payload":{"entries":[{"measurements":{"t":"1"}}]}}`)
	require.True(t, pub.WaitTimeout(10*time.Second))
	require.NoError(t, pub.Error())

	inbound := messaging.StreamName("inst-1", streams.InboundEvents)
	require.Eventually(t, func() bool {
		info, err := js.StreamInfo(inbound)
		return err == nil && info.State.Msgs >= 1
	}, 20*time.Second, 50*time.Millisecond, "the event never reached inbound-events")
	time.Sleep(time.Second)
	info, err := js.StreamInfo(inbound)
	require.NoError(t, err)
	require.EqualValues(t, 1, info.State.Msgs, "the event reached inbound-events by both copies")
	m, err := js.GetMsg(inbound, info.State.LastSeq)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(m.Header.Get(nats.MsgIdHdr), "acme.m"),
		"the event was forwarded from the capture path, not the store (dedup id %q)", m.Header.Get(nats.MsgIdHdr))
}
