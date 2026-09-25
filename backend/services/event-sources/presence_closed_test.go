// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These pin what a close of the system-account (presence) connection means, against a real
// broker that authenticates the system account. See sysConnGuard.
//
// Each waits for the guard's handler to have FINISHED (closes) before it reads liveness: a
// "still live" read taken before the asynchronous handler ran would pass whatever the handler
// does.

// authBroker is an embedded broker whose only user is the system account "sys" with password.
type authBroker struct {
	srv  *natsserver.Server
	opts *natsserver.Options
	cfg  config.NatsConfiguration
}

func runAuthBroker(t *testing.T, password string) *authBroker {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true,
		Users: []*natsserver.User{{Username: "sys", Password: password}}}
	srv, err := natsserver.NewServer(opts)
	require.NoError(t, err, "start an embedded broker")
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second), "embedded nats server not ready")
	t.Cleanup(srv.Shutdown)
	addr, ok := srv.Addr().(*net.TCPAddr)
	require.True(t, ok)
	cfg := config.NatsConfiguration{Hostname: "127.0.0.1", Port: uint32(addr.Port)}
	cfg.Auth.SysUser, cfg.Auth.SysPassword = "sys", password
	return &authBroker{srv: srv, opts: opts, cfg: cfg}
}

// revoke makes the broker stop accepting the system-account password, as a rotated
// credential the pod was not given would. The broker drops the connection, the client's
// reconnects are refused, and after the second identical refusal nats.go closes it for good.
func (b *authBroker) revoke(t *testing.T) {
	t.Helper()
	reload := *b.opts
	reload.Users = []*natsserver.User{{Username: "sys", Password: "rotated"}}
	require.NoError(t, b.srv.ReloadOptions(&reload))
}

// withLiveness installs a fresh Microservice whose liveness the test reads.
func withLiveness(t *testing.T) *core.Microservice {
	t.Helper()
	saved := Microservice
	t.Cleanup(func() { Microservice = saved })
	Microservice = &core.Microservice{FunctionalArea: "event-sources"}
	return Microservice
}

func dialGuarded(t *testing.T, cfg config.NatsConfiguration) (*nats.Conn, *sysConnGuard) {
	t.Helper()
	guard := &sysConnGuard{}
	conn, err := dialSystemAccount(context.Background(), cfg, 5*time.Second, guard)
	require.NoError(t, err)
	t.Cleanup(conn.Close)
	return conn, guard
}

func waitHandled(t *testing.T, g *sysConnGuard, n int32) {
	t.Helper()
	require.Eventually(t, func() bool { return g.closes.Load() >= n }, 20*time.Second, 10*time.Millisecond,
		"the ClosedHandler never finished")
}

// The defect: a broker that stops accepting the credential closes the tap's connection for
// good, and the pod stayed live and Ready with presence frozen. Now it is marked not live.
func TestAnUnrequestedCloseMarksNotLive(t *testing.T) {
	ms := withLiveness(t)
	b := runAuthBroker(t, "one")
	conn, guard := dialGuarded(t, b.cfg)
	guard.arm(conn)

	b.revoke(t)
	waitHandled(t, guard, 1)

	require.True(t, conn.IsClosed())
	err := ms.Live()
	require.Error(t, err, "a permanently closed presence connection must fail liveness")
	assert.Contains(t, err.Error(), "system-account (presence) connection closed permanently")
}

// The counterweight: the tap's own shutdown closes the connection, and that is not a fault.
func TestARequestedCloseDoesNotMarkNotLive(t *testing.T) {
	ms := withLiveness(t)
	b := runAuthBroker(t, "one")
	conn, guard := dialGuarded(t, b.cfg)
	guard.arm(conn)

	saved := brokerPresence
	t.Cleanup(func() { brokerPresence = saved })
	stopped := make(chan struct{})
	close(stopped)
	brokerPresence = &presenceRuntime{conn: conn, guard: guard, cancel: func() {}, stopped: stopped}

	stopBrokerPresence()
	waitHandled(t, guard, 1)

	require.True(t, conn.IsClosed(), "stopBrokerPresence must close the connection")
	require.NoError(t, ms.Live(), "the tap's own shutdown must not fail liveness")
}

// 🔴 THE CRASH-LOOP GUARD. With a refused credential nats.go closes the connection itself
// DURING the startup dial. That close must fail the dial (the tap then turns off), never
// liveness: judged a fault there, every restart would fail liveness for as long as the
// credential stayed refused.
func TestARefusedCredentialAtStartupDoesNotRestartThePod(t *testing.T) {
	ms := withLiveness(t)
	b := runAuthBroker(t, "one")
	cfg := b.cfg
	cfg.Auth.SysPassword = "wrong"

	guard := &sysConnGuard{}
	start := time.Now()
	conn, err := dialSystemAccount(context.Background(), cfg, 10*time.Second, guard)
	elapsed := time.Since(start)
	require.Nil(t, conn)
	require.Error(t, err, "a refused credential must fail the startup dial")
	assert.Contains(t, err.Error(), "closed", "the dial must say the library closed the connection")
	assert.Less(t, elapsed, 5*time.Second,
		"a connection the library closed must end the wait at once, not run out the window")

	waitHandled(t, guard, 1)
	require.NoError(t, ms.Live(), "a refused credential at startup must not fail liveness")
}

// The recheck dial must never carry the handler: a refused credential there is an answer
// ("not reachable"), not a fault. The guarded dial does carry it, which is the counterweight.
func TestTheRecheckDialCarriesNoClosedHandler(t *testing.T) {
	b := runAuthBroker(t, "one")

	conn, err := dialSystemAccount(context.Background(), b.cfg, 5*time.Second, nil)
	require.NoError(t, err)
	defer conn.Close()
	assert.Nil(t, conn.Opts.ClosedCB, "the recheck dial must install no ClosedHandler")

	guarded, _ := dialGuarded(t, b.cfg)
	assert.NotNil(t, guarded.Opts.ClosedCB, "the tap's dial must install the guard's handler")
}

// A close landing between the dial and arming is seen by the handler while unarmed and
// ignored; arming must judge it, or it would be missed for the pod's lifetime.
func TestACloseBeforeArmingIsJudgedAtArming(t *testing.T) {
	ms := withLiveness(t)
	b := runAuthBroker(t, "one")
	conn, guard := dialGuarded(t, b.cfg)

	conn.Close() // unrequested, unarmed
	waitHandled(t, guard, 1)
	require.NoError(t, ms.Live(), "an unarmed guard judges nothing")

	guard.arm(conn)
	require.Error(t, ms.Live(), "arming a connection that already closed must judge the close")
}

// Arming and the handler race when the close lands as the tap takes the connection over.
// Whichever order they run in, the close is judged, once.
func TestACloseRacingArmIsJudgedOnce(t *testing.T) {
	b := runAuthBroker(t, "one")
	for i := 0; i < 50; i++ {
		ms := withLiveness(t)
		conn, guard := dialGuarded(t, b.cfg)

		go conn.Close()
		guard.arm(conn)
		waitHandled(t, guard, 1)

		require.Truef(t, guard.judged.Load(), "iteration %d: the close was never judged", i)
		require.Errorf(t, ms.Live(), "iteration %d: a judged close must fail liveness", i)
	}
}
