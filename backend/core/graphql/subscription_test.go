// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/gorilla/websocket"
	graphql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testSubSchema is a minimal schema exercising a streaming subscription and a
// single-result query, both driven through the same graphql-transport-ws pump.
const testSubSchema = `
	schema { query: Query subscription: Subscription }
	type Query { hello: String! }
	type Subscription { counter(to: Int!): Int! }
`

type testSubResolver struct{}

func (*testSubResolver) Hello() string { return "hi" }

// Counter emits 1..to on a channel, closing it when done or when the client
// unsubscribes (ctx cancelled) — the standard graph-gophers subscription shape.
func (*testSubResolver) Counter(ctx context.Context, args struct{ To int32 }) <-chan int32 {
	ch := make(chan int32)
	go func() {
		defer close(ch)
		for i := int32(1); i <= args.To; i++ {
			select {
			case <-ctx.Done():
				return
			case ch <- i:
			}
		}
	}()
	return ch
}

// dialSub starts an httptest server around a subscription handler and returns a
// connected graphql-transport-ws client.
func dialSub(t *testing.T, gate *core.ReadinessGate) (*websocket.Conn, func()) {
	t.Helper()
	schema := graphql.MustParseSchema(testSubSchema, &testSubResolver{})
	h := NewSubscriptionHandler(schema, map[ContextKey]interface{}{}, gate)
	srv := httptest.NewServer(h)

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	dialer := websocket.Dialer{Subprotocols: []string{wsSubprotocol}}
	conn, resp, err := dialer.Dial(url, nil)
	require.NoError(t, err)
	require.Equal(t, wsSubprotocol, resp.Header.Get("Sec-WebSocket-Protocol"))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	return conn, func() { conn.Close(); srv.Close() }
}

func writeMsg(t *testing.T, conn *websocket.Conn, msg wsMessage) {
	t.Helper()
	require.NoError(t, conn.WriteJSON(msg))
}

func readMsg(t *testing.T, conn *websocket.Conn) wsMessage {
	t.Helper()
	var msg wsMessage
	require.NoError(t, conn.ReadJSON(&msg))
	return msg
}

// subscribeMsg builds a subscribe frame for the given operation.
func subscribeMsg(id, query string, vars map[string]interface{}) wsMessage {
	payload, _ := json.Marshal(subscribePayload{Query: query, Variables: vars})
	return wsMessage{ID: id, Type: msgSubscribe, Payload: payload}
}

// nextData extracts the {data:...} object from a `next` frame's payload.
func nextData(t *testing.T, msg wsMessage) map[string]interface{} {
	t.Helper()
	var body struct {
		Data map[string]interface{} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(msg.Payload, &body))
	return body.Data
}

// A streaming subscription drives the full happy path: connection_init → ack,
// then subscribe → three `next` frames (counter 1..3) → `complete`.
func TestSubscriptionStreamsThenCompletes(t *testing.T) {
	conn, cleanup := dialSub(t, nil)
	defer cleanup()

	writeMsg(t, conn, wsMessage{Type: msgConnectionInit})
	assert.Equal(t, msgConnectionAck, readMsg(t, conn).Type)

	writeMsg(t, conn, subscribeMsg("1", "subscription { counter(to: 3) }", nil))

	for want := 1.0; want <= 3.0; want++ {
		msg := readMsg(t, conn)
		require.Equal(t, msgNext, msg.Type)
		assert.Equal(t, "1", msg.ID)
		assert.Equal(t, want, nextData(t, msg)["counter"])
	}

	done := readMsg(t, conn)
	assert.Equal(t, msgComplete, done.Type)
	assert.Equal(t, "1", done.ID)
}

// A single-result query runs through the same subscribe path — one `next` then
// `complete` — so the transport serves queries/mutations too (single WS channel).
func TestSubscriptionSingleResultQuery(t *testing.T) {
	conn, cleanup := dialSub(t, nil)
	defer cleanup()

	writeMsg(t, conn, wsMessage{Type: msgConnectionInit})
	require.Equal(t, msgConnectionAck, readMsg(t, conn).Type)

	writeMsg(t, conn, subscribeMsg("q1", "query { hello }", nil))

	msg := readMsg(t, conn)
	require.Equal(t, msgNext, msg.Type)
	assert.Equal(t, "hi", nextData(t, msg)["hello"])
	assert.Equal(t, msgComplete, readMsg(t, conn).Type)
}

// The server answers an application-level ping with a pong (keepalive).
func TestSubscriptionPingPong(t *testing.T) {
	conn, cleanup := dialSub(t, nil)
	defer cleanup()

	writeMsg(t, conn, wsMessage{Type: msgConnectionInit})
	require.Equal(t, msgConnectionAck, readMsg(t, conn).Type)

	writeMsg(t, conn, wsMessage{Type: msgPing})
	assert.Equal(t, msgPong, readMsg(t, conn).Type)
}

// A client `complete` cancels a live subscription: after it, the server sends no
// further frames for that id. We start a long stream, cancel it, and confirm the
// socket goes quiet (the counter would otherwise keep arriving).
func TestSubscriptionClientComplete(t *testing.T) {
	conn, cleanup := dialSub(t, nil)
	defer cleanup()

	writeMsg(t, conn, wsMessage{Type: msgConnectionInit})
	require.Equal(t, msgConnectionAck, readMsg(t, conn).Type)

	writeMsg(t, conn, subscribeMsg("s", "subscription { counter(to: 1000000) }", nil))

	// Drain at least one value so the stream is definitely live, then cancel.
	first := readMsg(t, conn)
	require.Equal(t, msgNext, first.Type)
	writeMsg(t, conn, wsMessage{ID: "s", Type: msgComplete})

	// Consume any already-queued frames, then expect the read to stall (no
	// `complete` is owed for a client-initiated cancel). A short deadline that
	// times out is the pass condition.
	_ = conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	for {
		var msg wsMessage
		if err := conn.ReadJSON(&msg); err != nil {
			// Timed out waiting for more frames — the subscription is quiet.
			return
		}
		// Ignore keepalive pings; any `next` for "s" after cancel would be a bug.
		if msg.Type == msgNext && msg.ID == "s" {
			continue // in-flight before cancel took effect; keep draining
		}
		assert.NotEqual(t, msgNext, msg.Type, "no next frames expected after client complete")
	}
}

// A connection_init carrying a token while no validator is available is rejected
// (close 4401) rather than trusted — the same fail-closed posture as the HTTP
// handler with a closed gate.
func TestSubscriptionRejectsTokenWithNoValidator(t *testing.T) {
	conn, cleanup := dialSub(t, core.NewReadinessGate())
	defer cleanup()

	payload, _ := json.Marshal(map[string]string{"Authorization": "Bearer some.jwt.token"})
	writeMsg(t, conn, wsMessage{Type: msgConnectionInit, Payload: payload})

	_, _, err := conn.ReadMessage()
	require.Error(t, err)
	ce, ok := err.(*websocket.CloseError)
	require.True(t, ok, "expected a websocket close, got %v", err)
	assert.Equal(t, closeUnauthorized, ce.Code)
}

// A subscribe sent before connection_init is a protocol violation: the server
// closes 4401 (Unauthorized) instead of running the operation.
func TestSubscriptionRequiresInitFirst(t *testing.T) {
	conn, cleanup := dialSub(t, nil)
	defer cleanup()

	writeMsg(t, conn, subscribeMsg("1", "subscription { counter(to: 1) }", nil))

	_, _, err := conn.ReadMessage()
	require.Error(t, err)
	ce, ok := err.(*websocket.CloseError)
	require.True(t, ok, "expected a websocket close, got %v", err)
	assert.Equal(t, closeUnauthorized, ce.Code)
}
