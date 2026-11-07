// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"sync"
	"testing"
	"time"

	dctest "github.com/devicechain-io/dc-microservice/test"
	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/devicechain-io/dc-edge-agent/config"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// THE NETEM RIG.
//
// A bad WAN is the condition the edge spool exists for, and the one a unit test never makes: a
// round trip of 600 ms, a link that kills most connections, a cut that outlasts a day, a batch
// that arrives three days after it happened. This rig puts a fault-injecting TCP proxy between
// the agent's uplink and a real MQTT broker, and a model of the platform behind the broker that
// does what event-sources does with a gateway's events: it stores each key once, tells the
// gateway what it accepted, and sheds some events at a ceiling, telling the gateway that too.
//
// Honest scaling: the link faults are real (delay, connection kills, a cut that refuses and
// severs every connection), but time is compressed. The cut lasts seconds, not 24 hours, and the
// three-day-late batch is events that carry an occurredTime three days old. Nothing in the
// agent's retire logic reads a wall clock longer than the accept timeout, so the compression
// changes how long the rig takes, not what it proves. The platform is a model here; the real
// event-sources behaviour it stands in for is pinned in that service's own tests.
//
// The negative control is the same run with the agent retiring on PUBACK, as every release did
// before the acceptance signal: it loses the events the platform shed.

type netem struct {
	ln     net.Listener
	target string

	mu    sync.Mutex
	delay time.Duration // one way
	loss  float64       // probability per chunk that the connection is killed
	cut   bool
	conns map[net.Conn]struct{}
	rnd   *rand.Rand
}

func newNetem(t *testing.T, target string) *netem {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	n := &netem{ln: ln, target: target, conns: map[net.Conn]struct{}{}, rnd: rand.New(rand.NewSource(7))}
	go n.serve()
	t.Cleanup(func() { _ = ln.Close(); n.severAll() })
	return n
}

func (n *netem) addr() string { return n.ln.Addr().String() }

func (n *netem) set(delay time.Duration, loss float64) {
	n.mu.Lock()
	n.delay, n.loss = delay, loss
	n.mu.Unlock()
}

// setCut refuses new connections and severs every open one while on.
func (n *netem) setCut(on bool) {
	n.mu.Lock()
	n.cut = on
	n.mu.Unlock()
	if on {
		n.severAll()
	}
}

func (n *netem) severAll() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for c := range n.conns {
		_ = c.Close()
	}
}

func (n *netem) params() (time.Duration, float64, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.delay, n.loss, n.cut
}

func (n *netem) kill() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.loss > 0 && n.rnd.Float64() < n.loss
}

func (n *netem) serve() {
	for {
		c, err := n.ln.Accept()
		if err != nil {
			return
		}
		if _, _, cut := n.params(); cut {
			_ = c.Close()
			continue
		}
		up, err := net.Dial("tcp", n.target)
		if err != nil {
			_ = c.Close()
			continue
		}
		n.mu.Lock()
		n.conns[c], n.conns[up] = struct{}{}, struct{}{}
		n.mu.Unlock()
		go n.pump(c, up)
		go n.pump(up, c)
	}
}

type chunk struct {
	data []byte
	at   time.Time
}

// pump copies src to dst, delaying each chunk by the one-way delay without serialising them: a
// reader stamps the release time and a writer waits for it, so throughput is not delay-bound.
func (n *netem) pump(src, dst net.Conn) {
	q := make(chan chunk, 4096)
	done := make(chan struct{})
	go func() {
		defer close(q)
		buf := make([]byte, 4096)
		for {
			k, err := src.Read(buf)
			if k > 0 {
				if n.kill() {
					_ = src.Close()
					_ = dst.Close()
					return
				}
				d, _, _ := n.params()
				q <- chunk{data: append([]byte(nil), buf[:k]...), at: time.Now().Add(d)}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer close(done)
		for c := range q {
			if w := time.Until(c.at); w > 0 {
				time.Sleep(w)
			}
			if _, err := dst.Write(c.data); err != nil {
				return
			}
		}
	}()
	<-done
	_ = src.Close()
	_ = dst.Close()
	n.mu.Lock()
	delete(n.conns, src)
	delete(n.conns, dst)
	n.mu.Unlock()
	_, _ = io.Copy(io.Discard, src)
}

// modelPlatform stores each idempotency key once, accepts what it stores, and sheds every
// shedEvery-th NEW key the first time it sees it (a tenant over its ceiling), telling the
// gateway so. A shed key that is offered again is accepted: the tenant is back under its ceiling.
type modelPlatform struct {
	client    mqtt.Client
	accepts   string
	notices   string
	shedEvery int
	// noDedup turns the store's idempotency key off, so a redelivered offer is stored again.
	// It exists only for the negative control that proves the exactly-once assertion can fail.
	noDedup bool
	// withholdEvery > 0 stores every Nth new event but does NOT send its acceptance the first
	// time, so the agent offers an event the store already holds. This is what makes a
	// duplicate store possible at all; without it only shed events are ever offered twice.
	withholdEvery int
	withheld      map[string]bool
	storeN        int

	mu        sync.Mutex
	stored    map[string]int // key -> times stored (must end at 1)
	offered   map[string]int // key -> times offered
	shedKeys  map[string]bool
	order     int
	suppress  int // duplicate offers the store suppressed
	shedCount int
}

func newModelPlatform(t *testing.T, cloudAddr string, shedEvery int) *modelPlatform {
	t.Helper()
	m := &modelPlatform{
		accepts:   messaging.SubjectToMqttTopic(messaging.GatewayAcceptsSubject("test", "tenant1", "gw1")),
		notices:   messaging.SubjectToMqttTopic(messaging.GatewayNoticesSubject("test", "tenant1", "gw1")),
		shedEvery: shedEvery,
		stored:    map[string]int{}, offered: map[string]int{}, shedKeys: map[string]bool{},
		withholdEvery: 5, withheld: map[string]bool{},
	}
	m.client = mqttConnect(t, cloudAddr, "model-platform")
	if err := messaging.SubscribeMqttConfirmed(m.client, "test/tenant1/gateways/gw1/devices/+/events", 1, m.onEvent, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	return m
}

func (m *modelPlatform) onEvent(_ mqtt.Client, msg mqtt.Message) {
	var ev struct {
		AltId string `json:"altId"`
	}
	if json.Unmarshal(msg.Payload(), &ev) != nil || ev.AltId == "" {
		return
	}
	m.mu.Lock()
	m.offered[ev.AltId]++
	if _, seen := m.stored[ev.AltId]; !seen && !m.shedKeys[ev.AltId] {
		m.order++
		if m.shedEvery > 0 && m.order%m.shedEvery == 0 {
			m.shedKeys[ev.AltId] = true
			m.shedCount++
			m.mu.Unlock()
			body, _ := json.Marshal(messaging.GatewayNotice{AltId: ev.AltId, Device: "d", Reason: messaging.GatewayNoticeReasonOverCeiling})
			m.client.Publish(m.notices, 0, false, body)
			return
		}
	}
	withhold := false
	if m.stored[ev.AltId] > 0 && !m.noDedup {
		m.suppress++ // the store's idempotency key suppresses it: still one stored copy
	} else {
		if m.stored[ev.AltId] == 0 {
			m.storeN++
			if m.withholdEvery > 0 && m.storeN%m.withholdEvery == 0 && !m.withheld[ev.AltId] {
				m.withheld[ev.AltId] = true
				withhold = true
			}
		}
		m.stored[ev.AltId]++ // a copy written; the dedup above is what keeps this at 1
	}
	m.mu.Unlock()
	if withhold {
		return
	}
	body, _ := json.Marshal(messaging.GatewayAccept{AltIds: []string{ev.AltId}})
	m.client.Publish(m.accepts, 0, false, body)
}

func (m *modelPlatform) storedCount() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.stored) }

func (m *modelPlatform) missing(n int) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for i := 0; i < n; i++ {
		if m.stored[fmt.Sprintf("ev-%03d", i)] == 0 {
			out = append(out, fmt.Sprintf("ev-%03d", i))
		}
	}
	return out
}

func eventBody(i int, occurred time.Time) []byte {
	return []byte(fmt.Sprintf(`{"altId":"ev-%03d","occurredTime":%q,"eventType":"measurement","values":{"n":%d}}`,
		i, occurred.UTC().Format(time.RFC3339), i))
}

// runNetemScenario drives the whole scenario against an agent retiring as retireOn says and
// returns the platform and the agent for the caller to judge.
func runNetemScenario(t *testing.T, retireOn string) (*modelPlatform, *Agent, int) {
	t.Helper()
	return runNetemScenarioWith(t, retireOn, false)
}

func runNetemScenarioWith(t *testing.T, retireOn string, noDedup bool) (*modelPlatform, *Agent, int) {
	t.Helper()
	cloudPort := startCloudBroker(t)
	cloudAddr := fmt.Sprintf("127.0.0.1:%d", cloudPort)
	proxy := newNetem(t, cloudAddr)
	platform := newModelPlatform(t, cloudAddr, 4)
	platform.noDedup = noDedup

	a := newAgent(t, config.Configuration{
		InstanceId: "test",
		Local:      config.LocalConfiguration{ListenHost: "127.0.0.1", ListenPort: freePort(t), StoreDir: dctest.JetStreamStoreDir(t)},
		Uplink: config.UplinkConfiguration{Tenant: "tenant1", GatewayToken: "gw1",
			BrokerURL: "tcp://" + proxy.addr(), ConnectTimeoutSeconds: 2, BackoffMinSeconds: 1, BackoffMaxSeconds: 2,
			RetireOn: retireOn, AcceptTimeoutSeconds: 4},
	}, func(a *Agent) { a.overCeilingRetry = 2 * time.Second })
	t.Cleanup(runReady(t, a))

	local := fmt.Sprintf("127.0.0.1:%d", a.cfg.Local.ListenPort)
	publish := func(from, to int, occurred time.Time) {
		for i := from; i < to; i++ {
			devicePublish(t, local, fmt.Sprintf("dev-%d", i), fmt.Sprintf("test/tenant1/devices/dev%d/events", i%5), eventBody(i, occurred))
		}
	}

	// 600 ms round trip, 30% of chunks kill the connection.
	proxy.set(300*time.Millisecond, 0.30)
	publish(0, 30, time.Now())
	// 90% loss: the link barely holds a session.
	proxy.set(300*time.Millisecond, 0.90)
	publish(30, 50, time.Now())
	// The cut: nothing connects and every session is severed (24 h compressed to seconds).
	proxy.setCut(true)
	publish(50, 70, time.Now())
	// A batch that happened three days ago and is only now delivered to the box.
	publish(70, 90, time.Now().Add(-72*time.Hour))
	time.Sleep(6 * time.Second)
	// The link heals, still at 600 ms.
	proxy.setCut(false)
	proxy.set(300*time.Millisecond, 0)
	return platform, a, 90
}

func TestNetemRigAcceptModeLosesNothingAndStoresNothingTwice(t *testing.T) {
	platform, a, n := runNetemScenario(t, config.RetireOnAccept)

	waitUntil(t, 120*time.Second, func() bool { return platform.storedCount() == n && a.spoolDepth() == 0 })

	platform.mu.Lock()
	defer platform.mu.Unlock()
	for k, c := range platform.stored {
		if c != 1 {
			t.Errorf("event %s stored %d times, want exactly 1", k, c)
		}
	}
	if platform.shedCount == 0 {
		t.Fatal("the platform never shed anything: the over-ceiling path was not exercised")
	}
	if platform.suppress == 0 {
		t.Fatal("the store never suppressed a duplicate offer: the exactly-once check was not exercised")
	}
	if a.overCeiling.Load() == 0 {
		t.Errorf("the agent counted no over-ceiling refusal although the platform shed %d", platform.shedCount)
	}
	t.Logf("n=%d shed=%d agent_over_ceiling=%d offers_suppressed_by_the_store=%d", n, platform.shedCount, a.overCeiling.Load(), platform.suppress)
}

// TestNetemRigExactlyOnceAssertionCanFail is the negative control of the test above: the same
// run against a store with its idempotency key switched off. If no event is then stored twice,
// the link never caused a redelivery and "stored exactly once" proved nothing; if one is, the
// assertion above is able to fail and its pass means the dedup held.
func TestNetemRigExactlyOnceAssertionCanFail(t *testing.T) {
	platform, a, n := runNetemScenarioWith(t, config.RetireOnAccept, true)

	waitUntil(t, 120*time.Second, func() bool { return platform.storedCount() == n && a.spoolDepth() == 0 })

	platform.mu.Lock()
	defer platform.mu.Unlock()
	twice := 0
	for _, c := range platform.stored {
		if c > 1 {
			twice++
		}
	}
	if twice == 0 {
		t.Fatalf("with the store's dedup off, no event was stored twice (offers=%v): the run never redelivered, so the exactly-once test cannot fail", platform.offered)
	}
	t.Logf("dedup off: %d of %d events stored more than once", twice, n)
}

// TestNetemRigPubackModeLosesTheShedEvents is the negative control: every release before the
// acceptance signal retired on PUBACK, and this is what the same link and the same platform do
// to it. The events the platform shed are gone from the spool and never reach the store.
func TestNetemRigPubackModeLosesTheShedEvents(t *testing.T) {
	platform, a, n := runNetemScenario(t, config.RetireOnPuback)

	waitUntil(t, 120*time.Second, func() bool { return a.spoolDepth() == 0 })
	time.Sleep(5 * time.Second)

	missing := platform.missing(n)
	if platform.shedCount == 0 {
		t.Fatal("the platform never shed anything: the control proves nothing")
	}
	if len(missing) == 0 {
		t.Fatalf("puback mode lost nothing in a run where the platform shed %d events; the rig cannot tell the modes apart", platform.shedCount)
	}
	t.Logf("puback mode lost %d of %d events (platform shed %d): %v", len(missing), n, platform.shedCount, missing)
}
