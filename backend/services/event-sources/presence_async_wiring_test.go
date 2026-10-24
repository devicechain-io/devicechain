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

// tapWiring is how a test has the tap emit.
type tapWiring int

const (
	// tapAsync is production: the writer createNatsComponents built.
	tapAsync tapWiring = iota
	// tapSyncOptIn is the control: the test chose the synchronous tap, which is how the tap
	// behaved before the writer existed.
	tapSyncOptIn
	// tapNoWriter is the wiring fault: the writer is gone and nobody opted into the
	// synchronous tap.
	tapNoWriter
)

// startPresenceAsyncWiring wires the tap. window nil is the default.
func startPresenceAsyncWiring(t *testing.T, window *int, wiring tapWiring,
	natsCallbacks core.LifecycleCallbacks) *presenceAsyncWiring {
	t.Helper()
	savedConfig, savedMs, savedNats, savedGateway := Configuration, Microservice, NatsManager, GatewaySourceId
	savedInbound, savedFailed, savedPresence := InboundEventsWriter, FailedDecodeWriter, brokerPresence
	savedWriter, savedSvc, savedSources, savedGW := PresenceWriter, Svc, EventSources, GatewaySource
	savedSync, savedGate := presenceTapSynchronous, ingestGate
	t.Cleanup(func() {
		stopBrokerPresence()
		Configuration, Microservice, NatsManager, GatewaySourceId = savedConfig, savedMs, savedNats, savedGateway
		InboundEventsWriter, FailedDecodeWriter, brokerPresence = savedInbound, savedFailed, savedPresence
		PresenceWriter, Svc, EventSources, GatewaySource = savedWriter, savedSvc, savedSources, savedGW
		presenceTapSynchronous, ingestGate = savedSync, savedGate
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
	infra.UserManagement.InternalPort = umPort
	infra.DeviceState.Hostname, infra.DeviceState.Port = umHost, umPort
	// command-delivery is configured so the tap runs its command waker, whose stop the writer's
	// close must precede. A wake it sends goes nowhere useful, which nothing here reads.
	infra.CommandDelivery.Hostname, infra.CommandDelivery.Port = umHost, umPort
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
	switch wiring {
	case tapSyncOptIn:
		closePresenceWriter()
		PresenceWriter = nil
		presenceTapSynchronous = true
	case tapNoWriter:
		closePresenceWriter()
		PresenceWriter = nil
		presenceTapSynchronous = false
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

// failOne answers the oldest unanswered publish with a JetStream error, as a stream that
// refuses the write does.
func (w *presenceAsyncWiring) failOne() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.answered >= len(w.held) {
		return false
	}
	m := w.held[w.answered]
	w.answered++
	_ = m.Respond([]byte(`{"error":{"code":503,"err_code":10077,"description":"refused by the test"}}`))
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
		wiring tapWiring
		want   int
	}{
		{"configured", &seven, tapAsync, 7},
		{"unset", nil, tapAsync, esconfig.DefaultPresencePublishWindow},
		{"control: the synchronous tap, chosen, one per subject", &seven, tapSyncOptIn, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := startPresenceAsyncWiring(t, tc.window, tc.wiring, core.NewNoOpLifecycleCallbacks())
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

// 🔴 A FAILED EMIT SURFACES. The stream refuses the publish: the transition is neither dropped
// silently nor counted as emitted. presence_events_failed_total is exported at 0 before any
// failure, so a rate over it has a baseline.
func TestPresenceTapCountsAFailedEmit(t *testing.T) {
	w := startPresenceAsyncWiring(t, nil, tapAsync, core.NewNoOpLifecycleCallbacks())
	require.Equal(t, 1, testutil.CollectAndCount(PresenceFailedCounter), "the series must exist before the first failure")
	require.Zero(t, testutil.ToFloat64(PresenceFailedCounter))

	w.advisory(t, true, 1)
	require.NoError(t, w.nc.Flush())
	require.Eventually(t, func() bool { return w.seen() == 1 }, 10*time.Second, time.Millisecond,
		"the transition never reached the stream")
	require.True(t, w.failOne())
	require.Eventually(t, func() bool { return testutil.ToFloat64(PresenceFailedCounter) == 1 }, 10*time.Second,
		time.Millisecond, "a transition the stream refused must be counted as failed")
	assert.Zero(t, emittedTotal(), "a failed transition was counted as emitted")
}

// handledAdvisories counts the transitions the tap has taken past its advisory parsing, by
// standing in for the ingest gate every one of them passes before it is emitted.
func handledAdvisories(t *testing.T) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	ingestGate = func(string, string, time.Time, bool, processor.Origin) bool { n.Add(1); return true }
	return &n
}

// 🔴 A CLOSED WRITER IS A STOP, NOT A LOSS TO ALERT ON. The writer closes only in the stop
// hook, so an advisory refused by it is one that arrived as the tap was stopping. It is not
// emitted, and it is NOT counted on presence_events_failed_total, which would otherwise rise
// on every rolling restart and raise BrokerPresenceLosingTransitions on routine deploys.
func TestAClosedPresenceWriterIsNotCountedAsAFailedEmit(t *testing.T) {
	w := startPresenceAsyncWiring(t, nil, tapAsync, core.NewNoOpLifecycleCallbacks())
	handled := handledAdvisories(t)
	PresenceWriter.Close()
	w.feed(t, 3)
	require.Eventually(t, func() bool { return handled.Load() == 6 }, 10*time.Second, time.Millisecond,
		"the tap never handled the advisories (handled %d)", handled.Load())
	// The refusal settles inside Submit, on the delivery goroutine, before the gate is
	// consulted for the next advisory on the same subject; the last one per subject is
	// given a moment to finish.
	time.Sleep(100 * time.Millisecond)
	assert.Zero(t, testutil.ToFloat64(PresenceFailedCounter), "a refusal by the closed writer was counted as a failed emit")
	assert.Zero(t, emittedTotal())
	assert.Zero(t, w.seen(), "nothing may reach the stream through a closed writer")
}

// 🔴 NO WRITER AND NO OPT-IN IS A FAULT THAT SHOWS. The service refuses to start that way
// (requirePresenceWriter); a tap started that way anyway does not quietly emit synchronously,
// which would hand back the 88-a-second ceiling with nothing to show for it. Every transition
// is counted as failed and none reaches the stream.
func TestATapWithNoWriterFailsLoudlyInsteadOfEmittingSynchronously(t *testing.T) {
	w := startPresenceAsyncWiring(t, nil, tapNoWriter, core.NewNoOpLifecycleCallbacks())
	w.feed(t, 3)
	require.Eventually(t, func() bool { return testutil.ToFloat64(PresenceFailedCounter) == 6 }, 10*time.Second,
		time.Millisecond, "a tap with no writer must count every transition as failed")
	assert.Zero(t, w.seen(), "a tap with no writer emitted synchronously: the silent fallback is back")
	assert.Zero(t, emittedTotal())
}

// The startup check: an enabled tap must have its writer, unless a test chose otherwise.
func TestRequirePresenceWriter(t *testing.T) {
	savedConfig, savedWriter, savedSync := Configuration, PresenceWriter, presenceTapSynchronous
	t.Cleanup(func() { Configuration, PresenceWriter, presenceTapSynchronous = savedConfig, savedWriter, savedSync })
	off := false
	enabled := &esconfig.EventSourcesConfiguration{}
	enabled.ApplyDefaults()
	disabled := &esconfig.EventSourcesConfiguration{BrokerPresence: esconfig.BrokerPresence{Enabled: &off}}
	disabled.ApplyDefaults()

	for _, tc := range []struct {
		name    string
		cfg     *esconfig.EventSourcesConfiguration
		writer  messaging.AsyncWriter
		sync    bool
		refused bool
	}{
		{"enabled, no writer: refused", enabled, nil, false, true},
		{"enabled, a writer", enabled, nopAsyncWriter{}, false, false},
		{"enabled, no writer, a test chose the synchronous tap", enabled, nil, true, false},
		{"disabled, no writer", disabled, nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			Configuration, PresenceWriter, presenceTapSynchronous = tc.cfg, tc.writer, tc.sync
			if tc.refused {
				assert.Error(t, requirePresenceWriter())
			} else {
				assert.NoError(t, requirePresenceWriter())
			}
		})
	}
}

// nopAsyncWriter is an AsyncWriter that is merely present.
type nopAsyncWriter struct{ messaging.AsyncWriter }

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
	w = startPresenceAsyncWiring(t, nil, tapAsync, cb)

	// Probes: the tap's subscription stop and the writer's close each record their place.
	rt := brokerPresence
	origTapStop := rt.stops[0]
	rt.stops[0] = func() { origTapStop(); mark("subscriptions") }
	// The waker's stop is appended last, when command-delivery is configured (it is here).
	// A writer closed after it would hand its completions' wakes to a waker with no workers.
	require.GreaterOrEqual(t, len(rt.stops), 3, "the tap must run its command waker here")
	last := len(rt.stops) - 1
	origWakerStop := rt.stops[last]
	rt.stops[last] = func() { mark("waker"); origWakerStop() }
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
	assert.Equal(t, []string{"subscriptions", "writer", "waker", "broker"}, got,
		"the writer closes after the subscriptions stop, and before the command waker and the broker manager do")
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

// 🔴 A TAP THAT NEVER STARTED STILL CLOSES ITS WRITER, AS A STEP OF ITS OWN STOP. With no
// system-account credential the tap is off, but the writer was built (the tap is enabled) and
// the demotion-only runtime is what stopBrokerPresence unwinds: its stops must close it, since
// there is no deferred close behind them to do it instead.
func TestTheDemotionRuntimeClosesThePresenceWriter(t *testing.T) {
	savedConfig, savedMs, savedGateway, savedPresence := Configuration, Microservice, GatewaySourceId, brokerPresence
	savedWriter, savedInbound := PresenceWriter, InboundEventsWriter
	t.Cleanup(func() {
		stopBrokerPresence()
		Configuration, Microservice, GatewaySourceId, brokerPresence = savedConfig, savedMs, savedGateway, savedPresence
		PresenceWriter, InboundEventsWriter = savedWriter, savedInbound
	})
	um := newFakeUM(t)
	host, port := um.infra(t)
	Microservice = &core.Microservice{InstanceId: "inst-1", FunctionalArea: "event-sources"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	infra := &Microservice.InstanceConfiguration.Infrastructure
	infra.ServiceAuth.Secret = "shh"
	infra.UserManagement.Hostname, infra.UserManagement.Port = host, port
	infra.UserManagement.InternalPort = port
	infra.DeviceState.Hostname, infra.DeviceState.Port = host, port
	initializeMetrics()
	InboundEventsWriter = nopWriter{}
	Configuration = &esconfig.EventSourcesConfiguration{}
	Configuration.ApplyDefaults()
	GatewaySourceId = "mqtt1"
	brokerPresence = nil

	var closed atomic.Bool
	PresenceWriter = &closeProbe{AsyncWriter: closeableNop{}, onClose: func() { closed.Store(true) }}
	startBrokerPresence(context.Background())
	require.NotNil(t, brokerPresence, "no system-account credential must leave the demotion runtime")
	require.Nil(t, brokerPresence.conn, "the tap must not have started")
	require.False(t, closed.Load())

	stopBrokerPresence()
	assert.True(t, closed.Load(), "stopping the demotion-only runtime left the presence writer open")
}

// closeableNop is a present AsyncWriter whose Close does nothing.
type closeableNop struct{ messaging.AsyncWriter }

func (closeableNop) Close() {}

// The startup refusal is wired: afterMicroserviceStarted checks the writer before it starts
// the tap. Nothing else drives main's startup hook, so this reads it.
func TestStartupRefusesAnEnabledTapWithNoWriter(t *testing.T) {
	calls := callsIn(t, "afterMicroserviceStarted")
	require.True(t, calls["requirePresenceWriter"], "afterMicroserviceStarted no longer checks the presence writer")
	require.True(t, calls["startBrokerPresence"])
}
