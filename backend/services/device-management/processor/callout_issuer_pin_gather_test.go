// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// pingResponder answers $SYS.REQ.SERVER.PING the way one broker server does: a statsz document
// carrying its id and the cluster size it believes, after delay.
type pingResponder struct {
	id    string
	claim int
	delay time.Duration
}

// startPingServers runs an embedded server (an ordinary one, so any connection may use the
// subject) with one responder connection per entry, and returns a connection to ask from.
func startPingServers(t *testing.T, rs []pingResponder) *nats.Conn {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, NoLog: true, NoSigs: true})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(10*time.Second))
	t.Cleanup(srv.Shutdown)
	for _, r := range rs {
		nc, err := nats.Connect(srv.ClientURL())
		require.NoError(t, err)
		t.Cleanup(nc.Close)
		doc := map[string]any{"server": map[string]any{"id": r.id}, "statsz": map[string]any{"active_servers": r.claim}}
		body, err := json.Marshal(doc)
		require.NoError(t, err)
		r := r
		_, err = nc.Subscribe(issuerPinSysPing, func(m *nats.Msg) {
			go func() {
				time.Sleep(r.delay)
				_ = m.Respond(body)
			}()
		})
		require.NoError(t, err)
		require.NoError(t, nc.Flush())
	}
	ask, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(ask.Close)
	return ask
}

// 🔑 AN UNDER-COUNTED REPLY MUST NOT END THE GATHER. A freshly booted server counts a cluster of
// one for its first seconds, and the system connection reconnects to exactly such a server during
// a rolling restart. Its reply arrives first; the gather has to keep listening for the other two.
func TestSystemGatherWaitsPastAnUnderCountedFirstReply(t *testing.T) {
	a, b, c := newServerID(t), newServerID(t), newServerID(t)
	nc := startPingServers(t, []pingResponder{
		{id: a, claim: 1, delay: 0}, // the fresh pod: knows only itself
		{id: b, claim: 3, delay: 150 * time.Millisecond},
		{id: c, claim: 3, delay: 300 * time.Millisecond},
	})
	ids, err := NewSystemGather(nc, 2*time.Second)(context.Background())
	require.NoError(t, err)
	require.ElementsMatch(t, []string{a, b, c}, ids, "the gather stopped on the first server's claim of a cluster of one")
}

// When every reply agrees, there is nothing to wait for: the gather returns as soon as that many
// have answered rather than spending the window.
func TestSystemGatherReturnsEarlyWhenEveryReplyAgrees(t *testing.T) {
	a, b, c := newServerID(t), newServerID(t), newServerID(t)
	nc := startPingServers(t, []pingResponder{{id: a, claim: 3}, {id: b, claim: 3}, {id: c, claim: 3}})
	start := time.Now()
	ids, err := NewSystemGather(nc, 5*time.Second)(context.Background())
	require.NoError(t, err)
	require.ElementsMatch(t, []string{a, b, c}, ids)
	require.Less(t, time.Since(start), 2*time.Second, "agreeing replies still waited out the window")
}

// Claims that disagree are not an answer to "is everyone here": the window is spent in full.
func TestSystemGatherSpendsTheWindowWhenClaimsDisagree(t *testing.T) {
	a, b := newServerID(t), newServerID(t)
	nc := startPingServers(t, []pingResponder{{id: a, claim: 1}, {id: b, claim: 2}})
	start := time.Now()
	ids, err := NewSystemGather(nc, 700*time.Millisecond)(context.Background())
	require.NoError(t, err)
	require.ElementsMatch(t, []string{a, b}, ids)
	require.GreaterOrEqual(t, time.Since(start), 600*time.Millisecond, "disagreeing claims ended the wait early")
}

func TestSystemGatherNobodyAnsweringIsAnError(t *testing.T) {
	nc := startPingServers(t, nil)
	_, err := NewSystemGather(nc, 200*time.Millisecond)(context.Background())
	require.Error(t, err)
}
