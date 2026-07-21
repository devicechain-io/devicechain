// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package graphqlws is a client for the graphql-transport-ws subprotocol
// (ADR-037) — the dialing counterpart to core/graphql's server-side
// SubscriptionHandler. It exists because DeviceChain has TS and C# subscription
// clients but no Go one, and the load-test safety monitor (ADR-064) needs to
// watch live platform truth — measurementStream / alarmStream / detectionStream —
// over the same real tenant-scoped wire a browser uses, without polling the
// firehose.
//
// It is deliberately framework-neutral: Dial takes a TokenProvider
// (func(ctx)(string,error) — exactly the shape of userclient.TenantSession's
// AccessToken), so the token/refresh machinery stays in userclient and this
// package owns only the wire protocol. It reuses gorilla/websocket, already a
// direct core dependency, and adds no new one.
//
// # Delivery contract
//
// A Subscription hands each server `next` frame's `data` object to its channel,
// following the bufio.Scanner idiom: range the channel for values, then check
// Err() once it closes to learn WHY it ended.
//
//	sub, err := c.Subscribe(ctx, query, vars)
//	...
//	for data := range sub.C() {
//	    var ev MeasurementEvent
//	    _ = json.Unmarshal(data, &ev)
//	}
//	if err := sub.Err(); err != nil {
//	    // an `error` frame, an in-band GraphQL error, a slow-consumer overflow,
//	    // or the connection dropping — a TERMINAL condition, never a silent stop.
//	}
//	// sub.Err() == nil means the server sent `complete` — a clean end.
//
// The Err()-on-close split is load-bearing for the safety monitor: a monitor
// that could not tell "I fell behind" from "the platform dropped it" would
// defeat its own purpose (research/load-test-harness.md §4.0). So a consumer that
// cannot keep up overflows its buffer and ends with ErrSlowConsumer — an
// explicit, attributable "I fell behind" — rather than silently missing events
// that would read as a platform drop.
package graphqlws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// wsSubprotocol is the only subprotocol the server speaks (core/graphql
// subscription.go); a client MUST negotiate exactly this or be closed 4400.
const wsSubprotocol = "graphql-transport-ws"

// graphql-transport-ws message types (mirrors the server's vocabulary).
const (
	msgConnectionInit = "connection_init"
	msgConnectionAck  = "connection_ack"
	msgPing           = "ping"
	msgPong           = "pong"
	msgSubscribe      = "subscribe"
	msgNext           = "next"
	msgError          = "error"
	msgComplete       = "complete"
)

const (
	defaultHandshakeTimeout = 10 * time.Second
	defaultAckTimeout       = 10 * time.Second
	// defaultReadTimeout must exceed the server's ping period (25s) so a live
	// server's periodic ping refreshes the read deadline before it lapses; a
	// genuinely dead peer then trips it and ends every subscription.
	defaultReadTimeout = 60 * time.Second
	// writeWait bounds a single socket write.
	writeWait = 10 * time.Second
	// defaultBuffer is the per-subscription channel depth. Sized generously so a
	// briefly-busy consumer (a GC pause) absorbs a burst rather than tripping the
	// fail-closed overflow; a sustained-behind consumer still trips it.
	defaultBuffer = 1024
)

// ErrSlowConsumer terminates a subscription whose channel buffer filled because
// the consumer did not read fast enough. It is deliberately distinct from a
// clean complete and from a platform drop: the monitor treats it as "the client
// fell behind," never as evidence the platform lost an event (§4.0).
var ErrSlowConsumer = errors.New("graphqlws: subscription buffer overflow (consumer fell behind)")

// ErrClientClosed is the terminal error on subscriptions still open when the
// client is closed by the caller.
var ErrClientClosed = errors.New("graphqlws: client closed")

// TokenProvider supplies a bearer token for the connection_init payload. It is
// called once, at Dial. Return "" (with a nil error) to connect without a
// credential — the data plane tolerates an unauthenticated connect, though every
// tenant-scoped subscription then fails closed server-side with no tenant.
type TokenProvider func(context.Context) (string, error)

// wsMessage is a graphql-transport-ws envelope.
type wsMessage struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// subscribePayload is the body of a `subscribe` message.
type subscribePayload struct {
	OperationName string                 `json:"operationName,omitempty"`
	Query         string                 `json:"query"`
	Variables     map[string]interface{} `json:"variables,omitempty"`
}

type config struct {
	handshakeTimeout time.Duration
	ackTimeout       time.Duration
	readTimeout      time.Duration
	buffer           int
	dialer           *websocket.Dialer
}

// Option customises a Dial.
type Option func(*config)

// WithHandshakeTimeout bounds the WebSocket HTTP handshake.
func WithHandshakeTimeout(d time.Duration) Option { return func(c *config) { c.handshakeTimeout = d } }

// WithAckTimeout bounds how long Dial waits for connection_ack after sending
// connection_init.
func WithAckTimeout(d time.Duration) Option { return func(c *config) { c.ackTimeout = d } }

// WithReadTimeout sets the steady-state read deadline, refreshed on every frame.
// It must exceed the server's ping period (25s) or a healthy connection will be
// torn down between pings.
func WithReadTimeout(d time.Duration) Option { return func(c *config) { c.readTimeout = d } }

// WithBuffer sets the per-subscription channel depth (see ErrSlowConsumer).
func WithBuffer(n int) Option { return func(c *config) { c.buffer = n } }

// WithDialer supplies a preconfigured gorilla dialer (TLS config, proxy). Its
// Subprotocols and HandshakeTimeout are overridden to the protocol's needs.
func WithDialer(d *websocket.Dialer) Option { return func(c *config) { c.dialer = d } }

// Client is a live graphql-transport-ws connection multiplexing many
// subscriptions. It is safe for concurrent use.
type Client struct {
	conn        *websocket.Conn
	readTimeout time.Duration
	buffer      int

	writeMu sync.Mutex // gorilla permits only one concurrent writer

	mu       sync.Mutex
	subs     map[string]*Subscription
	nextID   int64
	closed   bool
	closeErr error
}

// Dial connects to endpoint (ws:// or wss://), negotiates the subprotocol, sends
// connection_init carrying the bearer from token (if any), and waits for
// connection_ack. On success it starts the read loop and returns a live Client.
//
// A token that authenticates the connect but is rejected by the server surfaces
// as a handshake error wrapping a *websocket.CloseError (code 4401) — so a
// caller can distinguish "unauthorized" from "unreachable".
func Dial(ctx context.Context, endpoint string, token TokenProvider, opts ...Option) (*Client, error) {
	cfg := config{
		handshakeTimeout: defaultHandshakeTimeout,
		ackTimeout:       defaultAckTimeout,
		readTimeout:      defaultReadTimeout,
		buffer:           defaultBuffer,
	}
	for _, o := range opts {
		o(&cfg)
	}
	// A zero/negative buffer would make the per-subscription channel unbuffered,
	// so the non-blocking send in push overflows on frame one for any consumer not
	// parked in a receive at that instant — a spurious ErrSlowConsumer that reads
	// like a platform fault. Clamp a misconfiguration back to the default.
	if cfg.buffer < 1 {
		cfg.buffer = defaultBuffer
	}

	dialer := cfg.dialer
	if dialer == nil {
		dialer = &websocket.Dialer{}
	}
	// The protocol pins these regardless of any caller-supplied dialer.
	d := *dialer
	d.Subprotocols = []string{wsSubprotocol}
	d.HandshakeTimeout = cfg.handshakeTimeout

	conn, resp, err := d.DialContext(ctx, endpoint, nil)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("graphqlws: dial %s: %w (http %d)", endpoint, err, resp.StatusCode)
		}
		return nil, fmt.Errorf("graphqlws: dial %s: %w", endpoint, err)
	}
	if conn.Subprotocol() != wsSubprotocol {
		_ = conn.Close()
		return nil, fmt.Errorf("graphqlws: server did not negotiate %q", wsSubprotocol)
	}

	c := &Client{
		conn:        conn,
		readTimeout: cfg.readTimeout,
		buffer:      cfg.buffer,
		subs:        make(map[string]*Subscription),
	}

	var params map[string]interface{}
	if token != nil {
		tok, terr := token(ctx)
		if terr != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("graphqlws: acquire token: %w", terr)
		}
		if tok != "" {
			params = map[string]interface{}{"Authorization": "Bearer " + tok}
		}
	}
	if err := c.writeInit(params); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("graphqlws: send connection_init: %w", err)
	}

	// Await connection_ack. A close frame here (e.g. 4401) is unwrapped into the
	// returned error so the caller can errors.As it to a *websocket.CloseError.
	_ = conn.SetReadDeadline(time.Now().Add(cfg.ackTimeout))
	var msg wsMessage
	if err := conn.ReadJSON(&msg); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("graphqlws: await connection_ack: %w", err)
	}
	if msg.Type != msgConnectionAck {
		_ = conn.Close()
		return nil, fmt.Errorf("graphqlws: expected connection_ack, got %q", msg.Type)
	}

	_ = conn.SetReadDeadline(time.Now().Add(cfg.readTimeout))
	go c.readLoop()
	return c, nil
}

// Subscribe starts one subscription (or a single-result query/mutation — the
// server serves both over this transport) and returns its Subscription. ctx
// bounds the subscribe send only; teardown is via Subscription.Close (or the
// server completing the stream). A Subscribe on a closed client fails.
func (c *Client) Subscribe(ctx context.Context, query string, vars map[string]interface{}) (*Subscription, error) {
	c.mu.Lock()
	if c.closed {
		err := c.closeErr
		c.mu.Unlock()
		if err == nil {
			err = ErrClientClosed
		}
		return nil, err
	}
	c.nextID++
	id := strconv.FormatInt(c.nextID, 10)
	sub := &Subscription{id: id, c: c, ch: make(chan json.RawMessage, c.buffer)}
	c.subs[id] = sub
	c.mu.Unlock()

	payload, err := json.Marshal(subscribePayload{Query: query, Variables: vars})
	if err != nil {
		c.dropSub(id)
		return nil, fmt.Errorf("graphqlws: marshal subscribe: %w", err)
	}
	if err := c.write(ctx, wsMessage{ID: id, Type: msgSubscribe, Payload: payload}); err != nil {
		c.dropSub(id)
		return nil, fmt.Errorf("graphqlws: send subscribe: %w", err)
	}
	return sub, nil
}

// Close tears down the connection and terminates every live subscription with
// ErrClientClosed (unless already ended by a server complete/error or a drop).
func (c *Client) Close() error {
	c.writeMu.Lock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
	_ = c.conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	c.writeMu.Unlock()
	// Attribute the terminal error BEFORE closing the socket. conn.Close unblocks
	// the readLoop, whose own shutdown(connection-closed) would otherwise race this
	// call and misattribute a deliberate Close as a drop. Both take c.mu, so -race
	// stays silent — it is an ordering race, not a memory one. shutdown is
	// first-caller-wins, so running it here first makes a caller-initiated close
	// deterministically ErrClientClosed, while a genuine earlier drop still wins.
	c.shutdown(ErrClientClosed)
	return c.conn.Close()
}

// readLoop demultiplexes inbound frames to their subscriptions until the socket
// closes or errors, at which point every remaining subscription is terminated.
func (c *Client) readLoop() {
	for {
		var msg wsMessage
		if err := c.conn.ReadJSON(&msg); err != nil {
			c.shutdown(fmt.Errorf("graphqlws: connection closed: %w", err))
			return
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(c.readTimeout))

		switch msg.Type {
		case msgNext:
			c.deliver(msg.ID, msg.Payload)
		case msgError:
			c.finish(msg.ID, decodeErrorFrame(msg.Payload))
		case msgComplete:
			c.finish(msg.ID, nil)
		case msgPing:
			_ = c.write(context.Background(), wsMessage{Type: msgPong, Payload: msg.Payload})
		case msgPong, msgConnectionAck:
			// Liveness / spurious late ack — the deadline refresh is the effect.
		default:
			// Be liberal: an unknown frame type is ignored, not fatal.
		}
	}
}

// deliver routes a `next` frame's data to its subscription. An in-band GraphQL
// error in the frame terminates the subscription; a full buffer terminates it
// with ErrSlowConsumer.
func (c *Client) deliver(id string, payload json.RawMessage) {
	c.mu.Lock()
	sub := c.subs[id]
	c.mu.Unlock()
	if sub == nil {
		return // unknown or already-finished id
	}
	data, gqlErr := splitNext(payload)
	if gqlErr != nil {
		c.finishLocal(id, gqlErr)
		return
	}
	if overflow := sub.push(data); overflow {
		c.finishLocal(id, ErrSlowConsumer)
	}
}

// finishLocal terminates a subscription the CLIENT decided to end — a buffer
// overflow, an in-band error, or a caller's Subscription.Close — and, unlike a
// server-driven complete/error, tells the server to stop with a `complete`
// frame. Without it the server's pump keeps streaming this id down the shared
// socket, forcing the single readLoop to parse-and-discard its firehose and
// starving the other live subscriptions on the same connection. The server
// no-ops a `complete` for an unknown id, so it is always safe to send.
func (c *Client) finishLocal(id string, err error) error {
	c.finish(id, err)
	return c.write(context.Background(), wsMessage{ID: id, Type: msgComplete})
}

// finish removes a subscription and closes its channel with err (nil = clean
// complete). Idempotent across the read loop, Close, and a caller's
// Subscription.Close all racing to end the same id.
func (c *Client) finish(id string, err error) {
	c.mu.Lock()
	sub := c.subs[id]
	if sub != nil {
		delete(c.subs, id)
	}
	c.mu.Unlock()
	if sub != nil {
		sub.finish(err)
	}
}

// dropSub removes a subscription slot that never got off the ground (its
// subscribe send failed) without delivering a terminal value — the caller sees
// the Subscribe error instead.
func (c *Client) dropSub(id string) {
	c.mu.Lock()
	delete(c.subs, id)
	c.mu.Unlock()
}

// shutdown marks the client closed and terminates every remaining subscription.
// The first caller's err wins (a real drop error is preserved over a later
// Close's ErrClientClosed).
func (c *Client) shutdown(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.closeErr = err
	subs := make([]*Subscription, 0, len(c.subs))
	for _, s := range c.subs {
		subs = append(subs, s)
	}
	c.subs = make(map[string]*Subscription)
	c.mu.Unlock()
	for _, s := range subs {
		s.finish(err)
	}
}

func (c *Client) writeInit(params map[string]interface{}) error {
	var payload json.RawMessage
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		payload = raw
	}
	return c.write(context.Background(), wsMessage{Type: msgConnectionInit, Payload: payload})
}

func (c *Client) write(ctx context.Context, msg wsMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	// Bound the write by the earlier of writeWait and the caller's ctx deadline,
	// so a caller-supplied deadline actually caps the send (Subscribe's contract).
	deadline := time.Now().Add(writeWait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.conn.SetWriteDeadline(deadline)
	return c.conn.WriteJSON(msg)
}

// Subscription is one live operation on a Client. Its channel yields each
// `next` frame's data object; when it closes, Err reports why (nil = the server
// completed the stream cleanly).
type Subscription struct {
	id string
	c  *Client
	ch chan json.RawMessage

	mu     sync.Mutex
	closed bool
	err    error
}

// C is the stream of `next` data objects. It closes on any terminal condition;
// check Err after the range ends.
func (s *Subscription) C() <-chan json.RawMessage { return s.ch }

// Err returns the terminal error once C is closed: nil for a clean server
// complete; an `error`-frame / in-band GraphQL error; ErrSlowConsumer; or a
// connection-drop / ErrClientClosed error. It is meaningful only after C closes.
func (s *Subscription) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Close ends the subscription from the client side: it stops delivery locally
// (Err becomes nil, a clean end) and best-effort tells the server to stop with a
// `complete` frame. Idempotent.
func (s *Subscription) Close() error {
	return s.c.finishLocal(s.id, nil)
}

// push enqueues data; it is called only by the read loop. It returns true if the
// buffer was full (overflow) so the caller terminates the subscription. Holding
// the mutex during a NON-BLOCKING send is what makes concurrent push/finish
// race-free: finish cannot close the channel mid-send, and a send never lands on
// a closed channel.
func (s *Subscription) push(data json.RawMessage) (overflow bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	select {
	case s.ch <- data:
		return false
	default:
		return true
	}
}

// finish closes the channel exactly once, recording the first terminal error.
func (s *Subscription) finish(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.err = err
	close(s.ch)
}

// splitNext extracts the `data` object from a `next` payload. A `next` carrying
// a non-empty GraphQL `errors` array is a per-operation failure, returned as an
// error (which terminates the subscription) rather than delivered as data.
func splitNext(payload json.RawMessage) (json.RawMessage, error) {
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, fmt.Errorf("graphqlws: decode next payload: %w", err)
	}
	if len(env.Errors) > 0 {
		return nil, joinGQLErrors(env.Errors)
	}
	return env.Data, nil
}

// decodeErrorFrame renders an `error` frame's payload (a bare GraphQL errors
// array) into a single error. It never returns nil — an error frame is always a
// terminal failure, even if its payload is unparseable or empty.
func decodeErrorFrame(payload json.RawMessage) error {
	var errs []gqlError
	if err := json.Unmarshal(payload, &errs); err != nil || len(errs) == 0 {
		return fmt.Errorf("graphqlws: subscription error")
	}
	return joinGQLErrors(errs)
}

type gqlError struct {
	Message string `json:"message"`
}

func joinGQLErrors(errs []gqlError) error {
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msgs = append(msgs, e.Message)
	}
	return fmt.Errorf("graphqlws: subscription error: %s", strings.Join(msgs, "; "))
}
