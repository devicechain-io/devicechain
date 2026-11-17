// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-event-sources/processor"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// A process that cannot see the stream does not answer "accepting": with no manager the
// admission refuses, and counts the refusal against its source.
func TestAdmitInboundRefusesWithoutAManager(t *testing.T) {
	savedMs, savedNats := Microservice, NatsManager
	t.Cleanup(func() { Microservice, NatsManager = savedMs, savedNats })
	Microservice = &core.Microservice{InstanceId: "inst-1", FunctionalArea: "event-sources"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	initializeMetrics()
	NatsManager = nil

	err := admitInbound("", "http1")
	require.ErrorIs(t, err, messaging.ErrStreamBackpressure)
	require.Equal(t, float64(1), testutil.ToFloat64(BackpressureCounter.WithLabelValues("http1")))
}

// An HTTP device posting into an ingest pipeline whose resolver has stopped reading, end to
// end through the service's own wiring: buildEventSources builds the HTTP source,
// createNatsComponents builds the inbound-events writer, and a device-management durable
// that never reads sits on the stream, which holds 20 messages.
//
// Before, every POST was answered 202 and the stream discarded its oldest event, one the
// device had been told was accepted, as soon as it filled. Now the service answers 503 with
// a Retry-After once the unread backlog reaches 90% of the ceiling, and keeps every event
// it accepted.
func TestHttpIngestAnswers503InsteadOfEvictingUnreadEvents(t *testing.T) {
	savedConfig, savedSources, savedMs, savedNats := Configuration, EventSources, Microservice, NatsManager
	savedGateway, savedGatewayId := GatewaySource, GatewaySourceId
	savedInbound, savedFailed, savedCapture := InboundEventsWriter, FailedDecodeWriter, CaptureReader
	t.Cleanup(func() {
		Configuration, EventSources, Microservice, NatsManager = savedConfig, savedSources, savedMs, savedNats
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

	microservice := func(area string) *core.Microservice {
		ms := &core.Microservice{InstanceId: "inst-1", FunctionalArea: area, Readiness: core.NewReadinessGate()}
		ms.UseMetricsRegistry(prometheus.NewRegistry())
		ms.Readiness.MarkReadyWithoutAuthSurface()
		ms.InstanceConfiguration.Infrastructure.Nats.Hostname = u.Hostname()
		ms.InstanceConfiguration.Infrastructure.Nats.Port = uint32(port)
		ms.InstanceConfiguration.Infrastructure.Nats.StreamMaxMsgs = 20
		return ms
	}

	// device-management's durable on inbound-events: created, never read.
	dm := messaging.NewNatsManager(microservice("device-management"), core.NewNoOpLifecycleCallbacks(),
		func(*messaging.NatsManager) error { return nil })
	require.NoError(t, dm.Initialize(context.Background()))
	t.Cleanup(func() { _ = dm.Stop(context.Background()) })
	_, err = dm.NewReader(streams.InboundEvents)
	require.NoError(t, err)

	Microservice = microservice("event-sources")
	initializeMetrics()
	Configuration = &config.EventSourcesConfiguration{
		EventSources: []config.EventSource{{
			Id:            "http1",
			Type:          config.SourceTypeHttp,
			Configuration: map[string]string{"port": "8081"},
			Decoder:       config.EventDecoder{Type: processor.DECODER_TYPE_JSON},
		}},
	}
	Configuration.ApplyDefaults()
	GatewaySource = nil
	buildTestRateLimiters(t)
	require.NoError(t, buildEventSources())

	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), createNatsComponents)
	NatsManager.RecordMaxDeliveries(func(*messaging.NatsManager) (messaging.MaxDeliveryFunc, error) {
		return func(context.Context, messaging.MaxDelivery) (messaging.MaxDeliveryOutcome, error) {
			return messaging.MaxDeliveryLettered, nil
		}, nil
	})
	ctx := context.Background()
	require.NoError(t, NatsManager.Initialize(ctx))
	require.NoError(t, NatsManager.Start(ctx))
	t.Cleanup(func() { _ = NatsManager.Stop(context.Background()) })

	source := EventSources[0].(*processor.HttpEventSource)
	source.Port = 0
	require.NoError(t, source.Initialize(ctx))
	require.NoError(t, source.Start(ctx))
	t.Cleanup(func() { _ = source.Stop(context.Background()) })

	const posts = 30
	statuses := make([]int, 0, posts)
	retryAfter := ""
	for i := 0; i < posts; i++ {
		resp, err := http.Post("http://"+source.Addr()+"/inst-1/acme/events", "application/json",
			strings.NewReader(`{"device":"sensor-001","eventType":"Measurement","payload":{"entries":[{"measurements":{"t":"1"}}]}}`))
		require.NoError(t, err)
		_ = resp.Body.Close()
		statuses = append(statuses, resp.StatusCode)
		if resp.StatusCode == http.StatusServiceUnavailable && retryAfter == "" {
			retryAfter = resp.Header.Get("Retry-After")
		}
		// The gate is measured by the traffic that consults it, every few seconds; this
		// fills far faster, so it says when each measurement happens.
		NatsManager.MeasureBackpressureForTesting(t, streams.InboundEvents)
	}

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	inbound := messaging.StreamName("inst-1", streams.InboundEvents)
	info, err := js.StreamInfo(inbound)
	require.NoError(t, err)
	_, err = js.GetMsg(inbound, 1)
	if errors.Is(err, nats.ErrMsgNotFound) {
		t.Fatalf("the first accepted event was evicted before device-management read it (stream first=%d last=%d); "+
			"statuses %v", info.State.FirstSeq, info.State.LastSeq, statuses)
	}
	require.NoError(t, err)

	accepted := 0
	for _, s := range statuses {
		if s == http.StatusAccepted {
			accepted++
		}
	}
	require.Equal(t, 18, accepted, "statuses %v: every event past 90%% of the ceiling must be refused", statuses)
	require.Equal(t, uint64(18), info.State.LastSeq, "the stream must hold exactly the events that were answered 202")
	require.Equal(t, http.StatusServiceUnavailable, statuses[posts-1], "statuses %v", statuses)
	require.Equal(t, "10", retryAfter, "a backpressure 503 must say when to retry")
	require.Equal(t, float64(posts-18), testutil.ToFloat64(BackpressureCounter.WithLabelValues("http1")),
		"every refused POST is counted against its source")
}

type httpAdmitRouter struct{}

func (httpAdmitRouter) Ready() error { return nil }
func (httpAdmitRouter) ShardGroup(tenant string) (int, error) {
	if tenant == "tenant-b" {
		return 1, nil
	}
	return 0, nil
}

func TestHttpShardAdmissionUsesTenantGate(t *testing.T) {
	savedMs, savedNats, savedCounter := Microservice, NatsManager, BackpressureCounter
	t.Cleanup(func() { Microservice, NatsManager, BackpressureCounter = savedMs, savedNats, savedCounter })
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
	ms := &core.Microservice{InstanceId: "inst-1", FunctionalArea: "event-sources"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ms.InstanceConfiguration.Infrastructure.Nats.Hostname = u.Hostname()
	ms.InstanceConfiguration.Infrastructure.Nats.Port = uint32(port)
	ms.InstanceConfiguration.Infrastructure.Nats.StreamMaxMsgs = 20
	Microservice = ms
	initializeMetrics()
	m := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*messaging.NatsManager) error { return nil })
	require.NoError(t, m.EnableStreamSharding(httpAdmitRouter{}, &mscfg.NatsShardGroupsConfiguration{Count: 2, Suffixes: []string{streams.InboundEvents}}))
	require.NoError(t, m.Initialize(context.Background()))
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	NatsManager = m
	_, err = m.NewWriter(streams.InboundEvents)
	require.NoError(t, err)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	// A real resolver durable never reads group zero; group one remains empty.
	_, err = js.AddConsumer(messaging.StreamName("inst-1", streams.InboundEvents), &nats.ConsumerConfig{
		Durable:   messaging.ReaderDurableName("inst-1", "device-management", streams.InboundEvents),
		AckPolicy: nats.AckExplicitPolicy,
	})
	require.NoError(t, err)
	for i := 0; i < 18; i++ {
		_, err = js.Publish(messaging.ScopedSubject("inst-1", "tenant-a", streams.InboundEvents), []byte("backlog"))
		require.NoError(t, err)
	}
	m.MeasureBackpressureForTesting(t, streams.InboundEvents)
	m.MeasureBackpressureForTesting(t, messaging.GroupSuffix(streams.InboundEvents, 1))
	require.ErrorIs(t, m.Backpressure(streams.InboundEvents), messaging.ErrStreamBackpressure)
	require.NoError(t, m.Backpressure(messaging.GroupSuffix(streams.InboundEvents, 1)))
	source, err := processor.NewHttpEventSource("http-shards", map[string]string{}, "inst-1", config.HttpIngest{},
		processor.NewJsonDecoder(map[string]string{}), func(string, []byte) {},
		func(string, string, *model.UnresolvedEvent, interface{}, string) error { return nil },
		func(string, string, []byte, error) error { return nil }, nil,
		func(string, string, time.Time, bool, processor.Origin, int) bool { return true }, admitInbound, nil)
	require.NoError(t, err)
	source.Port = 0
	require.NoError(t, source.Initialize(context.Background()))
	require.NoError(t, source.Start(context.Background()))
	t.Cleanup(func() { _ = source.Stop(context.Background()) })
	for _, tenant := range []string{"tenant-b", "tenant-a"} {
		resp, err := http.Post("http://"+source.Addr()+"/inst-1/"+tenant+"/events", "application/json",
			strings.NewReader(`{"device":"sensor-001","eventType":"Measurement","payload":{"entries":[{"measurements":{"t":"1"}}]}}`))
		require.NoError(t, err)
		_ = resp.Body.Close()
		if tenant == "tenant-b" {
			require.Equal(t, http.StatusAccepted, resp.StatusCode)
		} else {
			require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
			require.Equal(t, "10", resp.Header.Get("Retry-After"))
		}
	}
}
