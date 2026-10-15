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

	"github.com/devicechain-io/dc-event-sources/processor"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
)

// The three writers this service publishes inbound-events through — the gateway capture
// writer, an external MQTT source's writer and the presence tap's — each report their
// window and duplicate counters under their OWN writer label. All three share the suffix, so
// without the label a full window on one could not be told from a full window on another.
// Each is built through the function main wires it with, on a registry of its own, so a call
// site that named the wrong writer (or none) fails here by value. The labels are written as
// the literals a dashboard query names, not as the constants, so renaming one fails here too.
func TestEachInboundWriterReportsUnderItsOwnWriterLabel(t *testing.T) {
	savedMs, savedCfg := Microservice, Configuration
	t.Cleanup(func() { Microservice, Configuration = savedMs, savedCfg })

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

	for _, tc := range []struct {
		name  string
		build func(*messaging.NatsManager) (interface{ Close() }, error)
		want  string
	}{
		{"gateway capture", func(n *messaging.NatsManager) (interface{ Close() }, error) {
			return newGatewayWriter(n)
		}, "capture"},
		{"external mqtt", func(n *messaging.NatsManager) (interface{ Close() }, error) {
			return processor.NewExternalMqttWriter(n, Configuration.ExternalMqtt.PublishWindowSize())
		}, "external-mqtt"},
		{"presence", func(n *messaging.NatsManager) (interface{ Close() }, error) {
			return newPresenceWriter(n)
		}, "presence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			Microservice = &core.Microservice{InstanceId: "writerlabel", FunctionalArea: "event-sources", Readiness: core.NewReadinessGate()}
			Microservice.UseMetricsRegistry(reg)
			Microservice.Readiness.MarkReadyWithoutAuthSurface()
			Microservice.InstanceConfiguration.Infrastructure.Nats.Hostname = u.Hostname()
			Microservice.InstanceConfiguration.Infrastructure.Nats.Port = uint32(port)
			Microservice.MicroserviceConfigurationRaw = []byte(`{}`)
			require.NoError(t, parseConfiguration())

			var writer interface{ Close() }
			nmgr := messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(), func(n *messaging.NatsManager) error {
				var err error
				writer, err = tc.build(n)
				return err
			})
			ctx := context.Background()
			require.NoError(t, nmgr.Initialize(ctx))
			require.NoError(t, nmgr.Start(ctx))
			t.Cleanup(func() { _ = nmgr.Stop(context.Background()) })
			require.NotNil(t, writer)
			defer writer.Close()

			require.Equal(t, []string{tc.want}, dctest.PublishWriterLabels(t, reg, streams.InboundEvents),
				"the writer labels of the %s writer's inbound-events window series", tc.name)
		})
	}
}
