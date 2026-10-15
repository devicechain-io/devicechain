// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/processor"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// externalWiring is main's own wiring of an external MQTT source against a real broker:
// buildEventSources builds the source and its publisher, createNatsComponents gives the
// publisher its writer. Nothing but main decides that the publisher exists, that its window
// is externalMqtt.publishWindow, or that it refuses at a closed gate, so this is the only
// place those can be pinned.
type externalWiring struct {
	srv *natsserver.Server
	nc  *nats.Conn
	pub *processor.AsyncEventPublisher
}

func startExternalWiring(t *testing.T, window *int, streamMaxMsgs int64) *externalWiring {
	t.Helper()
	savedConfig, savedSources, savedMs, savedNats := Configuration, EventSources, Microservice, NatsManager
	savedGateway, savedGatewayId, savedPublishers := GatewaySource, GatewaySourceId, ExternalPublishers
	savedInbound, savedFailed, savedCapture := InboundEventsWriter, FailedDecodeWriter, CaptureReader
	t.Cleanup(func() {
		Configuration, EventSources, Microservice, NatsManager = savedConfig, savedSources, savedMs, savedNats
		GatewaySource, GatewaySourceId, ExternalPublishers = savedGateway, savedGatewayId, savedPublishers
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
		if streamMaxMsgs > 0 {
			ms.InstanceConfiguration.Infrastructure.Nats.StreamMaxMsgs = streamMaxMsgs
		}
		return ms
	}
	if streamMaxMsgs > 0 {
		// device-management's durable on inbound-events: created, never read, so the
		// stream's unread backlog is what closes the gate.
		dm := messaging.NewNatsManager(microservice("device-management"), core.NewNoOpLifecycleCallbacks(),
			func(*messaging.NatsManager) error { return nil })
		require.NoError(t, dm.Initialize(context.Background()))
		t.Cleanup(func() { _ = dm.Stop(context.Background()) })
		_, err = dm.NewReader(streams.InboundEvents)
		require.NoError(t, err)
	}

	Microservice = microservice("event-sources")
	initializeMetrics()
	Configuration = &config.EventSourcesConfiguration{
		EventSources: []config.EventSource{{
			Id:   "ext-a",
			Type: processor.TYPE_MQTT,
			// "localhost" against a 127.0.0.1 platform broker: the external branch, not the gateway's.
			Configuration: map[string]string{"host": "localhost", "port": "1883", "topic": "inst-1/+/devices/+/events"},
			Decoder:       config.EventDecoder{Type: processor.DECODER_TYPE_JSON},
		}},
		ExternalMqtt: config.ExternalMqtt{PublishWindow: window},
	}
	Configuration.ApplyDefaults()
	require.NoError(t, Configuration.Validate())
	GatewaySource, ExternalPublishers = nil, nil
	buildTestRateLimiters(t)
	require.NoError(t, buildEventSources())
	require.Len(t, ExternalPublishers, 1, "main built an external source with no publisher of its own")

	NatsManager = messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), createNatsComponents)
	NatsManager.RecordMaxDeliveries(func(*messaging.NatsManager) (messaging.MaxDeliveryFunc, error) {
		return func(context.Context, messaging.MaxDelivery) (messaging.MaxDeliveryOutcome, error) {
			return messaging.MaxDeliveryLettered, nil
		}, nil
	})
	ctx := context.Background()
	require.NoError(t, NatsManager.Initialize(ctx))
	require.NoError(t, NatsManager.Start(ctx))
	t.Cleanup(func() { closeExternalPublishers(); _ = NatsManager.Stop(context.Background()) })

	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return &externalWiring{srv: srv, nc: nc, pub: ExternalPublishers[0]}
}

// publish hands the publisher one decoded device event, as a decode worker does.
func (w *externalWiring) publish(t *testing.T, n int) error {
	t.Helper()
	dec := processor.NewJsonDecoder(map[string]string{})
	event, payload, err := dec.Decode([]byte(fmt.Sprintf(
		`{"device":"d1","eventType":"Measurement","payload":{"entries":[{"measurements":{"t":"%d"}}]}}`, n)), time.Time{})
	require.NoError(t, err)
	res := make(chan error, 1)
	w.pub.Publish("ext-a", "acme", event, payload, 0, func(err error) { res <- err })
	select {
	case err := <-res:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the publish was never settled")
		return nil
	}
}

// 🔴 externalMqtt.publishWindow REACHES THE WRITER. The inbound-events stream is replaced by a
// responder that holds every publish, so what the broker holds at once is the window main
// built the writer with: a configured 7 holds 7, and an unset one holds the default.
func TestExternalMqttWindowIsTheConfiguredOne(t *testing.T) {
	seven := 7
	for _, tc := range []struct {
		name   string
		window *int
		want   int
	}{
		{"configured", &seven, 7},
		{"unset", nil, config.DefaultExternalMqttPublishWindow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := startExternalWiring(t, tc.window, 0)
			js, err := w.nc.JetStream()
			require.NoError(t, err)
			stream := messaging.StreamName("inst-1", streams.InboundEvents)
			require.NoError(t, js.DeleteStream(stream))
			var mu sync.Mutex
			var held []*nats.Msg
			_, err = messaging.SubscribeSynced(w.nc, messaging.ScopedSubject("inst-1", "acme", streams.InboundEvents),
				func(m *nats.Msg) { mu.Lock(); held = append(held, m); mu.Unlock() })
			require.NoError(t, err)
			seen := func() int { mu.Lock(); defer mu.Unlock(); return len(held) }
			t.Cleanup(func() {
				mu.Lock()
				defer mu.Unlock()
				for i, m := range held {
					_ = m.Respond([]byte(fmt.Sprintf(`{"stream":%q,"seq":%d}`, stream, i+1)))
				}
			})

			total := tc.want + 20
			submitted := make(chan struct{})
			go func() {
				defer close(submitted)
				for i := 0; i < total; i++ {
					res := make(chan error, 1)
					dec := processor.NewJsonDecoder(map[string]string{})
					event, payload, err := dec.Decode([]byte(fmt.Sprintf(
						`{"device":"d1","eventType":"Measurement","payload":{"entries":[{"measurements":{"t":"%d"}}]}}`, i)), time.Time{})
					if err != nil {
						return
					}
					w.pub.Publish("ext-a", "acme", event, payload, 0, func(err error) { res <- err })
				}
			}()
			require.Eventually(t, func() bool { return seen() >= tc.want }, 10*time.Second, time.Millisecond)
			time.Sleep(200 * time.Millisecond)
			assert.Equal(t, tc.want, seen(), "publishes the broker held at once: the window main built the writer with")

			// Answer everything as it arrives, so the submitter can finish and the writer close.
			for answered := 0; answered < total; answered++ {
				require.Eventually(t, func() bool { return seen() > answered }, 10*time.Second, time.Millisecond)
				mu.Lock()
				m := held[answered]
				mu.Unlock()
				require.NoError(t, m.Respond([]byte(fmt.Sprintf(`{"stream":%q,"seq":%d}`, stream, answered+1))))
			}
			select {
			case <-submitted:
			case <-time.After(10 * time.Second):
				t.Fatal("the submitting goroutine never finished")
			}
			mu.Lock()
			held = held[:0]
			mu.Unlock()
		})
	}
}

// An external source's publish is REFUSED at a closed backpressure gate, as the synchronous
// publish was: the stream holds 20 messages, nothing reads it, and once the unread backlog
// reaches the gate's share the publisher stops writing, counts the refusal against the
// source on total_msg_backpressured (not as a lost event), and the stream keeps every
// event it took. Without the refusal the stream would evict its oldest, unread events.
func TestExternalMqttPublishIsRefusedAtAClosedGate(t *testing.T) {
	w := startExternalWiring(t, nil, 20)

	const posts = 30
	refused, stored := 0, 0
	for i := 0; i < posts; i++ {
		switch err := w.publish(t, i); {
		case err == nil:
			stored++
		case errors.Is(err, messaging.ErrStreamBackpressure):
			refused++
		default:
			t.Fatalf("publish %d failed for a reason other than backpressure: %v", i, err)
		}
		// The gate is measured by the traffic that consults it, every few seconds; this
		// fills far faster, so it says when each measurement happens.
		NatsManager.MeasureBackpressureForTesting(t, streams.InboundEvents)
	}
	require.Positive(t, refused, "the gate never refused an external source's publish")
	assert.Equal(t, float64(refused), testutil.ToFloat64(BackpressureCounter.WithLabelValues("ext-a")),
		"every refusal must be counted against the source")
	assert.Zero(t, testutil.ToFloat64(PublishFailedCounter.WithLabelValues("ext-a")),
		"a refusal at the gate is backpressure, not a lost event")

	js, err := w.nc.JetStream()
	require.NoError(t, err)
	info, err := js.StreamInfo(messaging.StreamName("inst-1", streams.InboundEvents))
	require.NoError(t, err)
	assert.Equal(t, uint64(stored), info.State.Msgs, "the stream must keep every event it accepted")
	assert.LessOrEqual(t, info.State.Msgs, uint64(20))
}

// A failed publish is counted on total_msg_publish_failed, labelled by source: here the
// service's writer is closed, so the publish cannot happen.
func TestExternalMqttFailedPublishIsCountedPerSource(t *testing.T) {
	w := startExternalWiring(t, nil, 0)
	require.NoError(t, w.publish(t, 0))
	require.Zero(t, testutil.ToFloat64(PublishFailedCounter.WithLabelValues("ext-a")))

	closeExternalPublishers()
	require.Error(t, w.publish(t, 1), "a publish after the writers were closed must fail, not vanish")
	assert.Equal(t, 1.0, testutil.ToFloat64(PublishFailedCounter.WithLabelValues("ext-a")))
}
