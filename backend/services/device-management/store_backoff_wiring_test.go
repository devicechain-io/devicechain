// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-device-management/processor"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	nats "github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// spyStoreBackoffGate records what the back-off was wired into the callout with.
type spyStoreBackoffGate struct {
	calls     int
	storeFull func() bool
	refused   prometheus.Counter
}

func (s *spyStoreBackoffGate) WithStoreBackoff(storeFull func() bool, refused prometheus.Counter) *processor.CalloutResponder {
	s.calls++
	s.storeFull, s.refused = storeFull, refused
	return nil
}

// registeredSeries is the names of every series on reg that contain fragment.
func registeredSeries(t *testing.T, reg *prometheus.Registry, fragment string) []string {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	var names []string
	for _, f := range families {
		if strings.Contains(f.GetName(), fragment) {
			names = append(names, f.GetName())
		}
	}
	return names
}

// 🔑 THE SETTING DECIDES THE WIRING, AND OFF MEANS NOTHING AT ALL. With
// infrastructure.nats.mqttFailClosed off, the service registers none of the back-off's series,
// samples nothing and leaves the callout ungated; on, it registers them once (on the initialize
// path), and the callout is gated on a back-off that reads the broker's real message store.
//
// GUARD: kills a wiring that ignores the setting (samples and gates with it off, or registers
// the series), one that never hands the back-off to the callout, and one that hands it a
// decision other than the sampler's.
func TestTheStoreBackoffIsWiredOnlyWithTheSettingOn(t *testing.T) {
	prevMs := Microservice
	t.Cleanup(func() { Microservice = prevMs })

	for _, on := range []bool{false, true} {
		reg := prometheus.NewRegistry()
		Microservice = &core.Microservice{InstanceId: "test", FunctionalArea: "device-management"}
		Microservice.UseMetricsRegistry(reg)
		Microservice.InstanceConfiguration.Infrastructure.Nats.MqttFailClosed = on
		metrics, refused := buildMqttStoreBackoffMetrics()
		series := registeredSeries(t, reg, "store")
		if !on {
			require.Nil(t, metrics)
			require.Nil(t, refused)
			require.Empty(t, series, "the back-off registered series with the setting off")
			spy := &spyStoreBackoffGate{}
			require.Nil(t, wireMqttStoreBackoff(false, nil, nil, nil, spy), "the back-off started with the setting off")
			require.Zero(t, spy.calls, "the callout was gated with the setting off")
			continue
		}
		require.NotNil(t, metrics)
		require.NotNil(t, refused)
		require.Len(t, series, 5, "the back-off's four series and the callout's refusal counter: %v", series)
	}

	// On, against a real broker whose message store is above its high-water mark.
	host, port := startEmbeddedNats(t)
	area := fmt.Sprintf("device-management-backoff-%d", time.Now().UnixNano())
	Microservice = &core.Microservice{InstanceId: area, FunctionalArea: area, Readiness: core.NewReadinessGate()}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port}
	nmgr := messaging.NewNatsManager(Microservice, core.NewNoOpLifecycleCallbacks(),
		func(*messaging.NatsManager) error { return nil })
	require.NoError(t, nmgr.Initialize(context.Background()))
	require.NoError(t, nmgr.Start(context.Background()))
	t.Cleanup(func() { _ = nmgr.Stop(context.Background()) })

	nc, err := nats.Connect(fmt.Sprintf("nats://%s:%d", host, port))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{Name: messaging.MqttMessageStore, Subjects: []string{"$MQTT.msgs.>"},
		Retention: nats.InterestPolicy, Discard: nats.DiscardNew, MaxMsgs: 10, Storage: nats.MemoryStorage})
	require.NoError(t, err)
	_, err = js.AddConsumer(messaging.MqttMessageStore, &nats.ConsumerConfig{Durable: "platform",
		AckPolicy: nats.AckExplicitPolicy, FilterSubject: "$MQTT.msgs.>"})
	require.NoError(t, err)
	for i := 0; i < 9; i++ {
		_, err := js.Publish("$MQTT.msgs.x", []byte("x"))
		require.NoError(t, err)
	}

	Microservice.InstanceConfiguration.Infrastructure.Nats.MqttFailClosed = true
	metrics, refused := buildMqttStoreBackoffMetrics()
	spy := &spyStoreBackoffGate{}
	b := wireMqttStoreBackoff(true, nmgr, metrics, refused, spy)
	require.NotNil(t, b)
	t.Cleanup(b.Stop)
	require.Equal(t, 1, spy.calls, "the callout was not gated on the back-off")
	require.Same(t, refused, spy.refused, "the callout counts its refusals on another counter")
	require.Eventually(t, spy.storeFull, 10*time.Second, 20*time.Millisecond,
		"the callout's gate does not follow the sampler: 9 of 10 is above the high-water mark")
	b.Stop()
	require.False(t, spy.storeFull(), "a stopped back-off still refuses connects")
}
