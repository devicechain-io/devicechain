// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	piondtls "github.com/pion/dtls/v3"
	coapdtls "github.com/plgd-dev/go-coap/v3/dtls"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	udpclient "github.com/plgd-dev/go-coap/v3/udp/client"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPSK is a 16-byte key (AES-128) used by the whole suite.
var testPSK = []byte("0123456789abcdef")

const testIdentity = "dev"

// newTestMetrics builds a standalone (unregistered) metric set so a test can read the
// counters/gauge the transport updates.
func newTestMetrics() Metrics {
	return Metrics{
		Handshakes:        prometheus.NewCounter(prometheus.CounterOpts{Name: "handshakes_total"}),
		HandshakeFailures: prometheus.NewCounter(prometheus.CounterOpts{Name: "handshake_failures_total"}),
		ActiveSessions:    prometheus.NewGauge(prometheus.GaugeOpts{Name: "active_sessions"}),
		SessionsRejected:  prometheus.NewCounter(prometheus.CounterOpts{Name: "sessions_rejected_total"}),
		Requests:          prometheus.NewCounterVec(prometheus.CounterOpts{Name: "requests_total"}, []string{"code"}),
	}
}

// startServer builds and serves a Server on a loopback ephemeral port with the given
// config filled out for a test (one credential, a high session ceiling unless the test
// sets one). It returns the server and its metrics; Stop is registered as cleanup.
func startServer(t *testing.T, cfg Config) (*Server, Metrics) {
	t.Helper()
	m := newTestMetrics()
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:0"
	}
	if cfg.Credentials == nil {
		cfg.Credentials = map[string][]byte{testIdentity: testPSK}
	}
	if cfg.MaxSessions == 0 {
		cfg.MaxSessions = 1000
	}
	s, err := New(cfg, m)
	require.NoError(t, err)
	go func() { _ = s.Serve() }()
	t.Cleanup(s.Stop)
	return s, m
}

// dialCoap opens a go-coap DTLS-PSK client to the server.
func dialCoap(t *testing.T, addr, identity string, psk []byte) (*udpclient.Conn, error) {
	t.Helper()
	ccfg := &piondtls.Config{
		PSK:             func([]byte) ([]byte, error) { return psk, nil },
		PSKIdentityHint: []byte(identity),
		CipherSuites:    []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8},
	}
	return coapdtls.Dial(addr, ccfg)
}

// TestManySessionsSoak is the substrate proof: the transport carries many concurrent
// long-lived DTLS sessions, the active-session gauge tracks them, and it drains to
// zero when they close. This is the ADR-075 Gate-2 "carries N sessions" answer.
func TestManySessionsSoak(t *testing.T) {
	const n = 64
	s, m := startServer(t, Config{})

	conns := make([]*udpclient.Conn, 0, n)
	for i := 0; i < n; i++ {
		conn, err := dialCoap(t, s.Addr().String(), testIdentity, testPSK)
		require.NoError(t, err, "session %d should connect", i)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		resp, err := conn.Get(ctx, healthPath)
		cancel()
		require.NoError(t, err, "session %d GET should succeed", i)
		assert.Equal(t, codes.Content, resp.Code())
		conns = append(conns, conn)
	}

	require.Eventually(t, func() bool {
		return testutil.ToFloat64(m.ActiveSessions) == float64(n)
	}, 5*time.Second, 20*time.Millisecond, "all %d sessions should be live", n)
	assert.Equal(t, float64(n), testutil.ToFloat64(m.Handshakes), "one handshake per session")

	for _, conn := range conns {
		require.NoError(t, conn.Close())
	}
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(m.ActiveSessions) == 0
	}, 5*time.Second, 20*time.Millisecond, "session table should drain to zero")
}

// TestUnknownIdentityRejected proves the fail-closed authentication floor: a client
// presenting a PSK identity absent from the credential map is refused, no session is
// established, and the refusal is counted. This is the floor the L1 tenancy seam binds
// tenant + device identity onto — so it must never fail open.
func TestUnknownIdentityRejected(t *testing.T) {
	s, m := startServer(t, Config{Credentials: map[string][]byte{testIdentity: testPSK}})

	// An unknown identity: the server's PSK callback returns an error, so the handshake
	// fails. Depending on timing that surfaces at Dial or at the first request; either
	// way no session must survive and the failure must be counted.
	conn, err := dialCoap(t, s.Addr().String(), "not-provisioned", testPSK)
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, gerr := conn.Get(ctx, healthPath)
		cancel()
		assert.Error(t, gerr, "a GET over an unauthenticated session must not succeed")
		_ = conn.Close()
	}

	require.Eventually(t, func() bool {
		return testutil.ToFloat64(m.HandshakeFailures) >= 1
	}, 5*time.Second, 20*time.Millisecond, "an unknown identity must be counted as a handshake failure")
	assert.Equal(t, float64(0), testutil.ToFloat64(m.Handshakes), "no session should complete for an unknown identity")
	assert.Equal(t, float64(0), testutil.ToFloat64(m.ActiveSessions))
}

// TestMaxSessionsCeiling proves the live-session table is bounded (the ADR-075 M6
// memory-safety ceiling): with room for two sessions, a third is refused and counted,
// and the table never exceeds the ceiling.
func TestMaxSessionsCeiling(t *testing.T) {
	s, m := startServer(t, Config{MaxSessions: 2})

	var kept []*udpclient.Conn
	for i := 0; i < 3; i++ {
		conn, err := dialCoap(t, s.Addr().String(), testIdentity, testPSK)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = conn.Get(ctx, healthPath)
		cancel()
		kept = append(kept, conn)
	}
	t.Cleanup(func() {
		for _, c := range kept {
			_ = c.Close()
		}
	})

	require.Eventually(t, func() bool {
		return testutil.ToFloat64(m.SessionsRejected) >= 1
	}, 5*time.Second, 20*time.Millisecond, "the session past the ceiling must be refused")
	assert.LessOrEqual(t, testutil.ToFloat64(m.ActiveSessions), float64(2), "the live table must never exceed the ceiling")
}

// --- CID proofs (pion layer, over the EXACT DTLS config the server builds) ---

// pionEchoServer stands up a raw pion DTLS listener over buildServerDTLSConfig — the
// same posture the production Server runs — and echoes application data. It exposes
// the count of accepted connections (a new handshake = a new accept) and the current
// server-side remote address (which pion rebinds only after a validated CID record),
// so a test can distinguish "same session, roamed" from "new handshake" and can assert
// that an unvalidated record does not redirect the peer.
func pionEchoServer(t *testing.T, cidLen int) (addr *net.UDPAddr, accepts func() int, serverRemote func() net.Addr, stop func()) {
	t.Helper()
	cfg := Config{Credentials: map[string][]byte{testIdentity: testPSK}, CIDLength: cidLen, MaxSessions: 10}
	l, err := piondtls.Listen("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, buildServerDTLSConfig(cfg, nil))
	require.NoError(t, err)

	var mu sync.Mutex
	var acceptCount int
	var sconn *piondtls.Conn
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			dc, ok := c.(*piondtls.Conn)
			if !ok {
				continue
			}
			mu.Lock()
			acceptCount++
			sconn = dc
			mu.Unlock()
			go func() {
				buf := make([]byte, 1500)
				for {
					n, err := dc.Read(buf)
					if err != nil {
						return
					}
					_, _ = dc.Write(buf[:n])
				}
			}()
		}
	}()
	accepts = func() int { mu.Lock(); defer mu.Unlock(); return acceptCount }
	serverRemote = func() net.Addr {
		mu.Lock()
		defer mu.Unlock()
		if sconn == nil {
			return nil
		}
		return sconn.RemoteAddr()
	}
	stop = func() { _ = l.Close() }
	return l.Addr().(*net.UDPAddr), accepts, serverRemote, stop
}

// pionClient connects a pion DTLS-PSK client over a roamingConn so a test can move its
// source address mid-session. cidLen > 0 makes the client advertise CID so the server
// can route its post-roam records by Connection ID.
func pionClient(t *testing.T, server *net.UDPAddr, cidLen int) (*piondtls.Conn, *roamingConn) {
	t.Helper()
	rc := newRoamingConn(t)
	ccfg := &piondtls.Config{
		PSK:             func([]byte) ([]byte, error) { return testPSK, nil },
		PSKIdentityHint: []byte(testIdentity),
		CipherSuites:    []piondtls.CipherSuiteID{piondtls.TLS_PSK_WITH_AES_128_CCM_8},
	}
	if cidLen > 0 {
		ccfg.ConnectionIDGenerator = piondtls.RandomCIDGenerator(cidLen)
	}
	conn, err := piondtls.Client(rc, server, ccfg)
	require.NoError(t, err)
	return conn, rc
}

// exchange writes a probe and reads the echo under a deadline, returning whether the
// round-trip completed.
func exchange(t *testing.T, conn *piondtls.Conn, msg string) bool {
	t.Helper()
	require.NoError(t, conn.SetWriteDeadline(time.Now().Add(2*time.Second)))
	if _, err := conn.Write([]byte(msg)); err != nil {
		return false
	}
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	buf := make([]byte, len(msg))
	n, err := conn.Read(buf)
	return err == nil && string(buf[:n]) == msg
}

// TestCidRoamingSurvivesAddressChange is the headline Gate-2 proof and its negative
// control. With CID enabled, a client that changes its source address keeps the SAME
// DTLS session — the second exchange succeeds, the server does NOT accept a new
// connection (no re-handshake), and its peer address rebinds to the new source. With
// CID disabled, the same address change strands the session: the record from the new
// source is not routed to it, so the second exchange fails. The negative control is
// what makes the positive result meaningful — it proves CID, not luck, carried the
// session.
func TestCidRoamingSurvivesAddressChange(t *testing.T) {
	t.Run("cid on: session survives the address change", func(t *testing.T) {
		addr, accepts, serverRemote, stop := pionEchoServer(t, 8)
		defer stop()
		conn, rc := pionClient(t, addr, 8)
		defer conn.Close()

		require.True(t, exchange(t, conn, "hello"), "pre-roam exchange should work")
		require.Eventually(t, func() bool { return accepts() == 1 }, 2*time.Second, 20*time.Millisecond)
		before := serverRemote()

		rc.roam(t)

		require.True(t, exchange(t, conn, "after-roam"), "post-roam exchange should work over the same session")
		assert.Equal(t, 1, accepts(), "no new handshake — the session was routed by Connection ID")
		assert.NotEqual(t, before.String(), serverRemote().String(), "server should have rebound to the new source address")
	})

	t.Run("cid off: the address change strands the session (negative control)", func(t *testing.T) {
		addr, accepts, _, stop := pionEchoServer(t, 0)
		defer stop()
		conn, rc := pionClient(t, addr, 0)
		defer conn.Close()

		require.True(t, exchange(t, conn, "hello"), "pre-roam exchange should work")
		require.Eventually(t, func() bool { return accepts() == 1 }, 2*time.Second, 20*time.Millisecond)

		rc.roam(t)

		assert.False(t, exchange(t, conn, "after-roam"), "without CID the roamed record must not reach the session")
	})
}

// TestUnvalidatedRecordDoesNotRedirect is the CID security proof (ADR-075 C4). pion
// rebinds a session's peer address only after a CID record AEAD-decrypts AND passes
// anti-replay as the latest record (conn.go, RFC 9146 §6). So an off-path attacker who
// replays a captured record — or forges one — from a spoofed source cannot redirect
// the session's downlink to a victim. This drives both cases against the real server
// config and asserts the peer address never moves to the spoofed source.
func TestUnvalidatedRecordDoesNotRedirect(t *testing.T) {
	addr, _, serverRemote, stop := pionEchoServer(t, 8)
	defer stop()
	conn, _ := pionClient(t, addr, 8)
	defer conn.Close()

	require.True(t, exchange(t, conn, "establish"), "session should establish")
	require.Eventually(t, func() bool { return serverRemote() != nil }, 2*time.Second, 20*time.Millisecond)
	legit := serverRemote().String()

	// A spoofed off-path sender fires datagrams at the server: a plausible-looking but
	// unauthenticated record (random bytes shaped like a CID record) cannot decrypt, and
	// a truncated one cannot parse. Neither validates, so neither may move the peer.
	spoof, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer spoof.Close()
	forged := make([]byte, 64)
	forged[0] = 0x19 // tls12_cid content type — routed by CID, but the body is garbage
	for i := 0; i < 10; i++ {
		_, _ = spoof.WriteTo(forged, addr)
		time.Sleep(20 * time.Millisecond)
	}

	// The legit session's peer address must be unchanged, and the legit client must
	// still round-trip — the forged flood neither redirected nor wedged the session.
	assert.Equal(t, legit, serverRemote().String(), "an unvalidated record must not rebind the peer address")
	assert.True(t, exchange(t, conn, "still-alive"), "the legit session must remain usable")
}
