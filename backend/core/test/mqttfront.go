// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"net"
	"strings"
	"sync"
	"testing"

	natsserver "github.com/nats-io/nats-server/v2/server"
)

// MQTTFront relays MQTT connections to a broker and lets a test read the broker's
// connection table only while no relayed connection is in its handshake.
//
// # Why it exists
//
// An embedded nats-server (v2.15.0 and v2.15.1, and upstream main as of 2026-10-16) has a
// data race between its monitor and an MQTT CONNECT: the read loop stores the session's
// client id (mqttParseConnect, mqtt.go) WITHOUT holding the client's lock, while Connz
// reads it (ConnInfo.fill -> getMQTTClientID) WITH the lock held. A test that polls
// Server.Connz while one of its MQTT clients is connecting is therefore a race under
// -race, whatever it asserts, and it fails CI as one.
//
// # How this orders the two
//
// Every relayed connection holds a read lock on handshakes from the moment it is
// accepted until the broker's first bytes come back to the client (an MQTT broker says
// nothing before CONNACK, so the first bytes ARE the CONNACK, or the close), and Quiet
// holds the write lock. So no Connz taken inside Quiet overlaps a CONNECT being parsed:
// it runs either before the broker has the connection at all or after the CONNACK left it.
//
// That is real-time ordering, and the race detector does not see happens-before across a
// socket. It does not need to: the broker emits CONNACK only under the client's lock
// (mqttEnqueueConnAck, then the write loop's flush), AFTER the read loop stored the id,
// and Connz takes that same lock to read it. The broker's own mutex is the edge; this
// relay only guarantees the lock is taken in that order. A client that connects
// straight to the broker port bypasses all of it, so point every MQTT client in the
// test at Port; MQTTClients refuses a table holding one that did not.
type MQTTFront struct {
	ln         net.Listener
	handshakes sync.RWMutex

	mu    sync.Mutex
	conns []net.Conn
	// relayed holds the local port of every connection this front opened to the broker,
	// which is the remote port the broker reports for it.
	relayed map[int]bool
}

// StartMQTTFront listens on an ephemeral 127.0.0.1 port and relays every connection to
// target ("host:port"). It stops with the test.
func StartMQTTFront(t *testing.T, target string) *MQTTFront {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mqtt front listen: %v", err)
	}
	f := &MQTTFront{ln: ln, relayed: map[int]bool{}}
	t.Cleanup(func() {
		_ = ln.Close()
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, c := range f.conns {
			_ = c.Close()
		}
	})
	go f.accept(target)
	return f
}

func (f *MQTTFront) accept(target string) {
	for {
		client, err := f.ln.Accept()
		if err != nil {
			return // listener closed by cleanup
		}
		// Taken BEFORE anything is relayed, so a Quiet in progress finishes first and a
		// Quiet that starts later waits for this handshake.
		f.handshakes.RLock()
		var once sync.Once
		done := func() { once.Do(f.handshakes.RUnlock) }
		server, err := net.Dial("tcp", target)
		if err != nil {
			done()
			_ = client.Close()
			continue // drop this one and keep accepting, as TCPProxy does
		}
		f.mu.Lock()
		f.conns = append(f.conns, client, server)
		f.relayed[server.LocalAddr().(*net.TCPAddr).Port] = true
		f.mu.Unlock()
		go relay(client, server, func() {})
		go relay(server, client, done)
	}
}

// relay copies from into to, calling first once the first bytes have been written to the
// destination, or when the copy ends without any. It closes both sides when it ends.
func relay(from, to net.Conn, first func()) {
	defer first()
	defer func() { _ = from.Close(); _ = to.Close() }()
	buf := make([]byte, 4096)
	for {
		n, err := from.Read(buf)
		if n > 0 {
			if _, werr := to.Write(buf[:n]); werr != nil {
				return
			}
			first()
		}
		if err != nil {
			return
		}
	}
}

// Port is the front's own port, to point an MQTT client at instead of the broker's.
func (f *MQTTFront) Port() int { return f.ln.Addr().(*net.TCPAddr).Port }

// Quiet runs fn while no relayed connection is between being accepted and receiving the
// broker's first reply. New connections wait until fn returns.
func (f *MQTTFront) Quiet(fn func()) {
	f.handshakes.Lock()
	defer f.handshakes.Unlock()
	fn()
}

// MQTTClients lists the client ids of the MQTT connections srv holds that start with
// prefix, read inside Quiet. It fails the test if any MQTT connection reached srv without
// passing through this front: that one's CONNECT is not fenced, so the read could race it.
func (f *MQTTFront) MQTTClients(t testing.TB, srv *natsserver.Server, prefix string) []string {
	t.Helper()
	var connz *natsserver.Connz
	var err error
	f.Quiet(func() { connz, err = srv.Connz(&natsserver.ConnzOptions{}) })
	if err != nil {
		t.Fatalf("connz: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for _, c := range connz.Conns {
		if c.Type != "mqtt" {
			continue
		}
		if !f.relayed[c.Port] {
			t.Fatalf("MQTT client %q reached the broker from port %d without passing through the front, "+
				"so reading the broker's connection table can race its CONNECT", c.MQTTClient, c.Port)
		}
		if strings.HasPrefix(c.MQTTClient, prefix) {
			ids = append(ids, c.MQTTClient)
		}
	}
	return ids
}
