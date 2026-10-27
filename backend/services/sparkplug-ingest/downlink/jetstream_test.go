// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package downlink

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/cmddownlink"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
)

// 🔴 TEST 6, against a real JetStream consumer with a short AckWait: a command waiting for its rate
// token is reported as in progress, so the broker does not redeliver it into a second copy while
// the first is still waiting. The wait is twice the AckWait; without the in-progress reports the
// message would be delivered again before it was ever claimed.
func TestAWaitingCommandIsNotRedeliveredByTheBroker(t *testing.T) {
	nmgr := startJetStream(t, "inst-ackwait")
	nmgr.SetAckWaitForTesting(t, 500*time.Millisecond)

	reader, err := nmgr.NewReader(streams.DeviceCommands, messaging.ReaderWithDeliverNew())
	require.NoError(t, err)
	writer, err := nmgr.NewWriter(streams.DeviceCommands)
	require.NoError(t, err)

	r := newRig(t)
	r.src.cfg.PublishPerSecond, r.src.cfg.Burst = 1, 1 // the second command waits 1 s, twice the AckWait
	r.d = New(reader, r.responses, r.fetcher, r.claimer, r.parker, r.store, []Source{r.src},
		Metrics{Commands: r.commands, Awaiting: r.awaiting},
		Options{Workers: 1, ReadPacer: core.NewReadPacer(nil, "device commands"), ProgressEvery: 100 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan struct{})
	go func() { defer close(done); r.d.Run(ctx, func() { close(ready) }) }()
	t.Cleanup(func() { cancel(); <-done })
	<-ready

	send := func(token string, value int) {
		raw := json.RawMessage(writeCmd("Setpoint", value))
		env, _ := json.Marshal(cmddownlink.DeliveryEnvelope{Token: token, DeviceToken: "pump-1", Name: CommandWrite, Payload: &raw, DispatchNonce: "n-" + token})
		tctx := core.WithTenant(context.Background(), "acme")
		require.NoError(t, writer.WriteToDevice(tctx, "pump-1", messaging.Message{
			Value: env, Headers: map[string]string{cmddownlink.HeaderTransport: "sparkplug"},
		}))
	}
	send("first", 1)
	send("second", 2)

	require.Eventually(t, func() bool { return len(r.src.published()) == 2 }, 20*time.Second, 20*time.Millisecond,
		"both commands should have been published; events: %v", r.log.all())

	t.Logf("EVENTS %v", r.log.all())
	// A redelivered copy would be read by the loop and worked once the first finished, which takes
	// a further wait for its own token: give it the time before looking.
	time.Sleep(2500 * time.Millisecond)
	assert.Len(t, r.src.published(), 2, "a redelivered copy was published")
	assert.Equal(t, 1, r.log.count("claim:second"), "the waiting command was delivered and claimed more than once: %v", r.log.all())
	assert.Equal(t, 1, r.log.count("claim:first"))

	js, err := nmgr.Conn().JetStream()
	require.NoError(t, err)
	var redelivered, pending int
	for name := range js.StreamNames() {
		for info := range js.ConsumersInfo(name) {
			redelivered += info.NumRedelivered
			pending += info.NumAckPending
		}
	}
	assert.Zero(t, redelivered, "the broker redelivered a command that was only waiting for its turn")
	assert.Zero(t, pending, "a command was left unacknowledged")
}

// The command reader is term-gated: outside a term it hands out nothing, even a message that is
// waiting; in a term it hands that message out. The pair is the control for each other.
func TestTheCommandReaderHandsOutNothingOutsideATerm(t *testing.T) {
	nmgr := startJetStream(t, "inst-gate")
	var held atomic.Bool
	reader, err := nmgr.NewReader(streams.DeviceCommands, ReaderOptions(held.Load)...)
	require.NoError(t, err)
	writer, err := nmgr.NewWriter(streams.DeviceCommands)
	require.NoError(t, err)
	require.NoError(t, writer.WriteToDevice(core.WithTenant(context.Background(), "acme"), "pump-1",
		messaging.Message{Value: []byte(`{"token":"c","deviceToken":"pump-1","name":"x","dispatchNonce":"n"}`)}))

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	got := make(chan messaging.Message, 1)
	go func() {
		if m, err := reader.ReadMessage(ctx); err == nil {
			got <- m
		}
	}()
	select {
	case <-got:
		t.Fatal("a replica outside a term was handed a command")
	case <-time.After(1500 * time.Millisecond):
	}

	held.Store(true)
	select {
	case m := <-got:
		assert.Contains(t, m.Subject, "device-commands")
	case <-time.After(20 * time.Second):
		t.Fatal("a replica in a term was handed nothing")
	}
}

// startJetStream runs an embedded broker and a started NatsManager for the sparkplug-ingest area.
func startJetStream(t *testing.T, instance string) *messaging.NatsManager {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, NoLog: true, NoSigs: true, ServerName: "dl-" + instance,
		StoreDir: dctest.JetStreamStoreDir(t),
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(15*time.Second))
	t.Cleanup(srv.Shutdown)
	u, err := url.Parse(srv.ClientURL())
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	ms := &core.Microservice{InstanceId: instance, FunctionalArea: "sparkplug-ingest", Readiness: core.NewReadinessGate()}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: u.Hostname(), Port: uint32(port)}
	ms.Readiness.MarkReadyWithoutAuthSurface()
	nmgr := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(*messaging.NatsManager) error { return nil })
	require.NoError(t, nmgr.Initialize(context.Background()))
	require.NoError(t, nmgr.Start(context.Background()))
	t.Cleanup(func() { _ = nmgr.Stop(context.Background()) })
	return nmgr
}
