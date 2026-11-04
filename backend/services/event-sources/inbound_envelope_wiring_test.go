// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-event-sources/processor"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
)

// The HTTP path's event reaches the group commit, not the plain writer, when the setting built
// one; and a request refused under backpressure is counted once, under its own source.
//
// GUARD: kills an onEventDecoded that ignores HTTPGroupCommit (the event would go to the nil
// InboundEventsWriter and panic), and a group commit whose refusals are not counted per request.
func TestHTTPEventsGoThroughTheGroupCommitWhenItIsBuilt(t *testing.T) {
	sealingFixture(t)
	savedGC := HTTPGroupCommit
	t.Cleanup(func() { HTTPGroupCommit = savedGC })

	var mu sync.Mutex
	var got []messaging.Message
	var refuse bool
	g, err := processor.NewHTTPEnvelopes(messaging.FormerConfig{Linger: 5 * time.Millisecond}, nil,
		func(_ context.Context, _ string, msg messaging.Message) error {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, msg)
			if refuse {
				return &messaging.BackpressureError{}
			}
			return nil
		}, countBackpressured)
	require.NoError(t, err)
	defer g.Stop()
	HTTPGroupCommit = g

	event := func() *model.UnresolvedEvent {
		return &model.UnresolvedEvent{Device: "sensor-001", EventType: eventtype.Measurement}
	}
	require.NoError(t, onEventDecoded("http", "acme", event(), &model.UnresolvedMeasurementsPayload{}, ""))
	mu.Lock()
	require.Len(t, got, 1, "the event was published through the group commit")
	refuse = true
	mu.Unlock()

	before := testutil.ToFloat64(BackpressureCounter.WithLabelValues("http"))
	err = onEventDecoded("http", "acme", event(), &model.UnresolvedMeasurementsPayload{}, "")
	require.ErrorIs(t, err, messaging.ErrStreamBackpressure)
	require.Equal(t, before+1, testutil.ToFloat64(BackpressureCounter.WithLabelValues("http")),
		"one refused request is one count, under its own source")
}

// createNatsComponents turns envelopes on exactly when the setting says so: the gateway source is
// given a round publisher (which refuses a writer the stream table does not let read its writes
// back) and the HTTP path a group commit. With the setting off neither exists.
//
// GUARD: kills a createNatsComponents that forgets UseEnvelopes or the group commit, or builds
// them with the setting off.
func TestCreateNatsComponentsWiresInboundEnvelopesOnlyWhenTheSettingIsOn(t *testing.T) {
	savedMs, savedCfg, savedGW, savedGC, savedCap := Microservice, Configuration, GatewaySource, HTTPGroupCommit, CaptureReader
	t.Cleanup(func() {
		Microservice, Configuration, GatewaySource, HTTPGroupCommit, CaptureReader = savedMs, savedCfg, savedGW, savedGC, savedCap
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

	for _, on := range []bool{false, true} {
		t.Run("inbound="+strconv.FormatBool(on), func(t *testing.T) {
			reg := prometheus.NewRegistry()
			Microservice = &core.Microservice{InstanceId: "envwire", FunctionalArea: "event-sources", Readiness: core.NewReadinessGate()}
			Microservice.UseMetricsRegistry(reg)
			Microservice.Readiness.MarkReadyWithoutAuthSurface()
			Microservice.InstanceConfiguration.Infrastructure.Nats.Hostname = u.Hostname()
			Microservice.InstanceConfiguration.Infrastructure.Nats.Port = uint32(port)
			Microservice.InstanceConfiguration.Infrastructure.Messaging.Envelopes.Inbound = on
			Microservice.MicroserviceConfigurationRaw = []byte(`{}`)
			require.NoError(t, parseConfiguration())
			initializeMetrics()
			HTTPGroupCommit = nil

			GatewaySource = processor.NewGatewayJetStreamSource(Microservice, "gw", processor.NewJsonDecoder(map[string]string{}),
				onMessageReceived, inboundEventMessage, onEventDecodeFailed, nil,
				func(string, string, time.Time, bool, processor.Origin, int) bool { return true }, nil)

			nmgr := messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), createNatsComponents)
			nmgr.RecordMaxDeliveries(func(*messaging.NatsManager) (messaging.MaxDeliveryFunc, error) {
				return func(context.Context, messaging.MaxDelivery) (messaging.MaxDeliveryOutcome, error) {
					return messaging.MaxDeliveryLettered, nil
				}, nil
			})
			ctx := context.Background()
			require.NoError(t, nmgr.Initialize(ctx))
			require.NoError(t, nmgr.Start(ctx))
			t.Cleanup(func() {
				stopHTTPGroupCommit()
				_ = nmgr.Stop(context.Background())
			})

			require.Equal(t, on, GatewaySource.EnvelopesEnabled(), "the gateway source's envelope mode")
			require.Equal(t, on, HTTPGroupCommit != nil, "the HTTP group commit")
		})
	}
}
