// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/processor"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
)

// TestStartsUnderItsDeclaredCatalog runs the service's REAL initialize and start callbacks
// (afterMicroserviceInitialized, afterMicroserviceStarted) over a Microservice marked Deployed,
// the way core.NewMicroservice marks every production one, so the broker manager holds every
// stream, bucket and durable the service opens to what core/streams and core/kv declare for
// "event-sources". Every other test here builds a Microservice with Deployed unset, which
// bypasses the catalog: a gap would crash-loop the pod with CI green.
//
// The configuration turns on every optional path that opens something on the broker: a
// gateway source (capture reader + ordered writer), an external MQTT source (async writer and
// the distributed lease bucket, acquired when the source starts), an HTTP source, the broker
// presence tap's writer, and mqttFailClosed (the MQTT message store, its three durable
// readers, the command-responses and device-reports forwarding writers).
func TestStartsUnderItsDeclaredCatalog(t *testing.T) {
	savedMs, savedCfg, savedSources, savedSvc, savedNats := Microservice, Configuration, EventSources, Svc, NatsManager
	savedTransit, savedGates := SecretTransit, safetyGates
	savedGateway, savedGatewayId, savedStoreSource := GatewaySource, GatewaySourceId, StoreSource
	savedPublishers, savedStore := ExternalPublishers, MqttMessageStore
	savedInbound, savedFailed, savedCapture, savedPresence := InboundEventsWriter, FailedDecodeWriter, CaptureReader, PresenceWriter
	savedWatch, savedBound, savedFCM, savedBM := MqttFailClosedWatch, MqttStoreBoundWatch, MqttFailClosedMetrics, MqttStoreBoundMetrics
	savedLive, savedBacklog, savedHTTP := RateLimiter, BacklogRateLimiter, HttpRateLimiter
	savedRLive, savedRBacklog, savedRHTTP := ReadingRateLimiter, BacklogReadingRateLimiter, HttpReadingRateLimiter
	savedShed, savedIngest, savedReading := ShedPriorityResolver, ingestGate, readingGate
	t.Cleanup(func() {
		Microservice, Configuration, EventSources, Svc, NatsManager = savedMs, savedCfg, savedSources, savedSvc, savedNats
		SecretTransit, safetyGates = savedTransit, savedGates
		GatewaySource, GatewaySourceId, StoreSource = savedGateway, savedGatewayId, savedStoreSource
		ExternalPublishers, MqttMessageStore = savedPublishers, savedStore
		InboundEventsWriter, FailedDecodeWriter, CaptureReader, PresenceWriter = savedInbound, savedFailed, savedCapture, savedPresence
		MqttFailClosedWatch, MqttStoreBoundWatch, MqttFailClosedMetrics, MqttStoreBoundMetrics = savedWatch, savedBound, savedFCM, savedBM
		RateLimiter, BacklogRateLimiter, HttpRateLimiter = savedLive, savedBacklog, savedHTTP
		ReadingRateLimiter, BacklogReadingRateLimiter, HttpReadingRateLimiter = savedRLive, savedRBacklog, savedRHTTP
		ShedPriorityResolver, ingestGate, readingGate = savedShed, savedIngest, savedReading
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

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	httpPort := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())

	// The gateway source names the platform broker by the configured NATS hostname; the
	// external source names the same broker by another spelling, so it takes the paho path.
	cfgJSON := fmt.Sprintf(`{"eventSources":[
	  {"id":"mqtt1","type":"mqtt","configuration":{"host":%q,"port":%q},"decoder":{"type":"json"}},
	  {"id":"ext1","type":"mqtt","configuration":{"host":"localhost","port":%q,"topic":"ext/+/#"},"decoder":{"type":"json"}},
	  {"id":"http1","type":"http","configuration":{"port":%q},"decoder":{"type":"json"}}
	]}`, u.Hostname(), strconv.Itoa(mqttPort), strconv.Itoa(mqttPort), strconv.Itoa(httpPort))

	Microservice = &core.Microservice{
		InstanceId:     "catalog-boot-es",
		FunctionalArea: "event-sources",
		Deployed:       true,
		Readiness:      core.NewReadinessGate(),
	}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.MicroserviceConfigurationRaw = []byte(cfgJSON)
	infra := &Microservice.InstanceConfiguration.Infrastructure
	infra.Nats.Hostname = u.Hostname()
	infra.Nats.Port = uint32(port)
	infra.Nats.MqttFailClosed = true
	infra.Secrets.RootKey = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	Microservice.InstanceConfiguration.ApplyDefaults()
	Microservice.Readiness.MarkReadyWithoutAuthSurface()
	// Fresh package state: initialize builds all of it.
	EventSources, GatewaySource, StoreSource, ExternalPublishers, PresenceWriter = nil, nil, nil, nil, nil
	MqttMessageStore = messaging.MqttMessageStoreState{}

	ctx := context.Background()
	require.NoError(t, afterMicroserviceInitialized(ctx),
		"initialize under the catalog (the broker manager connects)")
	t.Cleanup(func() { _ = beforeMicroserviceStopped(context.Background()) })
	require.NotNil(t, NatsManager)

	require.NoError(t, afterMicroserviceStarted(ctx),
		"start under the catalog (Svc.Start runs createNatsComponents, sources, MigratePendingRetention, MQTT store readers + forwarders, presence, session kick)")

	require.True(t, Configuration.BrokerPresence.IsEnabled(), "the presence tap's writer is part of what this covers")
	require.NotNil(t, PresenceWriter, "inbound-events async writer (presence_wiring.go newPresenceWriter, WriterPresence)")
	require.NotNil(t, CaptureReader, "device-events-capture reader")
	require.NotNil(t, InboundEventsWriter, "inbound-events writer")
	require.NotNil(t, FailedDecodeWriter, "failed-decode writer")
	require.False(t, MqttMessageStore.Epoch.IsZero(), "the fail-closed MQTT message store")

	// Lazy paths. The external source takes its distributed lease (the lease bucket) when it
	// starts, from main.go externalSourceLeases; wait for it to own the source.
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(ExternalMqttOwnerGauge.WithLabelValues("ext1")) == 1
	}, 20*time.Second, 50*time.Millisecond,
		"the external MQTT source never acquired its lease under the catalog (NewDistributedLease, main.go externalSourceLeases)")

	// The fail-closed store's readers and forwarders (mqtt_failclosed.go start): events,
	// command-responses and device-reports readers, the command-responses and device-reports
	// writers and the gateway's ordered writer, all started by afterMicroserviceStarted.
	require.True(t, mqttStoreIngest.running(), "the MQTT message store readers (mqtt_failclosed.go start)")

	// Opened directly, in the production mode, because only a live external connection would
	// reach them again: the external source's writer (processor/external_publisher.go
	// NewExternalMqttWriter, built once per source in createNatsComponents above) and the
	// gateway's ordered writer (processor/gateway_jetstream.go NewInboundWriter).
	nmgr := NatsManager
	w, err := processor.NewExternalMqttWriter(nmgr, Configuration.ExternalMqtt.PublishWindowSize())
	require.NoError(t, err, "inbound-events async writer as an external MQTT source opens it")
	w.Close()
	ow, err := newGatewayWriter(nmgr)
	require.NoError(t, err, "inbound-events ordered writer as the gateway opens it")
	ow.Close()
	_, err = nmgr.NewWriter(streams.InboundEvents)
	require.NoError(t, err, "inbound-events writer")
}
