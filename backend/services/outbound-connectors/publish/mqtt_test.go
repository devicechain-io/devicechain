// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package publish

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The dial refuses every network that is not TCP, and it does so itself — not only because
// connectorspec never hands it anything else. This calls the dial DIRECTLY, bypassing
// Build and every client, which is the only way to reach the check at all.
func TestDialRefusesNonTCPNetworks(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "broker.sock")
	ln := listen(t, "unix", sock, nil)

	s := NewSender(guardAllowing("127.0.0.0/8", "::1/128"))
	ctx, cancel := context.WithCancelCause(sendCtx(t, 2*time.Second))
	defer cancel(nil)
	log := &dialLog{ctx: ctx, cancel: cancel}

	for _, network := range []string{"unix", "unixpacket", "udp", "ip4:icmp"} {
		conn, err := s.dial(log)(ctx, network, sock)
		if conn != nil {
			_ = conn.Close()
		}
		require.Error(t, err, network)
		assert.True(t, isBlocked(err), "%s: want a blocked refusal, got %v", network, err)
	}
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(0), ln.accepts.Load(), "nothing may reach the socket")
	require.NotNil(t, log.blocked.Load(), "the refusal is recorded for the send")
	assert.Error(t, context.Cause(ctx), "and the send is cancelled")
}

// A target that skipped connectorspec.Build can still carry a scheme no client should
// open. The open function refuses it as a TERMINAL target error, and dials nothing.
func TestMQTTOpenFnRefusesAnUnknownScheme(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "broker.sock")
	ln := listen(t, "unix", sock, nil)

	s := NewSender(guardAllowing("127.0.0.0/8", "::1/128"))
	err := s.Send(sendCtx(t, 2*time.Second), mqttTarget(t, "unix://"+sock), []byte("x"), "k")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPublishConfig)
	assert.False(t, isBlocked(err))
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(0), ln.accepts.Load())
}

// The boundary, end to end over MQTT: an allowed broker receives the payload on the
// authored topic; the same broker with no allowance is refused before connect(2), and so
// is a hostname that resolves to it.
func TestMQTTBoundary(t *testing.T) {
	broker, ln := startBroker(t, "127.0.0.1:0")
	url := "tcp://127.0.0.1:" + ln.port()

	err := NewSender(guardAllowing("127.0.0.1/32")).Send(sendCtx(t, 5*time.Second),
		mqttTarget(t, url), []byte(`{"temp":72}`), "k")
	require.NoError(t, err)
	assert.Equal(t, []fakePublish{{topic: "alerts/1", payload: `{"temp":72}`}}, broker.messages())
	assert.Equal(t, int32(1), ln.accepts.Load())

	for _, u := range []string{url, "tcp://localhost:" + ln.port(), "mqtt://127.0.0.1:" + ln.port()} {
		start := time.Now()
		err := NewSender(nil).Send(sendCtx(t, 5*time.Second), mqttTarget(t, u), []byte("x"), "k")
		assert.True(t, isBlocked(err), "%s: want blocked, got %v", u, err)
		assert.Less(t, time.Since(start), 2*time.Second, "%s: a refusal is immediate", u)
	}
	assert.Equal(t, int32(1), ln.accepts.Load(), "no refused send may reach the broker")
	assert.Len(t, broker.messages(), 1)
}

// A refused broker makes the whole dispatch blocked the moment the client tries it, even
// when a later broker is allowed and reachable: the connector names a destination the
// tenant may not reach, and trying the others would only hide that. (A refused broker the
// client never tries — because an earlier one delivered — is never judged.)
func TestAnyBlockedMQTTBrokerIsTerminal(t *testing.T) {
	broker, ln := startBroker(t, "127.0.0.1:0")
	err := NewSender(guardAllowing("127.0.0.1/32")).Send(sendCtx(t, 5*time.Second),
		mqttTarget(t, "tcp://10.255.255.1:1883", "tcp://127.0.0.1:"+ln.port()), []byte("x"), "k")
	assert.True(t, isBlocked(err), "want blocked, got %v", err)
	assert.Empty(t, broker.messages())
}

// A set client id is a PREFIX: each send appends a random suffix so two concurrent sends
// through one connector are two sessions rather than a takeover.
func TestMQTTClientIDIsAPrefix(t *testing.T) {
	broker, ln := startBroker(t, "127.0.0.1:0")
	s := NewSender(guardAllowing("127.0.0.1/32"))
	target := mqttTarget(t, "tcp://127.0.0.1:"+ln.port())
	target.ClientID = "dc-"
	for i := 0; i < 2; i++ {
		require.NoError(t, s.Send(sendCtx(t, 5*time.Second), target, []byte("x"), "k"))
	}
	broker.mu.Lock()
	ids := append([]string(nil), broker.clientIDs...)
	broker.mu.Unlock()
	require.Len(t, ids, 2)
	for _, id := range ids {
		assert.True(t, strings.HasPrefix(id, "dc-") && len(id) == len("dc-")+21, "client id %q", id)
	}
	assert.NotEqual(t, ids[0], ids[1])
}

// ws:// and wss:// dial through the same guarded dial: reached when allowed, refused before
// connect otherwise, and TLS presents the authored host.
func TestMQTTOverWebSocketIsGuarded(t *testing.T) {
	wsBroker := &fakeBroker{}
	ws := httptest.NewServer(wsBrokerHandler(wsBroker))
	t.Cleanup(ws.Close)

	ca := newTestCA(t)
	wssBroker := &fakeBroker{}
	wss := httptest.NewUnstartedServer(wsBrokerHandler(wssBroker))
	sni := &sniRecorder{}
	wss.TLS = sni.config(ca.leaf(t, "localhost"))
	wss.StartTLS()
	t.Cleanup(wss.Close)

	wsURL := "ws://127.0.0.1:" + mustURL(t, ws.URL).Port() + "/mqtt"
	wssURL := "wss://localhost:" + mustURL(t, wss.URL).Port() + "/mqtt"

	// Refused: nothing is allowed, so neither server sees a connection.
	for _, u := range []string{wsURL, wssURL} {
		err := NewSender(nil).Send(sendCtx(t, 5*time.Second), mqttTarget(t, u), []byte("x"), "k")
		assert.True(t, isBlocked(err), "%s: want blocked, got %v", u, err)
	}
	assert.Empty(t, sni.seen(), "a refused wss dial must not reach the TLS handshake")
	assert.Empty(t, wsBroker.messages())

	// Allowed: ws delivers.
	require.NoError(t, NewSender(guardAllowing("127.0.0.0/8", "::1/128")).Send(sendCtx(t, 5*time.Second),
		mqttTarget(t, wsURL), []byte("over-ws"), "k"))
	assert.Equal(t, []fakePublish{{topic: "alerts/1", payload: "over-ws"}}, wsBroker.messages())

	// Allowed wss reaches the handshake presenting the AUTHORED host. The test CA is not in
	// the system pool, so verification fails; the SNI is what this asserts. Delivery over
	// verified TLS is TestMQTTTLSUsesAuthoredHostname's.
	err := NewSender(guardAllowing("127.0.0.0/8", "::1/128")).Send(sendCtx(t, 5*time.Second),
		mqttTarget(t, wssURL), []byte("x"), "k")
	require.Error(t, err)
	assert.False(t, isBlocked(err))
	require.NotEmpty(t, sni.seen())
	assert.Equal(t, "localhost", sni.seen()[0])
}

// TLS verifies the broker against the host the connector names. The body runs in a child
// process whose system root pool is the test CA (SSL_CERT_FILE is read once per process).
func TestMQTTTLSUsesAuthoredHostname(t *testing.T) {
	ca := childWithCA(t, "TestMQTTTLSUsesAuthoredHostname")
	if ca == nil {
		return
	}
	s := NewSender(guardAllowing("127.0.0.0/8", "::1/128"))

	for _, scheme := range []string{"ssl", "tls", "mqtts"} {
		broker := &fakeBroker{}
		sni := &sniRecorder{}
		cfg := sni.config(ca.leaf(t, "localhost"))
		ln := listen(t, "tcp", "127.0.0.1:0", func(c net.Conn) {
			tc := tls.Server(c, cfg)
			if tc.Handshake() != nil {
				return
			}
			broker.serve(tc)
		})
		err := s.Send(sendCtx(t, 5*time.Second), mqttTarget(t, scheme+"://localhost:"+ln.port()), []byte("tls"), "k")
		require.NoError(t, err, scheme)
		assert.Equal(t, []fakePublish{{topic: "alerts/1", payload: "tls"}}, broker.messages(), scheme)
		assert.Equal(t, []string{"localhost"}, sni.seen(), scheme)
	}

	// A certificate for another name, from the SAME trusted CA, fails verification.
	other := &fakeBroker{}
	otherCert := ca.leaf(t, "other.test")
	ln := listen(t, "tcp", "127.0.0.1:0", func(c net.Conn) {
		tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{otherCert}})
		if tc.Handshake() != nil {
			return
		}
		other.serve(tc)
	})
	err := s.Send(sendCtx(t, 5*time.Second), mqttTarget(t, "ssl://localhost:"+ln.port()), []byte("x"), "k")
	require.Error(t, err)
	var verr *tls.CertificateVerificationError
	assert.True(t, errors.As(err, &verr) || strings.Contains(err.Error(), "certificate"),
		"want a verification failure, got %v", err)
	assert.Empty(t, other.messages())
}

// A broker that accepts TCP and never answers CONNECT holds the send only until its
// deadline — not until paho's 30 s default — and leaves nothing running behind it.
func TestMQTTConnackStallReturnsByTheDeadline(t *testing.T) {
	// Reads until the client goes away and never answers.
	ln := listen(t, "tcp", "127.0.0.1:0", func(c net.Conn) { _, _ = io.Copy(io.Discard, c) })
	s := NewSender(guardAllowing("127.0.0.1/32"))
	base := goroutines()

	const budget = 700 * time.Millisecond
	start := time.Now()
	err := s.Send(sendCtx(t, budget), mqttTarget(t, "tcp://127.0.0.1:"+ln.port()), []byte("x"), "k")
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.LessOrEqual(t, elapsed, budget+200*time.Millisecond, "the send outlived its deadline")
	got := settlesTo(time.Second, base, goroutines)
	assert.LessOrEqual(t, got, base, "goroutines left behind by the send")
}

// A tarpit broker cannot keep a connection or a goroutine alive past the send, across many
// sends. There are two: one reads CONNECT and never answers it, the other acknowledges CONNECT
// and never acknowledges the publish. Each holds the connection open for as long as the client
// lets it.
func TestATarpitBrokerDoesNotOutliveTheSend(t *testing.T) {
	silent := listen(t, "tcp", "127.0.0.1:0", func(c net.Conn) { _, _ = io.Copy(io.Discard, c) })
	noAck := listen(t, "tcp", "127.0.0.1:0", func(c net.Conn) {
		buf := make([]byte, 4096)
		if _, err := c.Read(buf); err != nil {
			return
		}
		_, _ = c.Write([]byte{0x20, 0x02, 0x00, 0x00})
		_, _ = io.Copy(io.Discard, c)
	})
	targets := []string{"tcp://127.0.0.1:" + silent.port(), "tcp://127.0.0.1:" + noAck.port()}
	s := NewSender(guardAllowing("127.0.0.1/32"))
	// Warm up once each so lazily-started runtime goroutines are in the baseline.
	for _, u := range targets {
		_ = s.Send(sendCtx(t, 200*time.Millisecond), mqttTarget(t, u), []byte("x"), "k")
	}
	time.Sleep(200 * time.Millisecond)
	baseG, baseFD := goroutines(), openFDs(t)

	for i := 0; i < 20; i++ {
		err := s.Send(sendCtx(t, 150*time.Millisecond), mqttTarget(t, targets[i%2]), []byte("x"), "k")
		require.Error(t, err, "a tarpit never acknowledges")
	}
	assert.LessOrEqual(t, settlesTo(2*time.Second, baseG, goroutines), baseG, "goroutines")
	assert.LessOrEqual(t, settlesTo(2*time.Second, baseFD, func() int { return openFDs(t) }), baseFD, "open descriptors")
}

// A broker that answers CONNECT with a fixed header claiming a 200 MiB packet is refused
// before the client allocates the body.
func TestAnOversizedMQTTFrameIsRefusedBeforeAllocation(t *testing.T) {
	ln := listen(t, "tcp", "127.0.0.1:0", func(c net.Conn) {
		if _, err := c.Read(make([]byte, 4096)); err != nil {
			return
		}
		// CONNACK type with Remaining Length 200 MiB (varint), and nothing after it.
		n := 200 << 20
		hdr := []byte{0x20}
		for {
			b := byte(n % 128)
			n /= 128
			if n > 0 {
				b |= 0x80
			}
			hdr = append(hdr, b)
			if n == 0 {
				break
			}
		}
		_, _ = c.Write(hdr)
		_, _ = c.Read(make([]byte, 1))
	})
	s := NewSender(guardAllowing("127.0.0.1/32"))
	var err error
	grew := heapGrowth(func() {
		err = s.Send(sendCtx(t, 3*time.Second), mqttTarget(t, "tcp://127.0.0.1:"+ln.port()), []byte("x"), "k")
	})
	require.Error(t, err)
	assert.Less(t, grew, uint64(10<<20), "the send allocated %d bytes", grew)
}

// The frame parser, driven byte by byte and in one chunk, over packets that straddle reads.
func TestMQTTFrameCapScan(t *testing.T) {
	stream := []byte{0x20, 0x02, 0x00, 0x00, 0xD0, 0x00, 0x40, 0x02, 0x00, 0x01}
	f := &mqttFrameCap{}
	require.NoError(t, f.scan(stream))
	assert.Equal(t, 0, f.state)
	f = &mqttFrameCap{}
	for _, b := range stream {
		require.NoError(t, f.scan([]byte{b}))
	}
	assert.Equal(t, 0, f.state)

	// Exactly the cap is admitted; one byte past it is not.
	f = &mqttFrameCap{}
	require.NoError(t, f.scan([]byte{0x30, 0x80, 0x80, 0x04})) // 65536
	f = &mqttFrameCap{}
	assert.ErrorIs(t, f.scan([]byte{0x30, 0x81, 0x80, 0x04}), errOversizedPacket) // 65537
	f = &mqttFrameCap{}
	assert.Error(t, f.scan([]byte{0x30, 0x80, 0x80, 0x80, 0x80}), "a five-byte length is malformed")
}

// When a hostname resolves to one refused and one reachable address, the send connects to
// the reachable one: the guard refuses ADDRESSES, and the destination is not wholly
// refused. And when the reachable one is down, the failure is an ordinary, retryable one —
// not "blocked", even though a refusal happened along the way. The name is served by a
// fake resolver so the test does not depend on how this machine spells localhost.
//
// It runs in a child process because the fake resolver replaces net.DefaultResolver, a
// process global that a goroutine left over from another test may still be reading.
func TestAHostWithOneRefusedAndOneReachableAddressConnects(t *testing.T) {
	if !runInChild(t, "TestAHostWithOneRefusedAndOneReachableAddressConnects") {
		return
	}
	broker, ln := startBroker(t, "127.0.0.1:0")
	// Bind the IPv6 side too where the machine allows it, so a dial there would CONNECT if
	// the guard let it: the refusal is then the guard's and nothing else's.
	if ln6, err := net.Listen("tcp", "[::1]:"+ln.port()); err == nil {
		t.Cleanup(func() { _ = ln6.Close() })
	} else {
		t.Logf("[::1]:%s is not bindable here (%v); the guard refuses it before connect regardless", ln.port(), err)
	}
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	downPort := strconv.Itoa(closed.Addr().(*net.TCPAddr).Port)
	require.NoError(t, closed.Close())

	withFakeDNS(t, map[string][]netip.Addr{
		"dual.test": {netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("::1")},
	})
	addrs, err := net.DefaultResolver.LookupNetIP(context.Background(), "ip", "dual.test")
	require.NoError(t, err)
	require.Len(t, addrs, 2, "precondition: the name has both addresses")

	s := NewSender(guardAllowing("127.0.0.1/32"))
	require.NoError(t, s.Send(sendCtx(t, 5*time.Second), mqttTarget(t, "tcp://dual.test:"+ln.port()), []byte("dual"), "k"))
	assert.Equal(t, []fakePublish{{topic: "alerts/1", payload: "dual"}}, broker.messages())

	// The reachable address is down and the other is refused: an ordinary dial failure.
	err = s.Send(sendCtx(t, 3*time.Second), mqttTarget(t, "tcp://dual.test:"+downPort), []byte("x"), "k")
	require.Error(t, err)
	assert.False(t, isBlocked(err), "one refused and one down address is not blocked: %v", err)

	// Both addresses refused: blocked.
	err = NewSender(nil).Send(sendCtx(t, 3*time.Second), mqttTarget(t, "tcp://dual.test:"+ln.port()), []byte("x"), "k")
	assert.True(t, isBlocked(err), "every address refused is blocked: %v", err)
	assert.Len(t, broker.messages(), 1)
}

// The attempt tally is the whole classification, so it is pinned directly as well.
func TestAttemptTally(t *testing.T) {
	refuse := errors.New("refused")
	control := func(_ context.Context, _, address string, _ syscall.RawConn) error {
		if strings.HasPrefix(address, "10.") {
			return refuse
		}
		return nil
	}
	run := func(addrs ...string) (error, bool) {
		var tally attemptTally
		wrapped := tally.wrap(control)
		for _, a := range addrs {
			_ = wrapped(context.Background(), "tcp", a, nil)
		}
		return tally.allRefused()
	}
	if _, all := run(); all {
		t.Error("no attempts is not 'every attempt refused'")
	}
	if first, all := run("10.0.0.1:1", "10.0.0.2:1"); !all || first != refuse {
		t.Errorf("two refusals: all=%v first=%v", all, first)
	}
	if _, all := run("10.0.0.1:1", "192.0.2.1:1"); all {
		t.Error("one refused and one permitted address is not blocked")
	}
}

// oversizedCONNACK is a CONNACK fixed header whose Remaining Length claims 200 MiB, with no
// body after it.
func oversizedCONNACK() []byte {
	n := 200 << 20
	hdr := []byte{0x20}
	for {
		b := byte(n % 128)
		n /= 128
		if n > 0 {
			b |= 0x80
		}
		hdr = append(hdr, b)
		if n == 0 {
			return hdr
		}
	}
}

// The frame cap holds over WebSocket too. The WebSocket read limit bounds one WebSocket
// MESSAGE, and a message of a few bytes can carry an MQTT header announcing 200 MiB; only
// the MQTT frame cap stops the client allocating it.
func TestAnOversizedMQTTFrameOverWebSocketIsRefusedBeforeAllocation(t *testing.T) {
	up := websocket.Upgrader{Subprotocols: []string{"mqtt"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		if _, _, err := c.ReadMessage(); err != nil { // CONNECT
			return
		}
		if c.WriteMessage(websocket.BinaryMessage, oversizedCONNACK()) != nil {
			return
		}
		_, _, _ = c.ReadMessage() // hold the connection until the client goes away
	}))
	t.Cleanup(srv.Close)
	u := "ws://127.0.0.1:" + mustURL(t, srv.URL).Port() + "/mqtt"

	var err error
	grew := heapGrowth(func() {
		err = NewSender(guardAllowing("127.0.0.1/32")).Send(sendCtx(t, 3*time.Second),
			mqttTarget(t, u), []byte("x"), "k")
	})
	require.Error(t, err)
	assert.Less(t, grew, uint64(10<<20), "the send allocated %d bytes", grew)
}

// A connection the dial handed out is closed when the SEND ends, whatever the client above
// it does: here nobody calls Close, and the destination still sees the connection end.
func TestADialedConnectionClosesWhenTheSendEnds(t *testing.T) {
	ended := make(chan struct{})
	ln := listen(t, "tcp", "127.0.0.1:0", func(c net.Conn) {
		_, _ = io.Copy(io.Discard, c)
		close(ended)
	})
	s := NewSender(guardAllowing("127.0.0.1/32"))
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	log := &dialLog{ctx: ctx, cancel: cancel}
	// The client's own context never ends; only the send's does.
	conn, err := s.dial(log)(context.Background(), "tcp", "127.0.0.1:"+ln.port())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() }) // after the assertions, so a failure does not hang the listener

	cancel(nil)
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("the destination still holds the connection after the send ended")
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, err = conn.Read(make([]byte, 1))
	assert.ErrorIs(t, err, net.ErrClosed)
	_, err = conn.Write([]byte("x"))
	assert.ErrorIs(t, err, net.ErrClosed)
}

// The connect timeout is the time left in the send — never paho's 30 s default, which
// would outlive the send — and a send with no time left is not started.
func TestMQTTConnectTimeoutIsTheTimeLeft(t *testing.T) {
	s := NewSender(nil)
	const budget = 3 * time.Second
	ctx, cancel := context.WithCancelCause(sendCtx(t, budget))
	defer cancel(nil)
	log := &dialLog{ctx: ctx, cancel: cancel}
	opts, err := s.mqttOptions(ctx, log, mqttTarget(t, "tcp://127.0.0.1:1"))
	require.NoError(t, err)
	assert.LessOrEqual(t, opts.ConnectTimeout, budget)
	assert.Greater(t, opts.ConnectTimeout, budget-time.Second)
	assert.False(t, opts.AutoReconnect)
	assert.False(t, opts.ConnectRetry)
	assert.NotNil(t, opts.CustomOpenConnectionFn, "every connection comes from the guarded open function")

	spent, cancelSpent := context.WithDeadline(context.Background(), time.Now().Add(-time.Millisecond))
	defer cancelSpent()
	_, err = s.mqttOptions(spent, log, mqttTarget(t, "tcp://127.0.0.1:1"))
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

// Send refuses a context with no deadline: the deadline is what bounds the connect as well
// as the delivery, so without one a stalled destination would hold the worker forever.
func TestSendRequiresADeadline(t *testing.T) {
	_, ln := startBroker(t, "127.0.0.1:0")
	err := NewSender(guardAllowing("127.0.0.1/32")).Send(context.Background(),
		mqttTarget(t, "tcp://127.0.0.1:"+ln.port()), []byte("x"), "k")
	require.ErrorIs(t, err, ErrPublishConfig)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(0), ln.accepts.Load())
}
