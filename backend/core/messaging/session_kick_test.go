// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

func kickTestConn(t *testing.T) *nats.Conn {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second))
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

func quick(nc *nats.Conn) *SessionKickRequester {
	r := NewSessionKickRequester(nc, "inst-1")
	r.Attempts, r.Backoff, r.AttemptTimeout = 3, 5*time.Millisecond, time.Second
	return r
}

func TestSessionKickSubjectIsOutsideEveryStreamsCaptureSpace(t *testing.T) {
	require.Equal(t, "$DC.inst-1.session-kick", SessionKickSubject("inst-1"))
}

// Success is a CONFIRMED reply and nothing else.
func TestKickDeviceSessionsSucceedsOnlyOnAConfirmedReply(t *testing.T) {
	nc := kickTestConn(t)
	var got SessionKickRequest
	var answer atomic.Value
	answer.Store(SessionKickReply{OK: true, Kicked: 2})
	sub, err := nc.Subscribe(SessionKickSubject("inst-1"), func(m *nats.Msg) {
		require.NoError(t, json.Unmarshal(m.Data, &got))
		body, _ := json.Marshal(answer.Load().(SessionKickReply))
		_ = m.Respond(body)
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	require.NoError(t, nc.Flush())

	require.NoError(t, quick(nc).KickDeviceSessions(context.Background(), "acme", []string{"d1", "d2"}))
	require.Equal(t, SessionKickRequest{Tenant: "acme", Tokens: []string{"d1", "d2"}}, got)

	answer.Store(SessionKickReply{OK: false, Error: "only 1 of 2 servers answered"})
	err = quick(nc).KickDeviceSessions(context.Background(), "acme", []string{"d1"})
	require.ErrorIs(t, err, ErrSessionKickUnavailable)
	require.Contains(t, err.Error(), "only 1 of 2 servers answered")

	// An OK:false with no reason is still a failure.
	answer.Store(SessionKickReply{})
	require.ErrorIs(t, quick(nc).KickDeviceSessions(context.Background(), "acme", []string{"d1"}), ErrSessionKickUnavailable)
}

func TestKickDeviceSessionsFailsWhenNobodyAnswers(t *testing.T) {
	nc := kickTestConn(t)
	err := quick(nc).KickDeviceSessions(context.Background(), "acme", []string{"d1"})
	require.ErrorIs(t, err, ErrSessionKickUnavailable)
	require.Contains(t, err.Error(), "no service is answering")
}

func TestKickDeviceSessionsRetriesAFailedAttempt(t *testing.T) {
	nc := kickTestConn(t)
	var calls atomic.Int32
	sub, err := nc.Subscribe(SessionKickSubject("inst-1"), func(m *nats.Msg) {
		rep := SessionKickReply{Error: "not yet"}
		if calls.Add(1) == 3 {
			rep = SessionKickReply{OK: true}
		}
		body, _ := json.Marshal(rep)
		_ = m.Respond(body)
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	require.NoError(t, nc.Flush())
	require.NoError(t, quick(nc).KickDeviceSessions(context.Background(), "acme", []string{"d1"}))
	require.EqualValues(t, 3, calls.Load())

	// One attempt fewer than it takes is a failure, not a hang.
	calls.Store(0)
	r := quick(nc)
	r.Attempts = 2
	require.ErrorIs(t, r.KickDeviceSessions(context.Background(), "acme", []string{"d1"}), ErrSessionKickUnavailable)
}

func TestKickDeviceSessionsRefusesAMalformedRequestWithoutSendingIt(t *testing.T) {
	nc := kickTestConn(t)
	var calls atomic.Int32
	sub, err := nc.Subscribe(SessionKickSubject("inst-1"), func(*nats.Msg) { calls.Add(1) })
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	require.NoError(t, nc.Flush())

	r := quick(nc)
	for name, c := range map[string]struct {
		tenant string
		tokens []string
	}{
		"no tenant": {"", []string{"d1"}}, "no devices": {"acme", nil},
		"a wildcard token": {"acme", []string{"d.*"}}, "a bad tenant": {"a.b", []string{"d1"}},
	} {
		require.Error(t, r.KickDeviceSessions(context.Background(), c.tenant, c.tokens), name)
	}
	require.Zero(t, calls.Load())
}
