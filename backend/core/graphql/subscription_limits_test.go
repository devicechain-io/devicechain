// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
)

// limitsSchema has all three roots, so a test can prove which of them the socket
// will run.
const limitsSchema = `
	schema { query: Query mutation: Mutation subscription: Subscription }
	type Query { hello: String! }
	type Mutation { doIt: String! }
	type Subscription { ticks: Int! }
`

// limitsResolver counts every resolver call and every live tick stream.
//
// The counters are the measurement: "the mutation did not run" is asserted as the
// mutation resolver's call count being zero, not as the absence of a `next` frame —
// a frame can be missing for many reasons, a resolver that never ran for one.
type limitsResolver struct {
	queries   atomic.Int64
	mutations atomic.Int64
	live      atomic.Int64
}

func (r *limitsResolver) Hello() string {
	r.queries.Add(1)
	return "hi"
}

func (r *limitsResolver) DoIt() string {
	r.mutations.Add(1)
	return "done"
}

// Ticks emits an increasing counter every 50ms until its context ends.
func (r *limitsResolver) Ticks(ctx context.Context) <-chan int32 {
	ch := make(chan int32)
	r.live.Add(1)
	go func() {
		defer r.live.Add(-1)
		defer close(ch)
		tk := time.NewTicker(50 * time.Millisecond)
		defer tk.Stop()
		for i := int32(1); ; i++ {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
			}
			select {
			case <-ctx.Done():
				return
			case ch <- i:
			}
		}
	}()
	return ch
}

// serveLimits runs a subscription handler over limitsSchema and returns it, its
// resolver and a ws:// URL for it.
func serveLimits(t *testing.T, gate *core.ReadinessGate) (*SubscriptionHandler, *limitsResolver, string) {
	t.Helper()
	res := &limitsResolver{}
	h := NewSubscriptionHandler(MustParseSchema(limitsSchema, res), map[ContextKey]interface{}{}, gate)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return h, res, "ws" + strings.TrimPrefix(srv.URL, "http")
}

// dialInit dials url, sends connection_init with params (nil for none) and
// requires the ack.
func dialInit(t *testing.T, url string, params map[string]string) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{Subprotocols: []string{wsSubprotocol}}
	conn, _, err := dialer.Dial(url, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	init := wsMessage{Type: msgConnectionInit}
	if params != nil {
		init.Payload, _ = json.Marshal(params)
	}
	writeMsg(t, conn, init)
	require.Equal(t, msgConnectionAck, readMsg(t, conn).Type)
	return conn
}

// namedSubscribeMsg is subscribeMsg with an operationName.
func namedSubscribeMsg(id, query, operationName string) wsMessage {
	payload, _ := json.Marshal(subscribePayload{Query: query, OperationName: operationName})
	return wsMessage{ID: id, Type: msgSubscribe, Payload: payload}
}

// errorMessages decodes an `error` frame's payload into its messages.
func errorMessages(t *testing.T, msg wsMessage) []string {
	t.Helper()
	var errs []struct {
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(msg.Payload, &errs), "error payload %s", msg.Payload)
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Message)
	}
	return out
}

// A socket authenticated with a token ends when that token expires: with a 4401
// close, with no `complete` for the live stream ahead of it, and with every
// goroutine the connection owned unwound.
//
// The access TTL is 2s because a JWT's exp is whole seconds: the real exp is the
// issue time plus 2s, truncated, so it lands between 1 and 2 seconds out.
func TestSubscriptionClosesAtTokenExpiry(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	issuer := auth.NewIssuer(priv, "test", 2*time.Second, time.Hour)
	gate := core.NewReadinessGate()
	require.True(t, gate.MarkReady(auth.NewValidator(&priv.PublicKey)))

	tok, err := issuer.IssueAccess("acme", "alice", []string{"admin"}, nil, "jti-1")
	require.NoError(t, err)
	exp := tok.ExpiresAt.Truncate(time.Second) // what the signed claim actually carries

	h, res, url := serveLimits(t, gate)
	conn := dialInit(t, url, map[string]string{"Authorization": "Bearer " + tok.Token})
	_ = conn.SetReadDeadline(exp.Add(5 * time.Second))

	writeMsg(t, conn, subscribeMsg("s", "subscription { ticks }", nil))

	nexts := 0
	var closeErr *websocket.CloseError
	for {
		var msg wsMessage
		err := conn.ReadJSON(&msg)
		if err != nil {
			require.True(t, errors.As(err, &closeErr), "the socket ended with %v, want a close frame", err)
			break
		}
		switch msg.Type {
		case msgNext:
			nexts++
		case msgComplete:
			t.Fatalf("a `complete` for %q arrived before the close: a client reads that as a clean, "+
				"final end and never acts on the 4401 that says to come back with a fresh token", msg.ID)
		default:
			t.Fatalf("unexpected %s frame: %s", msg.Type, msg.Payload)
		}
	}
	closedAt := time.Now()

	assert.Equal(t, closeUnauthorized, closeErr.Code)
	assert.Equal(t, "token expired", closeErr.Text)
	assert.Greater(t, nexts, 0, "the stream never delivered, so this proves nothing about ending one")
	// Not early: the connection lives as long as its token, not less.
	assert.False(t, closedAt.Before(exp.Add(-100*time.Millisecond)),
		"closed at %s, before the token's exp %s", closedAt.Format(time.RFC3339Nano), exp.Format(time.RFC3339Nano))
	// Not late: the bound is generous for a loaded runner, but it is a bound.
	assert.False(t, closedAt.After(exp.Add(3*time.Second)),
		"closed at %s, long after the token's exp %s", closedAt.Format(time.RFC3339Nano), exp.Format(time.RFC3339Nano))

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && (h.liveConnections() != 0 || res.live.Load() != 0) {
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, 0, h.liveConnections(), "the connection's goroutines did not unwind")
	assert.Equal(t, int64(0), res.live.Load(), "the tick stream is still running after its socket closed")
}

// An anonymous connection has no token to outlive, so nothing arms a lifetime and
// the socket stays usable. (It also carries no tenant, so every tenant-scoped
// resolver refuses it already.)
func TestAnonymousSubscriptionHasNoLifetime(t *testing.T) {
	_, _, url := serveLimits(t, nil)
	conn := dialInit(t, url, nil)
	writeMsg(t, conn, subscribeMsg("s", "subscription { ticks }", nil))
	require.Equal(t, msgNext, readMsg(t, conn).Type)
}

// The WebSocket runs subscriptions and nothing else. graphql-go's Subscribe would run
// a query or a mutation to completion on the connect-time claims; each of these is
// refused with the transport's own message, the resolvers behind them never run, and
// the socket stays open for the subscription that follows.
//
// This REPLACES the old TestSubscriptionSingleResultQuery, which pinned the opposite
// contract: that a query over this socket returned one `next` and a `complete`.
func TestSubscriptionRejectsQueriesAndMutations(t *testing.T) {
	_, res, url := serveLimits(t, nil)
	conn := dialInit(t, url, nil)

	refused := []struct {
		id, query, operationName string
	}{
		{"query", "query { hello }", ""},
		{"named-query", "query Q { hello }", ""},
		{"shorthand", "{ hello }", ""},
		{"mutation", "mutation { doIt }", ""},
		{"selected-mutation", "subscription S { ticks } mutation M { doIt }", "M"},
		{"mutation-after-a-comment", "# subscription { ticks }\nmutation { doIt }", ""},
	}
	for _, tc := range refused {
		writeMsg(t, conn, namedSubscribeMsg(tc.id, tc.query, tc.operationName))
		msg := readMsg(t, conn)
		require.Equal(t, tc.id, msg.ID, "%s: frame for the wrong operation", tc.id)
		require.Equal(t, msgError, msg.Type, "%s: got %s %s", tc.id, msg.Type, msg.Payload)
		assert.Equal(t, []string{refusedOperationMessage}, errorMessages(t, msg), "%s", tc.id)
	}
	assert.Equal(t, int64(0), res.queries.Load(), "a query resolver ran over the WebSocket")
	assert.Equal(t, int64(0), res.mutations.Load(), "a mutation resolver ran over the WebSocket")

	// The same socket still streams, including the subscription picked out of a
	// document that also holds a mutation.
	writeMsg(t, conn, namedSubscribeMsg("sub", "subscription S { ticks } mutation M { doIt }", "S"))
	msg := readMsg(t, conn)
	require.Equal(t, msgNext, msg.Type, "got %s %s", msg.Type, msg.Payload)
	assert.Equal(t, "sub", msg.ID)
	assert.Equal(t, 1.0, nextData(t, msg)["ticks"])
	assert.Equal(t, int64(0), res.mutations.Load())
}

// A document the gate cannot read is refused with the reader's own syntax error, not
// the subscriptions-only message: that message tells the client to send the operation
// over HTTP, which is false advice for a subscription carrying a `//` comment. The
// refusal is an `error` frame for that operation alone; the socket stays open and the
// next subscription on it streams.
func TestSubscriptionRefusesAnUnreadableDocumentWithItsSyntaxError(t *testing.T) {
	_, res, url := serveLimits(t, nil)
	conn := dialInit(t, url, nil)

	unreadable := []struct{ id, query, operationName, reason string }{
		{"go-comment", "// x\nsubscription { ticks }", "", "comment is not accepted"},
		{"escaped-block-quote", `subscription { ticks } """x\"""`, "", `closing """ follows a backslash`},
		{"unterminated", "query { hello", "", "syntax error"},
		// graphql-go requires at least one selection; the old top-level walk did not.
		{"empty-selection", "subscription { }", "", "syntax error"},
		{"no-such-operation", "subscription S { ticks }", "T", `no operation is named "T"`},
	}
	for _, tc := range unreadable {
		writeMsg(t, conn, namedSubscribeMsg(tc.id, tc.query, tc.operationName))
		msg := readMsg(t, conn)
		require.Equal(t, tc.id, msg.ID, "%s: frame for the wrong operation", tc.id)
		require.Equal(t, msgError, msg.Type, "%s: got %s %s", tc.id, msg.Type, msg.Payload)
		msgs := errorMessages(t, msg)
		require.Len(t, msgs, 1, "%s", tc.id)
		assert.NotEqual(t, refusedOperationMessage, msgs[0], "%s", tc.id)
		assert.Contains(t, msgs[0], tc.reason, "%s", tc.id)
	}
	assert.Equal(t, int64(0), res.queries.Load())
	assert.Equal(t, int64(0), res.live.Load(), "a refused document started a stream")

	writeMsg(t, conn, subscribeMsg("sub", "subscription { ticks }", nil))
	msg := readMsg(t, conn)
	require.Equal(t, msgNext, msg.Type, "got %s %s", msg.Type, msg.Payload)
	assert.Equal(t, "sub", msg.ID)
}

// 🔴 THE GATE CHECKS THE LENGTH BEFORE IT READS. A WebSocket frame may be far larger
// than the query-length ceiling, and the gate is the first thing to read the document,
// so a gate that parsed first would walk a frame-sized document that the ceiling
// exists to refuse unread. A document over the ceiling that is otherwise a valid
// subscription is refused by the gate itself — an `error` frame, not the `next`
// carrying the error that Schema.Subscribe's own check would produce — and starts no
// stream. The schema is built by MustParseSchema, so the ceiling is the one a service
// resolves from its environment.
func TestSubscriptionGateChecksTheLengthFirst(t *testing.T) {
	t.Setenv(EnvGraphQLMaxQueryLength, "64")
	_, res, url := serveLimits(t, nil)
	conn := dialInit(t, url, nil)

	doc := "subscription { ticks } #"
	doc += strings.Repeat("x", 65-len(doc))
	require.Len(t, doc, 65)
	writeMsg(t, conn, subscribeMsg("long", doc, nil))
	msg := readMsg(t, conn)
	require.Equal(t, msgError, msg.Type, "got %s %s", msg.Type, msg.Payload)
	assert.Equal(t, []string{"query length 65 exceeds the maximum allowed query length of 64 bytes"},
		errorMessages(t, msg))
	assert.Equal(t, int64(0), res.live.Load())

	// The counterweight: the same subscription within the ceiling streams.
	writeMsg(t, conn, subscribeMsg("short", doc[:64], nil))
	msg = readMsg(t, conn)
	require.Equal(t, msgNext, msg.Type, "got %s %s", msg.Type, msg.Payload)
}

// The gate reads with the ceiling the served schema carries, and every served schema
// is built by MustParseSchema. A zero there would refuse every subscription — closed,
// but an outage — so the handler the GraphQL server really builds is read for its
// value.
func TestServedSubscriptionHandlerCarriesTheQueryLengthCeiling(t *testing.T) {
	gql, _, _ := startDrainServer(t, 0)
	require.NotNil(t, gql.subscriptions)
	assert.Equal(t, DefaultGraphQLMaxQueryLength, gql.subscriptions.Schema.maxQueryLength)
}

// Once the server has decided to end a connection, a pump whose operation then ends
// must not report `complete`. This drives that pump path directly, in the order the
// production paths never use — the operation cancelled while the socket is still
// open — so the only thing standing between it and a `complete` on the wire is the
// closing mark. The hook says when the pump has finished, so the check that follows is
// not a race against it: a pong is the next frame, or a `complete` was written first.
func TestClosingConnectionSendsNoComplete(t *testing.T) {
	exited := make(chan string, 1)
	res := &drainResolver{}
	h := NewSubscriptionHandler(MustParseSchema(drainSchema, res), nil, nil)
	h.pumpExited = func(id string) { exited <- id }
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialInit(t, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	writeMsg(t, conn, subscribeMsg("s", "subscription { ticker }", nil))
	require.Equal(t, msgNext, readMsg(t, conn).Type)

	h.mu.Lock()
	require.Len(t, h.conns, 1)
	var c *wsConnection
	for live := range h.conns {
		c = live
	}
	h.mu.Unlock()

	// What terminate does first, and then a cancel that reaches the pump before any
	// close frame does.
	c.closing.Store(true)
	c.mu.Lock()
	cancel := c.ops["s"]
	c.mu.Unlock()
	require.NotNil(t, cancel)
	cancel()

	select {
	case id := <-exited:
		require.Equal(t, "s", id)
	case <-time.After(5 * time.Second):
		t.Fatal("the pump did not exit after its operation was cancelled")
	}

	writeMsg(t, conn, wsMessage{Type: msgPing})
	got := readMsg(t, conn)
	assert.Equal(t, msgPong, got.Type,
		"the frame after the pump exited was %s %q: a closing connection reported `complete`", got.Type, got.ID)
}

// Without the mark, the same path DOES write `complete` — the counterweight that
// shows the test above is measuring the mark and not a pump that stays silent anyway.
func TestOpenConnectionReportsCompleteWhenItsStreamEnds(t *testing.T) {
	exited := make(chan string, 1)
	res := &drainResolver{}
	h := NewSubscriptionHandler(MustParseSchema(drainSchema, res), nil, nil)
	h.pumpExited = func(id string) { exited <- id }
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialInit(t, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	writeMsg(t, conn, subscribeMsg("s", "subscription { ticker }", nil))
	require.Equal(t, msgNext, readMsg(t, conn).Type)

	h.mu.Lock()
	var c *wsConnection
	for live := range h.conns {
		c = live
	}
	h.mu.Unlock()
	c.mu.Lock()
	cancel := c.ops["s"]
	c.mu.Unlock()
	cancel()
	<-exited

	got := readMsg(t, conn)
	assert.Equal(t, msgComplete, got.Type)
	assert.Equal(t, "s", got.ID)
}

// terminate marks the connection closing, and has done so by the time its close frame
// reaches the peer. The test above sets the mark by hand, so it pins only the pump's
// side of the contract; this one pins that terminate is what sets it. The mark is read
// only once the peer has seen the close frame, and it is read as the VALUE true — not
// inferred from a `complete` that failed to appear, which gorilla's refusal to write
// after a close frame would suppress with or without the mark.
func TestTerminateMarksTheConnectionClosing(t *testing.T) {
	res := &drainResolver{}
	h := NewSubscriptionHandler(MustParseSchema(drainSchema, res), nil, nil)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialInit(t, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	writeMsg(t, conn, subscribeMsg("s", "subscription { ticker }", nil))
	require.Equal(t, msgNext, readMsg(t, conn).Type)

	h.mu.Lock()
	require.Len(t, h.conns, 1)
	var c *wsConnection
	for live := range h.conns {
		c = live
	}
	h.mu.Unlock()
	require.False(t, c.closing.Load(), "a live connection was already marked closing")

	go c.terminate(closeUnauthorized, "token expired")

	// Drain any `next` frames already in flight until the close frame arrives.
	var closeErr *websocket.CloseError
	for {
		_, _, err := conn.ReadMessage()
		if err == nil {
			continue
		}
		require.ErrorAs(t, err, &closeErr, "the socket ended without a close frame: %v", err)
		break
	}
	assert.Equal(t, closeUnauthorized, closeErr.Code)
	assert.True(t, c.closing.Load(),
		"the peer saw terminate's close frame, but the connection was not marked closing")
}

// A schema with no Subscription root gets no WebSocket: the upgrade is refused with a
// 400 at the dispatcher, and the manager holds no subscription handler to drain.
func TestNoWebSocketWithoutASubscriptionRoot(t *testing.T) {
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "no-subscriptions"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	gate := core.NewReadinessGate()
	gate.MarkReadyWithoutAuthSurface()
	gql := &GraphQLManager{
		Microservice: ms,
		Schema:       MustParseSchema(`schema { query: Query } type Query { hello: String! }`, &testSubResolver{}),
		Gate:         gate,
		Port:         ephemeralPort,
	}
	require.NoError(t, gql.ExecuteInitialize(context.Background()))
	require.NoError(t, gql.ExecuteStart(context.Background()))
	t.Cleanup(func() { _ = gql.ExecuteStop(context.Background()) })

	assert.Nil(t, gql.subscriptions)

	dialer := websocket.Dialer{Subprotocols: []string{wsSubprotocol}}
	conn, resp, err := dialer.Dial("ws://"+gql.Server.Addr()+"/graphql", nil)
	if conn != nil {
		_ = conn.Close()
	}
	require.ErrorIs(t, err, websocket.ErrBadHandshake)
	require.NotNil(t, resp)
	assert.Equal(t, 400, resp.StatusCode)
}
