// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphqlws

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func lifecycleReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(terminalWait):
		t.Fatal("timed out waiting for lifecycle synchronization")
		var zero T
		return zero
	}
}

// Reuse the protocol fixture, but explicitly close and join the hijacked
// handler: httptest.Server.Close does not wait for WebSocket handlers.
func lifecycleServer(t *testing.T, script func(*websocket.Conn)) (string, func()) {
	t.Helper()
	connected := make(chan *websocket.Conn, 1)
	done := make(chan struct{})
	url := rawServer(t, func(conn *websocket.Conn) {
		defer close(done)
		connected <- conn
		_ = conn.SetReadDeadline(time.Now().Add(2 * terminalWait))
		_ = conn.SetWriteDeadline(time.Now().Add(2 * terminalWait))
		script(conn)
	})
	shutdown := func() {
		select {
		case conn := <-connected:
			_ = conn.Close()
			lifecycleReceive(t, done)
		default: // No upgrade occurred.
		}
	}
	t.Cleanup(shutdown)
	return url, shutdown
}

type observedCloseConn struct {
	net.Conn
	once   sync.Once
	closed chan struct{}
}

func (c *observedCloseConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.closed) })
	return err
}

func TestReadLoopFailureClosesConnection(t *testing.T) {
	for _, callerClose := range []bool{false, true} {
		name := "malformedJSON"
		if callerClose {
			name = "callerClose"
		}
		t.Run(name, func(t *testing.T) {
			active := make(chan struct{})
			peerEnd := make(chan error, 1)
			url, _ := lifecycleServer(t, func(conn *websocket.Conn) {
				var msg wsMessage
				if err := conn.ReadJSON(&msg); err != nil {
					peerEnd <- err
					return
				}
				if err := conn.WriteJSON(wsMessage{Type: msgConnectionAck}); err != nil {
					peerEnd <- err
					return
				}
				if err := conn.ReadJSON(&msg); err != nil {
					peerEnd <- err
					return
				}
				close(active)
				if !callerClose {
					if err := conn.WriteMessage(websocket.TextMessage, []byte("{invalid")); err != nil {
						peerEnd <- err
						return
					}
				}
				// Deliberately keep the peer open until the client closes it.
				_, _, err := conn.ReadMessage()
				peerEnd <- err
			})
			closed := make(chan struct{})
			dialer := &websocket.Dialer{NetDialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				return &observedCloseConn{Conn: conn, closed: closed}, nil
			}}
			c, err := Dial(dialCtx(t), url, nil, WithDialer(dialer))
			require.NoError(t, err)
			t.Cleanup(func() { _ = c.Close() })
			sub, err := c.Subscribe(context.Background(), "subscription { x }", nil)
			require.NoError(t, err)
			lifecycleReceive(t, active)
			if callerClose {
				require.NoError(t, c.Close())
			}
			_, ok := lifecycleReceiveSubscription(t, sub)
			require.False(t, ok)
			original := sub.Err()
			if callerClose {
				require.ErrorIs(t, original, ErrClientClosed)
			} else {
				var syntax *json.SyntaxError
				require.ErrorAs(t, original, &syntax)
			}
			lifecycleReceive(t, closed) // Must precede explicit caller cleanup.
			require.Error(t, lifecycleReceive(t, peerEnd))
			require.NoError(t, c.Close())
			require.Same(t, original, sub.Err())
		})
	}
}

func lifecycleReceiveSubscription(t *testing.T, sub *Subscription) (json.RawMessage, bool) {
	t.Helper()
	select {
	case data, ok := <-sub.C():
		return data, ok
	case <-time.After(terminalWait):
		t.Fatal("subscription did not terminate")
		return nil, false
	}
}
