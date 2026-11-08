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
	"strings"
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
	// Duplicate, when it returns true for the Nth publish, makes the broker keep it TWICE (and
	// PUBACK it once): a stored duplicate, the thing that offsets a loss in a count.
	Duplicate func(n int64) bool
	// Authorize, when set, decides whether a CONNECT is admitted (username, password, client id).
	Authorize func(username, password, clientID string) bool
	// Fanout, when true, delivers every event the broker KEEPS to the connections subscribed to a
	// filter that matches its topic ("+" and "#" wildcards), like a real broker. An event it
	// acknowledges without keeping (AckOnly) is delivered to nobody, which is the loss a
	// subscriber-side read-back must see.
	Fanout bool
	// RefuseSubscribe makes the broker answer every SUBSCRIBE with the refusal code 0x80, as a
	// broker does for a user its ACL does not let read the topic.
	RefuseSubscribe bool
	// GrantQoS0 makes the broker grant QoS 0 to every subscription whatever was asked, as a broker
	// that caps subscriber QoS does.
	GrantQoS0 bool
	// OnPublish, when set, sees every PUBLISH the broker reads, before it is acknowledged. It runs
	// on the publishing connection's goroutine and may call Deliver. It is how a test plays the
	// platform: read a device's report, answer on the device's desired topic.
	OnPublish func(clientID, topic string, payload []byte)

	addr string
	ln   net.Listener
	tls  bool

	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	sessions map[*session]struct{}
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
	b := &Broker{ln: ln, tls: withTLS, conns: map[net.Conn]struct{}{}, sessions: map[*session]struct{}{}}
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

// session is one admitted connection: writes to it are serialised, because the serving goroutine
// (acknowledgements) and Deliver (messages for its subscriptions) both write to the same socket.
type session struct {
	conn net.Conn
	wmu  sync.Mutex
	smu  sync.Mutex
	subs map[string]struct{}
}

func (s *session) write(p packets.ControlPacket) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return p.Write(s.conn)
}

func (s *session) subscribed(topic string) bool {
	s.smu.Lock()
	defer s.smu.Unlock()
	for filter := range s.subs {
		if filter == topic || topicMatches(filter, topic) {
			return true
		}
	}
	return false
}

// topicMatches reports whether an MQTT subscription filter covers a topic name: "+" is one
// level and a trailing "#" is every level below.
func topicMatches(filter, topic string) bool {
	f, t := strings.Split(filter, "/"), strings.Split(topic, "/")
	for i, l := range f {
		if l == "#" {
			return i == len(f)-1
		}
		if i >= len(t) || (l != "+" && l != t[i]) {
			return false
		}
	}
	return len(f) == len(t)
}

// Deliver publishes payload to every connection subscribed to exactly topic (no wildcards) and
// returns how many it reached. Delivery is QoS 0, the live path the platform uses for
// device-desired messages, so a message for a device that is not subscribed is lost, as it is on
// the real broker.
func (b *Broker) Deliver(topic string, payload []byte) int {
	b.mu.Lock()
	targets := make([]*session, 0, len(b.sessions))
	for s := range b.sessions {
		if s.subscribed(topic) {
			targets = append(targets, s)
		}
	}
	b.mu.Unlock()
	n := 0
	for _, s := range targets {
		pub := packets.NewControlPacket(packets.Publish).(*packets.PublishPacket)
		pub.TopicName, pub.Payload = topic, payload
		if s.write(pub) == nil {
			n++
		}
	}
	return n
}

// Subscribers is the number of connections subscribed to exactly topic.
func (b *Broker) Subscribers(topic string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for s := range b.sessions {
		if s.subscribed(topic) {
			n++
		}
	}
	return n
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
	sess := &session{conn: conn, subs: map[string]struct{}{}}
	ack := packets.NewControlPacket(packets.Connack).(*packets.ConnackPacket)
	if b.Authorize != nil && !b.Authorize(connect.Username, string(connect.Password), connect.ClientIdentifier) {
		ack.ReturnCode = packets.ErrRefusedNotAuthorised
		_ = sess.write(ack)
		return
	}
	if err := sess.write(ack); err != nil {
		return
	}
	b.mu.Lock()
	b.sessions[sess] = struct{}{}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.sessions, sess)
		b.mu.Unlock()
	}()
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
			if b.OnPublish != nil {
				b.OnPublish(connect.ClientIdentifier, p.TopicName, p.Payload)
			}
			if b.Silent != nil && b.Silent(n) {
				continue
			}
			if b.StoreOnly != nil && b.StoreOnly(n) {
				b.keep(connect, p)
				continue
			}
			if b.AckOnly == nil || !b.AckOnly(n) {
				b.keep(connect, p)
				if b.Duplicate != nil && b.Duplicate(n) {
					b.keep(connect, p)
				}
			}
			if p.Qos == 1 {
				pa := packets.NewControlPacket(packets.Puback).(*packets.PubackPacket)
				pa.MessageID = p.MessageID
				if sess.write(pa) != nil {
					return
				}
			}
		case *packets.SubscribePacket:
			sa := packets.NewControlPacket(packets.Suback).(*packets.SubackPacket)
			sa.MessageID = p.MessageID
			sess.smu.Lock()
			for i, t := range p.Topics {
				if b.RefuseSubscribe {
					sa.ReturnCodes = append(sa.ReturnCodes, 0x80)
					continue
				}
				sess.subs[t] = struct{}{}
				granted := p.Qoss[i]
				if b.GrantQoS0 {
					granted = 0
				}
				sa.ReturnCodes = append(sa.ReturnCodes, granted)
			}
			sess.smu.Unlock()
			if sess.write(sa) != nil {
				return
			}
		case *packets.PingreqPacket:
			if sess.write(packets.NewControlPacket(packets.Pingresp)) != nil {
				return
			}
		case *packets.DisconnectPacket:
			return
		}
	}
}

func (b *Broker) keep(c *packets.ConnectPacket, p *packets.PublishPacket) {
	if b.Fanout {
		defer b.Deliver(p.TopicName, p.Payload)
	}
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
