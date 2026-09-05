// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// These pin the property the presence tap's dial had been ASSERTING and not doing.
//
// 🔴 THE COMMENT ON THE DIAL CLAIMED RetryOnFailedConnect PREVENTED "the tap is disabled
// for the pod's lifetime", and the next statement produced exactly that. Under that option
// nats.Connect hands back a non-nil conn and a NIL error against a broker that is not
// there, so the dial "succeeded", Tap.Subscribe's Flush timed out ten seconds later, and
// the tap was recorded as subscribe_failed — a replica-local reason, so no demotion ran
// and the whole fleet stayed ASSERTED behind a gauge naming the wrong cause.

// TestDialSystemAccountRefusesAConnectionThatNeverConnects is the case that matters. A
// port nothing is listening on is the whole fixture: RetryOnFailedConnect is what makes
// the naive version of this succeed.
func TestDialSystemAccountRefusesAConnectionThatNeverConnects(t *testing.T) {
	defer shortenConnectWait(t, 300*time.Millisecond)()

	cfg := config.NatsConfiguration{Hostname: "127.0.0.1", Port: 1}
	cfg.Auth.SysUser = "sys"
	cfg.Auth.SysPassword = "sys"

	conn, err := dialSystemAccount(context.Background(), cfg)
	require.Nil(t, conn, "a dial that never reached the broker must hand back no connection to subscribe on")
	require.Error(t, err, "a connection that never connected must be reported as a failure, not handed on "+
		"for Subscribe to discover ten seconds later as a flush timeout")
	require.Contains(t, err.Error(), "still", "the error must name the connection's status")
}

// TestDialSystemAccountAcceptsAReachableBroker is the counterweight. Refusing an
// unreachable broker is only safe while a healthy one still connects — and connects
// PROMPTLY, since this runs inside the service's Starter.
func TestDialSystemAccountAcceptsAReachableBroker(t *testing.T) {
	port := runPlainBroker(t)
	defer shortenConnectWait(t, 10*time.Second)()

	cfg := config.NatsConfiguration{Hostname: "127.0.0.1", Port: uint32(port)}
	cfg.Auth.SysUser = "sys"
	cfg.Auth.SysPassword = "sys"

	start := time.Now()
	conn, err := dialSystemAccount(context.Background(), cfg)
	require.NoError(t, err, "a reachable broker must still be dialled")
	require.NotNil(t, conn)
	defer conn.Close()
	require.True(t, conn.IsConnected(), "the dial must return a connection that has actually connected")
	require.Less(t, time.Since(start), 5*time.Second, "the wait must cost nothing on a healthy broker")
}

// TestTheDialWaitHonoursCancellation. The dial runs in the service's Starter, so a
// shutdown arriving during a broker outage must not be held for the whole window.
func TestTheDialWaitHonoursCancellation(t *testing.T) {
	conn := unreachableConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitConnected(ctx, conn, time.Hour)
	require.ErrorIs(t, err, context.Canceled,
		"a cancelled context must end the wait rather than hold shutdown for the full window")
}

// TestAFlushOnAnUnconnectedConnFailsWithoutNamingTheCause documents why the fix is at the
// DIAL rather than a reclassification of the flush: the flush does fail, but its error
// says nothing about the broker being unreachable, so no amount of reading it could have
// produced the right tap-off reason.
func TestAFlushOnAnUnconnectedConnFailsWithoutNamingTheCause(t *testing.T) {
	conn := unreachableConn(t)

	_, err := conn.Subscribe("$SYS.>", func(*nats.Msg) {})
	require.NoError(t, err, "subscribing on a still-dialling conn SUCCEEDS locally; only the flush fails")
	err = conn.FlushTimeout(300 * time.Millisecond)
	require.Error(t, err, "the flush is where a dead connection surfaces")
	require.False(t, strings.Contains(strings.ToLower(err.Error()), "connect"),
		"the flush error does not name the cause; that is why the dial has to detect it")
}

// unreachableConn is the library behaviour this whole file exists for, asserted rather
// than assumed: a dial at a port nothing is listening on returns a live-looking conn.
func unreachableConn(t *testing.T) *nats.Conn {
	t.Helper()
	conn, err := nats.Connect("nats://127.0.0.1:1",
		nats.RetryOnFailedConnect(true), nats.MaxReconnects(-1))
	require.NoError(t, err, "the fixture depends on RetryOnFailedConnect returning a conn and no error")
	t.Cleanup(conn.Close)
	require.False(t, conn.IsConnected(), "the fixture is wrong: this conn must NOT be connected")
	return conn
}

// runPlainBroker starts an in-process broker on an ephemeral port. It requires no
// credentials — the dial's UserInfo is simply ignored — because what is under test is
// whether the wait lets a reachable broker through, not how it authenticates.
func runPlainBroker(t *testing.T) int {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true,
	})
	require.NoError(t, err, "start an embedded broker")
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "embedded nats server not ready")
	t.Cleanup(srv.Shutdown)
	addr, ok := srv.Addr().(*net.TCPAddr)
	require.True(t, ok, "the embedded broker must be listening on TCP")
	return addr.Port
}

func shortenConnectWait(t *testing.T, d time.Duration) func() {
	t.Helper()
	prior := systemAccountConnectWait
	systemAccountConnectWait = d
	return func() { systemAccountConnectWait = prior }
}
