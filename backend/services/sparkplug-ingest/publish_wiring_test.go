// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/adapter"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/service"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	"github.com/devicechain-io/dc-sparkplug-ingest/host"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These drive main's OWN wiring of the publish path against a real broker: what
// publisherFactory builds, how the service closes it, and what a leadership term does with it.
// Nothing but main decides that the window is ingest.publishWindow, that the writer refuses at
// a closed gate, or that it is closed before the broker manager stops, so this is the only
// place those can be pinned: a host test builds its own writer.

// keepPublishWriters restores the global the factory appends to.
func keepPublishWriters(t *testing.T) {
	t.Helper()
	saved := PublishWriters
	PublishWriters = nil
	t.Cleanup(func() {
		for _, w := range PublishWriters {
			w.Draining()
			w.Close()
		}
		PublishWriters = saved
	})
}

// submitTelemetry is one reading of the tenant "acme", as the receive path submits it.
func submitTelemetry(p host.Publisher, n int, done func(error)) {
	p.Submit(core.WithTenant(context.Background(), "acme"),
		[]messaging.Message{{Key: []byte("d"), Value: []byte(fmt.Sprintf(`{"n":%d}`, n)), DedupID: fmt.Sprintf("sp-wiring-%d", n)}}, done)
}

// 🔴 ingest.publishWindow REACHES THE WRITER. The inbound-events stream is replaced by a
// responder that holds every publish, so what the broker holds at once is the window main
// built the writer with: a configured 7 holds 7, and an unset one holds the default.
func TestPublishWindowIsTheConfiguredOne(t *testing.T) {
	seven := 7
	for _, tc := range []struct {
		name   string
		window *int
		want   int
	}{
		{"configured", &seven, 7},
		{"unset", nil, config.DefaultIngestPublishWindow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keepPublishWriters(t)
			startRunningManager(t)
			cfg := &config.SparkplugConfiguration{Ingest: config.Ingest{PublishWindow: tc.window}}
			cfg.ApplyDefaults()
			require.NoError(t, cfg.Validate())

			pub, err := publisherFactory(NatsManager, cfg)()
			require.NoError(t, err)
			require.Len(t, PublishWriters, 1, "the factory did not record the writer for the stop hook to close")

			nc := NatsManager.Conn()
			js, err := nc.JetStream()
			require.NoError(t, err)
			stream := messaging.StreamName(Microservice.InstanceId, streams.InboundEvents)
			require.NoError(t, js.DeleteStream(stream))
			var mu sync.Mutex
			var held []*nats.Msg
			_, err = messaging.SubscribeSynced(nc, messaging.ScopedSubject(Microservice.InstanceId, "acme", streams.InboundEvents),
				func(m *nats.Msg) { mu.Lock(); held = append(held, m); mu.Unlock() })
			require.NoError(t, err)
			seen := func() int { mu.Lock(); defer mu.Unlock(); return len(held) }
			answer := func(from int) {
				mu.Lock()
				defer mu.Unlock()
				for i := from; i < len(held); i++ {
					_ = held[i].Respond([]byte(fmt.Sprintf(`{"stream":%q,"seq":%d}`, stream, i+1)))
				}
			}
			t.Cleanup(func() { answer(0) })

			total := tc.want + 20
			submitted := make(chan struct{})
			go func() {
				defer close(submitted)
				for i := 0; i < total; i++ {
					submitTelemetry(pub, i, func(error) {})
				}
			}()
			require.Eventually(t, func() bool { return seen() >= tc.want }, 10*time.Second, time.Millisecond)
			time.Sleep(200 * time.Millisecond)
			assert.Equal(t, tc.want, seen(), "publishes the broker held at once: the window main built the writer with")

			// Answer everything as it arrives, so the submitter can finish and the writer close.
			for answered := 0; answered < total; {
				require.Eventually(t, func() bool { return seen() > answered }, 10*time.Second, time.Millisecond)
				answer(answered)
				answered = seen()
			}
			select {
			case <-submitted:
			case <-time.After(10 * time.Second):
				t.Fatal("the submitting goroutine never finished")
			}
		})
	}
}

// The writer main builds REFUSES telemetry at a closed backpressure gate, as the synchronous
// writer it replaced did, and still admits presence (BypassBackpressure). The stream holds 20
// messages and nothing reads it; once the unread backlog reaches the gate's share, a reading is
// refused whole, and the stream keeps every event it took.
func TestPublishWriterRefusesTelemetryButAdmitsPresenceAtAClosedGate(t *testing.T) {
	keepPublishWriters(t)
	startRunningManagerWith(t, 20, core.NewNoOpLifecycleCallbacks())
	// device-management's durable on inbound-events: created, never read, so the stream's
	// unread backlog is what closes the gate.
	dm := core.Microservice{InstanceId: Microservice.InstanceId, FunctionalArea: "device-management", Readiness: core.NewReadinessGate()}
	dm.UseMetricsRegistry(prometheus.NewRegistry())
	dm.InstanceConfiguration.Infrastructure.Nats = Microservice.InstanceConfiguration.Infrastructure.Nats
	dm.Readiness.MarkReadyWithoutAuthSurface()
	dmgr := messaging.NewNatsManager(&dm, core.NewNoOpLifecycleCallbacks(), func(*messaging.NatsManager) error { return nil })
	require.NoError(t, dmgr.Initialize(context.Background()))
	t.Cleanup(func() { _ = dmgr.Stop(context.Background()) })
	_, err := dmgr.NewReader(streams.InboundEvents)
	require.NoError(t, err)

	pub, err := publisherFactory(NatsManager, config.NewSparkplugConfiguration())()
	require.NoError(t, err)

	send := func(n int, bypass bool) error {
		res := make(chan error, 1)
		pub.Submit(core.WithTenant(context.Background(), "acme"), []messaging.Message{{
			Key: []byte("d"), Value: []byte("x"), DedupID: fmt.Sprintf("sp-gate-%d", n), BypassBackpressure: bypass,
		}}, func(err error) { res <- err })
		select {
		case err := <-res:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("the publish was never settled")
			return nil
		}
	}
	refused, stored := 0, 0
	for i := 0; i < 30; i++ {
		switch err := send(i, false); {
		case err == nil:
			stored++
		case errors.Is(err, messaging.ErrStreamBackpressure):
			refused++
		default:
			t.Fatalf("publish %d failed for a reason other than backpressure: %v", i, err)
		}
		NatsManager.MeasureBackpressureForTesting(t, streams.InboundEvents)
	}
	require.Positive(t, refused, "the gate never refused a reading: the writer was built not to refuse")
	require.NoError(t, send(1000, true), "presence must be admitted at a closed gate")

	js, err := NatsManager.Conn().JetStream()
	require.NoError(t, err)
	info, err := js.StreamInfo(messaging.StreamName(Microservice.InstanceId, streams.InboundEvents))
	require.NoError(t, err)
	assert.Equal(t, uint64(stored+1), info.State.Msgs, "the stream must keep every event it accepted, presence included")
}

// 🔴 THE CALLER, NOT THE FUNCTION. closePublishWriters is only worth anything if the service's
// own shutdown hook calls it, and calls it at the right moment: after the sources have stopped
// (their last term settled) and BEFORE the broker manager drains the connection the writers
// publish on, so that what a timed-out settle left in flight is failed and counted by its own
// writer. This drives the real beforeMicroserviceStopped over main's own wiring and asks, at
// the instant the broker manager begins to stop, whether the writer still takes a publish.
func TestServiceStopClosesThePublishWritersBeforeTheBrokerStops(t *testing.T) {
	keepPublishWriters(t)
	var pub host.Publisher
	var brokerStopping bool
	var atBrokerStop error
	cb := core.NewNoOpLifecycleCallbacks()
	cb.Stopper = core.LifecycleCallback{
		Preprocess: func(context.Context) error {
			brokerStopping = true
			res := make(chan error, 1)
			submitTelemetry(pub, 99, func(err error) { res <- err })
			select {
			case atBrokerStop = <-res:
			case <-time.After(10 * time.Second):
				atBrokerStop = errors.New("the publish was never settled")
			}
			return nil
		},
		Postprocess: func(context.Context) error { return nil },
	}
	startRunningManagerWith(t, 0, cb)
	Svc = service.FromManagers(Microservice, service.Managers{Nats: NatsManager})

	var err error
	pub, err = publisherFactory(NatsManager, config.NewSparkplugConfiguration())()
	require.NoError(t, err)
	res := make(chan error, 1)
	submitTelemetry(pub, 0, func(err error) { res <- err })
	select {
	case err := <-res:
		require.NoError(t, err, "the writer must work before the stop")
	case <-time.After(10 * time.Second):
		t.Fatal("the publish was never settled")
	}

	require.NoError(t, beforeMicroserviceStopped(context.Background()))
	require.True(t, brokerStopping, "the hook never stopped the broker manager")
	require.Error(t, atBrokerStop,
		"the writer still took a publish when the broker manager began to stop: "+
			"it must be closed after the sources and before the drain")
	assert.ErrorIs(t, atBrokerStop, messaging.ErrAsyncWriterClosed)
}

// blockingPublisher is a host.Publisher whose Settle waits for the test.
type blockingPublisher struct {
	settling chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (*blockingPublisher) Submit(_ context.Context, _ []messaging.Message, done func(error)) {
	done(nil)
}
func (p *blockingPublisher) Settle(ctx context.Context) error {
	p.once.Do(func() { close(p.settling) })
	select {
	case <-p.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// 🔴 A TERM'S PUBLISHES ARE SETTLED BEFORE ITS LEASE IS RELEASED. A successor leader begins
// the moment the lease is released, and "nothing of this term is published after that" holds
// only if the source has drained its writer first. This drives the real serveAsLeader over a
// real Manager and a real Client whose publisher is still settling: the lease must not be
// released while it is, and must be released once it has.
func TestServeAsLeaderSettlesTheSourcesPublishesBeforeReleasing(t *testing.T) {
	prevManager := Manager
	t.Cleanup(func() { Manager = prevManager })

	pub := &blockingPublisher{settling: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-pub.release:
		default:
			close(pub.release)
		}
	})
	client := host.NewClient(config.SparkplugSource{Tenant: "acme", HostId: "h", Broker: config.SourceBroker{URL: "tcp://127.0.0.1:1"}},
		host.Broker{URL: "tcp://127.0.0.1:1", ClientID: "settle-test"}, nil, nil, time.Now, host.Metrics{})
	client.SetPublisher(pub)
	Manager = host.NewManager([]*host.Client{client})

	rec := newLeaseTermRecorder()
	close(rec.letRenewerFinish) // the renewer is not what this is about
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		defer close(served)
		serveAsLeader(ctx, rec)
	}()
	<-rec.keepAliveStarted
	cancel() // end the term

	select {
	case <-pub.settling:
	case <-time.After(15 * time.Second):
		t.Fatal("the term ended without draining the source's publisher")
	}
	select {
	case <-rec.releaseCalled:
		t.Fatal("serveAsLeader released the lease while a source's publishes were still settling: " +
			"a successor would begin under publishes that are still on their way to the stream")
	case <-time.After(500 * time.Millisecond):
	}

	close(pub.release)
	select {
	case <-served:
	case <-time.After(15 * time.Second):
		t.Fatal("serveAsLeader did not return after the publisher settled")
	}
	select {
	case <-rec.releaseCalled:
	default:
		t.Fatal("serveAsLeader returned without releasing the lease")
	}
}

// Every source the service builds to ingest gets its OWN publisher (so the window is per
// connection and one stalled source holds only its own publishes), and an ingest path with no
// way to publish is refused rather than built to abandon everything.
func TestResolveSourcesGivesEachIngestingClientItsOwnPublisher(t *testing.T) {
	cfg := &config.SparkplugConfiguration{Sources: []config.SparkplugSource{src("tcp://a:1883"), src("ssl://b:8883")}}
	ingester := host.NewIngester(nil, nil, host.IngestMetrics{})
	limiter := adapter.NewSampleLimiter(core.StaticCeiling(10, 20), adapter.IngestLimiterMetrics{}, nil)

	var built atomic.Int32
	clients, err := resolveSources(cfg, "inst", ingester, limiter, nil, host.Metrics{}, func() (host.Publisher, error) {
		built.Add(1)
		return &blockingPublisher{}, nil
	})
	require.NoError(t, err)
	require.Len(t, clients, 2)
	assert.Equal(t, int32(2), built.Load(), "one publisher per source")
	assert.NotSame(t, clients[0].Publisher(), clients[1].Publisher(), "two connections share a publisher, and so a window")

	_, err = resolveSources(cfg, "inst", ingester, limiter, nil, host.Metrics{}, nil)
	require.Error(t, err, "an ingest path with no way to publish must be refused")

	failing := errors.New("no broker")
	_, err = resolveSources(cfg, "inst", ingester, limiter, nil, host.Metrics{}, func() (host.Publisher, error) { return nil, failing })
	require.ErrorIs(t, err, failing)
}

// The writer publisherFactory builds reports its window and duplicate counters under the
// sparkplug writer label, so this service's inbound-events window is not summed with any
// other producer's on a dashboard. A call site that named the wrong writer (or none) fails
// here by value.
func TestThePublishWriterReportsUnderTheSparkplugLabel(t *testing.T) {
	keepPublishWriters(t)
	startRunningManager(t)
	_, err := publisherFactory(NatsManager, config.NewSparkplugConfiguration())()
	require.NoError(t, err)
	require.Equal(t, []string{"sparkplug"},
		dctest.PublishWriterLabels(t, brokerMetrics, streams.InboundEvents),
		"the writer labels of the inbound-events window series")
}
