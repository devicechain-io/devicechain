// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/model"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	core "github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
)

// THE FAIL-CLOSED MQTT ACKNOWLEDGEMENT END TO END: a device publishing QoS 1 over the broker's
// MQTT gateway, the event-sources ingest built from this package's sources, the fail-closed
// store and its watch from core, and inbound-events as the platform sees it. The setting is
// turned on by setting the field on each pod's configuration. The same on a three-node cluster,
// under load, with leader moves, is failclosed_e2e_cluster_test.go.
//
// What is held: every message the device was PUBACKed reaches inbound-events EXACTLY ONCE, by
// its dedup id, through
//
//  1. a full store, which refuses the device (no PUBACK, connection closed) while its reader is
//     not running, and the device's resend after the reader drains it;
//  2. a publish whose inbound-events PubAck the source never sees, so its store copy is
//     redelivered and published again under the same id;
//  3. the store deleted and created again by the watch, whose sequences restart at 1 (M1);
//  4. a rollout turning the setting OFF (the pod with it on stops, the pod with it off releases
//     the store once its heartbeat is stale, then removes the drained durables) and one
//     turning it back ON while the pod with it off is still running (H1).
//
// The transitions are made with no publish in flight, which is the condition for exactly once:
// a publish in the seconds around a hand-over can reach inbound-events twice (never not at
// all), because the two copies of one device publish carry nothing that ties them together.

const (
	e2eInstance    = "fce2e"
	e2eTenant      = "acme"
	e2eDeviceToken = "dev-1"
	// e2eHot is the hot ceiling the pods run at, which the fail-closed store takes: a few
	// dozen padded publishes fill it.
	e2eHot = 64 << 10
)

// e2ePad makes each device message large enough that the store fills in a few dozen, while
// what reaches inbound-events (built from the alternate id alone) stays small.
var e2ePad = strings.Repeat("x", 1500)

type e2ePod struct {
	t           *testing.T
	name        string
	on          bool
	nmgr        *messaging.NatsManager
	capture     *GatewayJetStreamSource
	mu          sync.Mutex
	store       *GatewayJetStreamSource
	storeReader *messaging.MqttStoreReader
	watch       *messaging.MqttFailClosedWatch
	wrap        func(messaging.OrderedWriter) messaging.OrderedWriter
}

// e2eBuild is the inbound message: the device's alternate id as the body, and the dedup id
// the source named, so inbound-events can be counted by what the device sent.
func e2eBuild(_ string, tenant string, event *model.UnresolvedEvent, _ interface{}, dedupID string) (context.Context, messaging.Message, bool) {
	if event.AltId == nil {
		return nil, messaging.Message{}, false
	}
	return core.WithTenant(context.Background(), tenant), messaging.Message{Key: []byte(event.Device),
		Value: []byte(*event.AltId), DedupID: dedupID}, true
}

func e2eSource(t *testing.T, id string) *GatewayJetStreamSource {
	return NewGatewayJetStreamSource(nil, id, NewJsonDecoder(map[string]string{}),
		func(string, []byte) {}, e2eBuild,
		func(_ string, _ string, _ []byte, err error) error {
			t.Errorf("unexpected failed-decode: %v", err)
			return nil
		}, nil, admitAllReadings)
}

// startE2EPod starts a pod: its broker manager (which declares the store with the setting
// on), the capture-stream source, and the watch (every 200 ms). A pod with the setting on
// starts its store source when startStore is called; one with it off starts it when its start
// or its watch sees the store marked, as event-sources does.
func startE2EPod(t *testing.T, serverURL, name string, on bool, clock func() time.Time,
	wrap func(messaging.OrderedWriter) messaging.OrderedWriter) *e2ePod {
	t.Helper()
	return startE2EPodWith(t, serverURL, name, on, clock, wrap, e2ePodOpts{})
}

// e2ePodOpts is what the cluster test sets on a pod beyond the single-server one's defaults.
type e2ePodOpts struct {
	// nats, when set, has the last word on the pod's broker configuration.
	nats func(*mscfg.NatsConfiguration)
	// wrapCapture wraps the capture source's inbound writer, as wrap does the store source's.
	wrapCapture func(messaging.OrderedWriter) messaging.OrderedWriter
	// productionSettle keeps the from-mark settle at its production value instead of 0. A test
	// that turns the setting on while devices publish needs it: with no settle, a publish in
	// flight across the durables' creation can have its store copy skipped for no interest
	// and its direct copy skipped above the mark, which is the window the settle exists to
	// close.
	productionSettle bool
}

func startE2EPodWith(t *testing.T, serverURL, name string, on bool, clock func() time.Time,
	wrap func(messaging.OrderedWriter) messaging.OrderedWriter, opts e2ePodOpts) *e2ePod {
	t.Helper()
	if !opts.productionSettle {
		messaging.SetMqttFailClosedFromSettleForTesting(t, 0)
	}
	u, err := url.Parse(serverURL)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	ms := &core.Microservice{InstanceId: e2eInstance, FunctionalArea: "event-sources", Readiness: core.NewReadinessGate()}
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: u.Hostname(), Port: uint32(port),
		StreamMaxBytes: e2eHot, MqttFailClosed: on}
	if opts.nats != nil {
		opts.nats(&ms.InstanceConfiguration.Infrastructure.Nats)
	}
	ms.InstanceConfiguration.ApplyDefaults()
	ms.Readiness.MarkReadyWithoutAuthSurface()

	p := &e2ePod{t: t, name: name, on: on, wrap: wrap}
	var reader messaging.MessageReader
	var writer messaging.OrderedWriter
	p.nmgr = messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(), func(n *messaging.NatsManager) error {
		if on {
			// Twice: a take-over records the direct streams' from marks only on a pass after the
			// one that created the durables (with the settle shortened to 0 above), and until then
			// the capture reader would forward the direct copies the store holds too.
			for i := 0; i < 2; i++ {
				if _, err := n.ReconcileMqttFailClosed(context.Background()); err != nil {
					return err
				}
			}
		}
		r, err := n.NewReader(streams.DeviceEventsCapture)
		if err != nil {
			return err
		}
		reader = r
		writer, err = NewInboundWriter(n, 64)
		if err == nil && opts.wrapCapture != nil {
			writer = opts.wrapCapture(writer)
		}
		return err
	})
	p.nmgr.RecordMaxDeliveries(func(*messaging.NatsManager) (messaging.MaxDeliveryFunc, error) {
		return func(context.Context, messaging.MaxDelivery) (messaging.MaxDeliveryOutcome, error) {
			return messaging.MaxDeliveryLettered, nil
		}, nil
	})
	// A redelivery after a few seconds rather than a minute; the store's durables take it too.
	p.nmgr.SetAckWaitForTesting(t, 3*time.Second)
	p.nmgr.SetMqttFailClosedClockForTesting(t, clock)
	ctx := context.Background()
	require.NoError(t, p.nmgr.Initialize(ctx))
	require.NoErrorf(t, p.nmgr.Start(ctx), "starting %s", name)
	if !on {
		n := ms.InstanceConfiguration.Infrastructure.Nats
		require.NoError(t, p.nmgr.ReconcileMqttStores(ctx, n.MqttStoreMaxBytes, n.MqttQoS2StoreMaxBytes))
	}
	p.capture = e2eSource(t, "gw-"+name)
	require.NoError(t, p.capture.Initialize(ctx))
	p.capture.SetReader(reader)
	p.capture.SetWriter(writer)
	require.NoError(t, p.capture.Start(ctx))
	if !on && p.nmgr.MqttMessageStoreMarked() {
		p.startStore()
	}
	p.watch = p.nmgr.StartMqttFailClosedWatch(ctx, nil, 200*time.Millisecond, func(marked bool) {
		if marked && !on {
			p.startStore()
		}
	})
	t.Cleanup(p.stop)
	return p
}

func (p *e2ePod) startStore() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.store != nil {
		return
	}
	ctx := context.Background()
	r, err := p.nmgr.NewMqttMessageStoreReader(messaging.MqttFailClosedEvents)
	require.NoError(p.t, err)
	w, err := NewInboundWriter(p.nmgr, 64)
	require.NoError(p.t, err)
	if p.wrap != nil {
		w = p.wrap(w)
	}
	src := e2eSource(p.t, "gw-"+p.name)
	src.UseMessageStore()
	require.NoError(p.t, src.Initialize(ctx))
	src.SetReader(r)
	src.SetWriter(w)
	require.NoError(p.t, src.Start(ctx))
	p.store, p.storeReader = src, r
}

// stopStore stops the pod's store source and closes its reader, so the store is read by
// nobody in this pod until startStore is called again.
func (p *e2ePod) stopStore() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = p.store.Stop(ctx)
	p.storeReader.Close()
	p.store, p.storeReader = nil, nil
}

func (p *e2ePod) stop() {
	if p.nmgr == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p.watch.Stop()
	_ = p.capture.Stop(ctx)
	p.mu.Lock()
	if p.store != nil {
		_ = p.store.Stop(ctx)
		p.storeReader.Close()
	}
	p.mu.Unlock()
	_ = p.nmgr.Stop(ctx)
	p.nmgr = nil
}

// e2eDevice is a device with a persistent session: a publish the broker did not PUBACK stays
// in its store, and the next connection with the same session re-sends it, as MQTT 3.1.1
// requires of a CleanSession=0 client.
type e2eDevice struct {
	t     *testing.T
	url   string
	store mqtt.Store
	c     mqtt.Client
	lost  chan struct{}
}

func connectE2EDevice(t *testing.T, mqttURL string) *e2eDevice {
	t.Helper()
	d := &e2eDevice{t: t, url: mqttURL, store: mqtt.NewMemoryStore()}
	d.connect()
	return d
}

// connect opens a connection with the device's session and store, re-sending what the last
// one left unacknowledged.
func (d *e2eDevice) connect() {
	d.t.Helper()
	lost := make(chan struct{})
	var once sync.Once
	opts := mqtt.NewClientOptions().AddBroker(d.url).SetClientID("fce2e-device").
		SetCleanSession(false).SetStore(d.store).SetAutoReconnect(false).
		SetConnectTimeout(5 * time.Second).
		SetConnectionLostHandler(func(mqtt.Client, error) { once.Do(func() { close(lost) }) })
	c := mqtt.NewClient(opts)
	tok := c.Connect()
	require.True(d.t, tok.WaitTimeout(15*time.Second))
	require.NoError(d.t, tok.Error())
	d.c, d.lost = c, lost
	d.t.Cleanup(func() { c.Disconnect(100) })
}

func e2eBody(id string) string {
	return fmt.Sprintf(`{"device":%q,"altId":%q,"eventType":"Measurement","pad":%q,`+
		`"payload":{"entries":[{"measurements":{"t":"1"}}]}}`, e2eDeviceToken, id, e2ePad)
}

func (d *e2eDevice) publish(id string) mqtt.Token {
	return d.c.Publish(fmt.Sprintf("%s/%s/devices/%s/events", e2eInstance, e2eTenant, e2eDeviceToken), 1, false, e2eBody(id))
}

// publishAcked publishes ids one at a time, each PUBACKed within 10 s.
func (d *e2eDevice) publishAcked(ids ...string) {
	d.t.Helper()
	for _, id := range ids {
		tok := d.publish(id)
		require.Truef(d.t, tok.WaitTimeout(10*time.Second), "%s was not PUBACKed", id)
		require.NoError(d.t, tok.Error())
	}
}

func e2eIds(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s-%03d", prefix, i)
	}
	return out
}

// inboundCounts reads inbound-events as it stands: how many times each id is there.
func inboundCounts(t *testing.T, js nats.JetStreamContext) map[string]int {
	t.Helper()
	got := map[string]int{}
	info, err := js.StreamInfo(messaging.StreamName(e2eInstance, streams.InboundEvents))
	if errors.Is(err, nats.ErrStreamNotFound) {
		return got
	}
	require.NoError(t, err)
	for seq := info.State.FirstSeq; seq <= info.State.LastSeq && info.State.Msgs > 0; seq++ {
		m, err := js.GetMsg(messaging.StreamName(e2eInstance, streams.InboundEvents), seq)
		if err != nil {
			continue
		}
		got[string(m.Data)]++
	}
	return got
}

// awaitInbound waits until every id in want is in inbound-events.
func awaitInbound(t *testing.T, js nats.JetStreamContext, want map[string]bool, within time.Duration, what string) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		got := inboundCounts(t, js)
		missing := []string{}
		for id := range want {
			if got[id] == 0 {
				missing = append(missing, id)
			}
		}
		if len(missing) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %d PUBACKed messages never reached inbound-events, e.g. %v", what, len(missing), missing[:min(5, len(missing))])
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// lostPubAck reports the first publish of each id in fail as failed, after it has really been
// published: the inbound-events PubAck the source never saw.
type lostPubAck struct {
	messaging.OrderedWriter
	mu     sync.Mutex
	fail   map[string]bool
	failed map[string]int
}

func (w *lostPubAck) Publish(ctx context.Context, msg messaging.Message, done func(error)) {
	id := string(msg.Value)
	w.mu.Lock()
	lose := w.fail[id] && w.failed[id] == 0
	if lose {
		w.failed[id]++
	}
	w.mu.Unlock()
	if !lose {
		w.OrderedWriter.Publish(ctx, msg, done)
		return
	}
	w.OrderedWriter.Publish(ctx, msg, func(err error) {
		if err == nil {
			err = errors.New("the PubAck was lost")
		}
		done(err)
	})
}

func TestEveryPubackedMessageReachesInboundEventsExactlyOnce(t *testing.T) {
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true,
		StoreDir: dctest.JetStreamStoreDir(t), ServerName: "fce2e", NoLog: true, NoSigs: true,
		MQTT: natsserver.MQTTOpts{Host: "127.0.0.1", Port: -1}})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(15*time.Second))
	t.Cleanup(srv.Shutdown)
	mqttPort := dctest.MQTTPort(t, srv)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	mqttURL := fmt.Sprintf("tcp://127.0.0.1:%d", mqttPort)

	var clockMu sync.Mutex
	var skew time.Duration
	clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return time.Now().Add(skew) }
	advance := func(d time.Duration) { clockMu.Lock(); skew += d; clockMu.Unlock() }

	acked := map[string]bool{}
	lost := &lostPubAck{fail: map[string]bool{}, failed: map[string]int{}}

	// 1. A full store refuses the device while nothing reads it.
	podA := startE2EPod(t, srv.ClientURL(), "A-on", true, clock, func(w messaging.OrderedWriter) messaging.OrderedWriter {
		lost.OrderedWriter = w
		return lost
	})
	dev := connectE2EDevice(t, mqttURL)
	var refused mqtt.Token
	var refusedID string
	for i := 0; i < 500 && refused == nil; i++ {
		id := fmt.Sprintf("fill-%03d", i)
		tok := dev.publish(id)
		if tok.WaitTimeout(4*time.Second) && tok.Error() == nil {
			acked[id] = true
			continue
		}
		refused, refusedID = tok, id
	}
	require.NotNil(t, refused, "the store never filled")
	require.NotEmpty(t, acked)
	select {
	case <-dev.lost:
	case <-time.After(10 * time.Second):
		t.Fatal("a full store withheld the PUBACK but left the connection open")
	}
	require.Empty(t, inboundCounts(t, js), "the capture path forwarded a QoS 1 direct copy the store holds")

	// The reader drains; the device's resend lands. Two of the drained messages have their
	// inbound-events PubAck lost, so they are redelivered and published again.
	firstFill := "fill-000"
	lost.mu.Lock()
	lost.fail[firstFill], lost.fail["fill-001"] = true, true
	lost.mu.Unlock()
	podA.startStore()
	awaitInbound(t, js, acked, 30*time.Second, "after the drain")
	// The device reconnects with its session, and re-sends the packet it had no PUBACK for.
	dev.connect()
	acked[refusedID] = true
	awaitInbound(t, js, acked, 30*time.Second, "after the resend")
	lost.mu.Lock()
	require.Equal(t, 1, lost.failed[firstFill], "premise: a PubAck was lost and the message redelivered")
	lost.mu.Unlock()

	live := e2eIds("live", 10)
	dev.publishAcked(live...)
	for _, id := range live {
		acked[id] = true
	}
	awaitInbound(t, js, acked, 20*time.Second, "live")

	// 3. The store deleted and created again: sequences restart at 1 under a new epoch.
	before, err := js.StreamInfo(messaging.MqttMessageStore)
	require.NoError(t, err)
	require.NoError(t, js.DeleteStream(messaging.MqttMessageStore))
	require.Eventually(t, func() bool {
		si, err := js.StreamInfo(messaging.MqttMessageStore)
		return err == nil && si.Created.After(before.Created) && si.Config.Discard == nats.DiscardNew
	}, 10*time.Second, 50*time.Millisecond, "the watch never created the store again")
	time.Sleep(500 * time.Millisecond) // the pass after the re-creation reads the from marks; the capture reader's look refreshes
	recreated := e2eIds("recreated", 10)
	dev.publishAcked(recreated...)
	for _, id := range recreated {
		acked[id] = true
	}
	awaitInbound(t, js, acked, 30*time.Second, "after the store was created again")

	// 4. A rollout turning the setting off: B (off) comes up, A goes, B releases the store once
	// A's heartbeat is stale and removes the drained durables after the grace.
	podB := startE2EPod(t, srv.ClientURL(), "B-off", false, clock, nil)
	require.True(t, podB.nmgr.MqttMessageStoreMarked(), "the pod with the setting off saw the store held")
	podA.stop()
	advance(2 * time.Minute)
	require.Eventually(t, func() bool {
		// Released: the owner mark is gone (the store stays discard new until its durables
		// are drained, so the discard policy does not show it).
		si, err := js.StreamInfo(messaging.MqttMessageStore)
		if err != nil {
			return false
		}
		_, owned := si.Config.Metadata["io.devicechain.mqtt-fail-closed.owner"]
		return !owned
	}, 10*time.Second, 50*time.Millisecond, "the pod with the setting off never released the store")
	advance(2 * time.Minute)
	require.Eventually(t, func() bool {
		names := []string{}
		for n := range js.ConsumerNames(messaging.MqttMessageStore) {
			names = append(names, n)
		}
		for _, d := range messaging.MqttFailClosedDurables(e2eInstance) {
			for _, n := range names {
				if n == d.Name {
					return false
				}
			}
		}
		return true
	}, 10*time.Second, 50*time.Millisecond, "the drained durables were never removed")
	time.Sleep(500 * time.Millisecond)
	off := e2eIds("off", 10)
	dev.publishAcked(off...)
	for _, id := range off {
		acked[id] = true
	}
	awaitInbound(t, js, acked, 20*time.Second, "with the setting off")

	// ...and back on, while the pod with it off is still running: it sees the store held, and
	// skips the direct copies the store holds too.
	podA2 := startE2EPod(t, srv.ClientURL(), "A2-on", true, clock, nil)
	podA2.startStore()
	time.Sleep(time.Second) // B's watch has seen the store held again
	on := e2eIds("on-again", 10)
	dev.publishAcked(on...)
	for _, id := range on {
		acked[id] = true
	}
	podB.stop()
	last := e2eIds("on-only", 10)
	dev.publishAcked(last...)
	for _, id := range last {
		acked[id] = true
	}
	awaitInbound(t, js, acked, 30*time.Second, "with the setting on again")

	// Exactly once: the PUBACKed set and nothing else, each once.
	time.Sleep(time.Second) // a duplicate still in flight would land now
	got := inboundCounts(t, js)
	for id, n := range got {
		require.Truef(t, acked[id], "inbound-events holds %q, which was never PUBACKed", id)
		require.Equalf(t, 1, n, "%q reached inbound-events %d times", id, n)
	}
	require.Len(t, got, len(acked))
	_ = podA2
}
