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
	cfg := config.NatsConfiguration{Hostname: "127.0.0.1", Port: 1}
	cfg.Auth.SysUser = "sys"
	cfg.Auth.SysPassword = "sys"

	conn, err := dialSystemAccount(context.Background(), cfg, 300*time.Millisecond)
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

	cfg := config.NatsConfiguration{Hostname: "127.0.0.1", Port: uint32(port)}
	cfg.Auth.SysUser = "sys"
	cfg.Auth.SysPassword = "sys"

	start := time.Now()
	conn, err := dialSystemAccount(context.Background(), cfg, 10*time.Second)
	require.NoError(t, err, "a reachable broker must still be dialled")
	require.NotNil(t, conn)
	defer conn.Close()
	require.True(t, conn.IsConnected(), "the dial must return a connection that has actually connected")
	require.Less(t, time.Since(start), 5*time.Second, "the wait must cost nothing on a healthy broker")
}

// TestTheDialWindowsAreTheOnesEveryCommentQuotes is the pin the rest of this file cannot
// be.
//
// 🔴 EVERY OTHER TEST HERE PASSES ITS OWN WAIT, so none of them reads the production
// value at all: setting systemAccountConnectWait to zero would leave the whole suite
// green while turning the dial back into the fail-open it replaced. The number is also
// quoted in three prose surfaces — the comment on startPresenceDemotion, the
// device-presence concept page in both locales, and the chart's values.yaml — so it is
// asserted here against a LITERAL, not against itself.
func TestTheDialWindowsAreTheOnesEveryCommentQuotes(t *testing.T) {
	require.Equal(t, 30*time.Second, systemAccountConnectWait,
		"the startup dial window is published as thirty seconds; change the prose with the number")
	require.Equal(t, 5*time.Second, systemAccountRecheckWait,
		"the recheck window bounds one dial per drain pass")
}

// TestTheRecheckWindowIsShorterThanTheStartupWindow pins the ordering that makes the
// recheck's restart converge instead of looping.
//
// 🔴 THE RECHECK'S "YES" CAUSES A PROCESS RESTART, and the restarted process re-decides
// with the STARTUP window. While the recheck window is the shorter of the two, any broker
// the recheck accepted is one the startup dial — given more time, not less — also accepts,
// so the restart ends the tap-less run. Invert them and a broker reachable in thirty
// seconds but not in five restarts this pod on every pass, forever.
func TestTheRecheckWindowIsShorterThanTheStartupWindow(t *testing.T) {
	require.Less(t, systemAccountRecheckWait, systemAccountConnectWait,
		"a recheck window at or above the startup window turns the restart into a loop")
}

// TestSystemAccountReachableAnswersAboutRightNow. The recheck is a dial rather than a
// cached flag precisely so it can change its answer; both answers are asserted, because a
// probe that only ever says one of them is indistinguishable from a constant.
func TestSystemAccountReachableAnswersAboutRightNow(t *testing.T) {
	defer shortenRecheckWait(t, 300*time.Millisecond)()

	dead := config.NatsConfiguration{Hostname: "127.0.0.1", Port: 1}
	require.False(t, systemAccountReachable(context.Background(), dead),
		"a port nothing is listening on must not read as reachable")

	live := config.NatsConfiguration{Hostname: "127.0.0.1", Port: uint32(runPlainBroker(t))}
	require.True(t, systemAccountReachable(context.Background(), live),
		"a broker that is up must read as reachable, or the recheck can never end a tap-less run")
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

// shortenRecheckWait is the one place the production window is swapped out, and only for
// the probe that reads it directly. The value itself is pinned by literal above.
func shortenRecheckWait(t *testing.T, d time.Duration) func() {
	t.Helper()
	prior := systemAccountRecheckWait
	systemAccountRecheckWait = d
	return func() { systemAccountRecheckWait = prior }
}
