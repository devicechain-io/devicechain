// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphqlws

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gqlserver "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/gorilla/websocket"
	gqlgo "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- a real server handler to exercise the client against (no mock) ---------

const testSchema = `
	schema { query: Query subscription: Subscription }
	type Query { hello: String! }
	type Subscription { counter(to: Int!): Int! }
`

type testResolver struct{}

func (*testResolver) Hello() string { return "hi" }

func (*testResolver) Counter(ctx context.Context, args struct{ To int32 }) <-chan int32 {
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

// realServer stands up the actual core/graphql subscription handler around the
// test schema and returns its ws:// URL.
func realServer(t *testing.T) string {
	t.Helper()
	schema := gqlgo.MustParseSchema(testSchema, &testResolver{})
	h := gqlserver.NewSubscriptionHandler(schema, map[gqlserver.ContextKey]interface{}{}, nil)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// --- a hand-rolled server for protocol edges the real schema can't stage -----

func rawServer(t *testing.T, script func(conn *websocket.Conn)) string {
	t.Helper()
	up := websocket.Upgrader{
		Subprotocols: []string{wsSubprotocol},
		CheckOrigin:  func(*http.Request) bool { return true },
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		script(conn)
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func dialCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// drainCount ranges a subscription to completion in the background, reporting how
// many values arrived — so a test can await a terminal close without ever being
// the slow consumer that trips overflow.
func drainCount(sub *Subscription) <-chan int {
	done := make(chan int, 1)
	go func() {
		n := 0
		for range sub.C() {
			n++
		}
		done <- n
	}()
	return done
}

// readInt decodes a `next` data object of shape {"counter": N} or {"hello": ...}.
func readInt(t *testing.T, data json.RawMessage, field string) float64 {
	t.Helper()
	var m map[string]float64
	require.NoError(t, json.Unmarshal(data, &m))
	return m[field]
}

// --- happy paths, against the real handler -----------------------------------

func TestSubscribeStreamsThenCompletes(t *testing.T) {
	c, err := Dial(dialCtx(t), realServer(t), nil)
	require.NoError(t, err)
	defer c.Close()

	sub, err := c.Subscribe(context.Background(), "subscription { counter(to: 3) }", nil)
	require.NoError(t, err)

	var got []float64
	for data := range sub.C() {
		got = append(got, readInt(t, data, "counter"))
	}
	// A clean server complete leaves Err nil, and every emitted value arrived in
	// order — the completeness the liveness oracle relies on.
	assert.NoError(t, sub.Err())
	assert.Equal(t, []float64{1, 2, 3}, got)
}

func TestSingleResultQueryOverTransport(t *testing.T) {
	c, err := Dial(dialCtx(t), realServer(t), nil)
	require.NoError(t, err)
	defer c.Close()

	sub, err := c.Subscribe(context.Background(), "query { hello }", nil)
	require.NoError(t, err)

	var got []string
	for data := range sub.C() {
		var m map[string]string
		require.NoError(t, json.Unmarshal(data, &m))
		got = append(got, m["hello"])
	}
	assert.NoError(t, sub.Err())
	assert.Equal(t, []string{"hi"}, got)
}

func TestSubscribeAfterCloseFails(t *testing.T) {
	c, err := Dial(dialCtx(t), realServer(t), nil)
	require.NoError(t, err)
	require.NoError(t, c.Close())

	_, err = c.Subscribe(context.Background(), "query { hello }", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrClientClosed)
}

// --- protocol edges, against the raw server ----------------------------------

// The bearer from the TokenProvider rides the connection_init payload — the
// server's only authentication channel (it cannot read an HTTP header on a WS
// upgrade).
func TestBearerRidesConnectionInit(t *testing.T) {
	gotAuth := make(chan string, 1)
	url := rawServer(t, func(conn *websocket.Conn) {
		var m wsMessage
		if err := conn.ReadJSON(&m); err != nil {
			return
		}
		var p map[string]string
		_ = json.Unmarshal(m.Payload, &p)
		gotAuth <- p["Authorization"]
		_ = conn.WriteJSON(wsMessage{Type: msgConnectionAck})
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		_ = conn.ReadJSON(&m)
	})

	c, err := Dial(dialCtx(t), url, func(context.Context) (string, error) { return "tok-123", nil })
	require.NoError(t, err)
	defer c.Close()
	assert.Equal(t, "Bearer tok-123", <-gotAuth)
}

// A TokenProvider failure aborts the dial rather than connecting anonymously.
func TestTokenProviderErrorAbortsDial(t *testing.T) {
	url := rawServer(t, func(conn *websocket.Conn) {
		var m wsMessage
		_ = conn.ReadJSON(&m) // client closes before/at init
	})
	_, err := Dial(dialCtx(t), url, func(context.Context) (string, error) {
		return "", errors.New("no token today")
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "acquire token")
}

// An unauthorized close (4401) during the handshake surfaces as an error a
// caller can unwrap to a *websocket.CloseError — so the isolation/auth probes
// can tell "rejected" from "unreachable".
func TestDialUnauthorizedIsTypedCloseError(t *testing.T) {
	url := rawServer(t, func(conn *websocket.Conn) {
		var m wsMessage
		if err := conn.ReadJSON(&m); err != nil {
			return
		}
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(4401, "unauthorized"), time.Now().Add(time.Second))
	})

	_, err := Dial(dialCtx(t), url, func(context.Context) (string, error) { return "bad", nil })
	require.Error(t, err)
	var ce *websocket.CloseError
	require.True(t, errors.As(err, &ce), "want a *websocket.CloseError, got %v", err)
	assert.Equal(t, 4401, ce.Code)
}

func TestDialRejectsNonAckFirstFrame(t *testing.T) {
	url := rawServer(t, func(conn *websocket.Conn) {
		var m wsMessage
		if err := conn.ReadJSON(&m); err != nil {
			return
		}
		_ = conn.WriteJSON(wsMessage{Type: msgNext, ID: "x"})
	})
	_, err := Dial(dialCtx(t), url, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection_ack")
}

// An `error` frame terminates the subscription with a non-nil Err carrying the
// server's message — never a silent stop.
func TestErrorFrameIsTerminal(t *testing.T) {
	url := rawServer(t, func(conn *websocket.Conn) {
		var m wsMessage
		_ = conn.ReadJSON(&m) // init
		_ = conn.WriteJSON(wsMessage{Type: msgConnectionAck})
		var sub wsMessage
		if err := conn.ReadJSON(&sub); err != nil {
			return
		}
		payload, _ := json.Marshal([]gqlError{{Message: "boom"}})
		_ = conn.WriteJSON(wsMessage{ID: sub.ID, Type: msgError, Payload: payload})
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		_ = conn.ReadJSON(&m)
	})

	c, err := Dial(dialCtx(t), url, nil)
	require.NoError(t, err)
	defer c.Close()
	sub, err := c.Subscribe(context.Background(), "subscription { x }", nil)
	require.NoError(t, err)

	for range sub.C() { //nolint:revive // draining until terminal
	}
	require.Error(t, sub.Err())
	assert.Contains(t, sub.Err().Error(), "boom")
}

// A `next` frame that carries a non-empty GraphQL `errors` array (a mid-stream
// resolver failure) is terminal — it is surfaced through Err, never delivered as
// an empty/partial data object the consumer would misread as a real event.
func TestInBandErrorInNextIsTerminal(t *testing.T) {
	url := rawServer(t, func(conn *websocket.Conn) {
		var m wsMessage
		_ = conn.ReadJSON(&m)
		_ = conn.WriteJSON(wsMessage{Type: msgConnectionAck})
		var sub wsMessage
		if err := conn.ReadJSON(&sub); err != nil {
			return
		}
		payload, _ := json.Marshal(map[string]any{
			"data":   nil,
			"errors": []gqlError{{Message: "resolver blew up"}},
		})
		_ = conn.WriteJSON(wsMessage{ID: sub.ID, Type: msgNext, Payload: payload})
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		_ = conn.ReadJSON(&m)
	})

	c, err := Dial(dialCtx(t), url, nil)
	require.NoError(t, err)
	defer c.Close()
	sub, err := c.Subscribe(context.Background(), "subscription { x }", nil)
	require.NoError(t, err)

	var n int
	for range sub.C() {
		n++
	}
	assert.Zero(t, n, "the errored next must not be delivered as data")
	require.Error(t, sub.Err())
	assert.Contains(t, sub.Err().Error(), "resolver blew up")
}

// A connection drop mid-stream terminates every live subscription with a
// non-nil Err — the monitor must see the drop, not read it as "stream ended."
func TestConnectionDropIsTerminal(t *testing.T) {
	url := rawServer(t, func(conn *websocket.Conn) {
		var m wsMessage
		_ = conn.ReadJSON(&m)
		_ = conn.WriteJSON(wsMessage{Type: msgConnectionAck})
		var sub wsMessage
		if err := conn.ReadJSON(&sub); err != nil {
			return
		}
		data, _ := json.Marshal(map[string]any{"data": map[string]int{"counter": 1}})
		_ = conn.WriteJSON(wsMessage{ID: sub.ID, Type: msgNext, Payload: data})
		// return → close the socket abruptly (no complete)
	})

	c, err := Dial(dialCtx(t), url, nil)
	require.NoError(t, err)
	defer c.Close()
	sub, err := c.Subscribe(context.Background(), "subscription { counter(to: 100) }", nil)
	require.NoError(t, err)

	var n int
	for range sub.C() {
		n++
	}
	assert.Equal(t, 1, n, "the one delivered value arrives before the drop")
	require.Error(t, sub.Err())
	assert.False(t, errors.Is(sub.Err(), ErrSlowConsumer), "a drop is not a slow consumer")
}

// A consumer that never reads overflows its bounded buffer and ends with
// ErrSlowConsumer — an explicit, attributable "I fell behind," so the monitor
// never mistakes its own lag for a platform drop (§4.0).
func TestSlowConsumerOverflows(t *testing.T) {
	const flood = 200
	url := rawServer(t, func(conn *websocket.Conn) {
		var m wsMessage
		_ = conn.ReadJSON(&m)
		_ = conn.WriteJSON(wsMessage{Type: msgConnectionAck})
		var sub wsMessage
		if err := conn.ReadJSON(&sub); err != nil {
			return
		}
		data, _ := json.Marshal(map[string]any{"data": map[string]int{"counter": 1}})
		for i := 0; i < flood; i++ {
			if err := conn.WriteJSON(wsMessage{ID: sub.ID, Type: msgNext, Payload: data}); err != nil {
				return
			}
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		_ = conn.ReadJSON(&m)
	})

	c, err := Dial(dialCtx(t), url, nil, WithBuffer(2))
	require.NoError(t, err)
	defer c.Close()
	sub, err := c.Subscribe(context.Background(), "subscription { counter(to: 100000) }", nil)
	require.NoError(t, err)

	// Let the server flood and the read loop overflow before we read a thing.
	time.Sleep(150 * time.Millisecond)
	for range sub.C() { //nolint:revive // drain the buffered values
	}
	assert.ErrorIs(t, sub.Err(), ErrSlowConsumer)
}

// The server answers a client subscription; when the client Closes the sub, the
// server receives a `complete` frame for that id and the local channel ends
// cleanly (Err nil).
func TestClientCloseTellsServer(t *testing.T) {
	gotComplete := make(chan wsMessage, 1)
	url := rawServer(t, func(conn *websocket.Conn) {
		var m wsMessage
		_ = conn.ReadJSON(&m)
		_ = conn.WriteJSON(wsMessage{Type: msgConnectionAck})
		var sub wsMessage
		if err := conn.ReadJSON(&sub); err != nil {
			return
		}
		var fin wsMessage
		if err := conn.ReadJSON(&fin); err != nil {
			return
		}
		gotComplete <- fin
	})

	c, err := Dial(dialCtx(t), url, nil)
	require.NoError(t, err)
	defer c.Close()
	sub, err := c.Subscribe(context.Background(), "subscription { counter(to: 100) }", nil)
	require.NoError(t, err)

	require.NoError(t, sub.Close())
	// Local channel ends cleanly.
	for range sub.C() { //nolint:revive
	}
	assert.NoError(t, sub.Err())

	select {
	case fin := <-gotComplete:
		assert.Equal(t, msgComplete, fin.Type)
		assert.Equal(t, sub.id, fin.ID)
	case <-time.After(2 * time.Second):
		t.Fatal("server never received the complete frame")
	}
}

// A: Client.Close terminates every live subscription with ErrClientClosed — a
// deterministic attribution the safety monitor relies on at end-of-run. Before
// the Close/readLoop reorder this was a reproduced flake; stress it.
func TestClientCloseTerminatesLiveSubsWithClientClosed(t *testing.T) {
	c, err := Dial(dialCtx(t), realServer(t), nil)
	require.NoError(t, err)
	sub, err := c.Subscribe(context.Background(), "subscription { counter(to: 1000000) }", nil)
	require.NoError(t, err)

	done := drainCount(sub) // drain concurrently so we never overflow first
	time.Sleep(20 * time.Millisecond)
	require.NoError(t, c.Close())
	<-done
	assert.ErrorIs(t, sub.Err(), ErrClientClosed)
}

// B: a server that upgrades then never acks fails Dial within the ack timeout —
// not a hang. Mutation-relevant: the dial ctx bounds only the HTTP handshake, so
// the ack read deadline is the ONLY thing bounding this; without it Dial hangs
// forever against a wedged endpoint.
func TestDialAckTimeout(t *testing.T) {
	url := rawServer(t, func(conn *websocket.Conn) {
		var m wsMessage
		_ = conn.ReadJSON(&m) // read init, then go silent — never ack
		time.Sleep(2 * time.Second)
	})
	start := time.Now()
	_, err := Dial(dialCtx(t), url, nil, WithAckTimeout(200*time.Millisecond))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection_ack")
	assert.Less(t, time.Since(start), 1500*time.Millisecond, "must time out, not hang for the ack")
}

// C: THE core anti-fail-open guard. A server that acks then goes permanently
// silent (a hung-but-open TCP connection) must terminate the subscription via the
// steady-state read deadline — otherwise the monitor sits in "no events = no
// problems" limbo forever, the exact §4.0 failure. The terminal error must be a
// real drop, never a slow-consumer (nothing overflowed) and never a clean end.
func TestSilentPeerTerminatesSubscription(t *testing.T) {
	url := rawServer(t, func(conn *websocket.Conn) {
		var m wsMessage
		_ = conn.ReadJSON(&m)
		_ = conn.WriteJSON(wsMessage{Type: msgConnectionAck})
		var sub wsMessage
		_ = conn.ReadJSON(&sub)
		time.Sleep(3 * time.Second) // hold the socket open, send nothing
	})

	c, err := Dial(dialCtx(t), url, nil, WithReadTimeout(300*time.Millisecond))
	require.NoError(t, err)
	defer c.Close()
	sub, err := c.Subscribe(context.Background(), "subscription { x }", nil)
	require.NoError(t, err)

	start := time.Now()
	<-drainCount(sub)
	require.Error(t, sub.Err(), "a silent peer must not leave the stream open forever")
	assert.False(t, errors.Is(sub.Err(), ErrSlowConsumer), "nothing overflowed — this is a drop")
	assert.Less(t, time.Since(start), 2*time.Second)
}

// D: frames are routed by id. Completing one subscription must not touch another
// live one on the same client. A finish/deliver that ignored msg.ID would pass
// every single-subscription test but fail this.
func TestMultiplexedSubscriptionsRouteById(t *testing.T) {
	url := rawServer(t, func(conn *websocket.Conn) {
		var m wsMessage
		_ = conn.ReadJSON(&m)
		_ = conn.WriteJSON(wsMessage{Type: msgConnectionAck})
		var s1, s2 wsMessage
		_ = conn.ReadJSON(&s1)
		_ = conn.ReadJSON(&s2)
		// Complete s1 immediately; stream three values to s2 then complete it.
		_ = conn.WriteJSON(wsMessage{ID: s1.ID, Type: msgComplete})
		data, _ := json.Marshal(map[string]any{"data": map[string]int{"n": 7}})
		for i := 0; i < 3; i++ {
			_ = conn.WriteJSON(wsMessage{ID: s2.ID, Type: msgNext, Payload: data})
		}
		_ = conn.WriteJSON(wsMessage{ID: s2.ID, Type: msgComplete})
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		_ = conn.ReadJSON(&m)
	})

	c, err := Dial(dialCtx(t), url, nil)
	require.NoError(t, err)
	defer c.Close()
	sub1, err := c.Subscribe(context.Background(), "subscription { a }", nil)
	require.NoError(t, err)
	sub2, err := c.Subscribe(context.Background(), "subscription { b }", nil)
	require.NoError(t, err)

	d1, d2 := drainCount(sub1), drainCount(sub2)
	n1, n2 := <-d1, <-d2
	assert.NoError(t, sub1.Err())
	assert.Zero(t, n1, "the completed sub delivered nothing")
	assert.NoError(t, sub2.Err())
	assert.Equal(t, 3, n2, "the other sub streamed independently")
}

// E: a mid-stream server close code (e.g. 4429) surfaces through sub.Err() as an
// errors.As-able *websocket.CloseError — so a probe can read WHY the platform
// dropped it, not just that it did.
func TestMidStreamCloseCodeSurfaces(t *testing.T) {
	url := rawServer(t, func(conn *websocket.Conn) {
		var m wsMessage
		_ = conn.ReadJSON(&m)
		_ = conn.WriteJSON(wsMessage{Type: msgConnectionAck})
		var sub wsMessage
		_ = conn.ReadJSON(&sub)
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(4429, "too many"), time.Now().Add(time.Second))
	})

	c, err := Dial(dialCtx(t), url, nil)
	require.NoError(t, err)
	defer c.Close()
	sub, err := c.Subscribe(context.Background(), "subscription { x }", nil)
	require.NoError(t, err)

	<-drainCount(sub)
	require.Error(t, sub.Err())
	var ce *websocket.CloseError
	require.True(t, errors.As(sub.Err(), &ce), "want *websocket.CloseError, got %v", sub.Err())
	assert.Equal(t, 4429, ce.Code)
}

// The client answers a server ping with a pong carrying the same payload — the
// keepalive that survives idle proxies during a long, quiet subscription.
func TestServerPingAnswered(t *testing.T) {
	gotPong := make(chan wsMessage, 1)
	url := rawServer(t, func(conn *websocket.Conn) {
		var m wsMessage
		_ = conn.ReadJSON(&m)
		_ = conn.WriteJSON(wsMessage{Type: msgConnectionAck})
		_ = conn.WriteJSON(wsMessage{Type: msgPing, Payload: json.RawMessage(`{"t":1}`)})
		var pong wsMessage
		if err := conn.ReadJSON(&pong); err != nil {
			return
		}
		gotPong <- pong
	})

	c, err := Dial(dialCtx(t), url, nil)
	require.NoError(t, err)
	defer c.Close()

	select {
	case pong := <-gotPong:
		assert.Equal(t, msgPong, pong.Type)
		assert.JSONEq(t, `{"t":1}`, string(pong.Payload))
	case <-time.After(2 * time.Second):
		t.Fatal("client never answered the ping")
	}
}
