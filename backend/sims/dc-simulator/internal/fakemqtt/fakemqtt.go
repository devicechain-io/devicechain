// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package fakemqtt is a minimal MQTT 3.1.1 broker for the load harness's tests: it accepts
// CONNECTs, PUBACKs QoS 1 publishes and "persists" each published event the way the platform
// would, so a test can ask it what became of every event the harness sent. Its point is the
// part a mock of the client cannot give: a REAL paho client on a real socket, a broker that can
// lie (acknowledge an event and not keep it), and one that can be made to drop its connections.
package fakemqtt

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"math/big"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eclipse/paho.mqtt.golang/packets"
)

// Stored is one event the broker kept: the device and occurredTime the body named, and the
// session that published it.
type Stored struct {
	Device       string
	OccurredTime time.Time
	ClientID     string
	Topic        string
	Username     string
	Body         []byte
}

// Broker is a running fake. Configure the exported fields before the first client connects.
type Broker struct {
	// AckOnly, when it returns true for the Nth QoS 1 publish received (1-based), makes the
	// broker PUBACK it WITHOUT keeping it: the loss a PUBACKed event must never suffer.
	AckOnly func(n int64) bool
	// Silent, when it returns true for the Nth publish, makes the broker neither ack nor keep
	// it (the publisher's token times out).
	Silent func(n int64) bool
	// StoreOnly, when it returns true for the Nth publish, makes the broker keep it and say
	// nothing: an event the publisher cannot tell was stored.
	StoreOnly func(n int64) bool
	// Authorize, when set, decides whether a CONNECT is admitted (username, password, client id).
	Authorize func(username, password, clientID string) bool

	addr string
	ln   net.Listener
	tls  bool

	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	stored   []Stored
	connects atomic.Int64
	received atomic.Int64
	tlsSeen  atomic.Int64
	closed   atomic.Bool
}

// New starts a plaintext broker on a loopback port.
func New(t testing.TB) *Broker { return start(t, false) }

// NewTLS starts a broker that speaks TLS with a throwaway self-signed certificate. A client
// that verifies the certificate refuses it; one told to skip verification connects.
func NewTLS(t testing.TB) *Broker { return start(t, true) }

func start(t testing.TB, withTLS bool) *Broker {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &Broker{ln: ln, tls: withTLS, conns: map[net.Conn]struct{}{}}
	if withTLS {
		cert := selfSigned(t)
		b.ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	}
	b.addr = ln.Addr().String()
	go b.accept()
	t.Cleanup(b.Close)
	return b
}

// URL is the broker address a client dials, with the scheme that matches how it listens.
func (b *Broker) URL() string {
	if b.tls {
		return "ssl://" + b.addr
	}
	return "tcp://" + b.addr
}

// Addr is host:port.
func (b *Broker) Addr() string { return b.addr }

// Close stops the broker and drops every connection.
func (b *Broker) Close() {
	if b.closed.Swap(true) {
		return
	}
	_ = b.ln.Close()
	b.DropConnections()
}

// DropConnections closes every live client connection, leaving the listener up: the clients see
// a lost connection and must reconnect.
func (b *Broker) DropConnections() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for c := range b.conns {
		_ = c.Close()
	}
	b.conns = map[net.Conn]struct{}{}
}

// Connects is the number of CONNECTs admitted so far.
func (b *Broker) Connects() int64 { return b.connects.Load() }

// TLSConnects is the number of admitted CONNECTs that arrived over TLS.
func (b *Broker) TLSConnects() int64 { return b.tlsSeen.Load() }

// Received is the number of PUBLISH packets read, kept or not.
func (b *Broker) Received() int64 { return b.received.Load() }

// Stored returns a copy of every event the broker kept.
func (b *Broker) Stored() []Stored {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Stored(nil), b.stored...)
}

func (b *Broker) accept() {
	for {
		conn, err := b.ln.Accept()
		if err != nil {
			return
		}
		b.mu.Lock()
		if b.closed.Load() {
			b.mu.Unlock()
			_ = conn.Close()
			return
		}
		b.conns[conn] = struct{}{}
		b.mu.Unlock()
		go b.serve(conn)
	}
}

func (b *Broker) serve(conn net.Conn) {
	defer func() {
		_ = conn.Close()
		b.mu.Lock()
		delete(b.conns, conn)
		b.mu.Unlock()
	}()
	cp, err := packets.ReadPacket(conn)
	if err != nil {
		return
	}
	connect, ok := cp.(*packets.ConnectPacket)
	if !ok {
		return
	}
	ack := packets.NewControlPacket(packets.Connack).(*packets.ConnackPacket)
	if b.Authorize != nil && !b.Authorize(connect.Username, string(connect.Password), connect.ClientIdentifier) {
		ack.ReturnCode = packets.ErrRefusedNotAuthorised
		_ = ack.Write(conn)
		return
	}
	if err := ack.Write(conn); err != nil {
		return
	}
	b.connects.Add(1)
	if _, isTLS := conn.(*tls.Conn); isTLS {
		b.tlsSeen.Add(1)
	}
	for {
		cp, err := packets.ReadPacket(conn)
		if err != nil {
			return
		}
		switch p := cp.(type) {
		case *packets.PublishPacket:
			n := b.received.Add(1)
			if b.Silent != nil && b.Silent(n) {
				continue
			}
			if b.StoreOnly != nil && b.StoreOnly(n) {
				b.keep(connect, p)
				continue
			}
			if b.AckOnly == nil || !b.AckOnly(n) {
				b.keep(connect, p)
			}
			if p.Qos == 1 {
				pa := packets.NewControlPacket(packets.Puback).(*packets.PubackPacket)
				pa.MessageID = p.MessageID
				if pa.Write(conn) != nil {
					return
				}
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

func (b *Broker) keep(c *packets.ConnectPacket, p *packets.PublishPacket) {
	var body struct {
		Device       string `json:"device"`
		OccurredTime string `json:"occurredTime"`
	}
	_ = json.Unmarshal(p.Payload, &body)
	at, _ := time.Parse(time.RFC3339Nano, body.OccurredTime)
	b.mu.Lock()
	b.stored = append(b.stored, Stored{
		Device: body.Device, OccurredTime: at, ClientID: c.ClientIdentifier,
		Topic: p.TopicName, Username: c.Username, Body: append([]byte(nil), p.Payload...),
	})
	b.mu.Unlock()
}

func selfSigned(t testing.TB) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
