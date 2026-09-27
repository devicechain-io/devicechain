// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-event-sources/processor"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// inboundEventMessage is the ONE definition of what a decoded event is published as, and
// the gateway source pipelines it: the dedup id that keeps a redelivered capture message
// from being stored twice must survive the split from the synchronous path.
func TestTheInboundMessageCarriesTheCaptureDedupID(t *testing.T) {
	savedMs := Microservice
	t.Cleanup(func() { Microservice = savedMs })
	Microservice = &core.Microservice{InstanceId: "inst-1", FunctionalArea: "event-sources"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	initializeMetrics()

	event := func() *model.UnresolvedEvent {
		return &model.UnresolvedEvent{Device: "sensor-001", EventType: model.Measurement, AuthenticatedTransport: true}
	}
	ctx, msg, ok := inboundEventMessage("gw", "acme", event(), &model.UnresolvedMeasurementsPayload{}, 4242)
	require.True(t, ok)
	require.Equal(t, "acme:4242", msg.DedupID)
	require.Equal(t, []byte("sensor-001"), msg.Key)
	tenant, found := core.TenantFromContext(ctx)
	require.True(t, found)
	require.Equal(t, "acme", tenant, "the publish context must carry the tenant: the subject is derived from it")
	decoded, err := esproto.UnmarshalUnresolvedEvent(msg.Value)
	require.NoError(t, err)
	require.Equal(t, "gw", decoded.Source)
	require.False(t, decoded.AuthenticatedTransport, "the device-facing path must never mark an event transport-authenticated")

	_, msg, ok = inboundEventMessage("http", "acme", event(), &model.UnresolvedMeasurementsPayload{}, 0)
	require.True(t, ok)
	require.Equal(t, "", msg.DedupID, "a transport with no capture sequence publishes no dedup id")

	_, _, ok = inboundEventMessage("gw", "", event(), &model.UnresolvedMeasurementsPayload{}, 7)
	require.False(t, ok, "an event with no tenant is a deliberate drop")
}

// THE WIRING, through the code that does it. The gateway source refuses to start without
// an inbound-events writer, and the only thing that hands it one is createNatsComponents in
// main.go — which nothing else runs. So this builds the configured sources and the NATS
// components the way the service does, against a real JetStream server, and drives a
// captured message through to inbound-events.
func TestCreateNatsComponentsHandsTheGatewayItsWriter(t *testing.T) {
	savedConfig, savedSources, savedMs := Configuration, EventSources, Microservice
	savedGateway, savedGatewayId := GatewaySource, GatewaySourceId
	savedInbound, savedFailed, savedCapture := InboundEventsWriter, FailedDecodeWriter, CaptureReader
	t.Cleanup(func() {
		Configuration, EventSources, Microservice = savedConfig, savedSources, savedMs
		GatewaySource, GatewaySourceId = savedGateway, savedGatewayId
		InboundEventsWriter, FailedDecodeWriter, CaptureReader = savedInbound, savedFailed, savedCapture
	})

	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, NoLog: true, NoSigs: true,
		StoreDir: dctest.JetStreamStoreDir(t),
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second))
	t.Cleanup(srv.Shutdown)
	u, err := url.Parse(srv.ClientURL())
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	Microservice = &core.Microservice{InstanceId: "inst-1", FunctionalArea: "event-sources", Readiness: core.NewReadinessGate()}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.Readiness.MarkReadyWithoutAuthSurface()
	Microservice.InstanceConfiguration.Infrastructure.Nats.Hostname = u.Hostname()
	Microservice.InstanceConfiguration.Infrastructure.Nats.Port = uint32(port)
	initializeMetrics()
	Configuration = &config.EventSourcesConfiguration{
		EventSources: []config.EventSource{{
			Id:            "mqtt1",
			Type:          processor.TYPE_MQTT,
			Configuration: map[string]string{"host": u.Hostname(), "port": "1883"},
			Decoder:       config.EventDecoder{Type: processor.DECODER_TYPE_JSON},
		}},
	}
	Configuration.ApplyDefaults()
	GatewaySource = nil
	buildTestRateLimiters(t)
	require.NoError(t, buildEventSources())
	require.NotNil(t, GatewaySource, "a source pointed at the platform broker must be the gateway source")

	nmgr := messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), createNatsComponents)
	nmgr.RecordMaxDeliveries(func(*messaging.NatsManager) (messaging.MaxDeliveryFunc, error) {
		return func(context.Context, messaging.MaxDelivery) (messaging.MaxDeliveryOutcome, error) {
			return messaging.MaxDeliveryLettered, nil
		}, nil
	})
	ctx := context.Background()
	require.NoError(t, nmgr.Initialize(ctx))
	require.NoError(t, nmgr.Start(ctx))
	t.Cleanup(func() { _ = nmgr.Stop(context.Background()) })

	require.NoError(t, GatewaySource.Initialize(ctx))
	require.NoError(t, GatewaySource.Start(ctx),
		"the gateway source did not start: createNatsComponents did not hand it its reader and writer")

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	ack, err := js.Publish(messaging.DeviceEventsSubject("inst-1", "acme", "sensor-001"),
		[]byte(`{"device":"sensor-001","eventType":"Measurement","payload":{"entries":[{"measurements":{"t":"1"}}]}}`))
	require.NoError(t, err)

	inbound := messaging.StreamName("inst-1", streams.InboundEvents)
	require.Eventually(t, func() bool {
		info, err := js.StreamInfo(inbound)
		return err == nil && info.State.Msgs == 1
	}, 10*time.Second, 5*time.Millisecond, "the captured event never reached inbound-events")
	require.NoError(t, GatewaySource.Stop(ctx))

	info, err := js.StreamInfo(inbound)
	require.NoError(t, err)
	m, err := js.GetMsg(inbound, info.State.LastSeq)
	require.NoError(t, err)
	require.Equal(t, "acme:"+strconv.FormatUint(ack.Sequence, 10), m.Header.Get(nats.MsgIdHdr),
		"the published event must carry the dedup id of the capture message it came from")
}
