// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dctest "github.com/devicechain-io/dc-microservice/test"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/devicechain-io/dc-edge-agent/config"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// gatewayUplinkConfig is the uplink config the gateway tests share: tenant1, gateway gw1.
func gatewayUplinkConfig(cloudAddr string) config.UplinkConfiguration {
	return config.UplinkConfiguration{Tenant: "tenant1", GatewayToken: "gw1", BrokerURL: "tcp://" + cloudAddr}
}

// cloudSubscribeBytes is cloudSubscribe delivering raw payloads.
func cloudSubscribeBytes(t *testing.T, cloudAddr string, sink chan<- []byte) {
	t.Helper()
	sub := mqttConnect(t, cloudAddr, "cloud-sub-bytes")
	if err := messaging.SubscribeMqttConfirmed(sub, "test/+/gateways/+/devices/+/events", 1, func(_ mqtt.Client, m mqtt.Message) {
		sink <- append([]byte(nil), m.Payload()...)
	}, 5*time.Second); err != nil {
		t.Fatalf("cloud subscribe: %v", err)
	}
}

func testUplink(t *testing.T) *Uplink {
	t.Helper()
	cfg := config.Configuration{InstanceId: "test", Uplink: config.UplinkConfiguration{
		Tenant: "tenant1", GatewayToken: "gw1", BrokerURL: "tcp://127.0.0.1:1883",
		ConnectTimeoutSeconds: 1, BackoffMinSeconds: 1, BackoffMaxSeconds: 1,
	}}
	up, err := NewUplink(cfg.Uplink, cfg.InstanceId, testLogger())
	if err != nil {
		t.Fatalf("NewUplink: %v", err)
	}
	return up
}

// The client id is the gateway DEVICE's plain client id and carries no per-agent
// discriminator: the broker's callout admits only the authenticating device's own id.
func TestUplinkClientIDIsTheGatewayDevicesPlainID(t *testing.T) {
	up := testUplink(t)
	want, err := messaging.DeviceClientID("test", "tenant1", "gw1")
	if err != nil {
		t.Fatal(err)
	}
	if up.clientID != want {
		t.Errorf("client id = %q, want the plain device client id %q", up.clientID, want)
	}
	if !messaging.DeviceClientIDMatches(up.clientID, want) {
		t.Errorf("the callout would refuse %q", up.clientID)
	}
}

// EventTopic is the only way to build an uplink topic. A child token yields a topic inside the
// gateway's own namespace; anything that could escape it, and the gateway's own token, is
// refused. The control row is the first: a constructor that refused everything would satisfy
// every refusal below.
func TestEventTopicBuildsOnlyInsideTheGatewaysNamespace(t *testing.T) {
	up := testUplink(t)

	got, err := up.EventTopic("dev1")
	if err != nil {
		t.Fatalf("a plain child token must build: %v", err)
	}
	if want := "test/tenant1/gateways/gw1/devices/dev1/events"; got != want {
		t.Errorf("topic = %q, want %q", got, want)
	}

	for _, child := range []string{
		"", "a.b", "a/b", "+", "#", "*", ">", "dev 1", "dev:1", "../gw2/devices/x",
		"gw2/devices/x", "dev1/../x", "-dev", strings.Repeat("x", 129),
	} {
		topic, err := up.EventTopic(child)
		if err == nil {
			t.Errorf("EventTopic(%q) = %q, want an error: it could address outside the gateway's namespace", child, topic)
		}
	}

	if _, err := up.EventTopic("gw1"); err != ErrGatewayAddressedItself {
		t.Errorf("EventTopic(own token) error = %v, want ErrGatewayAddressedItself", err)
	}
}

// Every topic the constructor accepts is inside the namespace: a property over a spread of
// valid tokens, so a constructor that formatted one shape and another by accident would fail.
func TestEventTopicNeverLeavesTheNamespace(t *testing.T) {
	up := testUplink(t)
	prefix := "test/tenant1/gateways/gw1/devices/"
	for _, child := range []string{"a", "A1", "dev-1", "dev_1", "0", "x9-_Z", strings.Repeat("y", 128)} {
		topic, err := up.EventTopic(child)
		if err != nil {
			t.Fatalf("EventTopic(%q): %v", child, err)
		}
		if !strings.HasPrefix(topic, prefix) || !strings.HasSuffix(topic, "/events") ||
			strings.Count(topic, "/") != strings.Count(prefix, "/")+1 {
			t.Errorf("EventTopic(%q) = %q is not {prefix}{child}/events", child, topic)
		}
	}
}

func TestPrepareStripsBodyCredentials(t *testing.T) {
	at := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	in := []byte(`{"altId":"a-1","occurredTime":"2026-07-22T10:00:00Z","credentialType":"MQTT_BASIC",` +
		`"CredentialId":"c1","credentialsecret":"hunter2","values":{"t":1}}`)

	out, altId, stripped, err := prepareUplinkPayload(in, "iid", "42", 3, at, true)
	if err != nil {
		t.Fatal(err)
	}
	if stripped != 3 {
		t.Errorf("stripped = %d, want 3 (the match is case-insensitive, like the cloud decoder's)", stripped)
	}
	if altId != "a-1" {
		t.Errorf("altId = %q, want the device's own a-1", altId)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatal(err)
	}
	for k := range obj {
		if strings.HasPrefix(strings.ToLower(k), "credential") {
			t.Errorf("forwarded body still carries %q", k)
		}
	}
	if !bytes.Contains(out, []byte(`"values":{"t":1}`)) || string(obj["occurredTime"]) != `"2026-07-22T10:00:00Z"` {
		t.Errorf("the rest of the body must pass through untouched, got %s", out)
	}
	if bytes.Contains(out, []byte("hunter2")) {
		t.Error("the secret survived")
	}

	// The control: with strip off the same body is forwarded as it came (stampIdempotency's
	// own contract), so the assertions above are about the strip and not about re-marshalling.
	same, _, n, err := prepareUplinkPayload(in, "iid", "42", 3, at, false)
	if err != nil || n != 0 || !bytes.Equal(same, in) {
		t.Errorf("strip=false must forward untouched: n=%d err=%v out=%s", n, err, same)
	}

	// A body without a credential reports nothing stripped and mints what is missing.
	clean, altId, n, err := prepareUplinkPayload([]byte(`{"values":{"t":1}}`), "iid", "42", 9, at, true)
	if err != nil || n != 0 || !strings.HasPrefix(altId, "edge:iid:42:9") || !bytes.Contains(clean, []byte(altId)) {
		t.Errorf("clean body: n=%d altId=%q err=%v out=%s", n, altId, err, clean)
	}

	// A non-object payload is forwarded verbatim and nothing is claimed.
	raw := []byte{0xde, 0xad, 0xbe, 0xef}
	same, altId, n, err = prepareUplinkPayload(raw, "iid", "42", 1, at, true)
	if err != nil || n != 0 || altId != "" || !bytes.Equal(same, raw) {
		t.Errorf("non-JSON payload: n=%d altId=%q err=%v", n, altId, err)
	}
}

func TestRecentSentIsBounded(t *testing.T) {
	r := newRecentSent()
	for i := 0; i < recentSentMaxEntries+100; i++ {
		r.put(&sentEvent{altId: fmt.Sprintf("a-%d", i), payload: []byte("x")})
	}
	if _, ok := r.take("a-0"); ok {
		t.Error("the oldest entry should have been evicted past the entry bound")
	}
	if _, ok := r.take(fmt.Sprintf("a-%d", recentSentMaxEntries+99)); !ok {
		t.Error("the newest entry must be held")
	}
	if _, ok := r.take(fmt.Sprintf("a-%d", recentSentMaxEntries+99)); ok {
		t.Error("take must remove the entry")
	}

	// The byte bound holds independently of the entry count.
	b := newRecentSent()
	big := make([]byte, recentSentMaxBytes/4+1)
	for i := 0; i < 5; i++ {
		b.put(&sentEvent{altId: fmt.Sprintf("b-%d", i), payload: big})
	}
	if _, ok := b.take("b-0"); ok {
		t.Error("the oldest entry should have been evicted past the byte bound")
	}
	if _, ok := b.take("b-4"); !ok {
		t.Error("the newest entry must be held")
	}

	// A redelivery (same altId) replaces rather than duplicates.
	c := newRecentSent()
	c.put(&sentEvent{altId: "x", payload: []byte("1")})
	c.put(&sentEvent{altId: "x", payload: []byte("22")})
	if c.bytes != 2 || c.order.Len() != 1 {
		t.Errorf("a repeated altId must replace: bytes=%d entries=%d", c.bytes, c.order.Len())
	}
}

func TestDeadLettersRotateAndStayPrivate(t *testing.T) {
	dir := t.TempDir()
	d := newDeadLetters(dir)
	d.max = 600
	ev := &sentEvent{altId: "a", child: "c1", topic: "t", payload: []byte(`{"v":1}`)}
	for i := 0; i < 20; i++ {
		if err := d.write(newRefusedRecord("not_gateway_child", ev)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{refusedEventsFile, refusedEventsFile + ".1"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s mode %v: the dead-letter file holds event payloads and must be private", name, fi.Mode().Perm())
		}
		if fi.Size() > 600 {
			t.Errorf("%s is %d bytes, over the bound", name, fi.Size())
		}
	}
	// A non-JSON payload is kept exactly, base64.
	if err := d.write(newRefusedRecord("gateway_self", &sentEvent{child: "gw1", payload: []byte{0xff, 0x00, 0x01}})); err != nil {
		t.Fatal(err)
	}
	recs := readRefused(t, filepath.Join(dir, refusedEventsFile))
	last := recs[len(recs)-1]
	if len(last.PayloadBase64) != 3 || last.PayloadBase64[0] != 0xff || last.Payload != nil {
		t.Errorf("binary payload not preserved: %+v", last)
	}
}

func readRefused(t *testing.T, path string) []refusedRecord {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var out []refusedRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var r refusedRecord
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("dead-letter line does not decode: %v: %s", err, sc.Text())
		}
		out = append(out, r)
	}
	return out
}

// A notice for an event the agent still holds writes that event to the dead-letter file and
// counts it; a notice for one it no longer holds is counted as unmatched and logged, and
// writes nothing. Reasons the agent does not know are folded into one label.
func TestNoticeKeepsTheRefusedEventWhileItIsHeld(t *testing.T) {
	storeDir := dctest.JetStreamStoreDir(t)
	var logs syncBuffer
	a := newAgentLog(t, config.Configuration{
		InstanceId: "test",
		Local:      config.LocalConfiguration{StoreDir: storeDir},
		Uplink:     config.UplinkConfiguration{Tenant: "tenant1", GatewayToken: "gw1", BrokerURL: "tcp://127.0.0.1:1"},
	}, &logs)

	payload := []byte(`{"altId":"edge:i:e:7","occurredTime":"2026-07-22T10:00:00Z","values":{"t":1}}`)
	a.sent.put(&sentEvent{altId: "edge:i:e:7", child: "c2", topic: "test/tenant1/gateways/gw1/devices/c2/events", payload: payload})

	a.handleNotice(messaging.GatewayNotice{AltId: "edge:i:e:7", Device: "c2", Reason: messaging.GatewayNoticeReasonNotGatewayChild})
	if a.refused.Load() != 1 || a.refusedKept.Load() != 1 || a.refusedUnmatched.Load() != 0 {
		t.Fatalf("held notice: refused=%d kept=%d unmatched=%d, want 1/1/0", a.refused.Load(), a.refusedKept.Load(), a.refusedUnmatched.Load())
	}
	recs := readRefused(t, filepath.Join(storeDir, refusedEventsFile))
	if len(recs) != 1 || recs[0].Device != "c2" || recs[0].Reason != messaging.GatewayNoticeReasonNotGatewayChild ||
		recs[0].AltId != "edge:i:e:7" || !bytes.Equal(recs[0].Payload, payload) {
		t.Fatalf("dead-letter record = %+v", recs)
	}
	if !strings.Contains(logs.String(), "device=c2") || !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("the refusal must be a WARN naming the child, got:\n%s", logs.String())
	}

	// The same notice again: the event is gone, so it is unmatched, and nothing more is written.
	a.handleNotice(messaging.GatewayNotice{AltId: "edge:i:e:7", Device: "c2", Reason: messaging.GatewayNoticeReasonNotGatewayChild})
	a.handleNotice(messaging.GatewayNotice{Device: "c3", Reason: "a-reason-from-the-future"})
	if a.refused.Load() != 3 || a.refusedKept.Load() != 1 || a.refusedUnmatched.Load() != 2 {
		t.Errorf("after unmatched: refused=%d kept=%d unmatched=%d, want 3/1/2", a.refused.Load(), a.refusedKept.Load(), a.refusedUnmatched.Load())
	}
	if n := len(readRefused(t, filepath.Join(storeDir, refusedEventsFile))); n != 1 {
		t.Errorf("unmatched notices wrote %d dead-letter lines, want still 1", n)
	}
	if normaliseRefusalReason("a-reason-from-the-future") != refusalOther {
		t.Error("an unknown reason must fold into one label")
	}
}

// A local device publishing under the gateway's OWN token, and one whose token the platform's
// grammar cannot hold, are not forwarded and not dropped silently: each is counted as refused
// and kept in the dead-letter file, and the spool message is acked (it would otherwise block
// the head forever). A tenant the gateway does not speak for is counted and never spooled.
// The control is the ordinary child in the same run, which is forwarded.
func TestAgentRefusesWhatTheCloudCouldOnlyRefuse(t *testing.T) {
	cloudPort := startCloudBroker(t)
	cloudAddr := fmt.Sprintf("127.0.0.1:%d", cloudPort)
	storeDir := dctest.JetStreamStoreDir(t)
	agentPort := freePort(t)
	a := startAgent(t, config.Configuration{
		InstanceId: "test",
		Local:      config.LocalConfiguration{ListenHost: "127.0.0.1", ListenPort: agentPort, StoreDir: storeDir},
		Uplink:     gatewayUplinkConfig(cloudAddr),
	})
	waitUntil(t, 15*time.Second, a.uplink.Connected)

	got := make(chan string, 8)
	sub := mqttConnect(t, cloudAddr, "cloud-sub")
	if err := messaging.SubscribeMqttConfirmed(sub, "test/#", 1, func(_ mqtt.Client, m mqtt.Message) {
		got <- m.Topic()
	}, 5*time.Second); err != nil {
		t.Fatal(err)
	}

	local := fmt.Sprintf("127.0.0.1:%d", agentPort)
	dev := mqttConnect(t, local, "dev-all")
	pub := func(topic, body string) {
		t.Helper()
		if tok := dev.Publish(topic, 1, false, []byte(body)); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
			t.Fatalf("publish %s: %v", topic, tok.Error())
		}
	}
	pub("test/tenant1/devices/gw1/events", `{"altId":"self-1","values":{"t":1}}`)  // the gateway's own token
	pub("test/tenant1/devices/dev:1/events", `{"altId":"bad-1","values":{"t":1}}`) // a token the grammar cannot hold
	pub("test/other/devices/dev1/events", `{"altId":"other-1","values":{"t":1}}`)  // another tenant
	pub("test/tenant1/devices/dev1/events", `{"altId":"ok-1","values":{"t":1}}`)   // the control

	select {
	case topic := <-got:
		if want := "test/tenant1/gateways/gw1/devices/dev1/events"; topic != want {
			t.Fatalf("the cloud saw %q, want only the control child's %q", topic, want)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the control child was never forwarded")
	}
	waitUntil(t, 10*time.Second, func() bool { return a.refusedKept.Load() == 2 && a.tenantMismatched.Load() == 1 })
	waitUntil(t, 10*time.Second, func() bool { return a.spoolDepth() == 0 })

	select {
	case topic := <-got:
		t.Fatalf("the cloud saw a second event on %q: a refused event was forwarded", topic)
	case <-time.After(1500 * time.Millisecond):
	}
	if a.refused.Load() != 2 {
		t.Errorf("refused = %d, want 2 (gateway_self, invalid_device_token)", a.refused.Load())
	}
	recs := readRefused(t, filepath.Join(storeDir, refusedEventsFile))
	byAlt := map[string]refusedRecord{}
	for _, r := range recs {
		byAlt[r.AltId] = r
	}
	if r := byAlt["self-1"]; r.Reason != refusalGatewaySelf || r.Device != "gw1" || len(r.Payload) == 0 {
		t.Errorf("gateway-self record = %+v", r)
	}
	if r := byAlt["bad-1"]; r.Reason != refusalInvalidToken || r.Device != "dev:1" {
		t.Errorf("invalid-token record = %+v", r)
	}
	if _, ok := byAlt["other-1"]; ok {
		t.Error("another tenant's event must be counted and never stored")
	}

	// The metric carries the reasons.
	_, body := httpGet(t, "http://"+a.metrics.addr+"/metrics")
	for _, want := range []string{
		`devicechain_edge_refused_total{reason="gateway_self"} 1`,
		`devicechain_edge_refused_total{reason="invalid_device_token"} 1`,
		`devicechain_edge_refused_total{reason="not_gateway_child"} 0`,
		`devicechain_edge_tenant_mismatched_total 1`,
		`devicechain_edge_refused_kept_total 2`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

// A body credential is removed before the event leaves the box, counted, and the rest of the
// body is intact. (The platform refuses a body credential on the gateway namespace.)
func TestAgentStripsBodyCredentialsBeforeForwarding(t *testing.T) {
	cloudPort := startCloudBroker(t)
	cloudAddr := fmt.Sprintf("127.0.0.1:%d", cloudPort)
	agentPort := freePort(t)
	a := startAgent(t, config.Configuration{
		InstanceId: "test",
		Local:      config.LocalConfiguration{ListenHost: "127.0.0.1", ListenPort: agentPort, StoreDir: dctest.JetStreamStoreDir(t)},
		Uplink:     gatewayUplinkConfig(cloudAddr),
	})
	waitUntil(t, 15*time.Second, a.uplink.Connected)
	got := make(chan []byte, 1)
	cloudSubscribeBytes(t, cloudAddr, got)

	devicePublish(t, fmt.Sprintf("127.0.0.1:%d", agentPort), "dev1", "test/tenant1/devices/dev1/events",
		[]byte(`{"altId":"cred-1","occurredTime":"2026-07-22T10:00:00Z","credentialType":"MQTT_BASIC","credentialId":"c","credentialSecret":"hunter2","values":{"t":3}}`))

	select {
	case p := <-got:
		if bytes.Contains(p, []byte("hunter2")) || bytes.Contains(bytes.ToLower(p), []byte("credential")) {
			t.Fatalf("a credential left the box: %s", p)
		}
		if !bytes.Contains(p, []byte(`"values":{"t":3}`)) {
			t.Errorf("the rest of the body was damaged: %s", p)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the event was never forwarded")
	}
	waitUntil(t, 5*time.Second, func() bool { return a.bodyStripped.Load() == 3 })
}

// The drain keeps up to 64 publishes in flight and acks each on its own PUBACK. With a broker
// that takes 50 ms to answer, a serial drain does 20 events/s (640 events take 32 s); the
// window must finish in a small fraction of that, never exceed 64 in flight, and lose or
// duplicate nothing.
func TestDrainKeepsAWindowInFlightAndNeverMoreThan64(t *testing.T) {
	const events = 640
	const rtt = 50 * time.Millisecond

	cloudPort := startCloudBroker(t) // only so the uplink is Connected(); the override stands in for the publish
	var live, peak atomic.Int64
	// open holds every publish at the gate until the spool is full, so the timed phase below is
	// the drain alone and not the cost of fsyncing 640 local publishes on a loaded machine.
	var open atomic.Bool
	var mu sync.Mutex
	seen := map[string]int{}
	agentPort := freePort(t)
	uplink := gatewayUplinkConfig(fmt.Sprintf("127.0.0.1:%d", cloudPort))
	uplink.ConnectTimeoutSeconds = 120 // AckWait is twice this: the gate must not look like a lost ack
	a := newAgent(t, config.Configuration{
		InstanceId: "test",
		Local:      config.LocalConfiguration{ListenHost: "127.0.0.1", ListenPort: agentPort, StoreDir: dctest.JetStreamStoreDir(t)},
		Uplink:     uplink,
	}, func(a *Agent) {
		a.publishOverride = func(topic string, payload []byte) error {
			for !open.Load() {
				time.Sleep(5 * time.Millisecond)
			}
			n := live.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(rtt) // the PUBACK round trip
			live.Add(-1)
			var o struct {
				AltId string `json:"altId"`
			}
			_ = json.Unmarshal(payload, &o)
			mu.Lock()
			seen[o.AltId]++
			mu.Unlock()
			return nil
		}
	})
	t.Cleanup(runReady(t, a))
	waitUntil(t, 15*time.Second, a.uplink.Connected)

	dev := mqttConnect(t, fmt.Sprintf("127.0.0.1:%d", agentPort), "dev-window")
	q := &localQueue{t: t, dev: dev}
	for i := 0; i < events; i++ {
		q.publish("test/tenant1/devices/dev1/events", fmt.Sprintf(`{"altId":"w-%d","occurredTime":"2026-07-22T10:00:00Z","values":{"i":%d}}`, i, i))
	}
	q.flush()
	// The spool holds everything (the first window of it is parked at the gate): release it.
	waitUntil(t, 60*time.Second, func() bool { return a.spoolDepth() == events })
	start := time.Now()
	open.Store(true)
	waitUntil(t, 60*time.Second, func() bool { return a.forwarded.Load() == events })
	elapsed := time.Since(start)

	serial := events * rtt
	if elapsed > serial/3 {
		t.Errorf("drained %d events in %s; a serial drain takes %s and the window must be far faster", events, elapsed, serial)
	}
	if peak.Load() < 8 {
		t.Errorf("peak in flight = %d: the drain is not windowed", peak.Load())
	}
	// 64 as a literal, not uplinkWindow: the window is a documented number (the README, the
	// metric's help text), so a change to the constant must fail here rather than be agreed with.
	if peak.Load() > 64 {
		t.Errorf("peak in flight = %d, over the documented window of 64", peak.Load())
	}
	waitUntil(t, 10*time.Second, func() bool { return a.spoolDepth() == 0 })
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != events {
		t.Errorf("distinct events forwarded = %d, want %d", len(seen), events)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("%s forwarded %d times, want exactly once", id, n)
		}
	}
	t.Logf("%d events in %s (peak in flight %d; serial would be %s)", events, elapsed, peak.Load(), serial)
}

// A refused notices SUBSCRIBE fails the uplink connect, loudly. A gateway that cannot hear its
// refusals has its silent loss back, so it must not look connected.
func TestUplinkNotConnectedWhenNoticesSubscriptionIsRefused(t *testing.T) {
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dctest.JetStreamStoreDir(t),
		MQTT: natsserver.MQTTOpts{Host: "127.0.0.1", Port: -1},
		Users: []*natsserver.User{{
			Username: "tenant1:cred", Password: "pw",
			Permissions: &natsserver.Permissions{
				Publish:   &natsserver.SubjectPermission{Allow: []string{">"}},
				Subscribe: &natsserver.SubjectPermission{Deny: []string{">"}},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(15 * time.Second) {
		t.Fatal("broker not ready")
	}
	t.Cleanup(srv.Shutdown)
	t.Setenv("DC_TEST_UPLINK_PW", "pw")

	var logs syncBuffer
	up := gatewayUplinkConfig(fmt.Sprintf("127.0.0.1:%d", dctest.MQTTPort(t, srv)))
	up.Username, up.PasswordEnv = "tenant1:cred", "DC_TEST_UPLINK_PW"
	a := newAgentLog(t, config.Configuration{
		InstanceId: "test",
		Local:      config.LocalConfiguration{ListenHost: "127.0.0.1", ListenPort: freePort(t), StoreDir: dctest.JetStreamStoreDir(t)},
		Uplink:     up,
	}, &logs)
	t.Cleanup(runReady(t, a))

	waitUntil(t, 20*time.Second, func() bool { return strings.Contains(logs.String(), "subscribe to gateway notices") })
	if a.uplink.Connected() {
		t.Error("the uplink reports connected without a notices subscription")
	}
}

// AN UPGRADED BOX. A store written by the build before the gateway uplink captured every
// tenant of the instance ({instance}.*.devices.*.events) on both the stream and the durable
// forwarder. The new build narrows both to the configured tenant on its first boot over that
// store, and the backlog the old build spooled is forwarded in the gateway's namespace rather
// than stranded: the durable keeps its delivery bookkeeping, so nothing is lost and nothing
// already forwarded is sent again. Another tenant's spooled event is not forwarded (the cloud
// could only refuse it). The control is a fresh store, which is the first boot below.
func TestUpgradedStoreWithTheWideFilterDrainsItsBacklog(t *testing.T) {
	storeDir := dctest.JetStreamStoreDir(t)
	cloudPort := startCloudBroker(t)
	cloudAddr := fmt.Sprintf("127.0.0.1:%d", cloudPort)
	cfg := config.Configuration{
		InstanceId: "test",
		Local:      config.LocalConfiguration{ListenHost: "127.0.0.1", ListenPort: freePort(t), StoreDir: storeDir},
		Uplink:     gatewayUplinkConfig(cloudAddr),
	}

	// Boot once so the store carries this install's identity files, then stop.
	first := newAgent(t, cfg, nil)
	stop := runReady(t, first)
	waitUntil(t, 15*time.Second, first.uplink.Connected)
	stop()

	// Rewrite the store into the OLD build's shape: the wide subject on the stream and the
	// durable, and a backlog for this tenant and another one.
	const wide = "test.*.devices.*.events"
	old := newAgent(t, cfg, nil)
	srv, err := old.newServer(false)
	if err != nil {
		t.Fatal(err)
	}
	nc, err := nats.Connect("", nats.InProcessServer(srv))
	if err != nil {
		t.Fatal(err)
	}
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	si, err := js.StreamInfo(captureStream)
	if err != nil {
		t.Fatal(err)
	}
	scfg := si.Config
	scfg.Subjects = []string{wide}
	if _, err := js.UpdateStream(&scfg); err != nil {
		t.Fatalf("widen stream: %v", err)
	}
	ci, err := js.ConsumerInfo(captureStream, forwarderDurable)
	if err != nil {
		t.Fatal(err)
	}
	ccfg := ci.Config
	ccfg.FilterSubject = wide
	if _, err := js.UpdateConsumer(captureStream, &ccfg); err != nil {
		t.Fatalf("widen durable: %v", err)
	}
	for _, s := range []string{"test.tenant1.devices.dev1.events", "test.tenant2.devices.dev9.events"} {
		if _, err := js.Publish(s, []byte(`{"altId":"`+s+`","occurredTime":"2026-07-22T10:00:00Z","values":{"v":1}}`)); err != nil {
			t.Fatal(err)
		}
	}
	nc.Close()
	srv.Shutdown()
	srv.WaitForShutdown()

	got := make(chan mqtt.Message, 4)
	cloudSubscribe(t, cloudAddr, "cloud-sub-upgrade", got)

	upgraded := newAgent(t, cfg, nil)
	t.Cleanup(runReady(t, upgraded))

	select {
	case m := <-got:
		if want := "test/tenant1/gateways/gw1/devices/dev1/events"; m.Topic() != want {
			t.Errorf("backlog forwarded on %q, want %q", m.Topic(), want)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the old build's backlog was never forwarded after the upgrade")
	}
	select {
	case m := <-got:
		t.Errorf("forwarded a second event %q on %q: another tenant's backlog must not leave the box", m.Payload(), m.Topic())
	case <-time.After(2 * time.Second):
	}

	ci, err = upgraded.js.ConsumerInfo(captureStream, forwarderDurable)
	if err != nil {
		t.Fatal(err)
	}
	if ci.Config.FilterSubject != upgraded.captureSubject() {
		t.Errorf("durable filter = %q, want the narrowed %q", ci.Config.FilterSubject, upgraded.captureSubject())
	}
}
