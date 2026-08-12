// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func canaryFor(t *testing.T, mqttPort int, replica string) (*Canary, prometheus.Counter, prometheus.Counter) {
	t.Helper()
	observed := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_observed"})
	missed := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_missed"})
	c := NewCanary(testInstance, replica, fmt.Sprintf("tcp://127.0.0.1:%d", mqttPort),
		"dc_service", "svc", nil, CanaryMetrics{Observed: observed, Missed: missed}, 15*time.Second)
	return c, observed, missed
}

func counter(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	m := &dto.Metric{}
	require.NoError(t, c.(prometheus.Metric).Write(m))
	return m.GetCounter().GetValue()
}

// The healthy case: the canary opens its own MQTT connection and observes the broker
// announcing it.
func TestTheCanaryObservesItsOwnConnect(t *testing.T) {
	srv, mqttPort := broker(t)
	nc := sysConn(t, srv)
	c, observed, missed := canaryFor(t, mqttPort, "pod-a")

	stop, err := c.Watch(nc)
	require.NoError(t, err)
	t.Cleanup(stop)

	require.NoError(t, c.Probe(t.Context()), "the canary did not observe its own connect")
	require.Equal(t, 1.0, counter(t, observed))
	require.Equal(t, 0.0, counter(t, missed))
}

// 🔴 THE NEGATIVE CONTROL. A check is worth nothing until it has been shown to fail,
// and this instrument exists precisely because the obvious one (counting presence
// events) reads identically whether the tap is healthy or dead. Here the subscription
// is torn down before probing — the exact "bound but observing nothing" state — and the
// canary must report it.
//
// Without this test, a canary that returned nil unconditionally would pass the healthy
// case above and alarm on nothing, forever.
func TestTheCanaryFailsWhenNothingIsWatching(t *testing.T) {
	srv, mqttPort := broker(t)
	nc := sysConn(t, srv)
	c, observed, missed := canaryFor(t, mqttPort, "pod-a")

	stop, err := c.Watch(nc)
	require.NoError(t, err)
	stop() // the subscription is gone; the broker still publishes, nobody is listening

	err = c.Probe(t.Context())
	require.Error(t, err, "the canary reported success with no subscription at all")
	require.Equal(t, 0.0, counter(t, observed))
	require.Equal(t, 1.0, counter(t, missed), "a failed probe must be counted, not just returned")
}

// The canary must not become presence. It connects under a device-shaped client id, so
// the broker announces it exactly like a device — and the tenant it names does not
// exist.
func TestTheCanaryDoesNotAssertPresence(t *testing.T) {
	srv, mqttPort := broker(t)
	emitter := newRecordingEmitter()
	_, nc := tapFor(t, srv, emitter)
	c, _, _ := canaryFor(t, mqttPort, "pod-a")

	stop, err := c.Watch(nc)
	require.NoError(t, err)
	t.Cleanup(stop)
	require.NoError(t, c.Probe(t.Context()))

	emitter.refute(t, "presence for the canary's own connection", func(r recorded) bool {
		return r.Tenant == CanaryTenant
	})

	// The counterweight: a real device on the same broker IS asserted, so the
	// exclusion above is about the canary rather than a tap that emits nothing.
	dev := connectDevice(t, mqttPort, testInstance+":acme:sensor-001")
	t.Cleanup(func() { dev.Disconnect(50) })
	emitter.await(t, "a real device's connect alongside the canary", isDevice("acme", "sensor-001", true))
}

// Two replicas' canaries must not take each other's connections over. A duplicate MQTT
// client id is a takeover (measured against the broker), so a shared canary id would
// make N replicas evict one another in a loop and each report the others' evictions as
// its own failure.
func TestTwoReplicasCanariesDoNotCollide(t *testing.T) {
	a, err := CanaryClientID(testInstance, "pod-a")
	require.NoError(t, err)
	b, err := CanaryClientID(testInstance, "pod-b")
	require.NoError(t, err)
	require.NotEqual(t, a, b, "every replica's canary must present a distinct client id")
}

// 🔴 A REPLICA MUST OBSERVE ITS OWN CONNECT, NOT ANY CANARY'S. The tap's whole reason
// for giving each replica its own subscription is that the queue group would otherwise
// let one replica's health be reported by another's traffic. Accepting a sibling's
// advisory rebuilds exactly that: replica A's observation path could be broken while
// replica B's probes keep A green, which is the cannot-fail instrument this design
// exists to avoid.
//
// The advisory is fed to observe directly rather than through a broker, because the
// claim is about the matching rule and a real broker cannot be made to deliver a
// sibling's advisory on demand.
func TestACanaryDoesNotAcceptASiblingsConnect(t *testing.T) {
	c, _, _ := canaryFor(t, 0, "pod-a")
	mine, err := CanaryClientID(testInstance, "pod-a")
	require.NoError(t, err)
	sibling, err := CanaryClientID(testInstance, "pod-b")
	require.NoError(t, err)

	seen := make(chan struct{}, 1)
	c.mu.Lock()
	c.want, c.seen = mine, seen
	c.mu.Unlock()

	c.observe([]byte(advisoryFor(sibling)))
	select {
	case <-seen:
		t.Fatal("replica pod-a accepted pod-b's canary connect as its own")
	default:
	}

	// The counterweight: its OWN advisory must still satisfy it, or the rule above
	// would be satisfied by a canary that never observes anything.
	c.observe([]byte(advisoryFor(mine)))
	select {
	case <-seen:
	default:
		t.Fatal("replica pod-a did not accept its own canary connect")
	}
}

// advisoryFor renders a connect advisory for a client id, in the broker's own wire shape.
func advisoryFor(clientID string) string {
	return `{"client":{"acc":"APP","client_id":"` + clientID + `","client_type":"mqtt",` +
		`"start":"2026-08-12T12:37:44.076882575-04:00"},"type":"io.nats.server.advisory.v1.client_connect"}`
}
