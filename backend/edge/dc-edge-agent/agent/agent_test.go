// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"

	"github.com/devicechain-io/dc-edge-agent/config"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// freePort reserves an ephemeral port and releases it, returning the number. A brief
// TOCTOU window exists before the caller binds it; acceptable in tests, and the
// embedded MQTT gateway has no ephemeral-port accessor to use instead.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// newCloudBroker stands in for the cloud Instance's broker: an embedded nats-server
// with the MQTT gateway on mqttPort, so the agent's uplink can publish to it and a
// subscriber can assert receipt. It returns the server so a test can stop/restart it;
// it does NOT auto-register cleanup — the caller owns the lifecycle. storeDir is passed
// in so a restart on the same port reuses the store.
func newCloudBroker(t *testing.T, mqttPort int, storeDir string) *natsserver.Server {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		JetStream: true,
		StoreDir:  storeDir,
		MQTT:      natsserver.MQTTOpts{Host: "127.0.0.1", Port: mqttPort},
	})
	if err != nil {
		t.Fatalf("new cloud broker: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(15 * time.Second) {
		t.Fatal("cloud broker not ready")
	}
	return srv
}

// startCloudBroker is the auto-cleaned convenience wrapper for tests that only need a
// broker running for the whole test.
func startCloudBroker(t *testing.T, mqttPort int) {
	t.Helper()
	srv := newCloudBroker(t, mqttPort, t.TempDir())
	t.Cleanup(srv.Shutdown)
}

func mqttConnect(t *testing.T, addr, clientID string) mqtt.Client {
	t.Helper()
	opts := mqtt.NewClientOptions().
		AddBroker("tcp://" + addr).
		SetClientID(clientID).
		SetConnectTimeout(5 * time.Second)
	c := mqtt.NewClient(opts)
	tok := c.Connect()
	if !tok.WaitTimeout(10*time.Second) || tok.Error() != nil {
		t.Fatalf("mqtt connect %s: %v", addr, tok.Error())
	}
	t.Cleanup(func() { c.Disconnect(100) })
	return c
}

func devicePublish(t *testing.T, addr, clientID, topic string, payload []byte) {
	t.Helper()
	device := mqttConnect(t, addr, clientID)
	if tok := device.Publish(topic, 1, false, payload); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("device publish: %v", tok.Error())
	}
}

func waitUntil(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", d)
}

// newAgent builds an agent and applies optional configuration (e.g. a publishOverride)
// before it is started.
func newAgent(t *testing.T, cfg config.Configuration, configure func(*Agent)) *Agent {
	t.Helper()
	cfg.ApplyDefaults()
	a, err := New(cfg, testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if configure != nil {
		configure(a)
	}
	return a
}

// runReady starts a.Run and waits for readiness, returning a stop func that cancels Run
// and waits for it to return (draining the store cleanly before t.TempDir removal). It
// does NOT auto-register cleanup — tests that stop-and-restart own the lifecycle.
func runReady(t *testing.T, a *Agent) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- a.Run(ctx) }()
	select {
	case <-a.ready:
	case err := <-runErr:
		t.Fatalf("agent exited before ready: %v", err)
	case <-time.After(25 * time.Second):
		t.Fatal("agent not ready within 25s")
	}
	return func() {
		cancel()
		select {
		case <-runErr:
		case <-time.After(10 * time.Second):
			// A hung Run must fail the test, not return quietly: the restart test would
			// then open the SAME StoreDir from a second agent while this one is still
			// live, and nats-server takes no OS-level store lock → silent corruption
			// surfacing as an unrelated flake.
			t.Fatal("agent Run did not return within 10s of cancellation")
		}
	}
}

// startAgent runs an agent for the whole test (auto-cleaned) and returns it once ready.
func startAgent(t *testing.T, cfg config.Configuration) *Agent {
	t.Helper()
	a := newAgent(t, cfg, nil)
	t.Cleanup(runReady(t, a))
	return a
}

func cloudSubscribe(t *testing.T, cloudAddr, clientID string, sink chan<- mqtt.Message) {
	t.Helper()
	sub := mqttConnect(t, cloudAddr, clientID)
	if tok := sub.Subscribe("test/+/devices/+/events", 1, func(_ mqtt.Client, m mqtt.Message) {
		sink <- m
	}); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("cloud subscribe: %v", tok.Error())
	}
}

// TestBridgeForwardsDeviceTransparently: a device publishing golden MQTT to the agent's
// local listener has its event forwarded to the cloud on the SAME topic with the SAME
// bytes — the device-transparent bridge. Because this device supplies BOTH idempotency
// fields, E2's stamping is a no-op and the payload is byte-identical (which also pins
// that device-set values are never rewritten).
func TestBridgeForwardsDeviceTransparently(t *testing.T) {
	cloudPort := freePort(t)
	startCloudBroker(t, cloudPort)
	cloudAddr := fmt.Sprintf("127.0.0.1:%d", cloudPort)

	agentPort := freePort(t)
	a := startAgent(t, config.Configuration{
		InstanceId: "test",
		AgentId:    "site1",
		Local:      config.LocalConfiguration{ListenHost: "127.0.0.1", ListenPort: agentPort, StoreDir: t.TempDir()},
		Uplink:     config.UplinkConfiguration{BrokerURL: fmt.Sprintf("tcp://%s", cloudAddr)},
	})
	waitUntil(t, 15*time.Second, a.uplink.Connected)

	got := make(chan mqtt.Message, 1)
	cloudSubscribe(t, cloudAddr, "cloud-sub", got)

	topic := "test/tenant1/devices/dev1/events"
	payload := []byte(`{"altId":"dev-1","occurredTime":"2026-07-22T10:30:00Z","eventType":"measurement","values":{"temp":21.5}}`)
	devicePublish(t, fmt.Sprintf("127.0.0.1:%d", agentPort), "dev1", topic, payload)

	select {
	case m := <-got:
		if m.Topic() != topic {
			t.Errorf("forwarded topic = %q, want %q (device-transparency)", m.Topic(), topic)
		}
		if !bytes.Equal(m.Payload(), payload) {
			t.Errorf("forwarded payload = %q, want %q (byte-identical; device-set keys must not be rewritten)", m.Payload(), payload)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("cloud did not receive the forwarded event within 15s")
	}
	if a.forwarded.Load() == 0 {
		t.Error("forwarded counter is 0 after a successful forward")
	}
	// A correctly-addressed event must NOT be counted as an instance mismatch — this
	// keeps the mismatch counter honest (paired with TestInstanceMismatchIsCounted, it
	// pins that the predicate discriminates rather than counting everything).
	if a.mismatched.Load() != 0 {
		t.Errorf("mismatched = %d for a correctly-addressed event, want 0", a.mismatched.Load())
	}
}

// TestStampsIdempotencyWhenDeviceOmitsThem: a device that omits altId and occurredTime
// has both spliced in on the way to the cloud (the exactly-once carve-outs), while its
// own payload fields survive. altId is minted in the edge: namespace; occurredTime is a
// valid timestamp.
func TestStampsIdempotencyWhenDeviceOmitsThem(t *testing.T) {
	cloudPort := freePort(t)
	startCloudBroker(t, cloudPort)
	cloudAddr := fmt.Sprintf("127.0.0.1:%d", cloudPort)

	agentPort := freePort(t)
	a := startAgent(t, config.Configuration{
		InstanceId: "test",
		AgentId:    "site1",
		Local:      config.LocalConfiguration{ListenHost: "127.0.0.1", ListenPort: agentPort, StoreDir: t.TempDir()},
		Uplink:     config.UplinkConfiguration{BrokerURL: fmt.Sprintf("tcp://%s", cloudAddr)},
	})
	waitUntil(t, 15*time.Second, a.uplink.Connected)

	got := make(chan mqtt.Message, 1)
	cloudSubscribe(t, cloudAddr, "cloud-sub", got)

	devicePublish(t, fmt.Sprintf("127.0.0.1:%d", agentPort), "dev1",
		"test/tenant1/devices/dev1/events",
		[]byte(`{"eventType":"measurement","values":{"temp":7.5}}`))

	select {
	case m := <-got:
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(m.Payload(), &obj); err != nil {
			t.Fatalf("forwarded payload not JSON: %v (%s)", err, m.Payload())
		}
		var altId, occurred string
		_ = json.Unmarshal(obj["altId"], &altId)
		_ = json.Unmarshal(obj["occurredTime"], &occurred)
		if !strings.HasPrefix(altId, fmt.Sprintf("edge:%s:", a.installId)) {
			t.Errorf("minted altId = %q, want edge:%s:<epoch>:<seq>", altId, a.installId)
		}
		if _, err := time.Parse(time.RFC3339Nano, occurred); err != nil {
			t.Errorf("stamped occurredTime %q not a valid timestamp: %v", occurred, err)
		}
		if _, ok := obj["values"]; !ok {
			t.Error("device payload field 'values' was dropped by stamping")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("cloud did not receive the forwarded event within 15s")
	}
}

// TestForwardFailureLeavesEventBufferedWithStableKey is E2's check that matters. It
// pins BOTH load-bearing guards at once, deterministically:
//   - ack-after-PUBACK: a forward that FAILS is not acked, so the event is redelivered
//     (not lost). Mutating the ack to unconditional makes the event vanish → 0 deliveries.
//   - replay-stable key: the failed attempt and the redelivered attempt carry a
//     BYTE-IDENTICAL altId and occurredTime — the only thing that lets the cloud's
//     partial unique index fold the duplicate. Mutating the stamp to a per-forward
//     clock/counter makes the two keys differ.
//
// publishOverride fails the first attempt while the real uplink is genuinely connected
// (the mid-batch-drop case the Connected() pause cannot otherwise produce), then lets
// the redelivery through.
func TestForwardFailureLeavesEventBufferedWithStableKey(t *testing.T) {
	cloudPort := freePort(t)
	startCloudBroker(t, cloudPort) // real cloud up so the uplink is Connected() and the drain runs
	cloudAddr := fmt.Sprintf("127.0.0.1:%d", cloudPort)

	var mu sync.Mutex
	var attempts [][]byte
	failing := true

	agentPort := freePort(t)
	a := newAgent(t, config.Configuration{
		InstanceId: "test",
		AgentId:    "site1",
		Local:      config.LocalConfiguration{ListenHost: "127.0.0.1", ListenPort: agentPort, StoreDir: t.TempDir()},
		// Low connect timeout ⇒ AckWait = 2s, so the redelivery after the failed
		// attempt lands within the test's patience.
		Uplink: config.UplinkConfiguration{BrokerURL: fmt.Sprintf("tcp://%s", cloudAddr), ConnectTimeoutSeconds: 1},
	}, func(a *Agent) {
		a.publishOverride = func(topic string, payload []byte) error {
			mu.Lock()
			attempts = append(attempts, append([]byte(nil), payload...))
			f := failing
			mu.Unlock()
			if f {
				return fmt.Errorf("simulated cloud rejection")
			}
			return nil // "delivered"
		}
	})
	t.Cleanup(runReady(t, a))
	waitUntil(t, 15*time.Second, a.uplink.Connected)

	// A device that OMITS occurredTime, so minting is exercised (a device that stamped
	// its own would false-pass the stamp).
	devicePublish(t, fmt.Sprintf("127.0.0.1:%d", agentPort), "dev1",
		"test/tenant1/devices/dev1/events",
		[]byte(`{"eventType":"measurement","values":{"temp":1}}`))

	attemptCount := func() int { mu.Lock(); defer mu.Unlock(); return len(attempts) }
	// First attempt fails → event stays buffered.
	waitUntil(t, 10*time.Second, func() bool { return attemptCount() >= 1 })
	// Let the redelivery through.
	mu.Lock()
	failing = false
	mu.Unlock()
	// Redelivery after AckWait → a second attempt, which succeeds and is acked.
	waitUntil(t, 15*time.Second, func() bool { return attemptCount() >= 2 })

	// Give the loop a beat to confirm no THIRD attempt (a successful forward must be
	// acked and never redelivered).
	time.Sleep(3 * time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want exactly 2 (fail then redeliver-succeed); a 3rd means the successful forward was not acked", len(attempts))
	}
	first, second := attempts[0], attempts[1]

	altId := func(b []byte) string {
		var o map[string]json.RawMessage
		if err := json.Unmarshal(b, &o); err != nil {
			t.Fatalf("attempt not JSON: %v", err)
		}
		var s string
		_ = json.Unmarshal(o["altId"], &s)
		return s
	}
	occurred := func(b []byte) string {
		var o map[string]json.RawMessage
		_ = json.Unmarshal(b, &o)
		var s string
		_ = json.Unmarshal(o["occurredTime"], &s)
		return s
	}
	if altId(first) == "" || !strings.HasPrefix(altId(first), "edge:") {
		t.Errorf("first attempt altId = %q, want a minted edge: key", altId(first))
	}
	if altId(first) != altId(second) {
		t.Errorf("altId differs across redelivery: %q vs %q (key not replay-stable)", altId(first), altId(second))
	}
	if occurred(first) == "" || occurred(first) != occurred(second) {
		t.Errorf("occurredTime differs across redelivery: %q vs %q (stamp not replay-stable)", occurred(first), occurred(second))
	}
}

// TestBuffersAcrossOutageAndRestart is the WAN-outage store-and-forward drive: sever the
// uplink, publish while it is down, RESTART the agent during the outage, then restore
// the cloud — the buffered event must survive both the outage and the restart and drain
// exactly once. A never-severed run would not exercise the buffer at all.
func TestBuffersAcrossOutageAndRestart(t *testing.T) {
	cloudPort := freePort(t)
	cloudStore := t.TempDir()
	srv := newCloudBroker(t, cloudPort, cloudStore)
	cloudAddr := fmt.Sprintf("127.0.0.1:%d", cloudPort)
	cloudURL := fmt.Sprintf("tcp://%s", cloudAddr)

	storeDir := t.TempDir() // shared across the restart — this is where the spool lives

	// Agent boot #1.
	a1 := newAgent(t, config.Configuration{
		InstanceId: "test",
		AgentId:    "site1",
		Local:      config.LocalConfiguration{ListenHost: "127.0.0.1", ListenPort: freePort(t), StoreDir: storeDir},
		Uplink:     config.UplinkConfiguration{BrokerURL: cloudURL},
	}, nil)
	stop1 := runReady(t, a1)
	waitUntil(t, 15*time.Second, a1.uplink.Connected)
	agent1Addr := fmt.Sprintf("127.0.0.1:%d", a1.cfg.Local.ListenPort)

	// Sever the cloud.
	srv.Shutdown()
	waitUntil(t, 15*time.Second, func() bool { return !a1.uplink.Connected() })

	// A device publishes while the uplink is down (omitting occurredTime). The LOCAL
	// gateway PUBACKs it durably; the drain is paused, so it must be buffered, not lost.
	devicePublish(t, agent1Addr, "dev-outage",
		"test/tenant1/devices/dev1/events",
		[]byte(`{"eventType":"measurement","values":{"t":42}}`))
	waitUntil(t, 10*time.Second, func() bool { return a1.spoolDepth() >= 1 })
	if a1.forwarded.Load() != 0 {
		t.Fatalf("forwarded = %d during an outage, want 0 (nothing should reach the cloud while it is down)", a1.forwarded.Load())
	}

	// Restart the agent DURING the outage: stop #1, boot #2 on the SAME store.
	stop1()
	a2 := newAgent(t, config.Configuration{
		InstanceId: "test",
		AgentId:    "site1",
		Local:      config.LocalConfiguration{ListenHost: "127.0.0.1", ListenPort: freePort(t), StoreDir: storeDir},
		Uplink:     config.UplinkConfiguration{BrokerURL: cloudURL},
	}, nil)
	t.Cleanup(runReady(t, a2))
	// The buffered event survived the restart (cloud still down, so still spooled).
	waitUntil(t, 10*time.Second, func() bool { return a2.spoolDepth() >= 1 })

	// Restore the cloud on the same port+store, subscribe, and expect the drain.
	srv2 := newCloudBroker(t, cloudPort, cloudStore)
	t.Cleanup(srv2.Shutdown)
	got := make(chan mqtt.Message, 1)
	cloudSubscribe(t, cloudAddr, "cloud-sub-restore", got)
	waitUntil(t, 20*time.Second, a2.uplink.Connected)

	select {
	case m := <-got:
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(m.Payload(), &obj); err != nil {
			t.Fatalf("drained payload not JSON: %v", err)
		}
		var altId string
		_ = json.Unmarshal(obj["altId"], &altId)
		if !strings.HasPrefix(altId, "edge:") {
			t.Errorf("drained event altId = %q, want a minted edge: key", altId)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("buffered event never drained to the restored cloud (lost across outage+restart)")
	}
}

// TestMintedKeyIsStableAcrossRestart pins the MAJOR-4 guard's testable half: the minted
// altId (edge:<installId>:<streamEpoch>:<seq>) must be BYTE-IDENTICAL for the same
// buffered event before and after an agent restart — i.e. installId, streamEpoch and the
// stream sequence are all RECOVERED from the persisted store, not regenerated per boot.
// (The single-process failure test can't catch a per-boot epoch; this can.)
//
// Boot #1 fails the forward (event stays buffered) and captures its minted key; boot #2
// on the same store redelivers it, succeeds, and captures the key again. They must match.
func TestMintedKeyIsStableAcrossRestart(t *testing.T) {
	cloudPort := freePort(t)
	startCloudBroker(t, cloudPort) // up throughout so both agents' uplinks are Connected()
	cloudURL := fmt.Sprintf("tcp://127.0.0.1:%d", cloudPort)
	storeDir := t.TempDir() // shared across the restart

	var mu sync.Mutex
	var keys []string
	fail := true // boot #1 fails (buffer the event); flipped before boot #2
	capture := func(_ string, payload []byte) error {
		var o map[string]json.RawMessage
		_ = json.Unmarshal(payload, &o)
		var altId string
		_ = json.Unmarshal(o["altId"], &altId)
		mu.Lock()
		keys = append(keys, altId)
		f := fail
		mu.Unlock()
		if f {
			return fmt.Errorf("simulated failure to keep the event buffered")
		}
		return nil
	}
	keyCount := func() int { mu.Lock(); defer mu.Unlock(); return len(keys) }

	// Boot #1: a device omits occurredTime (so minting is exercised); the forward fails.
	a1 := newAgent(t, config.Configuration{
		InstanceId: "test",
		AgentId:    "site1",
		Local:      config.LocalConfiguration{ListenHost: "127.0.0.1", ListenPort: freePort(t), StoreDir: storeDir},
		Uplink:     config.UplinkConfiguration{BrokerURL: cloudURL, ConnectTimeoutSeconds: 1},
	}, func(a *Agent) { a.publishOverride = capture })
	stop1 := runReady(t, a1)
	waitUntil(t, 15*time.Second, a1.uplink.Connected)
	devicePublish(t, fmt.Sprintf("127.0.0.1:%d", a1.cfg.Local.ListenPort), "dev1",
		"test/tenant1/devices/dev1/events", []byte(`{"eventType":"measurement","values":{"t":1}}`))
	waitUntil(t, 10*time.Second, func() bool { return keyCount() >= 1 })
	mu.Lock()
	keyBefore := keys[0]
	mu.Unlock()
	stop1() // restart

	// Boot #2 on the SAME store: let the redelivery through.
	mu.Lock()
	fail = false
	mu.Unlock()
	a2 := newAgent(t, config.Configuration{
		InstanceId: "test",
		AgentId:    "site1",
		Local:      config.LocalConfiguration{ListenHost: "127.0.0.1", ListenPort: freePort(t), StoreDir: storeDir},
		Uplink:     config.UplinkConfiguration{BrokerURL: cloudURL, ConnectTimeoutSeconds: 1},
	}, func(a *Agent) { a.publishOverride = capture })
	t.Cleanup(runReady(t, a2))
	waitUntil(t, 20*time.Second, func() bool { return keyCount() >= 2 })

	mu.Lock()
	keyAfter := keys[len(keys)-1]
	mu.Unlock()
	if keyBefore == "" || !strings.HasPrefix(keyBefore, "edge:") {
		t.Fatalf("boot #1 minted key = %q, want an edge: key", keyBefore)
	}
	if keyBefore != keyAfter {
		t.Errorf("minted altId changed across restart: %q -> %q (streamEpoch/seq not recovered from the persisted stream)", keyBefore, keyAfter)
	}
}

// TestClientPortUnboundMqttOnly pins F5: DontListen keeps the plain NATS client port
// UNBOUND while the MQTT listener is the only exposed surface. Mutation check: flipping
// DontListen to false in newServer makes srv.Addr() non-nil and fails this test.
func TestClientPortUnboundMqttOnly(t *testing.T) {
	agentPort := freePort(t)
	cfg := config.Configuration{
		InstanceId: "test",
		AgentId:    "site1",
		Local:      config.LocalConfiguration{ListenHost: "127.0.0.1", ListenPort: agentPort, StoreDir: t.TempDir()},
		Uplink:     config.UplinkConfiguration{BrokerURL: "tcp://127.0.0.1:1"},
	}
	cfg.ApplyDefaults()
	a, err := New(cfg, testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv, err := a.newServer(true)
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	t.Cleanup(srv.Shutdown)

	// The plain NATS client port must NOT be bound.
	if addr := srv.Addr(); addr != nil {
		t.Errorf("client port is bound at %v; DontListen should leave it unbound (F5)", addr)
	}
	// The MQTT listener MUST be reachable (it is the intended device surface).
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", agentPort), 3*time.Second)
	if err != nil {
		t.Fatalf("MQTT listener not reachable on :%d: %v", agentPort, err)
	}
	_ = conn.Close()
}

// TestInstanceMismatchIsCounted pins F9: a device publishing on a DIFFERENT instanceId
// than the agent forwards is not silently dropped without trace — the mismatch is
// counted so a config typo is visible.
func TestInstanceMismatchIsCounted(t *testing.T) {
	cloudPort := freePort(t)
	startCloudBroker(t, cloudPort)

	agentPort := freePort(t)
	a := startAgent(t, config.Configuration{
		InstanceId: "test",
		AgentId:    "site1",
		Local:      config.LocalConfiguration{ListenHost: "127.0.0.1", ListenPort: agentPort, StoreDir: t.TempDir()},
		Uplink:     config.UplinkConfiguration{BrokerURL: fmt.Sprintf("tcp://127.0.0.1:%d", cloudPort)},
	})

	// Publish on instanceId "wrong", not "test".
	devicePublish(t, fmt.Sprintf("127.0.0.1:%d", agentPort), "dev-wrong",
		"wrong/tenant1/devices/dev1/events", []byte(`{}`))
	waitUntil(t, 10*time.Second, func() bool { return a.mismatched.Load() > 0 })
}

// TestUplinkReconnectsAndResumesForwarding exercises the reconnect loop: the uplink
// notices the cloud broker drop, reconnects when it returns, and forwards again.
// Without this, a mutation to clearClient, the waitUntilDisconnected return, or the
// backoff reset passes the whole suite. The device supplies both idempotency fields so
// the post-reconnect payload is byte-identical.
func TestUplinkReconnectsAndResumesForwarding(t *testing.T) {
	cloudPort := freePort(t)
	cloudStore := t.TempDir()
	srv := newCloudBroker(t, cloudPort, cloudStore)
	cloudAddr := fmt.Sprintf("127.0.0.1:%d", cloudPort)

	agentPort := freePort(t)
	a := startAgent(t, config.Configuration{
		InstanceId: "test",
		AgentId:    "site1",
		Local:      config.LocalConfiguration{ListenHost: "127.0.0.1", ListenPort: agentPort, StoreDir: t.TempDir()},
		Uplink:     config.UplinkConfiguration{BrokerURL: fmt.Sprintf("tcp://%s", cloudAddr)},
	})
	waitUntil(t, 15*time.Second, a.uplink.Connected)

	// Cloud broker drops.
	srv.Shutdown()
	waitUntil(t, 15*time.Second, func() bool { return !a.uplink.Connected() })

	// Cloud broker returns on the same port; the uplink must reconnect on its own.
	srv2 := newCloudBroker(t, cloudPort, cloudStore)
	t.Cleanup(srv2.Shutdown)
	waitUntil(t, 20*time.Second, a.uplink.Connected)

	got := make(chan mqtt.Message, 1)
	cloudSubscribe(t, cloudAddr, "cloud-sub-2", got)
	payload := []byte(`{"altId":"dev-rc","occurredTime":"2026-07-22T10:30:00Z","eventType":"measurement","values":{"temp":9.0}}`)
	devicePublish(t, fmt.Sprintf("127.0.0.1:%d", agentPort), "dev-reconnect",
		"test/tenant1/devices/dev1/events", payload)
	select {
	case m := <-got:
		if !bytes.Equal(m.Payload(), payload) {
			t.Errorf("post-reconnect payload = %q, want %q", m.Payload(), payload)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("event was not forwarded after the uplink reconnected")
	}
}
