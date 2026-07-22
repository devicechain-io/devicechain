// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	natsserver "github.com/nats-io/nats-server/v2/server"

	"github.com/devicechain-io/dc-edge-agent/config"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// freePort reserves an ephemeral port and releases it, returning the number. A
// brief TOCTOU window exists before the caller binds it; acceptable in tests, and
// the embedded MQTT gateway has no ephemeral-port accessor to use instead.
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
// subscriber can assert receipt. It returns the server so a test can stop/restart
// it (the reconnect test); it does NOT auto-register cleanup — the caller owns the
// lifecycle. storeDir is passed in so a restart on the same port reuses the store.
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

// startCloudBroker is the auto-cleaned convenience wrapper for tests that only need
// a broker running for the whole test.
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

// startAgent runs an agent with the given config and returns it once its substrate
// is ready. It fails the test if the agent exits before becoming ready.
func startAgent(t *testing.T, cfg config.Configuration) *Agent {
	t.Helper()
	cfg.ApplyDefaults()
	a, err := New(cfg, testLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- a.Run(ctx) }()
	// Cleanup cancels Run AND waits for it to return, so the embedded JetStream
	// store is fully shut down before t.TempDir is removed (avoids a flush race).
	t.Cleanup(func() {
		cancel()
		select {
		case <-runErr:
		case <-time.After(10 * time.Second):
		}
	})
	select {
	case <-a.ready:
	case err := <-runErr:
		t.Fatalf("agent exited before ready: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("agent not ready within 20s")
	}
	return a
}

// TestBridgeForwardsDeviceTransparently is E1's check that matters: a device
// publishing golden MQTT to the agent's local listener has its event forwarded to
// the cloud on the SAME topic with the SAME bytes — the device-transparent bridge.
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

	// Cloud subscriber must be established (SUBACK) before the publish — the golden
	// path is not retained, so a late subscriber would miss the message.
	got := make(chan mqtt.Message, 1)
	cloudSub := mqttConnect(t, cloudAddr, "cloud-sub")
	if tok := cloudSub.Subscribe("test/+/devices/+/events", 1, func(_ mqtt.Client, m mqtt.Message) {
		got <- m
	}); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("cloud subscribe: %v", tok.Error())
	}

	device := mqttConnect(t, fmt.Sprintf("127.0.0.1:%d", agentPort), "dev1")
	topic := "test/tenant1/devices/dev1/events"
	payload := []byte(`{"eventType":"measurement","values":{"temp":21.5}}`)
	if tok := device.Publish(topic, 1, false, payload); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("device publish: %v", tok.Error())
	}

	select {
	case m := <-got:
		if m.Topic() != topic {
			t.Errorf("forwarded topic = %q, want %q (device-transparency)", m.Topic(), topic)
		}
		if !bytes.Equal(m.Payload(), payload) {
			t.Errorf("forwarded payload = %q, want %q (byte-identical)", m.Payload(), payload)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("cloud did not receive the forwarded event within 15s")
	}
	if a.forwarded.Load() == 0 {
		t.Error("forwarded counter is 0 after a successful forward")
	}
	// A correctly-addressed event must NOT be counted as an instance mismatch —
	// this keeps the mismatch counter honest (paired with TestInstanceMismatchIsCounted,
	// it pins that the predicate discriminates rather than counting everything).
	if a.mismatched.Load() != 0 {
		t.Errorf("mismatched = %d for a correctly-addressed event, want 0", a.mismatched.Load())
	}
}

// TestClientPortUnboundMqttOnly pins F5: DontListen keeps the plain NATS client
// port UNBOUND while the MQTT listener is the only exposed surface. Mutation check:
// flipping DontListen to false in startServer makes srv.Addr() non-nil and fails
// this test.
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
	if err := a.startServer(); err != nil {
		t.Fatalf("startServer: %v", err)
	}
	t.Cleanup(a.srv.Shutdown)

	// The plain NATS client port must NOT be bound.
	if addr := a.srv.Addr(); addr != nil {
		t.Errorf("client port is bound at %v; DontListen should leave it unbound (F5)", addr)
	}
	// The MQTT listener MUST be reachable (it is the intended device surface).
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", agentPort), 3*time.Second)
	if err != nil {
		t.Fatalf("MQTT listener not reachable on :%d: %v", agentPort, err)
	}
	_ = conn.Close()
}

// TestInstanceMismatchIsCounted pins F9: a device publishing on a DIFFERENT
// instanceId than the agent forwards is not silently dropped without trace — the
// mismatch is counted so a config typo is visible.
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

	device := mqttConnect(t, fmt.Sprintf("127.0.0.1:%d", agentPort), "dev-wrong")
	// Publish on instanceId "wrong", not "test".
	if tok := device.Publish("wrong/tenant1/devices/dev1/events", 1, false, []byte(`{}`)); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("device publish: %v", tok.Error())
	}
	waitUntil(t, 10*time.Second, func() bool { return a.mismatched.Load() > 0 })
}

// TestUplinkReconnectsAndResumesForwarding exercises the reconnect half of E1
// ("proves the uplink"): the uplink notices the cloud broker drop, reconnects when
// it returns, and forwards again. Without this, a mutation to clearClient, the
// waitUntilDisconnected return, or the backoff reset passes the whole suite.
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

	// A device event published after the outage must forward to the restored broker.
	got := make(chan mqtt.Message, 1)
	cloudSub := mqttConnect(t, cloudAddr, "cloud-sub-2")
	if tok := cloudSub.Subscribe("test/+/devices/+/events", 1, func(_ mqtt.Client, m mqtt.Message) {
		got <- m
	}); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("cloud subscribe: %v", tok.Error())
	}
	device := mqttConnect(t, fmt.Sprintf("127.0.0.1:%d", agentPort), "dev-reconnect")
	payload := []byte(`{"eventType":"measurement","values":{"temp":9.0}}`)
	if tok := device.Publish("test/tenant1/devices/dev1/events", 1, false, payload); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Fatalf("device publish: %v", tok.Error())
	}
	select {
	case m := <-got:
		if !bytes.Equal(m.Payload(), payload) {
			t.Errorf("post-reconnect payload = %q, want %q", m.Payload(), payload)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("event was not forwarded after the uplink reconnected")
	}
}
