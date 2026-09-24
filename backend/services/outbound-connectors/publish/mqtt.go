// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package publish

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/gorilla/websocket"

	"github.com/devicechain-io/dc-outbound-connectors/connectorspec"
)

// maxInboundPacket caps one inbound MQTT packet. A publisher receives CONNACK, PUBACK,
// PUBREC, PUBCOMP and PINGRESP — a handful of bytes each. The cap exists because the
// client allocates a packet's whole body from its 4-byte length prefix before reading it,
// so a broker claiming 200 MiB would otherwise get 200 MiB allocated.
const maxInboundPacket = 64 << 10

// errOversizedPacket is returned by mqttFrameCap for a packet above maxInboundPacket.
var errOversizedPacket = errors.New("publish: the broker announced an MQTT packet larger than a publisher can receive")

// sendMQTT publishes one message and disconnects.
//
// The client is configured never to reach anywhere on its own: no reconnect, no connect
// retry, and every connection comes from openFn, which dials through the guarded dial.
// paho's SetDialer is deliberately NOT used — its WebSocket path and its ALL_PROXY
// handling for tcp:// both bypass the dialer it is given.
func (s *Sender) sendMQTT(ctx context.Context, log *dialLog, t connectorspec.MQTTTarget, payload []byte) error {
	deadline, _ := ctx.Deadline()
	opts := mqtt.NewClientOptions()
	opts.Servers = append([]*url.URL(nil), t.Brokers...)
	clientID, err := mqttClientID(t.ClientID)
	if err != nil {
		return err
	}
	opts.SetClientID(clientID)
	if t.Username != "" {
		opts.SetUsername(t.Username)
	}
	if t.Password != "" {
		opts.SetPassword(t.Password)
	}
	opts.SetAutoReconnect(false)
	opts.SetConnectRetry(false)
	opts.SetCleanSession(true)
	opts.SetKeepAlive(30 * time.Second)
	opts.SetWriteTimeout(5 * time.Second)
	opts.SetCustomOpenConnectionFn(s.mqttOpen(ctx, log))

	// The connect timeout is the time left in the send, computed right before Connect. It is
	// never 0 (to paho, a deadline of now) and never the 30 s default, which would outlive
	// the send and the message's redelivery clock.
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return context.DeadlineExceeded
	}
	opts.SetConnectTimeout(remaining)

	client := mqtt.NewClient(opts)
	if err := waitToken(ctx, client.Connect()); err != nil {
		return err
	}
	defer client.Disconnect(0)
	return waitToken(ctx, client.Publish(t.Topic, t.QoS, false, payload))
}

// waitToken waits for a paho token or the send's end, whichever is first.
func waitToken(ctx context.Context, tok mqtt.Token) error {
	select {
	case <-tok.Done():
		return tok.Error()
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

// mqttClientID returns the authored prefix plus a random suffix, or "" (a broker-assigned
// id, valid with a clean session) when no prefix was authored. The suffix makes each send
// its own session: a fixed id shared by two concurrent sends through one connector would
// have the broker disconnect the first.
func mqttClientID(prefix string) (string, error) {
	if prefix == "" {
		return "", nil
	}
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz_-"
	var b [21]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("mqtt client id: %w", err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return prefix + string(b[:]), nil
}

// mqttOpen is paho's open-connection function for one send. Every scheme dials through
// the guarded dial; the MQTT stream above it is framed by mqttFrameCap.
func (s *Sender) mqttOpen(ctx context.Context, log *dialLog) mqtt.OpenConnectionFunc {
	dial := s.dial(log)
	return func(u *url.URL, _ mqtt.ClientOptions) (net.Conn, error) {
		useTLS, ws, ok := connectorspec.MQTTSchemeUsesTLS(u.Scheme)
		if !ok {
			// Reachable only when a target skipped connectorspec.Build. Terminal: no
			// redelivery changes a stored scheme.
			err := fmt.Errorf("%w: mqtt scheme %q is not supported", ErrPublishConfig, u.Scheme)
			log.fail(err)
			return nil, err
		}
		if ws {
			c, err := dialWebSocket(ctx, dial, u, useTLS)
			if err != nil {
				return nil, err
			}
			return newMQTTFrameCap(c), nil
		}
		conn, err := dial(ctx, "tcp", u.Host)
		if err != nil {
			return nil, err
		}
		if useTLS {
			tc := tls.Client(conn, &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12})
			if err := tc.HandshakeContext(ctx); err != nil {
				_ = conn.Close()
				return nil, err
			}
			conn = tc
		}
		return newMQTTFrameCap(conn), nil
	}
}

// dialWebSocket opens an MQTT-over-WebSocket connection through the guarded dial. Proxy is
// nil explicitly (no environment proxy), TLS verifies the authored host, and the handshake
// is bounded by the send. gorilla does not follow redirects on the handshake.
func dialWebSocket(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error), u *url.URL, useTLS bool) (net.Conn, error) {
	deadline, _ := ctx.Deadline()
	d := websocket.Dialer{
		NetDialContext:   dial,
		Proxy:            nil,
		Subprotocols:     []string{"mqtt"},
		HandshakeTimeout: time.Until(deadline),
	}
	if useTLS {
		d.TLSClientConfig = &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12}
	}
	c, resp, err := d.DialContext(ctx, u.String(), nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	// Room for one maximal packet plus its largest fixed header.
	c.SetReadLimit(maxInboundPacket + 5)
	return newWSConn(c), nil
}

// mqttFrameCap parses each MQTT fixed header as it passes and refuses a packet whose
// Remaining Length exceeds maxInboundPacket BEFORE the client reads that length and
// allocates it.
type mqttFrameCap struct {
	net.Conn
	mu sync.Mutex
	// state: 0 = at a fixed-header byte, 1 = inside the Remaining Length varint,
	// 2 = inside a packet body.
	state      int
	length     int
	multiplier int
	lenBytes   int
	body       int
	err        error
}

func newMQTTFrameCap(c net.Conn) *mqttFrameCap {
	return &mqttFrameCap{Conn: c}
}

func (f *mqttFrameCap) Read(p []byte) (int, error) {
	f.mu.Lock()
	if f.err != nil {
		err := f.err
		f.mu.Unlock()
		return 0, err
	}
	f.mu.Unlock()
	n, err := f.Conn.Read(p)
	f.mu.Lock()
	defer f.mu.Unlock()
	if ferr := f.scan(p[:n]); ferr != nil {
		f.err = ferr
		_ = f.Conn.Close()
		return 0, ferr
	}
	return n, err
}

// scan advances the header parser over b.
func (f *mqttFrameCap) scan(b []byte) error {
	for i := 0; i < len(b); {
		switch f.state {
		case 0:
			f.state, f.length, f.multiplier, f.lenBytes = 1, 0, 1, 0
			i++
		case 1:
			c := b[i]
			i++
			f.length += int(c&0x7f) * f.multiplier
			f.multiplier *= 128
			f.lenBytes++
			if f.length > maxInboundPacket {
				return errOversizedPacket
			}
			if c&0x80 != 0 {
				if f.lenBytes == 4 {
					return fmt.Errorf("publish: malformed MQTT remaining length")
				}
				continue
			}
			if f.length == 0 {
				f.state = 0
			} else {
				f.state, f.body = 2, f.length
			}
		case 2:
			skip := min(f.body, len(b)-i)
			f.body -= skip
			i += skip
			if f.body == 0 {
				f.state = 0
			}
		}
	}
	return nil
}
