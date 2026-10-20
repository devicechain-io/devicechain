// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"fmt"
	"io"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
)

// fakeBroker accepts connections and, for each, reports the first bytes it reads and then
// answers only when told to: a broker parsing a CONNECT for exactly as long as a test likes.
type fakeBroker struct {
	ln       net.Listener
	accepted chan net.Conn
	got      chan struct{}
	answer   chan struct{}
}

func startFakeBroker(t *testing.T) *fakeBroker {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &fakeBroker{ln: ln, accepted: make(chan net.Conn, 8), got: make(chan struct{}, 8), answer: make(chan struct{})}
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			b.accepted <- c
			go func() {
				buf := make([]byte, 64)
				if _, err := c.Read(buf); err != nil {
					return
				}
				b.got <- struct{}{}
				<-b.answer
				_, _ = c.Write([]byte("CONNACK"))
			}()
		}
	}()
	return b
}

// dialAndSend connects to the front and sends a CONNECT stand-in.
func dialAndSend(t *testing.T, f *MQTTFront) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(f.Port())))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := c.Write([]byte("CONNECT")); err != nil {
		t.Fatal(err)
	}
	return c
}

func within(t *testing.T, ch <-chan struct{}, d time.Duration, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(d):
		t.Fatal(what)
	}
}

func quietAsync(f *MQTTFront) <-chan struct{} {
	ran := make(chan struct{})
	go f.Quiet(func() { close(ran) })
	return ran
}

// A Quiet asked for while the broker is still parsing a CONNECT runs only once the broker
// has answered it, and the answer reaches the client.
func TestMQTTFrontQuietWaitsForTheBrokersAnswer(t *testing.T) {
	b := startFakeBroker(t)
	f := StartMQTTFront(t, b.ln.Addr().String())
	c := dialAndSend(t, f)
	within(t, b.got, 5*time.Second, "the CONNECT never reached the broker")

	ran := quietAsync(f)
	select {
	case <-ran:
		t.Fatal("Quiet ran while the broker had not answered the CONNECT")
	case <-time.After(300 * time.Millisecond):
	}
	close(b.answer)
	within(t, ran, 5*time.Second, "Quiet never ran after the broker answered")

	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 16)
	n, err := c.Read(buf)
	if err != nil || string(buf[:n]) != "CONNACK" {
		t.Fatalf("the client read %q, %v; want the broker's answer", buf[:n], err)
	}
}

// A connection that ends before the broker answers does not hold Quiet for ever.
func TestMQTTFrontAClosedHandshakeReleasesQuiet(t *testing.T) {
	b := startFakeBroker(t)
	f := StartMQTTFront(t, b.ln.Addr().String())
	c := dialAndSend(t, f)
	within(t, b.got, 5*time.Second, "the CONNECT never reached the broker")
	_ = c.Close()
	within(t, quietAsync(f), 5*time.Second, "a handshake that was closed still held Quiet")
}

// A connection that arrives while Quiet is running reaches the broker only after it returns.
func TestMQTTFrontHoldsNewConnectionsDuringQuiet(t *testing.T) {
	b := startFakeBroker(t)
	close(b.answer)
	f := StartMQTTFront(t, b.ln.Addr().String())
	f.Quiet(func() {
		dialAndSend(t, f)
		select {
		case <-b.accepted:
			t.Fatal("a connection reached the broker while Quiet was running")
		case <-time.After(300 * time.Millisecond):
		}
	})
	select {
	case <-b.accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("the held connection never reached the broker")
	}
	within(t, b.got, 5*time.Second, "the held connection's CONNECT never reached the broker")
	// It was answered, so it no longer holds Quiet.
	within(t, quietAsync(f), 5*time.Second, "an answered handshake still held Quiet")
}

// mqttConnect sends a minimal MQTT 3.1.1 CONNECT for clientID through conn and waits for
// the broker's CONNACK.
func mqttConnect(conn net.Conn, clientID string) error {
	vh := []byte{0, 4, 'M', 'Q', 'T', 'T', 4, 0x02, 0, 60}
	pl := append([]byte{byte(len(clientID) >> 8), byte(len(clientID))}, clientID...)
	pkt := append([]byte{0x10, byte(len(vh) + len(pl))}, append(vh, pl...)...)
	if _, err := conn.Write(pkt); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	ack := make([]byte, 4)
	_, err := io.ReadFull(conn, ack)
	return err
}

// startMQTTBroker starts an embedded nats-server with an MQTT listener and returns it with
// that listener's port.
func startMQTTBroker(t *testing.T) (*natsserver.Server, int) {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, NoLog: true, NoSigs: true, ServerName: "mqtt-front",
		StoreDir: JetStreamStoreDir(t), MQTT: natsserver.MQTTOpts{Host: "127.0.0.1", Port: -1},
	})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(15 * time.Second) {
		t.Fatal("embedded nats server not ready")
	}
	t.Cleanup(srv.Shutdown)
	return srv, MQTTPort(t, srv)
}

// connectMQTT opens an MQTT session for clientID on port and waits for its CONNACK.
func connectMQTT(t *testing.T, port int, clientID string) {
	t.Helper()
	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := mqttConnect(c, clientID); err != nil {
		t.Fatal(err)
	}
}

// fatalRecorder records a Fatalf and ends the goroutine, as testing.T does, so a test can
// require that a helper fails the test that calls it.
type fatalRecorder struct {
	testing.TB
	msg string
}

func (r *fatalRecorder) Helper() {}
func (r *fatalRecorder) Fatalf(format string, args ...any) {
	r.msg = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// MQTTClients lists what came through the front, by prefix, and refuses a table holding an
// MQTT client that connected to the broker directly: that client's CONNECT was never fenced.
func TestMQTTFrontListsItsClientsAndRefusesOneThatBypassedIt(t *testing.T) {
	srv, mqttPort := startMQTTBroker(t)
	f := StartMQTTFront(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(mqttPort)))
	connectMQTT(t, f.Port(), "pod:a")
	connectMQTT(t, f.Port(), "other:b")
	if got := f.MQTTClients(t, srv, "pod:"); len(got) != 1 || got[0] != "pod:a" {
		t.Fatalf("MQTTClients(pod:) = %v, want [pod:a]", got)
	}

	connectMQTT(t, mqttPort, "pod:direct")
	rec := &fatalRecorder{TB: t}
	done := make(chan struct{})
	go func() { defer close(done); f.MQTTClients(rec, srv, "pod:") }()
	<-done
	if !strings.Contains(rec.msg, `"pod:direct"`) || !strings.Contains(rec.msg, "without passing through the front") {
		t.Fatalf("a client that bypassed the front was listed; the helper said %q", rec.msg)
	}
}

// 🔴 THE UPSTREAM RACE, AGAINST A REAL BROKER. Clients connect over MQTT back to back while
// Connz is read in a loop, the way a test polling for a client does. Read directly, that is
// a data race inside nats-server that -race reports here on the first run; read through
// Quiet, it must never be one. Without -race this only checks the relay carries a real MQTT
// handshake. The connects are few and COUNTED, not timed, and each client resets rather than
// closes: a closed socket sits in TIME_WAIT for minutes, and a loop bounded by a clock
// exhausted this machine's ephemeral ports under -count, failing every other test on it.
func TestMQTTFrontKeepsConnzOffAConnectInProgress(t *testing.T) {
	srv, mqttPort := startMQTTBroker(t)
	f := StartMQTTFront(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(mqttPort)))

	const workers, each = 4, 10
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	var connects atomic.Int64
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(f.Port())))
				if err != nil {
					errs <- err
					return
				}
				err = mqttConnect(c, fmt.Sprintf("front-%d-%d", w, i))
				_ = c.(*net.TCPConn).SetLinger(0)
				_ = c.Close()
				if err != nil {
					errs <- fmt.Errorf("connect %d-%d: %w", w, i, err)
					return
				}
				connects.Add(1)
			}
		}(w)
	}
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	var reads int
	for running := true; running; {
		select {
		case <-finished:
			running = false
		default:
		}
		f.MQTTClients(t, srv, "front-")
		reads++
	}
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if connects.Load() != workers*each || reads < 2 {
		t.Fatalf("nothing overlapped: %d of %d connects, %d reads", connects.Load(), workers*each, reads)
	}
}
