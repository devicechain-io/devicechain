// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	esconfig "github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/processor"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// presenceAsyncWiring is the broker presence tap as main wires it, against a real broker:
// createNatsComponents builds the tap's writer at brokerPresence.publishWindow, and
// startBrokerPresence subscribes the tap to the advisories and gives it that writer. Nothing but
// main decides that the tap emits asynchronously, at that window, or that the writer is closed
// when the service stops, so this is the only place those can be pinned.
//
// The inbound-events stream is REPLACED by a responder that holds every publish, so what the
// broker holds at once is the number of publishes in flight: the instrument that reads the
// concurrency the tap actually achieves.
type presenceAsyncWiring struct {
	srv *natsserver.Server
	nc  *nats.Conn

	mu       sync.Mutex
	held     []*nats.Msg
	answered int
	stream   string
}

// startPresenceAsyncWiring wires the tap. window nil is the default; synchronous builds the
// control: the same wiring with the tap's writer removed, which is how the tap behaved before.
func startPresenceAsyncWiring(t *testing.T, window *int, synchronous bool,
	natsCallbacks core.LifecycleCallbacks) *presenceAsyncWiring {
	t.Helper()
	savedConfig, savedMs, savedNats, savedGateway := Configuration, Microservice, NatsManager, GatewaySourceId
	savedInbound, savedFailed, savedPresence := InboundEventsWriter, FailedDecodeWriter, brokerPresence
	savedWriter, savedSvc, savedSources, savedGW := PresenceWriter, Svc, EventSources, GatewaySource
	t.Cleanup(func() {
		stopBrokerPresence()
		Configuration, Microservice, NatsManager, GatewaySourceId = savedConfig, savedMs, savedNats, savedGateway
		InboundEventsWriter, FailedDecodeWriter, brokerPresence = savedInbound, savedFailed, savedPresence
		PresenceWriter, Svc, EventSources, GatewaySource = savedWriter, savedSvc, savedSources, savedGW
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

	um := newFakeUM(t)
	umHost, umPort := um.infra(t)
	Microservice = &core.Microservice{InstanceId: "inst-1", FunctionalArea: "event-sources", Readiness: core.NewReadinessGate()}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	Microservice.Readiness.MarkReadyWithoutAuthSurface()
	infra := &Microservice.InstanceConfiguration.Infrastructure
	infra.Nats.Hostname, infra.Nats.Port = u.Hostname(), uint32(port)
	// The test broker has no authentication, so it accepts these as it would any credential.
	infra.Nats.Auth.SysUser, infra.Nats.Auth.SysPassword = "sys", "sys"
	infra.ServiceAuth.Secret = "shh"
	infra.UserManagement.Hostname, infra.UserManagement.Port = umHost, umPort
	infra.DeviceState.Hostname, infra.DeviceState.Port = umHost, umPort
	initializeMetrics()

	Configuration = &esconfig.EventSourcesConfiguration{BrokerPresence: esconfig.BrokerPresence{PublishWindow: window}}
	Configuration.ApplyDefaults()
	require.NoError(t, Configuration.Validate())
	GatewaySource, GatewaySourceId = nil, "mqtt1"
	buildTestRateLimiters(t)
	// The ingest ceiling is not under test, and a burst of advisories must not be refused by it.
	ingestGate = func(string, string, time.Time, bool, processor.Origin) bool { return true }

	NatsManager = messaging.NewNatsManager(Microservice, natsCallbacks, createNatsComponents)
	NatsManager.RecordMaxDeliveries(func(*messaging.NatsManager) (messaging.MaxDeliveryFunc, error) {
		return func(context.Context, messaging.MaxDelivery) (messaging.MaxDeliveryOutcome, error) {
			return messaging.MaxDeliveryLettered, nil
		}, nil
	})
	ctx := context.Background()
	require.NoError(t, NatsManager.Initialize(ctx))
	require.NoError(t, NatsManager.Start(ctx))
	t.Cleanup(func() { _ = NatsManager.Stop(context.Background()) })
	if synchronous {
		closePresenceWriter()
		PresenceWriter = nil
	}

	w := &presenceAsyncWiring{srv: srv, stream: messaging.StreamName("inst-1", streams.InboundEvents)}
	w.nc, err = nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(w.nc.Close)
	js, err := w.nc.JetStream()
	require.NoError(t, err)
	require.NoError(t, js.DeleteStream(w.stream))
	_, err = messaging.SubscribeSynced(w.nc, messaging.ScopedSubject("inst-1", "acme", streams.InboundEvents),
		func(m *nats.Msg) { w.mu.Lock(); w.held = append(w.held, m); w.mu.Unlock() })
	require.NoError(t, err)
	// Whatever a test leaves unanswered is answered at cleanup, so no writer waits on it.
	t.Cleanup(func() { w.answerAll() })

	Svc = service.FromManagers(Microservice, service.Managers{Nats: NatsManager})
	EventSources = nil
	startBrokerPresence(ctx)
	require.NotNil(t, brokerPresence, "the tap must be running")
	require.NotNil(t, brokerPresence.conn, "the tap must be running")
	return w
}

func (w *presenceAsyncWiring) seen() int { w.mu.Lock(); defer w.mu.Unlock(); return len(w.held) }

// inFlight is what the broker holds right now, unanswered.
func (w *presenceAsyncWiring) inFlight() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.held) - w.answered
}

// answerOne answers the oldest unanswered publish with a PubAck.
func (w *presenceAsyncWiring) answerOne() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.answered >= len(w.held) {
		return false
	}
	m := w.held[w.answered]
	w.answered++
	_ = m.Respond([]byte(fmt.Sprintf(`{"stream":%q,"seq":%d}`, w.stream, w.answered)))
	return true
}

func (w *presenceAsyncWiring) answerAll() {
	for w.answerOne() {
	}
}

// answerUntil keeps answering as publishes arrive until n have been answered, bounded.
func (w *presenceAsyncWiring) answerUntil(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		w.mu.Lock()
		done := w.answered >= n
		w.mu.Unlock()
		if done {
			return
		}
		if !w.answerOne() {
			if time.Now().After(deadline) {
				t.Fatalf("only %d of %d publishes ever reached the broker", w.seen(), n)
			}
			time.Sleep(time.Millisecond)
		}
	}
}

// advisory publishes one broker connection advisory for device n, as nats-server does.
func (w *presenceAsyncWiring) advisory(t *testing.T, connected bool, n int) {
	t.Helper()
	start := time.Unix(0, 1_800_000_000_000_000_000+int64(n)*1000).UTC()
	subject, body := natsauth.SysConnectSubject, fmt.Sprintf(
		`{"client":{"client_id":"inst-1:acme:dev-%d","client_type":"mqtt","start":%q}}`, n, start.Format(time.RFC3339Nano))
	if !connected {
		subject, body = natsauth.SysDisconnectSubject, fmt.Sprintf(
			`{"client":{"client_id":"inst-1:acme:dev-%d","client_type":"mqtt","start":%q,"stop":%q},"reason":"Client Closed"}`,
			n, start.Format(time.RFC3339Nano), start.Add(time.Second).Format(time.RFC3339Nano))
	}
	require.NoError(t, w.nc.Publish(subject, []byte(body)))
}

// feed publishes n connects and n disconnects for distinct devices, then flushes.
func (w *presenceAsyncWiring) feed(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		w.advisory(t, true, i)
		w.advisory(t, false, n+i)
	}
	require.NoError(t, w.nc.Flush())
}

// stableInFlight waits for the in-flight count to reach want, then checks that it STAYS there
// while more advisories are waiting: a window is a ceiling that is reached and held.
func (w *presenceAsyncWiring) stableInFlight(t *testing.T, want int) {
	t.Helper()
	require.Eventually(t, func() bool { return w.inFlight() >= want }, 10*time.Second, time.Millisecond,
		"the broker never held %d publishes at once (held %d)", want, w.inFlight())
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, want, w.inFlight(), "publishes the broker held at once")
}

func emittedTotal() float64 {
	return testutil.ToFloat64(PresenceEmittedCounter.WithLabelValues("connected")) +
		testutil.ToFloat64(PresenceEmittedCounter.WithLabelValues("disconnected"))
}

// 🔴 brokerPresence.publishWindow REACHES THE WRITER, AND IT IS WHAT THE BROKER HOLDS. With the
// stream replaced by a responder that holds every publish, a configured 7 holds exactly 7 and
// an unset one holds the default; the SYNCHRONOUS control holds exactly 2, one per subject,
// which is what the tap held before the change and what bounded a pod near 88 transitions a
// second per subject.
func TestPresenceTapKeepsTheConfiguredWindowInFlight(t *testing.T) {
	seven := 7
	for _, tc := range []struct {
		name   string
		window *int
		sync   bool
		want   int
	}{
		{"configured", &seven, false, 7},
		{"unset", nil, false, esconfig.DefaultPresencePublishWindow},
		{"control: no async writer, one per subject", &seven, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := startPresenceAsyncWiring(t, tc.window, tc.sync, core.NewNoOpLifecycleCallbacks())
			total := tc.want*2 + 20
			if total < 40 {
				total = 40
			}
			w.feed(t, total)
			w.stableInFlight(t, tc.want)

			// Answer everything as it arrives: every advisory ends up emitted, none failed.
			w.answerUntil(t, 2*total)
			require.Eventually(t, func() bool { return emittedTotal() == float64(2*total) }, 10*time.Second,
				time.Millisecond, "every acknowledged transition must be counted as emitted")
			assert.Zero(t, testutil.ToFloat64(PresenceFailedCounter))
		})
	}
}

// 🔴 A FAILED EMIT SURFACES. The tap's writer is closed under a running tap, which is how a
// publish fails here without a broker fault: the advisory is neither dropped silently nor
// counted as emitted. presence_events_failed_total is exported at 0 before any failure, so a
// rate over it has a baseline.
func TestPresenceTapCountsAFailedEmit(t *testing.T) {
	w := startPresenceAsyncWiring(t, nil, false, core.NewNoOpLifecycleCallbacks())
	require.Equal(t, 1, testutil.CollectAndCount(PresenceFailedCounter), "the series must exist before the first failure")
	require.Zero(t, testutil.ToFloat64(PresenceFailedCounter))

	PresenceWriter.Close()
	w.advisory(t, true, 1)
	require.NoError(t, w.nc.Flush())
	require.Eventually(t, func() bool { return testutil.ToFloat64(PresenceFailedCounter) == 1 }, 10*time.Second,
		time.Millisecond, "a transition that could not be emitted must be counted as failed")
	assert.Zero(t, emittedTotal(), "a failed transition was counted as emitted")
	assert.Zero(t, w.seen(), "nothing may reach the stream through a closed writer")
}

// closeProbe records the moment the tap's writer is closed.
type closeProbe struct {
	messaging.AsyncWriter
	onClose func()
}

func (p *closeProbe) Close() { p.onClose(); p.AsyncWriter.Close() }

// 🔴 THE STOP HOOK, DRIVEN AS THE SERVICE DRIVES IT. beforeMicroserviceStopped must (1) stop the
// tap's subscriptions, (2) close the writer, waiting for the publishes it holds, and only then
// (3) stop the broker manager. Observed at the caller, with three publishes held unanswered:
// the hook does not return while they are, returns once they are answered with each one
// counted, the broker manager stops with the writer already closed, and an advisory that
// arrives after the stop is neither emitted nor counted as a failure.
func TestStopHookClosesThePresenceWriterAfterTheSubscriptionsAndBeforeTheBroker(t *testing.T) {
	var (
		order        []string
		orderMu      sync.Mutex
		w            *presenceAsyncWiring
		atBrokerStop = make(chan error, 1)
		brokerRan    atomic.Bool
		failedLate   atomic.Int64
		heldLate     atomic.Int64
	)
	mark := func(s string) { orderMu.Lock(); order = append(order, s); orderMu.Unlock() }

	cb := core.NewNoOpLifecycleCallbacks()
	cb.Stopper = core.LifecycleCallback{
		Preprocess: func(context.Context) error {
			mark("broker")
			// A late advisory, after the tap stopped and before the broker manager does.
			w.advisory(t, true, 900)
			_ = w.nc.Flush()
			time.Sleep(300 * time.Millisecond)
			failedLate.Store(int64(testutil.ToFloat64(PresenceFailedCounter)))
			heldLate.Store(int64(w.seen()))
			PresenceWriter.Submit(context.Background(), nil, func(err error) { atBrokerStop <- err })
			brokerRan.Store(true)
			return nil
		},
		Postprocess: func(context.Context) error { return nil },
	}
	w = startPresenceAsyncWiring(t, nil, false, cb)

	// Probes: the tap's subscription stop and the writer's close each record their place.
	rt := brokerPresence
	origTapStop := rt.stops[0]
	rt.stops[0] = func() { origTapStop(); mark("subscriptions") }
	var closedOnce sync.Once
	PresenceWriter = &closeProbe{AsyncWriter: PresenceWriter, onClose: func() { closedOnce.Do(func() { mark("writer") }) }}

	// Three transitions in flight at the stop.
	for i := 0; i < 3; i++ {
		w.advisory(t, true, i)
	}
	require.NoError(t, w.nc.Flush())
	w.stableInFlight(t, 3)

	hookDone := make(chan error, 1)
	go func() { hookDone <- beforeMicroserviceStopped(context.Background()) }()

	select {
	case err := <-hookDone:
		t.Fatalf("the stop hook returned (%v) while three publishes were unacknowledged: the writer was not "+
			"settled before the service moved on", err)
	case <-time.After(500 * time.Millisecond):
	}

	w.answerAll()
	select {
	case err := <-hookDone:
		require.NoError(t, err)
	case <-time.After(20 * time.Second):
		t.Fatal("the stop hook never returned after the publishes were acknowledged")
	}

	orderMu.Lock()
	got := append([]string(nil), order...)
	orderMu.Unlock()
	assert.Equal(t, []string{"subscriptions", "writer", "broker"}, got,
		"the writer closes after the subscriptions stop and before the broker manager does")
	assert.Equal(t, 3.0, emittedTotal(), "what the writer held at the stop must be settled and counted")

	require.True(t, brokerRan.Load(), "the broker manager's stop never ran")
	select {
	case err := <-atBrokerStop:
		require.ErrorIs(t, err, messaging.ErrAsyncWriterClosed, "the writer was still open when the broker manager began to stop")
	case <-time.After(5 * time.Second):
		t.Fatal("the probe submit was never settled")
	}
	assert.Zero(t, failedLate.Load(), "a late advisory reached the tap after its subscriptions stopped")
	assert.EqualValues(t, 3, heldLate.Load(), "a late advisory was emitted after the stop")
}

// A tap that is switched off by configuration builds no writer at all.
func TestNoPresenceWriterWhenThePresenceTapIsDisabled(t *testing.T) {
	off := false
	savedConfig, savedWriter := Configuration, PresenceWriter
	t.Cleanup(func() { Configuration, PresenceWriter = savedConfig, savedWriter })
	PresenceWriter = nil
	Configuration = &esconfig.EventSourcesConfiguration{BrokerPresence: esconfig.BrokerPresence{Enabled: &off}}
	Configuration.ApplyDefaults()
	require.NoError(t, buildPresenceWriter(nil), "a disabled tap must not even touch the broker manager")
	assert.Nil(t, PresenceWriter)
}
