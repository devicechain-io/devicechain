// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// externalWindow is the shipped default window, written out rather than read from the
// config constant, so that changing it is a change to a value made here too, with a
// measurement behind it, and not an edit a test compared against itself.
const externalWindow = 128

// extHolder stands in for the inbound-events stream: it answers nothing until told to, so
// what the broker is holding at once is exactly what the source has in flight.
type extHolder struct {
	mu       sync.Mutex
	held     []*nats.Msg
	answered int
	stream   string
	reply    func(seq int) []byte
}

func (h *extHolder) seen() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.held)
}

func (h *extHolder) unanswered() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.held) - h.answered
}

// answerAll answers every publish held so far, with a PubAck unless reply says otherwise.
func (h *extHolder) answerAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ; h.answered < len(h.held); h.answered++ {
		seq := h.answered + 1
		body := []byte(fmt.Sprintf(`{"stream":%q,"seq":%d}`, h.stream, seq))
		if h.reply != nil {
			body = h.reply(seq)
		}
		_ = h.held[h.answered].Respond(body)
	}
}

// extRig is one embedded broker playing both the operator's MQTT broker and the platform's
// JetStream broker, with the external source's real writer built against it.
type extRig struct {
	t    *testing.T
	srv  *natsserver.Server
	nc   *nats.Conn
	nmgr *messaging.NatsManager
	// front is where every MQTT client in the rig connects, so that a read of the broker's
	// connection table never overlaps a CONNECT (see mqttClients).
	front  *dctest.MQTTFront
	pub    *AsyncEventPublisher
	holder *extHolder
	// writers counts the AsyncWriters built, which must be one per publisher however many
	// terms it serves.
	writers atomic.Int32
	failed  atomic.Int32
	pressed atomic.Int32
}

// newExtRig starts the broker and builds the publisher's writer at window. hold deletes the
// inbound-events stream and installs a responder that holds every publish.
func newExtRig(t *testing.T, window int, hold bool) *extRig {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, NoLog: true, NoSigs: true, ServerName: "ext-async",
		StoreDir: dctest.JetStreamStoreDir(t), MQTT: natsserver.MQTTOpts{Host: "127.0.0.1", Port: -1},
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(15*time.Second), "embedded nats server not ready")
	t.Cleanup(srv.Shutdown)
	mqttPort := dctest.MQTTPort(t, srv)
	u, err := url.Parse(srv.ClientURL())
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)

	r := &extRig{t: t, srv: srv, front: dctest.StartMQTTFront(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(mqttPort)))}
	pub, err := NewAsyncEventPublisher("ext", benchInboundMessage, PublishHooks{
		Backpressured: func(string) { r.pressed.Add(1) },
		Failed:        func(string, error) { r.failed.Add(1) },
	})
	require.NoError(t, err)
	r.pub = pub

	ms := &core.Microservice{InstanceId: testInstance, FunctionalArea: "event-sources", Readiness: core.NewReadinessGate()}
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: u.Hostname(), Port: uint32(port)}
	ms.Readiness.MarkReadyWithoutAuthSurface()
	r.nmgr = messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(n *messaging.NatsManager) error {
		w, err := NewExternalMqttWriter(n, window)
		if err != nil {
			return err
		}
		r.writers.Add(1)
		pub.SetWriter(w)
		return nil
	})
	ctx := context.Background()
	require.NoError(t, r.nmgr.Initialize(ctx))
	require.NoError(t, r.nmgr.Start(ctx))
	t.Cleanup(func() { pub.Close(); _ = r.nmgr.Stop(context.Background()) })

	r.nc, err = nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(r.nc.Close)
	if hold {
		r.hold()
	}
	return r
}

func (r *extRig) hold() {
	js, err := r.nc.JetStream()
	require.NoError(r.t, err)
	stream := messaging.StreamName(testInstance, streams.InboundEvents)
	require.NoError(r.t, js.DeleteStream(stream))
	h := &extHolder{stream: stream}
	_, err = messaging.SubscribeSynced(r.nc, messaging.ScopedSubject(testInstance, "acme", streams.InboundEvents),
		func(m *nats.Msg) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.held = append(h.held, m)
		})
	require.NoError(r.t, err)
	r.holder = h
	// Whatever a test leaves unanswered is answered at the end, so a failing assertion fails
	// the test instead of leaving a stop waiting on the broker for the module timeout.
	r.t.Cleanup(h.answerAll)
}

// source builds the external source through its real constructor, publishing through pub.
func (r *extRig) source(pub EventPublisher, clientID string, owns func() bool) *MqttEventSource {
	r.t.Helper()
	es, err := NewMqttEventSource("ext", clientID, map[string]string{
		"host": "127.0.0.1", "port": strconv.Itoa(r.front.Port()), "topic": GatewayTopic(testInstance),
	}, nil, "", "", NewJsonDecoder(map[string]string{}),
		func(string, []byte) {}, pub,
		func(_ string, _ string, _ []byte, err error) error {
			r.t.Errorf("unexpected failed-decode: %v", err)
			return nil
		},
		nil, admitAllReadings, admitAll, owns, func(err error) { r.t.Errorf("the source asked to end the process: %v", err) })
	require.NoError(r.t, err)
	return es
}

func (r *extRig) start(es *MqttEventSource) {
	r.t.Helper()
	require.NoError(r.t, startWithin(r.t, es, 30*time.Second))
}

// events publishes n device events to the broker's MQTT side, from index from.
func (r *extRig) events(from, n int) {
	r.t.Helper()
	for i := from; i < from+n; i++ {
		dev := fmt.Sprintf("dev-%d", i%20)
		require.NoError(r.t, r.nc.Publish(messaging.DeviceEventsSubject(testInstance, "acme", dev),
			[]byte(fmt.Sprintf(`{"device":%q,"eventType":"Measurement","payload":{"entries":[{"measurements":{"t":"%d"}}]}}`, dev, i))))
	}
	require.NoError(r.t, r.nc.Flush())
}

// stopWithErr stops the source and fails, instead of hanging, if it takes longer than d.
func stopWithErr(t *testing.T, es *MqttEventSource, d time.Duration) error {
	t.Helper()
	res := make(chan error, 1)
	go func() { res <- es.Stop(context.Background()) }()
	select {
	case err := <-res:
		return err
	case <-time.After(d):
		t.Fatalf("the source did not stop within %v", d)
		return nil
	}
}

// 🔴 THE DEFECT. The external source's five decode workers each published and waited for
// their own PubAck, so the broker was holding five publishes at a time and the source could
// not exceed five per round trip, cluster-wide (one pod reads a source). The inbound-events
// stream is replaced by a responder that holds every publish unanswered, so what it holds at
// once IS the source's in-flight count, exactly.
//
// The control is the synchronous publish this replaced, through the same source: it must
// hold DECODE_WORKER_COUNT. A test that only saw the new number could not tell the change
// from a broker that happens to hold that many.
func TestExternalMqttKeepsTheWindowInFlight(t *testing.T) {
	const events = 600 // past window + decode queue + workers, so the window is what limits it
	for _, arm := range []struct {
		name  string
		async bool
		want  int
	}{
		{"control: the synchronous publish holds one per decode worker", false, DECODE_WORKER_COUNT},
		{"the window", true, externalWindow},
	} {
		t.Run(arm.name, func(t *testing.T) {
			rig := newExtRig(t, externalWindow, false)
			var publisher EventPublisher = rig.pub
			if !arm.async {
				// Built BEFORE the stream is replaced: building a writer ensures its stream.
				sync, err := rig.nmgr.NewWriter(streams.InboundEvents)
				require.NoError(t, err)
				publisher = InlinePublisher(func(source, tenant string, event *model.UnresolvedEvent,
					payload interface{}, id string) error {
					ctx, msg, ok := benchInboundMessage(source, tenant, event, payload, id)
					if !ok {
						return nil
					}
					return sync.WriteMessages(ctx, msg)
				})
			}
			rig.hold()
			es := rig.source(publisher, "devicechain:inst-1:ext:pod-a", alwaysOwns)
			rig.start(es)
			t.Cleanup(func() { rig.stopAnswering(es) })

			rig.events(0, events)
			require.Eventually(t, func() bool { return rig.holder.seen() >= arm.want }, 15*time.Second, time.Millisecond,
				"the source never had %d publishes out at once", arm.want)
			time.Sleep(300 * time.Millisecond)
			require.Equal(t, arm.want, rig.holder.seen(),
				"publishes the broker held at once, all unanswered: the source's in-flight count")
		})
	}
}

// 🔴 THE LEASE INVARIANT. "A message of this term must not be published after the next owner
// has begun" held because Stop joined the workers. With an asynchronous publish the workers
// are long done while publishes are still in flight, so Stop must wait for the writer to
// settle them, BOUNDED, and then nothing more may reach the stream.
func TestExternalMqttStopSettlesWhatItSubmittedAndPublishesNothingAfter(t *testing.T) {
	rig := newExtRig(t, externalWindow, true)
	es := rig.source(rig.pub, "devicechain:inst-1:ext:pod-a", alwaysOwns)
	rig.start(es)

	const events = 20
	rig.events(0, events)
	require.Eventually(t, func() bool { return rig.holder.seen() == events }, 15*time.Second, time.Millisecond,
		"the publishes never reached the broker")

	stopped := make(chan error, 1)
	go func() { stopped <- es.Stop(context.Background()) }()
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned (%v) with %d publishes still unanswered: the lease would be released "+
			"under publishes that are still on their way to the stream", err, rig.holder.unanswered())
	case <-time.After(500 * time.Millisecond):
	}

	rig.holder.answerAll()
	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return after every publish was answered")
	}

	seen := rig.holder.seen()
	require.Equal(t, events, seen)
	rig.events(events, 50)
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, seen, rig.holder.seen(), "a stopped source published")
	assert.Zero(t, rig.failed.Load())
}

// A broker that never answers must not hold the stop, and with it the lease, for ever: Stop
// gives up at its bound, says so, and returns the error.
func TestExternalMqttStopIsBoundedWhenTheBrokerNeverAnswers(t *testing.T) {
	saved := externalSettleTimeout
	externalSettleTimeout = 400 * time.Millisecond
	t.Cleanup(func() { externalSettleTimeout = saved })
	logs := captureDebugLog(t)

	rig := newExtRig(t, externalWindow, true)
	es := rig.source(rig.pub, "devicechain:inst-1:ext:pod-a", alwaysOwns)
	rig.start(es)
	rig.events(0, 5)
	require.Eventually(t, func() bool { return rig.holder.seen() == 5 }, 15*time.Second, time.Millisecond)

	begun := time.Now()
	err := stopWithErr(t, es, 10*time.Second)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.GreaterOrEqual(t, time.Since(begun), 300*time.Millisecond, "it gave up before its bound")
	assert.Contains(t, logs.String(), "could not confirm that everything it published is stored")
}

// 🔴 A FAILED PUBLISH IS NEVER A SILENT DROP. The broker refuses each publish; every one is
// counted on the failure hook and logged with its source and tenant.
func TestExternalMqttFailedPublishIsCountedAndLogged(t *testing.T) {
	logs := captureDebugLog(t)
	rig := newExtRig(t, externalWindow, true)
	rig.holder.reply = func(int) []byte {
		return []byte(`{"error":{"code":503,"err_code":10008,"description":"the stream refused it"}}`)
	}
	es := rig.source(rig.pub, "devicechain:inst-1:ext:pod-a", alwaysOwns)
	rig.start(es)
	t.Cleanup(func() { rig.stopAnswering(es) })

	rig.events(0, 3)
	require.Eventually(t, func() bool { rig.holder.answerAll(); return rig.failed.Load() >= 1 }, 15*time.Second,
		5*time.Millisecond, "a refused publish was not counted")
	assert.Contains(t, logs.String(), "is lost: this transport does not retry")
	assert.Contains(t, logs.String(), `"source":"ext"`)
}

// termLog wraps a publisher so a test can see WHEN a term settles relative to the release of
// its lease, in the same record the fake lease writes.
type termLog struct {
	EventPublisher
	log *eventLog
	pod string
}

func (p termLog) Settle(ctx context.Context) error {
	err := p.EventPublisher.Settle(ctx)
	p.log.add("settle:" + p.pod)
	return err
}

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	require.Eventually(t, cond, d, 10*time.Millisecond, what)
}

// 🔴 ONE WRITER ACROSS MANY TERMS, SETTLED BEFORE EACH RELEASE. The owned source builds a
// fresh MqttEventSource per lease term; a writer holds a reply subscription for the life of
// the connection, so one per term would leak. Over several terms (each ended the way a lost
// lease ends one) the writer is built once, the broker's subscription count and this
// process's goroutine count do not grow, and every release is preceded by its term's settle.
func TestExternalMqttTermsShareOneWriterAndSettleBeforeRelease(t *testing.T) {
	fastOwnerRetries(t)
	rig := newExtRig(t, externalWindow, false)
	log := &eventLog{}
	bucket := newFakeBucket(log)
	var terms atomic.Int32
	owned, err := NewOwnedMqttSource("ext", testInstance, "pod-a",
		func(clientID string, owns func() bool) (TermSource, error) {
			terms.Add(1)
			return rig.source(termLog{rig.pub, log, "pod-a"}, clientID, owns), nil
		},
		func() (SourceLeases, error) { return bucket.leasesFor("pod-a"), nil },
		OwnerHooks{
			Fail:     func(err error) { t.Errorf("the source asked to end the process: %v", err) },
			SetOwner: func(string, bool) {}, NotOwner: func(string) {},
		})
	require.NoError(t, err)
	require.NoError(t, owned.Initialize(context.Background()))
	require.NoError(t, owned.Start(context.Background()))
	t.Cleanup(func() { _ = owned.Stop(context.Background()) })

	partition := OwnershipPartition("ext")
	reading := func(want int) bool {
		return len(rig.mqttClients("devicechain:inst-1:ext:")) == want
	}
	waitFor(t, "the first term never began reading", 15*time.Second, func() bool { return reading(1) })

	// Each cycle: the lease is lost, the term settles and releases, and the standby takes
	// the source again.
	const cycles, settled = 12, 4
	var baseSubs uint32
	var baseGoroutines int
	for i := 1; i <= cycles; i++ {
		lease := bucket.leaseOf(partition)
		require.NotNil(t, lease)
		lease.expire()
		waitFor(t, fmt.Sprintf("cycle %d: the term never released", i), 15*time.Second, func() bool {
			return log.count("release:pod-a") >= i
		})
		waitFor(t, fmt.Sprintf("cycle %d: the source never resumed", i), 15*time.Second, func() bool {
			return bucket.held(partition) && int(terms.Load()) >= i+2 && reading(1)
		})
		if i == settled { // after the first cycles the steady state is set
			baseSubs, baseGoroutines = settledCounts(rig.srv)
		}
	}

	assert.Equal(t, int32(1), rig.writers.Load(), "a writer was built for something other than the process")
	subs, goroutines := settledCounts(rig.srv)
	assert.LessOrEqual(t, subs, baseSubs+2,
		"the broker's subscription count grew over %d terms: a term leaked a subscription", cycles-settled)
	assert.LessOrEqual(t, goroutines, baseGoroutines+3,
		"goroutines grew over %d terms: a term leaked", cycles-settled)

	// Every release was preceded by the settle of its own term: the record alternates.
	var order []string
	for _, e := range log.snapshot() {
		if e == "settle:pod-a" || e == "release:pod-a" {
			order = append(order, e)
		}
	}
	require.GreaterOrEqual(t, len(order), 2*cycles)
	for i := 0; i+1 < len(order); i += 2 {
		require.Equal(t, []string{"settle:pod-a", "release:pod-a"}, order[i:i+2],
			"a term released its lease without settling first: %v", order)
	}
}

// stopAnswering stops the source while answering every publish as it arrives, so a source
// that still has a backlog to publish can finish it and stop.
func (r *extRig) stopAnswering(es *MqttEventSource) {
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(2 * time.Millisecond):
				r.holder.answerAll()
			}
		}
	}()
	stopWithin(r.t, es, 20*time.Second)
}

// settledCounts reads the broker's subscription count and this process's goroutine count
// once both have stopped changing: a session that has just connected or disconnected is
// still registering or dropping its subscriptions, and a count taken mid-flight is noise.
func settledCounts(srv *natsserver.Server) (uint32, int) {
	subs, goroutines := srv.NumSubscriptions(), runtime.NumGoroutine()
	for stable, deadline := 0, time.Now().Add(5*time.Second); stable < 3 && time.Now().Before(deadline); {
		time.Sleep(100 * time.Millisecond)
		s, g := srv.NumSubscriptions(), runtime.NumGoroutine()
		if s == subs && g == goroutines {
			stable++
		} else {
			stable = 0
		}
		subs, goroutines = s, g
	}
	return subs, goroutines
}

// mqttClients lists the MQTT client ids connected to the broker that start with prefix,
// read through the front: the broker stores a client's id unlocked while it parses the
// CONNECT and Connz reads it locked, so a read that overlaps a term's connect is a data
// race inside nats-server (see dctest.MQTTFront).
func (r *extRig) mqttClients(prefix string) []string {
	r.t.Helper()
	return r.front.MQTTClients(r.t, r.srv, prefix)
}

// gatedPublisher holds every Publish until gate closes, so a decode worker can be inside its
// hand-off when the source is stopped.
type gatedPublisher struct {
	EventPublisher
	entered chan struct{}
	gate    chan struct{}
}

func (p gatedPublisher) Publish(source, tenant string, event *model.UnresolvedEvent, payload interface{},
	dedupID string, done func(error)) {
	p.entered <- struct{}{}
	<-p.gate
	p.EventPublisher.Publish(source, tenant, event, payload, dedupID, done)
}

// The settle comes AFTER the workers are joined. A worker still inside its hand-off when the
// stop begins submits after a settle that ran too early had found nothing in flight, and the
// stop would return with that publish on its way to the stream.
func TestExternalMqttStopSettlesAfterTheWorkersHaveSubmitted(t *testing.T) {
	rig := newExtRig(t, externalWindow, true)
	gated := gatedPublisher{EventPublisher: rig.pub, entered: make(chan struct{}, 8), gate: make(chan struct{})}
	es := rig.source(gated, "devicechain:inst-1:ext:pod-a", alwaysOwns)
	rig.start(es)

	rig.events(0, 1)
	select {
	case <-gated.entered:
	case <-time.After(15 * time.Second):
		t.Fatal("no decode worker took the event")
	}

	stopped := make(chan error, 1)
	go func() { stopped <- es.Stop(context.Background()) }()
	time.Sleep(200 * time.Millisecond) // Stop is now waiting on the worker, which is held in its hand-off
	close(gated.gate)
	require.Eventually(t, func() bool { return rig.holder.seen() == 1 }, 15*time.Second, time.Millisecond,
		"the released worker never published")

	select {
	case err := <-stopped:
		t.Fatalf("Stop returned (%v) with %d publish(es) unanswered: it settled before the worker had submitted",
			err, rig.holder.unanswered())
	case <-time.After(400 * time.Millisecond):
	}
	rig.holder.answerAll()
	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return after the publish was answered")
	}
}

// 🔴 A SETTLE THAT TIMES OUT STILL RELEASES THE LEASE, AND THE LOSS IS NOT SILENT. The
// platform broker holds every publish unanswered, so the term's settle cannot finish: the
// owned source's stop must give up at the bound, say so, and release the lease anyway (a
// lease held for ever would leave the source unread by every pod). What was in flight is
// then lost to this pod, and each event is counted on the failure hook once its publish
// reaches its own ceiling; none vanishes uncounted.
func TestExternalMqttSettleTimeoutStillReleasesTheLease(t *testing.T) {
	fastOwnerRetries(t)
	saved := externalSettleTimeout
	externalSettleTimeout = 300 * time.Millisecond
	t.Cleanup(func() { externalSettleTimeout = saved })
	logs := captureDebugLog(t)

	rig := newExtRig(t, externalWindow, true)
	log := &eventLog{}
	bucket := newFakeBucket(log)
	owned, err := NewOwnedMqttSource("ext", testInstance, "pod-a",
		func(clientID string, owns func() bool) (TermSource, error) {
			return rig.source(termLog{rig.pub, log, "pod-a"}, clientID, owns), nil
		},
		func() (SourceLeases, error) { return bucket.leasesFor("pod-a"), nil },
		OwnerHooks{
			Fail:     func(err error) { t.Errorf("the source asked to end the process: %v", err) },
			SetOwner: func(string, bool) {}, NotOwner: func(string) {},
		})
	require.NoError(t, err)
	require.NoError(t, owned.Initialize(context.Background()))
	require.NoError(t, owned.Start(context.Background()))
	partition := OwnershipPartition("ext")
	waitFor(t, "the term never began reading", 15*time.Second, func() bool {
		return len(rig.mqttClients("devicechain:inst-1:ext:")) == 1
	})

	const inFlight = 5
	rig.events(0, inFlight)
	waitFor(t, "the publishes never reached the broker", 15*time.Second, func() bool {
		return rig.holder.seen() == inFlight
	})

	stopped := make(chan error, 1)
	go func() { stopped <- owned.Stop(context.Background()) }()
	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("the owned source's stop was held by a settle that cannot finish")
	}
	assert.False(t, bucket.held(partition), "a settle that timed out kept the lease")
	order := log.snapshot()
	require.GreaterOrEqual(t, log.index("settle:pod-a"), 0, "the term never settled: %v", order)
	require.Less(t, log.index("settle:pod-a"), log.index("release:pod-a"),
		"the lease was released before the term tried to settle: %v", order)
	assert.Contains(t, logs.String(), "could not confirm that everything it published is stored")
	assert.Zero(t, rig.failed.Load(), "nothing has failed yet: the publishes are still within their ceiling")

	waitFor(t, "an event left in flight by the timed-out settle was never counted as lost", 15*time.Second,
		func() bool { return rig.failed.Load() == inFlight })
}
