// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/processor"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mqttClientsOf lists the MQTT client ids connected to srv that start with prefix.
func mqttClientsOf(t *testing.T, srv *natsserver.Server, prefix string) []string {
	t.Helper()
	connz, err := srv.Connz(&natsserver.ConnzOptions{})
	require.NoError(t, err)
	var ids []string
	for _, c := range connz.Conns {
		if strings.HasPrefix(c.MQTTClient, prefix) {
			ids = append(ids, c.MQTTClient)
		}
	}
	return ids
}

// 🔴 THE OTHER HALF OF THE FIX, THROUGH MAIN'S OWN WIRING. A client id per pod alone would
// give every pod's session every message, and this path publishes no dedup id, so every
// event would be stored once per pod. Nothing but main decides that the external source is
// wrapped in an owner and that the owner's leases come from the platform broker, so this
// builds the sources with buildEventSources as two pods, starts both against one real
// broker (an embedded nats-server with JetStream, for the lease bucket, and its MQTT
// listener, standing in for the operator's broker), and requires exactly one of them to
// connect. Then it stops that one and requires the other to take over.
//
// It also reads the owner gauge main reports through, which is what the
// ExternalMqttSourceNotReadByOnePod alert sums across pods: 1 on the pod that reads, 0 on
// one that stands by. The two pods share one process here, so they write one gauge, and it
// is read only at moments when the last pod to write it is known: pod-a alone after it
// starts and takes the source, pod-b after its own start as a standby, and pod-b again
// after it takes over.
func TestOnlyOnePodConnectsToAnExternalBroker(t *testing.T) {
	savedConfig, savedSources, savedMs, savedNats := Configuration, EventSources, Microservice, NatsManager
	t.Cleanup(func() {
		Configuration, EventSources, Microservice, NatsManager = savedConfig, savedSources, savedMs, savedNats
	})

	mqttPort := dctest.FreeTCPPort(t)
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, NoLog: true, NoSigs: true, ServerName: "owner-wiring",
		StoreDir: dctest.JetStreamStoreDir(t), MQTT: natsserver.MQTTOpts{Host: "127.0.0.1", Port: mqttPort},
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(15*time.Second))
	t.Cleanup(srv.Shutdown)
	u, err := url.Parse(srv.ClientURL())
	require.NoError(t, err)
	natsPort, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	microservice := func() *core.Microservice {
		ms := &core.Microservice{InstanceId: "inst-1", FunctionalArea: "event-sources"}
		ms.UseMetricsRegistry(prometheus.NewRegistry())
		ms.InstanceConfiguration.Infrastructure.Nats.Hostname = u.Hostname()
		ms.InstanceConfiguration.Infrastructure.Nats.Port = uint32(natsPort)
		return ms
	}
	ctx := context.Background()
	NatsManager = messaging.NewNatsManager(microservice(), core.NewNoOpLifecycleCallbacks(),
		func(*messaging.NatsManager) error { return nil })
	require.NoError(t, NatsManager.Initialize(ctx))
	require.NoError(t, NatsManager.Start(ctx))
	t.Cleanup(func() { _ = NatsManager.Stop(context.Background()) })

	// The broker is named "localhost" while the platform broker is 127.0.0.1, so the source
	// takes the external branch, not the gateway's.
	build := func(pod string) core.LifecycleComponent {
		t.Setenv("HOSTNAME", pod)
		Microservice = microservice()
		initializeMetrics()
		Configuration = &config.EventSourcesConfiguration{EventSources: []config.EventSource{{
			Id:   "ext-a",
			Type: processor.TYPE_MQTT,
			// A device's events only: a "+/#" filter would also match the broker's own
			// traffic, the lease bucket's among it.
			Configuration: map[string]string{"host": "localhost", "port": strconv.Itoa(mqttPort),
				"topic": "inst-1/+/devices/+/events"},
			Decoder: config.EventDecoder{Type: processor.DECODER_TYPE_JSON},
		}}}
		buildTestRateLimiters(t)
		require.NoError(t, buildEventSources())
		require.Len(t, EventSources, 1)
		require.IsType(t, &processor.OwnedMqttSource{}, EventSources[0],
			"main built an external source that is not owned, so every pod would read it")
		return EventSources[0]
	}
	podA, podB := build("pod-a"), build("pod-b")
	ownerGauge := func() float64 { return testutil.ToFloat64(ExternalMqttOwnerGauge.WithLabelValues("ext-a")) }

	const prefix = "devicechain:inst-1:ext-a:"
	stopped := map[core.LifecycleComponent]bool{}
	t.Cleanup(func() {
		for _, p := range []core.LifecycleComponent{podA, podB} {
			if !stopped[p] {
				_ = p.Stop(context.Background())
			}
		}
	})

	// pod-a starts alone, so it takes the lease and reads before its start returns.
	require.NoError(t, podA.Initialize(ctx))
	require.NoError(t, podA.Start(ctx))
	assert.Equal(t, 1.0, ownerGauge(), "the pod that reads the source did not report 1")
	require.NoError(t, podB.Initialize(ctx))
	require.NoError(t, podB.Start(ctx))
	assert.Equal(t, 0.0, ownerGauge(), "a standby pod did not report 0")

	// Several standby retries' worth, so a second reader would have had time to connect.
	time.Sleep(3 * time.Second)
	connected := mqttClientsOf(t, srv, prefix)
	require.Equal(t, []string{prefix + "pod-a"}, connected, "exactly one pod may read the source, the one that took it")

	require.NoError(t, podA.Stop(ctx))
	stopped[podA] = true
	require.Eventually(t, func() bool {
		ids := mqttClientsOf(t, srv, prefix)
		return len(ids) == 1 && ids[0] == prefix+"pod-b"
	}, 15*time.Second, 50*time.Millisecond, "the standby never took the source over")
	require.Eventually(t, func() bool { return ownerGauge() == 1 }, 5*time.Second, 20*time.Millisecond,
		"the pod that took the source over did not report 1")
}
