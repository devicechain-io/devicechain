// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	dctest "github.com/devicechain-io/dc-microservice/test"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/nats-io/jwt/v2"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/devicechain-io/dc-edge-agent/config"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/natsauth"
)

// THE AGENT AGAINST A REAL BROKER WITH THE AUTH CALLOUT CONFIGURED.
//
// The agent's other tests uplink to a broker with no authentication, which is the arrangement in
// which the failure this slice fixes cannot occur: with no grant to violate, a publish outside
// the gateway's namespace, a client id that is not the device's own and a refused subscription
// all succeed. Here the broker is configured the way the deployed one is (an APP account, the
// static service login, an auth_callout), a responder mints each device's grant with the
// platform's own natsauth.SignDeviceUserJWT from the credential it authenticated, and the agent
// connects through it as a gateway device.
//
// The platform's resolver is not in this module, so a small stand-in plays its part: it reads the
// capture stream the broker fills, accepts an event only for a child it has been told is
// assigned to the gateway, and otherwise sends the gateway the same notice the real resolver
// sends (messaging.GatewayNoticePublisher). What is under test here is the AGENT's half: that
// it connects, publishes only inside its grant, hears a refusal, and keeps the refused event.

const (
	calloutInstance = "inst-1"
	calloutTenant   = "acme"
	calloutGateway  = "gw-1"
)

// startCalloutCloud runs the broker and the responder and returns the MQTT port, a service
// connection, and the capture stream the stand-in platform reads.
func startCalloutCloud(t *testing.T) (mqttPort int, svc *nats.Conn) {
	t.Helper()
	creds, err := natsauth.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}

	dir := dctest.JetStreamStoreDir(t)
	conf := filepath.Join(dir, "nats.conf")
	body := fmt.Sprintf(`
		port: -1
		server_name: "edge-callout-e2e"
		jetstream { store_dir: %q }
		mqtt { host: "127.0.0.1", port: -1 }
		system_account: SYS
		accounts {
		  APP { jetstream: enabled, users: [{user: %q, password: %q}] }
		  SYS {}
		}
		authorization {
		  timeout: 5
		  auth_callout { issuer: %q, auth_users: [%q], account: "APP" }
		}
	`, filepath.Join(dir, "js"), natsauth.ServiceUser, creds.ServicePassword, creds.IssuerPublic, natsauth.ServiceUser)
	if err := os.WriteFile(conf, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	opts, err := natsserver.ProcessConfigFile(conf)
	if err != nil {
		t.Fatalf("process nats config: %v", err)
	}
	opts.NoLog, opts.NoSigs = true, true
	srv, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(20 * time.Second) {
		t.Fatal("embedded nats server not ready")
	}
	t.Cleanup(srv.Shutdown)

	svc, err = nats.Connect(srv.ClientURL(), nats.UserInfo(natsauth.ServiceUser, creds.ServicePassword))
	if err != nil {
		t.Fatalf("service connect: %v", err)
	}
	t.Cleanup(svc.Close)

	// The responder: username "{tenant}:{credentialId}", where the credential id is the device
	// token in this rig; the MQTT client id must be that device's own (what the real callout
	// enforces); the grant is the platform's own.
	sub, err := svc.Subscribe(natsauth.AuthCalloutRequestSubject, func(msg *nats.Msg) {
		rc, err := jwt.DecodeAuthorizationRequestClaims(string(msg.Data))
		if err != nil {
			return
		}
		req := rc.AuthorizationRequest
		tenant, token, _ := strings.Cut(req.ConnectOptions.Username, ":")
		var userJWT, errMsg string
		required, cerr := messaging.DeviceClientID(calloutInstance, tenant, token)
		switch {
		case cerr != nil:
			errMsg = "bad identity"
		case !messaging.DeviceClientIDMatches(req.ClientInformation.MQTT, required):
			errMsg = "client id is not the device's own"
		default:
			userJWT, err = natsauth.SignDeviceUserJWT(creds.IssuerSeed, req.UserNkey, calloutInstance, tenant, token, time.Now(), time.Hour)
			if err != nil {
				errMsg = err.Error()
			}
		}
		resp, err := natsauth.EncodeAuthResponse(creds.IssuerSeed, req.Server.ID, req.UserNkey, userJWT, errMsg)
		if err != nil {
			return
		}
		_ = msg.Respond([]byte(resp))
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := svc.Flush(); err != nil {
		t.Fatal(err)
	}
	return dctest.MQTTPort(t, srv), svc
}

// standInPlatform is the resolver's part, reduced to the one decision the agent's behaviour
// depends on. It records what it accepted and refused.
type standInPlatform struct {
	mu       sync.Mutex
	assigned map[string]bool
	accepted []platformEvent
	refused  []platformEvent
	subjects map[string]bool
}

type platformEvent struct {
	Gateway, Child string
	Body           map[string]json.RawMessage
}

func startStandInPlatform(t *testing.T, svc *nats.Conn, assigned ...string) *standInPlatform {
	t.Helper()
	p := &standInPlatform{assigned: map[string]bool{}, subjects: map[string]bool{}}
	for _, c := range assigned {
		p.assigned[c] = true
	}
	js, err := svc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{
		Name: "CLOUD_CAPTURE", Subjects: messaging.DeviceEventsSubjects(calloutInstance), Storage: nats.MemoryStorage,
	}); err != nil {
		t.Fatal(err)
	}
	notices := messaging.NewGatewayNoticePublisher(func() *nats.Conn { return svc }, calloutInstance)
	_, err = js.Subscribe(messaging.GatewayEventsWildcard(calloutInstance), func(m *nats.Msg) {
		gateway, child, ok := messaging.ParseDeviceEventsSubject(m.Subject)
		var body map[string]json.RawMessage
		_ = json.Unmarshal(m.Data, &body)
		var altId string
		_ = json.Unmarshal(body["altId"], &altId)

		p.mu.Lock()
		p.subjects[m.Subject] = true
		ev := platformEvent{Gateway: gateway, Child: child, Body: body}
		// Exactly the resolver's refusals relevant here: not assigned to this gateway, or a
		// credential in the body of a broker-asserted topic.
		_, hasCred := body["credentialSecret"]
		if !ok || !p.assigned[child] || hasCred {
			p.refused = append(p.refused, ev)
			p.mu.Unlock()
			if !p.assigned[child] {
				_ = notices.Notice(calloutTenant, gateway, messaging.GatewayNotice{
					AltId: altId, Device: child, Reason: messaging.GatewayNoticeReasonNotGatewayChild,
				})
			}
			return
		}
		p.accepted = append(p.accepted, ev)
		p.mu.Unlock()
	}, nats.AckNone())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (p *standInPlatform) counts() (accepted, refused int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.accepted), len(p.refused)
}

func (p *standInPlatform) subjectList() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for s := range p.subjects {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// calloutAgent starts an agent that connects to the callout broker as the gateway device.
func calloutAgent(t *testing.T, mqttPort int, brokerAddr string) *Agent {
	t.Helper()
	t.Setenv("DC_TEST_GATEWAY_SECRET", "not-checked-by-this-rig")
	a := newAgent(t, config.Configuration{
		InstanceId: calloutInstance,
		Local:      config.LocalConfiguration{ListenHost: "127.0.0.1", ListenPort: freePort(t), StoreDir: dctest.JetStreamStoreDir(t)},
		Uplink: config.UplinkConfiguration{
			BrokerURL: "tcp://" + brokerAddr, Tenant: calloutTenant, GatewayToken: calloutGateway,
			Username: calloutTenant + ":" + calloutGateway, PasswordEnv: "DC_TEST_GATEWAY_SECRET",
		},
	}, nil)
	t.Cleanup(runReady(t, a))
	waitUntil(t, 20*time.Second, a.uplink.Connected)
	return a
}

// The gateway connects as its own device, publishes its children inside its namespace, and a
// child the platform refuses is REPORTED and KEPT, not lost. The accepted child is the control:
// a rig in which nothing is accepted would "pass" every refusal assertion.
func TestAgentAsGatewayAgainstARealCalloutBroker(t *testing.T) {
	mqttPort, svc := startCalloutCloud(t)
	platform := startStandInPlatform(t, svc, "c1") // c1 is assigned to the gateway; c2 exists but is NOT
	a := calloutAgent(t, mqttPort, fmt.Sprintf("127.0.0.1:%d", mqttPort))

	local := fmt.Sprintf("127.0.0.1:%d", a.cfg.Local.ListenPort)
	dev := mqttConnect(t, local, "site-devices")
	pub := func(device, body string) {
		t.Helper()
		topic := fmt.Sprintf("%s/%s/devices/%s/events", calloutInstance, calloutTenant, device)
		if tok := dev.Publish(topic, 1, false, []byte(body)); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
			t.Fatalf("local publish: %v", tok.Error())
		}
	}
	// A device that leaks a credential in its body (the platform would refuse it), and one
	// that sets no event time (the agent stamps the time it was first stored).
	before := time.Now().UTC().Add(-time.Second)
	pub("c1", `{"altId":"c1-a","values":{"t":1},"credentialType":"MQTT_BASIC","credentialId":"x","credentialSecret":"hunter2"}`)
	pub("c1", `{"altId":"c1-b","values":{"t":2}}`)
	pub("c1", `{"altId":"c1-c","values":{"t":3}}`)
	pub("c2", `{"altId":"c2-a","values":{"t":4}}`)
	pub("c2", `{"altId":"c2-b","values":{"t":5}}`)

	waitUntil(t, 20*time.Second, func() bool {
		acc, ref := platform.counts()
		return acc == 3 && ref == 2
	})

	// The broker took only publishes inside the gateway's own namespace; nothing was a
	// permission violation (a violation is PUBACKed and dropped, so it would show here as a
	// missing subject).
	if got, want := platform.subjectList(), []string{
		fmt.Sprintf("%s.%s.gateways.%s.devices.c1.events", calloutInstance, calloutTenant, calloutGateway),
		fmt.Sprintf("%s.%s.gateways.%s.devices.c2.events", calloutInstance, calloutTenant, calloutGateway),
	}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("captured subjects = %v, want %v", got, want)
	}

	platform.mu.Lock()
	for _, ev := range platform.accepted {
		if ev.Gateway != calloutGateway || ev.Child != "c1" {
			t.Errorf("accepted event = %+v, want child c1 via %s", ev, calloutGateway)
		}
		if _, leaked := ev.Body["credentialSecret"]; leaked {
			t.Errorf("a body credential reached the platform: %+v", ev.Body)
		}
		var occurred string
		_ = json.Unmarshal(ev.Body["occurredTime"], &occurred)
		ts, err := time.Parse(time.RFC3339Nano, occurred)
		if err != nil || ts.Before(before) || ts.After(time.Now().Add(time.Second)) {
			t.Errorf("occurredTime = %q (%v): the stamp must be the time the event was first stored", occurred, err)
		}
	}
	platform.mu.Unlock()

	// The refusal reached the agent, was counted, and the refused events are kept locally.
	waitUntil(t, 15*time.Second, func() bool { return a.refused.Load() == 2 && a.refusedKept.Load() == 2 })
	recs := readRefused(t, filepath.Join(a.cfg.Local.StoreDir, refusedEventsFile))
	if len(recs) != 2 {
		t.Fatalf("dead-letter file holds %d records, want 2: %+v", len(recs), recs)
	}
	alts := []string{recs[0].AltId, recs[1].AltId}
	sort.Strings(alts)
	if fmt.Sprint(alts) != "[c2-a c2-b]" {
		t.Errorf("kept events = %v, want the two refused ones", alts)
	}
	for _, r := range recs {
		if r.Device != "c2" || r.Reason != messaging.GatewayNoticeReasonNotGatewayChild || len(r.Payload) == 0 {
			t.Errorf("record = %+v", r)
		}
	}
	// The spool itself drained: everything was PUBACKed, and the refusal is the cloud's verdict.
	waitUntil(t, 10*time.Second, func() bool { return a.spoolDepth() == 0 })
	_, body := httpGet(t, "http://"+a.metrics.addr+"/metrics")
	if !strings.Contains(body, `devicechain_edge_refused_total{reason="not_gateway_child"} 2`) {
		t.Errorf("metrics do not show the refusals:\n%s", grepLine(body, "refused"))
	}
}

// The callout admits only the gateway device's own plain client id; a second agent with the
// same identity takes the session over rather than running beside it. This pins that the agent
// presents the plain id (a per-agent suffix was the old behaviour, which this broker refuses).
func TestAgentConnectsWithTheGatewayDevicesPlainClientID(t *testing.T) {
	mqttPort, _ := startCalloutCloud(t)
	up := testCalloutUplink(t, mqttPort)

	// The control: the plain id connects (this is what the agent presents).
	if err := mqttConnectOnly(up.broker, up.clientID, up.username); err != nil {
		t.Fatalf("the plain client id was refused: %v", err)
	}
	// The old shape is refused by the same broker, so the success above is about the id.
	if err := mqttConnectOnly(up.broker, "dc-edge-agent-"+calloutInstance+"-site1", up.username); err == nil {
		t.Error("a per-agent client id was accepted; the callout is not enforcing the device's own id")
	}
}

func testCalloutUplink(t *testing.T, mqttPort int) *Uplink {
	t.Helper()
	t.Setenv("DC_TEST_GATEWAY_SECRET", "x")
	up, err := NewUplink(config.UplinkConfiguration{
		BrokerURL: fmt.Sprintf("tcp://127.0.0.1:%d", mqttPort), Tenant: calloutTenant, GatewayToken: calloutGateway,
		Username: calloutTenant + ":" + calloutGateway, PasswordEnv: "DC_TEST_GATEWAY_SECRET",
		ConnectTimeoutSeconds: 5, BackoffMinSeconds: 1, BackoffMaxSeconds: 1,
	}, calloutInstance, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	return up
}

func mqttConnectOnly(broker, clientID, username string) error {
	opts := mqtt.NewClientOptions().AddBroker(broker).SetClientID(clientID).SetUsername(username).
		SetConnectRetry(false).SetAutoReconnect(false).SetConnectTimeout(10 * time.Second)
	c := mqtt.NewClient(opts)
	tok := c.Connect()
	if !tok.WaitTimeout(15 * time.Second) {
		return fmt.Errorf("connect did not settle")
	}
	if err := tok.Error(); err != nil {
		return err
	}
	c.Disconnect(100)
	return nil
}

// A site's backlog drains at the window's rate, not the serial rate, through a real callout
// broker over a link with real latency: 400 events queue while the uplink is down, then the
// link comes up. A serial drain would need 400 x RTT; the window needs a small multiple of RTT.
// Everything arrives exactly once and keeps its original event time.
func TestBacklogDrainsFasterThanSerialThroughACalloutBroker(t *testing.T) {
	const events = 400
	const oneWay = 40 * time.Millisecond
	rtt := 2 * oneWay

	mqttPort, svc := startCalloutCloud(t)
	platform := startStandInPlatform(t, svc, "c1")
	proxy := newDelayProxy(t, fmt.Sprintf("127.0.0.1:%d", mqttPort), oneWay)

	// The agent starts with the link CUT, so the events accumulate in the spool.
	proxy.setUp(false)
	a := calloutAgent2(t, proxy.addr)
	dev := mqttConnect(t, fmt.Sprintf("127.0.0.1:%d", a.cfg.Local.ListenPort), "site-devices")
	var toks []mqtt.Token
	first := time.Now().UTC()
	for i := 0; i < events; i++ {
		body := fmt.Sprintf(`{"altId":"d-%d","values":{"i":%d}}`, i, i)
		toks = append(toks, dev.Publish(fmt.Sprintf("%s/%s/devices/c1/events", calloutInstance, calloutTenant), 1, false, []byte(body)))
	}
	for _, tk := range toks {
		tk.Wait()
	}
	waitUntil(t, 30*time.Second, func() bool { return a.spoolDepth() == events })
	time.Sleep(1500 * time.Millisecond) // the stored time must differ visibly from the forward time

	start := time.Now()
	proxy.setUp(true)
	waitUntil(t, 90*time.Second, func() bool { acc, _ := platform.counts(); return acc == events })
	elapsed := time.Since(start)

	if serial := time.Duration(events) * rtt; elapsed > serial/3 {
		t.Errorf("drained %d events in %s over a %s-RTT link; serial would take %s", events, elapsed, rtt, serial)
	}
	t.Logf("%d events drained in %s over a %s RTT link (serial: %s)", events, elapsed, rtt, time.Duration(events)*rtt)

	platform.mu.Lock()
	defer platform.mu.Unlock()
	seen := map[string]bool{}
	for _, ev := range platform.accepted {
		var altId, occurred string
		_ = json.Unmarshal(ev.Body["altId"], &altId)
		_ = json.Unmarshal(ev.Body["occurredTime"], &occurred)
		if seen[altId] {
			t.Errorf("%s arrived twice", altId)
		}
		seen[altId] = true
		ts, err := time.Parse(time.RFC3339Nano, occurred)
		// The ORIGINAL time: the moment the agent first stored it, well before the link came up.
		if err != nil || ts.After(start.UTC().Add(-time.Second)) || ts.Before(first.Add(-time.Second)) {
			t.Fatalf("%s occurredTime = %q (%v): a replay must keep the time the event was first stored, not the forward time", altId, occurred, err)
		}
	}
	if len(seen) != events {
		t.Errorf("distinct events = %d, want %d", len(seen), events)
	}
}

func calloutAgent2(t *testing.T, brokerAddr string) *Agent {
	t.Helper()
	t.Setenv("DC_TEST_GATEWAY_SECRET", "x")
	a := newAgent(t, config.Configuration{
		InstanceId: calloutInstance,
		Local:      config.LocalConfiguration{ListenHost: "127.0.0.1", ListenPort: freePort(t), StoreDir: dctest.JetStreamStoreDir(t)},
		Uplink: config.UplinkConfiguration{
			BrokerURL: "tcp://" + brokerAddr, Tenant: calloutTenant, GatewayToken: calloutGateway,
			Username: calloutTenant + ":" + calloutGateway, PasswordEnv: "DC_TEST_GATEWAY_SECRET",
			ConnectTimeoutSeconds: 5, BackoffMinSeconds: 1, BackoffMaxSeconds: 1,
		},
	}, nil)
	t.Cleanup(runReady(t, a))
	return a
}

// delayProxy forwards TCP to a target with a fixed one-way delay in each direction, and can be
// cut: while down it refuses (closes) every new connection and severs the open ones.
type delayProxy struct {
	addr   string
	target string
	delay  time.Duration
	l      net.Listener

	mu    sync.Mutex
	up    bool
	conns []net.Conn
}

func newDelayProxy(t *testing.T, target string, oneWay time.Duration) *delayProxy {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &delayProxy{addr: l.Addr().String(), target: target, delay: oneWay, l: l, up: true}
	t.Cleanup(func() { _ = l.Close(); p.sever() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			up := p.up
			p.mu.Unlock()
			if !up {
				_ = c.Close()
				continue
			}
			go p.serve(c)
		}
	}()
	return p
}

func (p *delayProxy) setUp(up bool) {
	p.mu.Lock()
	p.up = up
	p.mu.Unlock()
	if !up {
		p.sever()
	}
}

func (p *delayProxy) sever() {
	p.mu.Lock()
	conns := p.conns
	p.conns = nil
	p.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (p *delayProxy) serve(c net.Conn) {
	up, err := net.Dial("tcp", p.target)
	if err != nil {
		_ = c.Close()
		return
	}
	p.mu.Lock()
	p.conns = append(p.conns, c, up)
	p.mu.Unlock()
	go p.pipe(up, c)
	go p.pipe(c, up)
}

// pipe copies src to dst, releasing each chunk one delay after it was read.
func (p *delayProxy) pipe(dst, src net.Conn) {
	type chunk struct {
		b  []byte
		at time.Time
	}
	ch := make(chan chunk, 8192)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for c := range ch {
			time.Sleep(time.Until(c.at))
			if _, err := dst.Write(c.b); err != nil {
				for range ch { // drain so the reader never blocks
				}
				return
			}
		}
	}()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			ch <- chunk{append([]byte(nil), buf[:n]...), time.Now().Add(p.delay)}
		}
		if err != nil {
			if err != io.EOF {
				_ = err
			}
			break
		}
	}
	close(ch)
	<-done
	_ = dst.Close()
	_ = src.Close()
}
