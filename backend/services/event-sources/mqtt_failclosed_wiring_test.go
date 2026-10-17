// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net/url"
	"strconv"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
)

// fcWiringBroker starts a JetStream server with the MQTT gateway on, and points a fresh
// Microservice at it with the fail-closed setting as given. Nothing has connected over MQTT,
// so the gateway has created none of its streams: whatever exists afterwards, the service made.
func fcWiringBroker(t *testing.T, on bool) (nats.JetStreamContext, *messaging.NatsManager) {
	t.Helper()
	savedConfig, savedSources, savedMs := Configuration, EventSources, Microservice
	savedGateway, savedPublishers := GatewaySource, ExternalPublishers
	savedInbound, savedFailed, savedCapture := InboundEventsWriter, FailedDecodeWriter, CaptureReader
	savedStore := MqttMessageStore
	t.Cleanup(func() {
		Configuration, EventSources, Microservice = savedConfig, savedSources, savedMs
		GatewaySource, ExternalPublishers = savedGateway, savedPublishers
		InboundEventsWriter, FailedDecodeWriter, CaptureReader = savedInbound, savedFailed, savedCapture
		MqttMessageStore = savedStore
	})

	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, NoLog: true, NoSigs: true,
		StoreDir: dctest.JetStreamStoreDir(t),
		MQTT:     natsserver.MQTTOpts{Host: "127.0.0.1", Port: dctest.FreeTCPPort(t)},
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
	Microservice.InstanceConfiguration.Infrastructure.Nats.Hostname = u.Hostname()
	Microservice.InstanceConfiguration.Infrastructure.Nats.Port = uint32(port)
	Microservice.InstanceConfiguration.Infrastructure.Nats.MqttFailClosed = on
	Microservice.InstanceConfiguration.ApplyDefaults()
	Configuration = &config.EventSourcesConfiguration{}
	Configuration.ApplyDefaults()
	EventSources, GatewaySource, ExternalPublishers = nil, nil, nil
	MqttMessageStore = messaging.MqttMessageStoreState{}

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
	return js, nmgr
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
				messaging.DurableName("inst-1", "event-sources", streams.DeviceEventsCapture))
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
