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
	dtlsserver "github.com/plgd-dev/go-coap/v3/dtls/server"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/net/monitor/inactivity"
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
	// The ceiling is transiently exceeded by one (a handshake is accepted, then closed),
	// and the rejected conn's decrement is async, so assert the table SETTLES at/under the
	// ceiling rather than reading it at a single instant.
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(m.ActiveSessions) <= 2
	}, 5*time.Second, 20*time.Millisecond, "the live table must settle at or below the ceiling")
}

// TestEmptyKeyIdentityRejected mutation-guards the server's own len(key) > 0 check in
// the PSK callback: a credential map entry with an empty key must NOT authenticate,
// even though ResolveCredentials would normally never produce one (defense in depth for
// a directly-constructed Config).
func TestEmptyKeyIdentityRejected(t *testing.T) {
	s, m := startServer(t, Config{Credentials: map[string][]byte{testIdentity: {}}})

	conn, err := dialCoap(t, s.Addr().String(), testIdentity, testPSK)
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, gerr := conn.Get(ctx, healthPath)
		cancel()
		assert.Error(t, gerr)
		_ = conn.Close()
	}
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(m.HandshakeFailures) >= 1
	}, 5*time.Second, 20*time.Millisecond, "an identity mapped to an empty key must be refused")
	assert.Equal(t, float64(0), testutil.ToFloat64(m.Handshakes))
}

// TestDisableInactivityMonitorInstallsNilMonitor pins the C1 fix: go-coap's
// DefaultConfig ships a 16-second idle reaper that would silently sever a healthy but
// quiet device (defeating CID for queue-mode sleepers). When no idle timeout is
// configured, New installs disableInactivityMonitor, which must replace that default
// with a nil monitor. This asserts the override directly, without a 20-second wait.
func TestDisableInactivityMonitorInstallsNilMonitor(t *testing.T) {
	cfg := dtlsserver.DefaultConfig
	// The stock default is the 16s reaper, not a nil monitor.
	_, defaultIsNil := cfg.CreateInactivityMonitor().(*inactivity.NilMonitor[*udpclient.Conn])
	require.False(t, defaultIsNil, "sanity: go-coap's default should be a real (reaping) monitor")

	disableInactivityMonitor{}.DTLSServerApply(&cfg)
	_, ok := cfg.CreateInactivityMonitor().(*inactivity.NilMonitor[*udpclient.Conn])
	assert.True(t, ok, "with no idle timeout, reaping must be disabled via a nil monitor")
}

// TestIdleTimeoutReapsWhenConfigured proves the reaper works when an idle timeout IS
// configured: a session that carries no traffic is closed and the table drains.
func TestIdleTimeoutReapsWhenConfigured(t *testing.T) {
	s, m := startServer(t, Config{IdleTimeout: 1 * time.Second})

	conn, err := dialCoap(t, s.Addr().String(), testIdentity, testPSK)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, err = conn.Get(ctx, healthPath)
	cancel()
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	require.Eventually(t, func() bool {
		return testutil.ToFloat64(m.ActiveSessions) == 0
	}, 10*time.Second, 100*time.Millisecond, "an idle session must be reaped when a timeout is configured")
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
	var conns []*piondtls.Conn // tracked so stop() closes them (Close on the listener does not)
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
			conns = append(conns, dc)
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
	stop = func() {
		_ = l.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close() // unblocks the per-conn echo goroutine's Read
		}
	}
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
// anti-replay as the latest record (conn.go:1226, RFC 9146 §6). So an off-path attacker
// who replays a captured record — or forges one — from a spoofed source cannot redirect
// the session's downlink to a victim.
//
// The two attack datagrams here carry the session's REAL server-issued Connection ID
// (captured from the client's own last outbound record, which a passive observer sees
// in the clear), so both are ROUTED BY CID straight to the live session's conn — i.e.
// they reach the rebind logic, unlike a garbage CID that the listener would drop before
// it. There the replay fails anti-replay (its sequence number was already consumed) and
// the corrupted copy fails AEAD; neither sets the "valid latest CID record" condition,
// so neither moves the peer address. This exercises the property, rather than passing
// because the datagram never arrived.
func TestUnvalidatedRecordDoesNotRedirect(t *testing.T) {
	addr, _, serverRemote, stop := pionEchoServer(t, 8)
	defer stop()
	conn, rc := pionClient(t, addr, 8)
	defer conn.Close()

	require.True(t, exchange(t, conn, "establish"), "session should establish")
	require.Eventually(t, func() bool { return serverRemote() != nil }, 2*time.Second, 20*time.Millisecond)
	legit := serverRemote().String()

	record := rc.lastOutbound() // a real tls12_cid application record carrying the session's CID
	require.NotEmpty(t, record, "should have captured an outbound CID record")
	// Non-vacuity: content type 25 (tls12_cid) is what makes the listener route this
	// datagram to the live session by Connection ID rather than dropping it as an
	// unknown flow — so both attack datagrams genuinely reach the rebind logic.
	require.Equal(t, byte(25), record[0], "captured record must be a tls12_cid record so it routes by CID")
	corrupt := append([]byte(nil), record...)
	corrupt[len(corrupt)-1] ^= 0xFF // same CID → routed to the session; tail flip → AEAD fails

	spoof, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer spoof.Close()
	for i := 0; i < 5; i++ {
		_, _ = spoof.WriteTo(record, addr)  // replay: routes by CID, fails anti-replay
		_, _ = spoof.WriteTo(corrupt, addr) // forge: routes by CID, fails AEAD
		time.Sleep(20 * time.Millisecond)
	}

	// The legit session's peer address must be unchanged, and the legit client must
	// still round-trip — neither the replay nor the forgery redirected or wedged it.
	assert.Equal(t, legit, serverRemote().String(), "a replayed/forged record must not rebind the peer address")
	assert.True(t, exchange(t, conn, "still-alive"), "the legit session must remain usable")
}
