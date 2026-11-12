// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphqlws

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

type lifecycleDialResult struct {
	client *Client
	err    error
}

func TestDialCancellationWhileAwaitingAck(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancelWithoutDeadline"
		if deadline {
			name = "callerDeadline"
		}
		t.Run(name, func(t *testing.T) {
			initialized := make(chan struct{})
			peerEnd := make(chan error, 1)
			url, shutdownPeer := lifecycleServer(t, func(conn *websocket.Conn) {
				var msg wsMessage
				if err := conn.ReadJSON(&msg); err != nil {
					peerEnd <- err
					return
				}
				close(initialized)
				// Withhold ack and wait for the client to close the socket.
				_, _, err := conn.ReadMessage()
				peerEnd <- err
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := context.Canceled
			if deadline {
				var expire context.CancelFunc
				ctx, expire = context.WithTimeout(ctx, 2*time.Second)
				defer expire()
				want = context.DeadlineExceeded
			}
			result := make(chan lifecycleDialResult, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				c, err := Dial(ctx, url, nil, WithAckTimeout(time.Hour))
				result <- lifecycleDialResult{c, err}
			}()
			t.Cleanup(func() {
				cancel()
				shutdownPeer() // Also releases Dial if its cancellation hook is removed.
				lifecycleReceive(t, done)
				select {
				case got := <-result:
					if got.client != nil {
						_ = got.client.Close()
					}
				default:
				}
			})
			lifecycleReceive(t, initialized)
			if deadline {
				lifecycleReceive(t, ctx.Done())
			} else {
				cancel()
			}
			got := lifecycleReceive(t, result)
			require.Nil(t, got.client)
			require.ErrorIs(t, got.err, want)
			require.Error(t, lifecycleReceive(t, peerEnd))
		})
	}
}

func TestDialCancelAfterAckKeepsClientAlive(t *testing.T) {
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
		if err := conn.WriteJSON(wsMessage{ID: msg.ID, Type: msgNext, Payload: json.RawMessage(`{"data":{"x":1}}`)}); err != nil {
			peerEnd <- err
			return
		}
		_, _, err := conn.ReadMessage()
		peerEnd <- err
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := Dial(ctx, url, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	cancel()
	sub, err := c.Subscribe(context.Background(), "subscription { x }", nil)
	require.NoError(t, err)
	data, ok := lifecycleReceiveSubscription(t, sub)
	require.True(t, ok)
	require.JSONEq(t, `{"x":1}`, string(data))
	_ = c.Close()
	require.ErrorIs(t, sub.Err(), ErrClientClosed)
	require.Error(t, lifecycleReceive(t, peerEnd))
}
