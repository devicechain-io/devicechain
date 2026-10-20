// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package host

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/devicechain-io/dc-sparkplug-ingest/codec"
	"github.com/devicechain-io/dc-sparkplug-ingest/config"
	sppb "github.com/devicechain-io/dc-sparkplug-ingest/proto"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// These tests drive the REAL Client, over a REAL paho connection, against ONE embedded broker
// that plays both parts: the customer's MQTT broker the Host Application reads (the NATS
// server's MQTT gateway) and the platform's JetStream broker the Host publishes into. What the
// platform side HOLDS is therefore a direct reading of how many publishes the Host has in
// flight, and nothing about the Host is faked but the GraphQL client.

const spInstance = "inst-1"

// spWindow is the shipped default window, written out rather than read from the config
// constant, so that changing it is a change made here too, with a measurement behind it.
const spWindow = 32

// spHolder stands in for the inbound-events stream: it answers nothing until told to, so what
// the broker holds at once is exactly what the Host has in flight.
type spHolder struct {
	mu       sync.Mutex
	held     []*nats.Msg
	answered int
	stream   string
}

func (h *spHolder) seen() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.held)
}

func (h *spHolder) unanswered() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.held) - h.answered
}

// answerAll answers every publish held so far with a PubAck.
func (h *spHolder) answerAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ; h.answered < len(h.held); h.answered++ {
		body := []byte(fmt.Sprintf(`{"stream":%q,"seq":%d}`, h.stream, h.answered+1))
		_ = h.held[h.answered].Respond(body)
	}
}

// spRig is the embedded broker, a NatsManager against it, and the AsyncWriter a Client
// publishes through.
type spRig struct {
	t       *testing.T
	srv     *natsserver.Server
	nc      *nats.Conn
	nmgr    *messaging.NatsManager
	mqttURL string
	writer  messaging.AsyncWriter
	holder  *spHolder
	js      nats.JetStreamContext
}

// newSpRig starts the broker and builds one AsyncWriter at window, refusing at a closed
// backpressure gate as the service wires it. streamMaxMsgs, when positive, bounds the
// inbound-events stream and makes an unread consumer's backlog close the gate.
func newSpRig(t *testing.T, window int, streamMaxMsgs int64) *spRig {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, NoLog: true, NoSigs: true, ServerName: "sp-async",
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

	microservice := func(area string) *core.Microservice {
		ms := &core.Microservice{InstanceId: spInstance, FunctionalArea: area, Readiness: core.NewReadinessGate()}
		ms.UseMetricsRegistry(prometheus.NewRegistry())
		ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: u.Hostname(), Port: uint32(port)}
		if streamMaxMsgs > 0 {
			ms.InstanceConfiguration.Infrastructure.Nats.StreamMaxMsgs = streamMaxMsgs
		}
		ms.Readiness.MarkReadyWithoutAuthSurface()
		return ms
	}
	r := &spRig{t: t, srv: srv, mqttURL: fmt.Sprintf("tcp://127.0.0.1:%d", mqttPort)}
	if streamMaxMsgs > 0 {
		// device-management's durable on inbound-events: created, never read, so the
		// stream's unread backlog is what closes the gate.
		dm := messaging.NewNatsManager(microservice("device-management"), core.NewNoOpLifecycleCallbacks(),
			func(*messaging.NatsManager) error { return nil })
		require.NoError(t, dm.Initialize(context.Background()))
		t.Cleanup(func() { _ = dm.Stop(context.Background()) })
		_, err = dm.NewReader(streams.InboundEvents)
		require.NoError(t, err)
	}
	r.nmgr = messaging.NewNatsManager(microservice("sparkplug-ingest"), core.NewNoOpLifecycleCallbacks(),
		func(*messaging.NatsManager) error { return nil })
	ctx := context.Background()
	require.NoError(t, r.nmgr.Initialize(ctx))
	require.NoError(t, r.nmgr.Start(ctx))
	t.Cleanup(func() { _ = r.nmgr.Stop(context.Background()) })

	r.writer, err = r.nmgr.NewAsyncWriter(streams.InboundEvents, messaging.WriterSparkplug, window, true)
	require.NoError(t, err)
	t.Cleanup(func() { r.writer.Draining(); r.writer.Close() })

	r.nc, err = nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(r.nc.Close)
	r.js, err = r.nc.JetStream()
	require.NoError(t, err)
	return r
}

// hold deletes the inbound-events stream and installs a responder that holds every publish.
func (r *spRig) hold() {
	stream := messaging.StreamName(spInstance, streams.InboundEvents)
	require.NoError(r.t, r.js.DeleteStream(stream))
	h := &spHolder{stream: stream}
	_, err := messaging.SubscribeSynced(r.nc, messaging.ScopedSubject(spInstance, "acme", streams.InboundEvents),
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

func (r *spRig) streamName() string { return messaging.StreamName(spInstance, streams.InboundEvents) }

// spMetrics are the counters a test reads off the Client it drives.
type spMetrics struct {
	failures prometheus.Counter
	enqueued prometheus.Counter
	requests prometheus.Counter
	messages *prometheus.CounterVec
	emitted  prometheus.Counter
	presence prometheus.Counter
}

func newSpMetrics() *spMetrics {
	return &spMetrics{
		failures: prometheus.NewCounter(prometheus.CounterOpts{Name: "ingest_failures"}),
		enqueued: prometheus.NewCounter(prometheus.CounterOpts{Name: "rebirth_enqueued"}),
		requests: prometheus.NewCounter(prometheus.CounterOpts{Name: "rebirth_requests"}),
		messages: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "messages"}, []string{"type"}),
		emitted:  prometheus.NewCounter(prometheus.CounterOpts{Name: "measurements_emitted"}),
		presence: prometheus.NewCounter(prometheus.CounterOpts{Name: "presence_emitted"}),
	}
}

func (m *spMetrics) host() Metrics {
	return Metrics{IngestFailures: m.failures, RebirthEnqueued: m.enqueued, RebirthRequests: m.requests, Messages: m.messages}
}

func (m *spMetrics) received(typ string) int {
	return int(testutil.ToFloat64(m.messages.WithLabelValues(typ)))
}

// failingWriter is the Emitter's own writer, which answers every write with an error: the receive path publishes through
// the Client's Publisher, and a regression that wrote through the Emitter instead would fail
// loudly here rather than quietly restore the one-publish-at-a-time ceiling.
type failingWriter struct{}

func (failingWriter) WriteMessages(context.Context, ...messaging.Message) error {
	return fmt.Errorf("the emitter's own writer was used: the receive path must publish through the Client's Publisher")
}

// client builds a Client over the real ingest pipeline (fake GraphQL only) publishing through pub.
func (r *spRig) client(pub Publisher, m *spMetrics) *Client {
	ing := NewIngester(NewRegistrar(fakeCreateGQL{}, "url", nil), NewEmitter(failingWriter{}, time.Now),
		IngestMetrics{MeasurementsEmitted: m.emitted, PresenceEmitted: m.presence})
	src := config.SparkplugSource{Tenant: "acme", HostId: "h1", Groups: []string{"plant-a"}, AutoRegister: true, DeviceTypeToken: "sp-node"}
	c := NewClient(src, Broker{URL: r.mqttURL, ClientID: "sp-async-host"}, ing, admitAllSamples{}, time.Now, m.host())
	c.SetPublisher(pub)
	return c
}

// online connects c and waits until the broker holds its ONLINE STATE, which it publishes
// after subscribing: from then on a message published to the group reaches it.
func (r *spRig) online(c *Client) {
	r.t.Helper()
	states := watchState(r.t, r.mqttURL, StateTopic("h1"))
	c.Connect()
	require.Eventually(r.t, func() bool {
		for _, st := range states() {
			if st.Online {
				return true
			}
		}
		return false
	}, 20*time.Second, 10*time.Millisecond, "the Host never announced ONLINE")
}

// mqttFeeder publishes Sparkplug messages to the broker's MQTT side as an edge node does.
type mqttFeeder struct {
	t  *testing.T
	cl mqtt.Client
}

func (r *spRig) feeder() *mqttFeeder {
	r.t.Helper()
	opts := mqtt.NewClientOptions().AddBroker(r.mqttURL).SetClientID("edge-node-sim")
	cl := mqtt.NewClient(opts)
	tok := cl.Connect()
	require.True(r.t, tok.WaitTimeout(15*time.Second), "feeder connect timed out")
	require.NoError(r.t, tok.Error())
	r.t.Cleanup(func() { cl.Disconnect(100) })
	return &mqttFeeder{t: r.t, cl: cl}
}

func (f *mqttFeeder) publish(topic string, payload []byte) {
	f.t.Helper()
	tok := f.cl.Publish(topic, 1, false, payload)
	require.True(f.t, tok.WaitTimeout(15*time.Second), "feeder publish timed out")
	require.NoError(f.t, tok.Error())
}

// nodeBirth is an NBIRTH (seq 0) for a node in plant-a declaring one metric, m1, valued 1.
func nodeBirth(t *testing.T) []byte { return birthOf(t, 1) }

// uniqueData is an NDATA at seq carrying m1 = value, dated at the clock. Every call with a
// different value is a different event, so two of them never share a dedup id.
func uniqueData(t *testing.T, seq uint64, value float64) []byte {
	t.Helper()
	m := valuedData(1, value)
	m.Timestamp = proto.Uint64(uint64(time.Now().UnixMilli()))
	enc, err := codec.Encode(&sppb.Payload{Seq: proto.Uint64(seq), Metrics: []*sppb.Payload_Metric{m}})
	require.NoError(t, err)
	return enc
}

const spBirthTopic = "spBv1.0/plant-a/NBIRTH/node-3"
const spDataTopic = "spBv1.0/plant-a/NDATA/node-3"

// stopAnswering answers whatever the broker holds until c has stopped, so a stop that waits
// for the stream can finish; for a test that ends with publishes still held.
func (r *spRig) stopAnswering(c *Client) {
	r.t.Helper()
	quit, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-quit:
				return
			case <-time.After(5 * time.Millisecond):
				if r.holder != nil {
					r.holder.answerAll()
				}
			}
		}
	}()
	stopWithin(r.t, c, 30*time.Second)
	close(quit)
	<-done
}

// stopWithin stops c and fails, instead of hanging, if it takes longer than d.
func stopWithin(t *testing.T, c *Client, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() { c.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("the client did not stop within %v", d)
	}
}

// syncPublisher is the publish the Client had before it had a window: the receive goroutine
// writes through the synchronous writer and returns only once the stream acknowledged it. It
// is the control arm of the window measurement.
type syncPublisher struct{ w messaging.MessageWriter }

func (p syncPublisher) Submit(ctx context.Context, msgs []messaging.Message, done func(error)) {
	done(p.w.WriteMessages(ctx, msgs...))
}
func (syncPublisher) Settle(context.Context) error { return nil }

// 🔴 THE DEFECT. A Sparkplug connection ran with ordered delivery, so paho handled its messages
// one at a time on one goroutine, and that goroutine waited for the stream's acknowledgement of
// each before it took the next: the broker held ONE publish at a time per connection, which at
// a replicated broker's ~11 ms acknowledgement is ~88 messages a second, and every source runs
// on one pod. The inbound-events stream is replaced by a responder that holds every publish
// unanswered, so what it holds at once IS the connection's in-flight count, exactly.
//
// The control is the synchronous publish this replaced, through the same Client: it must hold
// exactly one. A test that only saw the new number could not tell the change from a broker
// that happens to hold that many.
func TestSparkplugKeepsTheWindowInFlight(t *testing.T) {
	const data = 100 // past the window, so the window is what limits it
	for _, arm := range []struct {
		name  string
		async bool
		want  int
	}{
		{"control: the synchronous publish holds one per connection", false, 1},
		{"the window", true, spWindow},
	} {
		t.Run(arm.name, func(t *testing.T) {
			rig := newSpRig(t, spWindow, 0)
			var pub Publisher = rig.writer
			if !arm.async {
				// Built BEFORE the stream is replaced: building a writer ensures its stream.
				sw, err := rig.nmgr.NewWriter(streams.InboundEvents)
				require.NoError(t, err)
				pub = syncPublisher{sw}
			}
			rig.hold()
			m := newSpMetrics()
			c := rig.client(pub, m)
			rig.online(c)
			t.Cleanup(func() { rig.stopAnswering(c) })

			feed := rig.feeder()
			feed.publish(spBirthTopic, nodeBirth(t))
			for i := 1; i <= data; i++ {
				feed.publish(spDataTopic, uniqueData(t, uint64(i), float64(i)))
			}
			require.Eventually(t, func() bool { return rig.holder.seen() >= arm.want }, 20*time.Second, time.Millisecond,
				"the connection never had %d publishes out at once", arm.want)
			time.Sleep(300 * time.Millisecond)
			require.Equal(t, arm.want, rig.holder.seen(),
				"publishes the broker held at once, all unanswered: the connection's in-flight count")
		})
	}
}

// 🔴 ONLY THE PUBLISH WAIT LEAVES THE RECEIVE GOROUTINE; OBSERVE DOES NOT. The session machine
// validates a strict per-node seq (next == last+1) and must see every message in the order the
// broker delivered it. With the stream holding every acknowledgement, 20 NDATA (the birth's two
// submissions and these fit in the window) must all still be OBSERVED, in order, by the receive
// goroutine: no gap, so no rebirth is requested. Then the stream answers and the rest of 100
// follow through the same goroutine, still with no rebirth.
//
// The control is the synchronous publish: the receive goroutine is parked on the first
// acknowledgement, so it has observed none of the 20. A run that asked for no rebirth because
// it observed nothing would pass the rebirth assertion and fail the observed one.
func TestSparkplugObserveStaysSerialUnderAHeldResponder(t *testing.T) {
	const held, total = 20, 100
	for _, arm := range []struct {
		name         string
		async        bool
		wantObserved int
	}{
		{"control: the synchronous publish parks the receive goroutine", false, 0},
		{"the window", true, held},
	} {
		t.Run(arm.name, func(t *testing.T) {
			rig := newSpRig(t, spWindow, 0)
			var pub Publisher = rig.writer
			if !arm.async {
				sw, err := rig.nmgr.NewWriter(streams.InboundEvents)
				require.NoError(t, err)
				pub = syncPublisher{sw}
			}
			rig.hold()
			m := newSpMetrics()
			c := rig.client(pub, m)
			rig.online(c)
			t.Cleanup(func() { rig.stopAnswering(c) })

			feed := rig.feeder()
			feed.publish(spBirthTopic, nodeBirth(t))
			for i := 1; i <= held; i++ {
				feed.publish(spDataTopic, uniqueData(t, uint64(i), float64(i)))
			}
			if arm.wantObserved > 0 {
				require.Eventually(t, func() bool { return m.received("NDATA") == arm.wantObserved }, 20*time.Second,
					time.Millisecond, "the receive goroutine did not get through the messages while the stream held every acknowledgement")
				require.Eventually(t, func() bool { return rig.holder.seen() == held+2 }, 10*time.Second, time.Millisecond,
					"the publishes the observed messages produced were not all held at the broker")
			} else {
				time.Sleep(time.Second)
			}
			assert.Equal(t, arm.wantObserved, m.received("NDATA"), "NDATA the receive goroutine had observed")
			assert.Zero(t, testutil.ToFloat64(m.enqueued),
				"a rebirth was requested: the seq check saw a gap, so the messages were not observed in order")
			assert.Zero(t, testutil.ToFloat64(m.failures))
			if !arm.async {
				return
			}

			// Past the window the receive goroutine waits for room, and the rest follows in order.
			quit, done := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(done)
				for {
					select {
					case <-quit:
						return
					case <-time.After(2 * time.Millisecond):
						rig.holder.answerAll()
					}
				}
			}()
			defer func() { close(quit); <-done }()
			for i := held + 1; i <= total; i++ {
				feed.publish(spDataTopic, uniqueData(t, uint64(i), float64(i)))
			}
			require.Eventually(t, func() bool { return m.received("NDATA") == total }, 30*time.Second, time.Millisecond,
				"the receive goroutine did not get through all %d messages once the stream answered", total)
			assert.Zero(t, testutil.ToFloat64(m.enqueued), "a rebirth was requested over a clean stream")
			assert.Zero(t, testutil.ToFloat64(m.failures))
		})
	}
}

// recordingPublisher wraps a Publisher and counts submissions and the errors they came back
// with, so a test can tell that an outage really bit.
type recordingPublisher struct {
	inner  Publisher
	calls  atomic.Int32
	errors atomic.Int32
}

func (p *recordingPublisher) Submit(ctx context.Context, msgs []messaging.Message, done func(error)) {
	p.calls.Add(1)
	p.inner.Submit(ctx, msgs, func(err error) {
		if err != nil {
			p.errors.Add(1)
		}
		done(err)
	})
}
func (p *recordingPublisher) Settle(ctx context.Context) error { return p.inner.Settle(ctx) }

// storedMeasurements reads every measurement the inbound-events stream holds, in stream order,
// as the values of the metric named name.
func (r *spRig) storedMeasurements(name string) []string {
	r.t.Helper()
	info, err := r.js.StreamInfo(r.streamName())
	require.NoError(r.t, err)
	var out []string
	for seq := info.State.FirstSeq; seq <= info.State.LastSeq && info.State.Msgs > 0; seq++ {
		raw, err := r.js.GetMsg(r.streamName(), seq)
		if err != nil {
			continue
		}
		ev, err := esproto.UnmarshalUnresolvedEvent(raw.Data)
		require.NoError(r.t, err)
		p, ok := ev.Payload.(*esmodel.UnresolvedMeasurementsPayload)
		if !ok {
			continue
		}
		for _, e := range p.Entries {
			if v, ok := e.Measurements[name]; ok {
				out = append(out, v)
			}
		}
	}
	return out
}

// 🔴 EXACTLY ONCE AFTER AN OUTAGE. The inbound-events stream disappears under 20 messages (a
// broker that has no stream to answer), every first publish fails, and the stream comes back
// within the retry budget. Each failed publish is resubmitted by the Client, and every one of
// the 20 must be stored exactly once: not dropped, and not twice. IngestFailures stays 0,
// because nothing was abandoned.
//
// A run that never hit the outage proves nothing, so the publisher is wrapped to show that
// submissions failed and were retried.
func TestSparkplugStoresEveryMessageExactlyOnceAfterAnOutage(t *testing.T) {
	const data = 20
	rig := newSpRig(t, spWindow, 0)
	rec := &recordingPublisher{inner: rig.writer}
	m := newSpMetrics()
	c := rig.client(rec, m)
	rig.online(c)
	t.Cleanup(func() { stopWithin(t, c, 30*time.Second) })

	feed := rig.feeder()
	feed.publish(spBirthTopic, nodeBirth(t))
	require.Eventually(t, func() bool { return rec.calls.Load() == 2 && rig.storedMeasurementCount() == 1 },
		20*time.Second, 5*time.Millisecond, "the birth was not stored")

	info, err := rig.js.StreamInfo(rig.streamName())
	require.NoError(t, err)
	cfg := info.Config
	require.NoError(t, rig.js.DeleteStream(rig.streamName()))

	for i := 1; i <= data; i++ {
		feed.publish(spDataTopic, uniqueData(t, uint64(i), float64(100+i)))
	}
	require.Eventually(t, func() bool { return rec.errors.Load() >= 1 }, 20*time.Second, time.Millisecond,
		"the outage never failed a publish, so this run proves nothing about recovery")
	// Back well inside the first backoff, so a resubmission finds it.
	_, err = rig.js.AddStream(&cfg)
	require.NoError(t, err)

	require.Eventually(t, func() bool { return len(rig.storedMeasurements("m1")) >= data }, 20*time.Second,
		10*time.Millisecond, "the messages were not all stored after the outage")
	// The wait for the last, then settle everything the Client still has in flight.
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, rig.writer.Settle(sctx))

	got := rig.storedMeasurements("m1")
	counts := map[string]int{}
	for _, v := range got {
		counts[v]++
	}
	for i := 1; i <= data; i++ {
		assert.Equal(t, 1, counts[strconv.Itoa(100+i)], "value %d must be stored exactly once", 100+i)
	}
	assert.Len(t, got, data, "the stream holds something other than the %d messages sent after the outage", data)
	assert.Greater(t, int(rec.calls.Load()), 2+data, "no publish was ever resubmitted")
	assert.Zero(t, testutil.ToFloat64(m.failures), "a message was abandoned that the retry budget could have saved")
}

// storedMeasurementCount is how many measurement events the stream holds.
func (r *spRig) storedMeasurementCount() int {
	return len(r.storedMeasurements("m1"))
}

// 🔴 THE GATE STILL REFUSES TELEMETRY, AND STILL ADMITS PRESENCE. The stream holds 20 messages
// and nothing reads it; once the unread backlog reaches the gate's share, a reading is refused
// at the writer (counted on IngestFailures, not retried), and the stream keeps every event it
// took instead of evicting its oldest unread ones. A node that connects while the gate is
// closed still has its CONNECTED stored: presence bypasses the gate, so the device is not
// left looking offline.
func TestSparkplugRefusesTelemetryAtAClosedGateButAdmitsPresence(t *testing.T) {
	const posts = 30
	rig := newSpRig(t, spWindow, 20)
	m := newSpMetrics()
	c := rig.client(rig.writer, m)
	rig.online(c)
	t.Cleanup(func() { stopWithin(t, c, 30*time.Second) })

	msgs := func() uint64 {
		info, err := rig.js.StreamInfo(rig.streamName())
		require.NoError(t, err)
		return info.State.Msgs
	}

	// accounted is how many readings have an outcome: stored (counted when acknowledged) or
	// refused (counted as a failure). Waiting on it, and not on the receipt of a message, is
	// what makes "the previous message is settled" true before the gate is measured.
	accounted := func() int {
		return int(testutil.ToFloat64(m.emitted)) + int(testutil.ToFloat64(m.failures))
	}
	feed := rig.feeder()
	feed.publish(spBirthTopic, nodeBirth(t))
	require.Eventually(t, func() bool { return accounted() == 1 && testutil.ToFloat64(m.presence) == 1 },
		20*time.Second, time.Millisecond, "the birth was not stored")
	require.Equal(t, uint64(2), msgs(), "the birth stores a measurement and a CONNECTED")

	for i := 1; i <= posts; i++ {
		feed.publish(spDataTopic, uniqueData(t, uint64(i), float64(i)))
		require.Eventually(t, func() bool { return accounted() == 1+i }, 20*time.Second, time.Millisecond)
		// The gate is measured by the traffic that consults it, every few seconds; this fills
		// far faster, so it says when each measurement happens.
		rig.nmgr.MeasureBackpressureForTesting(t, streams.InboundEvents)
	}
	refused := int(testutil.ToFloat64(m.failures))
	require.Positive(t, refused, "the gate never refused a reading")
	assert.Equal(t, uint64(2+posts-refused), msgs(), "the stream must keep every event it accepted, and take none it refused")
	assert.LessOrEqual(t, msgs(), uint64(20))

	// A different node connecting now: its reading is refused, its presence is not.
	before := msgs()
	feed.publish("spBv1.0/plant-a/NBIRTH/node-4", nodeBirth(t))
	require.Eventually(t, func() bool { return testutil.ToFloat64(m.presence) == 2 && accounted() == 2+posts },
		20*time.Second, time.Millisecond, "the second node's birth had no outcome")
	assert.Equal(t, before+1, msgs(), "the CONNECTED of a node that came up while the gate was closed must be stored")
	assert.Equal(t, float64(refused+1), testutil.ToFloat64(m.failures), "and its reading refused and counted")
}

// 🔴 NO PUBLISH AFTER STOP. Stop is what a leadership term ends with before it releases the
// lease, and "nothing of this term is published after the next leader has begun" holds only
// once the writer has nothing in flight, which is not the same as the receive goroutine
// having returned. Twenty publishes are held unanswered: Stop must NOT return (the lease
// would be released under them), must return once they are answered, and nothing may reach
// the stream afterwards, not even from a message handler that was still running.
func TestSparkplugStopSettlesWhatItSubmittedAndPublishesNothingAfter(t *testing.T) {
	const data = 19 // plus the birth's two submissions = 21 messages submitted... held up to the window
	rig := newSpRig(t, spWindow, 0)
	rig.hold()
	m := newSpMetrics()
	c := rig.client(rig.writer, m)
	rig.online(c)

	feed := rig.feeder()
	feed.publish(spBirthTopic, nodeBirth(t))
	for i := 1; i <= data; i++ {
		feed.publish(spDataTopic, uniqueData(t, uint64(i), float64(i)))
	}
	require.Eventually(t, func() bool { return rig.holder.seen() == data+2 }, 20*time.Second, time.Millisecond,
		"the publishes never reached the broker")

	stopped := make(chan struct{})
	go func() { c.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatalf("Stop returned with %d publishes still unanswered: the lease would be released "+
			"under publishes that are still on their way to the stream", rig.holder.unanswered())
	case <-time.After(500 * time.Millisecond):
	}

	rig.holder.answerAll()
	select {
	case <-stopped:
	case <-time.After(15 * time.Second):
		t.Fatal("Stop did not return after every publish was answered")
	}
	seen := rig.holder.seen()
	require.Equal(t, data+2, seen)
	assert.Zero(t, testutil.ToFloat64(m.failures), "Stop abandoned something that was stored")

	// A handler still running when Stop returned (paho's quiesce can end with one) must not
	// publish: it finds the Client stopped and abandons, counted.
	c.onMessage(nil, fakeMessage{topic: spDataTopic, payload: uniqueData(t, uint64(data+1), 999)})
	feed.publish(spDataTopic, uniqueData(t, uint64(data+2), 1000))
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, seen, rig.holder.seen(), "a stopped Client published")
}

// 🔴 A SECOND TERM REUSES THE WRITER. The service builds one AsyncWriter per source for the life
// of the process, because each holds a reply subscription until the connection closes, so a
// leadership term only DRAINS it (Settle) and the next term publishes through the same one.
// The writer must therefore still take a publish after a Stop, and the Client must publish
// again after a Connect.
func TestSparkplugSecondTermPublishesThroughTheSameWriter(t *testing.T) {
	rig := newSpRig(t, spWindow, 0)
	m := newSpMetrics()
	c := rig.client(rig.writer, m)

	rig.online(c)
	feed := rig.feeder()
	feed.publish(spBirthTopic, nodeBirth(t))
	feed.publish(spDataTopic, uniqueData(t, 1, 11))
	require.Eventually(t, func() bool { return len(rig.storedMeasurements("m1")) == 2 }, 20*time.Second, 10*time.Millisecond)
	stopWithin(t, c, 30*time.Second)

	// The next term: the same Client, the same writer, a new session. A node the first term
	// never saw, so the session machine treats its birth as new.
	rig.online(c)
	t.Cleanup(func() { stopWithin(t, c, 30*time.Second) })
	feed.publish("spBv1.0/plant-a/NBIRTH/node-9", nodeBirth(t))
	feed.publish("spBv1.0/plant-a/NDATA/node-9", uniqueData(t, 1, 22))
	require.Eventually(t, func() bool { return len(rig.storedMeasurements("m1")) == 4 }, 20*time.Second, 10*time.Millisecond,
		"the second term stored nothing through the writer the first one drained")
	assert.Zero(t, testutil.ToFloat64(m.failures))
}

// 🔴 ONE NODE'S MESSAGES REACH THE STREAM IN THE ORDER THEY WERE RECEIVED. The window runs
// publishes concurrently, but they are submitted from the one receive goroutine, and the writer
// keeps one submitter's order, so what the stream holds for a node is its readings in the order
// the node sent them. Nothing downstream depends on it today (the ids are content-derived and
// presence is ordered by session), but it is what the serial receive path always did, and the
// design keeps it for nothing.
func TestSparkplugKeepsOneNodesStreamOrder(t *testing.T) {
	const data = 80
	rig := newSpRig(t, spWindow, 0)
	m := newSpMetrics()
	c := rig.client(rig.writer, m)
	rig.online(c)
	t.Cleanup(func() { stopWithin(t, c, 30*time.Second) })

	feed := rig.feeder()
	feed.publish(spBirthTopic, nodeBirth(t))
	for i := 1; i <= data; i++ {
		feed.publish(spDataTopic, uniqueData(t, uint64(i), float64(1000+i)))
	}
	require.Eventually(t, func() bool { return len(rig.storedMeasurements("m1")) == data+1 }, 30*time.Second,
		10*time.Millisecond, "not every message was stored")
	got := rig.storedMeasurements("m1")
	want := []string{"1"}
	for i := 1; i <= data; i++ {
		want = append(want, strconv.Itoa(1000+i))
	}
	assert.Equal(t, want, got, "the stream holds the node's readings in a different order than it sent them")
}

// serialProbe wraps the real ingest pipeline and records whether two messages were ever being
// prepared at once, and the order their values arrived in. Preparing is the receive goroutine's
// work, done right after Observe, so overlap means messages were handled concurrently and
// Observe is no longer a serial check.
type serialProbe struct {
	inner    SampleIngester
	inFlight atomic.Int32
	overlap  atomic.Bool
	mu       sync.Mutex
	values   []float64
}

func (p *serialProbe) PrepareIngest(ctx context.Context, tenant string, policy IngestPolicy, externalId string, samples []Sample) (*Publication, error) {
	if p.inFlight.Add(1) > 1 {
		p.overlap.Store(true)
	}
	defer p.inFlight.Add(-1)
	p.mu.Lock()
	for _, s := range samples {
		p.values = append(p.values, s.Value)
	}
	p.mu.Unlock()
	time.Sleep(3 * time.Millisecond) // wide enough that a concurrent handler overlaps
	return p.inner.PrepareIngest(ctx, tenant, policy, externalId, samples)
}

func (p *serialProbe) PreparePresence(ctx context.Context, tenant string, policy IngestPolicy, events []PresenceEvent) (*Publication, error) {
	return p.inner.PreparePresence(ctx, tenant, policy, events)
}

// 🔴 OBSERVE IS SERIAL, shown directly and not through its side effect: the messages are handled
// one at a time, in the order the broker delivered them, with the stream answering as fast as it
// can. (A rebirth request is the side effect of a seq gap, which a concurrent run only
// SOMETIMES produces; overlap and order are what it always breaks.)
func TestSparkplugHandlesOneConnectionsMessagesOneAtATimeInOrder(t *testing.T) {
	const data = 60
	rig := newSpRig(t, spWindow, 0)
	m := newSpMetrics()
	ing := NewIngester(NewRegistrar(fakeCreateGQL{}, "url", nil), NewEmitter(failingWriter{}, time.Now),
		IngestMetrics{MeasurementsEmitted: m.emitted, PresenceEmitted: m.presence})
	probe := &serialProbe{inner: ing}
	src := config.SparkplugSource{Tenant: "acme", HostId: "h1", Groups: []string{"plant-a"}, AutoRegister: true, DeviceTypeToken: "sp-node"}
	c := NewClient(src, Broker{URL: rig.mqttURL, ClientID: "sp-async-host"}, probe, admitAllSamples{}, time.Now, m.host())
	c.SetPublisher(rig.writer)
	rig.online(c)
	t.Cleanup(func() { stopWithin(t, c, 30*time.Second) })

	feed := rig.feeder()
	feed.publish(spBirthTopic, nodeBirth(t))
	for i := 1; i <= data; i++ {
		feed.publish(spDataTopic, uniqueData(t, uint64(i), float64(1000+i)))
	}
	// Wait for the PROBE to have seen every message, not for the received counter: the
	// counter is bumped when a message is described, before its samples reach PrepareIngest,
	// so waiting on it alone could read the probe one message short and report a correct
	// order as a wrong one.
	require.Eventually(t, func() bool {
		probe.mu.Lock()
		defer probe.mu.Unlock()
		return m.received("NDATA") == data && len(probe.values) >= data+1
	}, 30*time.Second, 5*time.Millisecond)
	assert.False(t, probe.overlap.Load(), "two messages were being handled at once: Observe is no longer a serial check")
	probe.mu.Lock()
	defer probe.mu.Unlock()
	want := []float64{1}
	for i := 1; i <= data; i++ {
		want = append(want, float64(1000+i))
	}
	assert.Equal(t, want, probe.values, "messages were handled in a different order than the broker delivered them")
}
