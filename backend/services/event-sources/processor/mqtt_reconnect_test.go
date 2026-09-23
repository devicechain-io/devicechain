// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/messaging"
	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/eclipse/paho.mqtt.golang/packets"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failRecorder stands in for the process-ending hook, so what the source asks for can be
// read back instead of ending the test binary.
type failRecorder struct{ errs chan error }

func newFailRecorder() *failRecorder { return &failRecorder{errs: make(chan error, 8)} }

func (f *failRecorder) fail(err error) { f.errs <- err }

// requireNone asserts the source never asked to end the process.
func (f *failRecorder) requireNone(t *testing.T) {
	t.Helper()
	select {
	case err := <-f.errs:
		t.Fatalf("the source asked to end the process: %v", err)
	default:
	}
}

// newExternalMqttSource builds the external-broker source through its real constructor.
func newExternalMqttSource(t *testing.T, host string, port int, topic string, received func(string, []byte),
	fail func(error)) *MqttEventSource {
	t.Helper()
	if received == nil {
		received = func(string, []byte) {}
	}
	es, err := NewMqttEventSource("ext", map[string]string{
		"host": host, "port": strconv.Itoa(port), "topic": topic,
	}, nil, "", "", NewJsonDecoder(map[string]string{}, 0),
		received,
		func(string, string, *model.UnresolvedEvent, interface{}, uint64) error { return nil },
		func(string, string, []byte, error) error { return nil },
		nil, fail)
	require.NoError(t, err)
	return es
}

// startWithin starts the source under a deadline of its own, so a start that hangs fails
// this test rather than stalling the package until the module timeout.
func startWithin(t *testing.T, es *MqttEventSource, d time.Duration) error {
	t.Helper()
	require.NoError(t, es.Initialize(context.Background()))
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return es.Start(ctx)
}

// stopWithin stops the source, bounded.
func stopWithin(t *testing.T, es *MqttEventSource, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = es.Stop(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Errorf("the source did not stop within %v", d)
	}
}

// 🔴 THE DEFECT THIS PINS. The external-broker source subscribed once, in Start. paho
// reconnects on its own after a broker restart, with a clean session, so the broker held
// no subscription for the new connection: the source was connected, reported nothing
// wrong, and ingested nothing until the pod restarted.
//
// It runs the real paho source against a real broker (an embedded nats-server with its
// MQTT listener), restarts that broker on the same ports and store, waits for paho to
// reconnect, and then requires a device event to arrive again.
func TestExternalMqttSourceResubscribesAfterBrokerRestart(t *testing.T) {
	natsPort := dctest.FreeTCPPort(t)
	mqttPort := dctest.FreeTCPPort(t)
	storeDir := t.TempDir()
	start := func() *natsserver.Server {
		srv, err := natsserver.NewServer(&natsserver.Options{
			Host: "127.0.0.1", Port: natsPort, JetStream: true, StoreDir: storeDir,
			ServerName: "reconnect", MQTT: natsserver.MQTTOpts{Host: "127.0.0.1", Port: mqttPort},
		})
		require.NoError(t, err)
		go srv.Start()
		require.True(t, srv.ReadyForConnections(15*time.Second), "the broker did not come up")
		return srv
	}
	srv := start()
	t.Cleanup(func() { srv.Shutdown() })

	var received atomic.Int64
	fails := newFailRecorder()
	es := newExternalMqttSource(t, "127.0.0.1", mqttPort, GatewayTopic(testInstance),
		func(string, []byte) { received.Add(1) }, fails.fail)
	require.NoError(t, startWithin(t, es, 45*time.Second))
	t.Cleanup(func() { stopWithin(t, es, 10*time.Second) })

	subject := messaging.DeviceEventsSubject(testInstance, "acme", "d1")
	publishUntil := func(s *natsserver.Server, d time.Duration) bool {
		nc, err := nats.Connect(s.ClientURL())
		require.NoError(t, err)
		defer nc.Close()
		base := received.Load()
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) {
			require.NoError(t, nc.Publish(subject, []byte(`{"device":"d1"}`)))
			_ = nc.Flush()
			time.Sleep(250 * time.Millisecond)
			if received.Load() > base {
				return true
			}
		}
		return false
	}

	require.True(t, publishUntil(srv, 10*time.Second), "baseline: nothing was received before the restart")

	srv.Shutdown()
	srv.WaitForShutdown()
	srv = start()

	deadline := time.Now().Add(30 * time.Second)
	for !es.Client.IsConnectionOpen() && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	require.True(t, es.Client.IsConnectionOpen(), "paho did not reconnect after the broker restart")

	require.True(t, publishUntil(srv, 15*time.Second),
		"the source reconnected and reports connected, but ingests nothing: it did not re-subscribe")
	fails.requireNone(t)
}

// subackResponse is how the scripted broker answers a connection's SUBSCRIBE.
type subackResponse int

const (
	grantSubscribe   subackResponse = iota // SUBACK granting QoS 1
	refuseSubscribe                        // SUBACK 0x80
	closeOnSubscribe                       // drop the connection instead of answering
)

// scriptedBroker is a broker whose answer to SUBSCRIBE is chosen PER CONNECTION, so a
// test can decide what the first connection gets and what each reconnect gets. It speaks
// just enough MQTT 3.1.1, with paho's own packet codec, for paho to treat it as a broker —
// the refusal has to arrive over the wire, because paho's handling of it is the thing
// under test (see core/messaging/mqtt_subscribe_test.go for the same reasoning).
//
// Connections past the end of the script are granted.
type scriptedBroker struct {
	t      *testing.T
	script []subackResponse
	port   int

	mu    sync.Mutex
	conns []net.Conn
	// subscribes receives the (1-based) number of each connection that sent SUBSCRIBE.
	subscribes chan int
}

func startScriptedBroker(t *testing.T, script ...subackResponse) *scriptedBroker {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	b := &scriptedBroker{t: t, script: script, port: ln.Addr().(*net.TCPAddr).Port, subscribes: make(chan int, 32)}
	t.Cleanup(func() {
		_ = ln.Close()
		b.mu.Lock()
		defer b.mu.Unlock()
		for _, c := range b.conns {
			_ = c.Close()
		}
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			b.mu.Lock()
			b.conns = append(b.conns, conn)
			n := len(b.conns)
			b.mu.Unlock()
			go b.serve(conn, n)
		}
	}()
	return b
}

func (b *scriptedBroker) response(n int) subackResponse {
	if n <= len(b.script) {
		return b.script[n-1]
	}
	return grantSubscribe
}

func (b *scriptedBroker) serve(conn net.Conn, n int) {
	defer func() { _ = conn.Close() }()
	for {
		pkt, err := packets.ReadPacket(conn)
		if err != nil {
			return
		}
		switch p := pkt.(type) {
		case *packets.ConnectPacket:
			ack := packets.NewControlPacket(packets.Connack).(*packets.ConnackPacket)
			ack.ReturnCode = packets.Accepted
			if ack.Write(conn) != nil {
				return
			}
		case *packets.SubscribePacket:
			b.subscribes <- n
			resp := b.response(n)
			if resp == closeOnSubscribe {
				return
			}
			code := byte(1)
			if resp == refuseSubscribe {
				code = 0x80
			}
			ack := packets.NewControlPacket(packets.Suback).(*packets.SubackPacket)
			ack.MessageID = p.MessageID
			for range p.Topics {
				ack.ReturnCodes = append(ack.ReturnCodes, code)
			}
			if ack.Write(conn) != nil {
				return
			}
		case *packets.UnsubscribePacket:
			ack := packets.NewControlPacket(packets.Unsuback).(*packets.UnsubackPacket)
			ack.MessageID = p.MessageID
			if ack.Write(conn) != nil {
				return
			}
		case *packets.PingreqPacket:
			if packets.NewControlPacket(packets.Pingresp).Write(conn) != nil {
				return
			}
		case *packets.DisconnectPacket:
			return
		}
	}
}

// drop closes connection n from the broker's side, as a broker restart would.
func (b *scriptedBroker) drop(n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	require.GreaterOrEqual(b.t, len(b.conns), n, "connection %d does not exist", n)
	_ = b.conns[n-1].Close()
}

// connections is how many connections the broker has accepted.
func (b *scriptedBroker) connections() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.conns)
}

// awaitSubscribe waits for connection n to send SUBSCRIBE, and fails the test if it does
// not within d.
func (b *scriptedBroker) awaitSubscribe(t *testing.T, n int, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case got := <-b.subscribes:
			if got == n {
				return
			}
		case <-deadline:
			t.Fatalf("connection %d never sent SUBSCRIBE within %v", n, d)
		}
	}
}

// TestAReconnectRefusedSubscriptionFailsTheProcess: a broker that grants the first
// subscription and REFUSES it after a reconnect would leave the source connected and
// ingesting nothing, the same state the startup check exists to refuse. It must end the
// process, and say why.
func TestAReconnectRefusedSubscriptionFailsTheProcess(t *testing.T) {
	b := startScriptedBroker(t, grantSubscribe, refuseSubscribe)
	fails := newFailRecorder()
	es := newExternalMqttSource(t, "127.0.0.1", b.port, "acme/#", nil, fails.fail)
	require.NoError(t, startWithin(t, es, 20*time.Second))
	t.Cleanup(func() { stopWithin(t, es, 10*time.Second) })

	b.drop(1)

	select {
	case err := <-fails.errs:
		assert.ErrorIs(t, err, messaging.ErrSubscriptionRefused,
			"the process was ended for something other than the refusal: %v", err)
		assert.Contains(t, err.Error(), `"ext"`, "the error does not name the source")
		assert.Contains(t, err.Error(), "acme/#", "the error does not name the refused topic")
		assert.Contains(t, err.Error(), "re-subscribe", "the error does not say it happened on a reconnect")
	case <-time.After(20 * time.Second):
		t.Fatal("a reconnect whose subscription the broker refused did not end the process")
	}
}

// TestAConnectionDroppedMidResubscribeRetriesInsteadOfFailing is the counterweight. A
// connection that goes away while its SUBSCRIBE is outstanding is not the broker saying
// no: paho reconnects straight away, and the next connection subscribes for itself. That
// must not end the process — with a flapping broker, or replicas that share a client id
// and evict each other, it would otherwise end it on every drop.
func TestAConnectionDroppedMidResubscribeRetriesInsteadOfFailing(t *testing.T) {
	b := startScriptedBroker(t, grantSubscribe, closeOnSubscribe, grantSubscribe)
	fails := newFailRecorder()
	es := newExternalMqttSource(t, "127.0.0.1", b.port, "acme/#", nil, fails.fail)
	require.NoError(t, startWithin(t, es, 20*time.Second))
	t.Cleanup(func() { stopWithin(t, es, 10*time.Second) })

	b.awaitSubscribe(t, 1, 5*time.Second)
	b.drop(1)
	b.awaitSubscribe(t, 2, 20*time.Second)
	b.awaitSubscribe(t, 3, 20*time.Second)

	// Give a wrongly-classified connection-2 failure time to be reported: its goroutine
	// wakes when paho fails its token, which is before connection 3 is even dialled.
	time.Sleep(500 * time.Millisecond)
	fails.requireNone(t)
	assert.True(t, es.Client.IsConnectionOpen(), "the source is not connected after the third connection")
}

// TestAFirstRefusedSubscriptionStillFailsStart pins the behaviour kept from before: a
// refusal on the FIRST connection fails the start rather than being handed to the
// process-ending hook. And the failed start must not leave an auto-reconnecting client
// behind it, which would keep dialling a broker from a process that is going away.
func TestAFirstRefusedSubscriptionStillFailsStart(t *testing.T) {
	b := startScriptedBroker(t, refuseSubscribe)
	fails := newFailRecorder()
	es := newExternalMqttSource(t, "127.0.0.1", b.port, "acme/#", nil, fails.fail)

	err := startWithin(t, es, 20*time.Second)
	require.Error(t, err, "a refused first subscription let the source start")
	assert.ErrorIs(t, err, messaging.ErrSubscriptionRefused)
	assert.Contains(t, err.Error(), "would ingest nothing")

	// paho's first reconnect would be immediate; a second after, the broker has still
	// seen only the one connection.
	time.Sleep(time.Second)
	assert.Equal(t, 1, b.connections(), "a failed start left a client that reconnected")
	assert.False(t, es.Client.IsConnectionOpen(), "a failed start left its client connected")
	fails.requireNone(t)
}

// TestClassifyResubscribe pins the decision itself, including the case the integration
// tests cannot order deterministically: a refusal that arrives after a NEWER connection
// has already come up is still fatal, because the newer connection will be given the
// same answer.
func TestClassifyResubscribe(t *testing.T) {
	refused := fmt.Errorf("%w to %q (SUBACK 0x80)", messaging.ErrSubscriptionRefused, "t")
	silent := fmt.Errorf("%w to %q within 30s", messaging.ErrSubscriptionUnacknowledged, "t")
	dropped := fmt.Errorf("subscribing to %q: %w", "t", errors.New("connection lost before Subscribe completed"))

	for _, tc := range []struct {
		name       string
		err        error
		superseded bool
		want       resubscribeAction
	}{
		{"refused on the current connection", refused, false, resubscribeFatal},
		{"refused after a newer connection came up", refused, true, resubscribeFatal},
		{"no SUBACK on the current connection", silent, false, resubscribeFatal},
		{"no SUBACK but a newer connection came up", silent, true, resubscribeRetry},
		{"connection dropped under the SUBSCRIBE", dropped, false, resubscribeRetry},
		{"connection dropped and already replaced", dropped, true, resubscribeRetry},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classifyResubscribe(tc.err, tc.superseded))
		})
	}
}

// A source with no way to end the process would have to swallow a refused re-subscribe.
func TestAnMqttSourceWithoutAFailHookIsRefused(t *testing.T) {
	_, err := NewMqttEventSource("ext", map[string]string{"host": "h", "port": "1883", "topic": "t"},
		nil, "", "", NewJsonDecoder(map[string]string{}, 0),
		func(string, []byte) {},
		func(string, string, *model.UnresolvedEvent, interface{}, uint64) error { return nil },
		func(string, string, []byte, error) error { return nil },
		nil, nil)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "end the process"), "unexpected error: %v", err)
}
