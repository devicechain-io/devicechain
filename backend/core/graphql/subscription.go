// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/gorilla/websocket"
	graphql "github.com/graph-gophers/graphql-go"
	"github.com/rs/zerolog/log"
)

// wsSubprotocol is the modern graphql-over-WebSocket subprotocol (implemented by
// the `graphql-ws` npm library and StrawberryShake/.NET). It supersedes the
// legacy `graphql-ws` (subscriptions-transport-ws) subprotocol, which we do not
// speak. Clients MUST negotiate exactly this subprotocol (ADR-037).
const wsSubprotocol = "graphql-transport-ws"

const (
	// connectionInitWait bounds how long the server waits for the client's
	// connection_init before closing 4408 (spec: ConnectionInitialisationTimeout).
	connectionInitWait = 10 * time.Second
	// writeWait bounds a single socket write.
	writeWait = 10 * time.Second
	// pongWait is how long the server tolerates silence from the client before
	// treating the connection as dead. Refreshed on every received message.
	pongWait = 60 * time.Second
	// pingPeriod is how often the server sends an application-level ping. Kept
	// below pongWait so a live client's pong always refreshes the read deadline
	// before it lapses; also defeats idle-timeout at intervening proxies/ingress.
	pingPeriod = 25 * time.Second
)

// graphql-transport-ws close codes (spec §"Communication").
const (
	closeBadRequest       = 4400
	closeUnauthorized     = 4401
	closeInitTimeout      = 4408
	closeSubscriberExists = 4409
	closeTooManyInit      = 4429
)

// terminateCloseWait bounds the close frame terminate sends — for a Shutdown and for a
// token's expiry alike — and is deliberately far shorter than writeWait. The frame is
// a courtesy: the socket is closed immediately afterwards either way, and that close
// is what actually ends the connection. A peer that has stopped reading must
// therefore not be able to extend a drain, or an expired session, by the full write
// timeout.
const terminateCloseWait = time.Second

// refusedOperationMessage is the error an operation other than a subscription gets.
const refusedOperationMessage = "only subscription operations are accepted over a WebSocket; " +
	"send queries and mutations over HTTP"

// graphql-transport-ws message types.
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

// wsMessage is a graphql-transport-ws envelope. Payload is left raw so it can be
// a connection_init's connectionParams, a subscribe request, or a next/error
// result depending on Type.
type wsMessage struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// subscribePayload is the body of a `subscribe` message: a single GraphQL
// operation, which must be a subscription (see startOperation).
type subscribePayload struct {
	OperationName string                 `json:"operationName"`
	Query         string                 `json:"query"`
	Variables     map[string]interface{} `json:"variables"`
}

// SubscriptionHandler serves GraphQL subscriptions over the graphql-transport-ws
// protocol (ADR-037). It authenticates the connection from the connection_init
// payload — not an HTTP Authorization header — because browsers cannot set
// custom headers on a WebSocket handshake, so the token must ride in-band. It
// then drives the schema's native Subscribe execution, streaming each result as
// a `next` frame.
//
// 🔴 TWO LIMITS MAKE THE SOCKET NO MORE THAN A SUBSCRIPTION CHANNEL, AND BOTH ARE
// SECURITY BOUNDARIES. The token is checked once, at connection_init, so:
//
//   - the connection ends, with close code 4401, when the token it authenticated with
//     expires. Without that, pings kept a socket — and every stream on it — alive
//     indefinitely on a credential that had long since stopped working over HTTP. A
//     client that wants to continue reconnects and presents a fresh token.
//   - only subscription operations are accepted. graphql-go's Subscribe also runs a
//     query or a mutation, so the socket was a write session on the connect-time
//     claims. Queries and mutations belong on HTTP, which authenticates every request.
//
// The handler is routed on the same /graphql path as the HTTP handler (see
// ExecuteInitialize), and only for a schema that HAS a Subscription root: with
// operations limited to subscriptions, a socket on any other schema could do nothing.
// A handler built directly over such a schema answers every subscribe with an error
// frame.
type SubscriptionHandler struct {
	Schema           *Schema
	ContextProviders map[ContextKey]interface{}
	// Gate supplies the late-bound JWT validator, exactly as for the HTTP handler.
	Gate     *core.ReadinessGate
	policy   authPolicy
	upgrader websocket.Upgrader

	// MaxMessageBytes bounds a single inbound WebSocket message. ExecuteInitialize
	// sets it from infrastructure.graphql.maxSubscriptionMessageBytes.
	//
	// 🔴 ITS ZERO VALUE MEANS THE PLATFORM DEFAULT, NOT "UNLIMITED", AND THAT IS WHY
	// readLimit EXISTS RATHER THAN A BARE FIELD READ. gorilla gates its own check on
	// `if c.readLimit > 0`, so a dropped assignment, a handler built by a test, or an
	// instance config that never ran ApplyDefaults would all hand it 0 and restore
	// exactly the unbounded read this field exists to close — silently, since an
	// absent ceiling looks identical to a generous one until someone sends a large
	// frame. The zero value is the value a mistake produces, so it has to be the safe
	// one.
	MaxMessageBytes int64

	// mu guards conns, the set of connections that have been upgraded and have not
	// yet finished. It is what makes Shutdown possible at all: net/http's Shutdown
	// neither closes nor waits for HIJACKED connections, and every WebSocket here is
	// hijacked, so without a registry of its own this server has no way to name the
	// connections it is about to walk away from.
	mu    sync.Mutex
	conns map[*wsConnection]struct{}

	// pumpExited, when set, is called as each operation's pump goroutine returns. It
	// is nil in production and set only by this package's tests, before the handler
	// serves anything: it tells a test that a pump has FINISHED, including any frame
	// it was going to write, where the alternative is guessing at it with a sleep.
	pumpExited func(id string)
}

// NewSubscriptionHandler creates a data-plane subscription handler. It uses the
// same tenant policy as the HTTP data-plane handler: a token is optional, but a
// present token is verified and its tenant stamped into context (tenant-scoped
// operations still fail closed at the DB without one).
func NewSubscriptionHandler(schema *Schema, providers map[ContextKey]interface{}, gate *core.ReadinessGate) *SubscriptionHandler {
	return &SubscriptionHandler{
		Schema:           schema,
		ContextProviders: providers,
		Gate:             gate,
		policy:           tenantPolicy,
		MaxMessageBytes:  config.DefaultGraphQLMaxSubscriptionMessageBytes,
		conns:            make(map[*wsConnection]struct{}),
		upgrader: websocket.Upgrader{
			Subprotocols: []string{wsSubprotocol},
			// Origin is not an authentication boundary here: a connection is
			// authenticated by the token in connection_init, and cross-origin
			// embedders (the standalone dashboard app, ADR-039) are a first-class
			// case. The token is the boundary, so we accept any origin.
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
}

// graphqlDispatcher routes a WebSocket upgrade to the subscription handler and
// every other request (POST queries/mutations) to the HTTP handler, so both
// share the single /graphql path. A nil wsH means the schema offers no
// subscriptions, and an upgrade is refused with a 400 rather than handed to the
// HTTP handler, which would answer it with a confusing decode error.
func graphqlDispatcher(httpH http.Handler, wsH http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			if wsH == nil {
				http.Error(w, "this service offers no GraphQL subscriptions", http.StatusBadRequest)
				return
			}
			wsH.ServeHTTP(w, r)
			return
		}
		httpH.ServeHTTP(w, r)
	})
}

// hasSubscriptionRoot reports whether schema defines a Subscription root operation
// type — the same test graphql-go's Subscribe applies before it will run anything.
func hasSubscriptionRoot(schema *Schema) bool {
	if schema == nil || schema.inner == nil {
		return false
	}
	_, ok := schema.inner.ASTSchema().RootOperationTypes[opSubscription]
	return ok
}

// validator returns the live validator, or nil when the gate is absent or closed.
func (h *SubscriptionHandler) validator() *auth.Validator {
	if h.Gate == nil {
		return nil
	}
	return h.Gate.Validator()
}

// readLimit resolves the effective inbound-message ceiling. See MaxMessageBytes for
// why the zero value has to mean the default rather than reaching gorilla as 0.
func (h *SubscriptionHandler) readLimit() int64 {
	if h.MaxMessageBytes > 0 {
		return h.MaxMessageBytes
	}
	return config.DefaultGraphQLMaxSubscriptionMessageBytes
}

// ServeHTTP upgrades the request to a WebSocket and runs the connection.
func (h *SubscriptionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote an error response.
		return
	}
	// 🔴 BEFORE THE FIRST READ, WHICH IS BEFORE AUTHENTICATION. The credential
	// arrives inside connection_init — the first frame — so the frame is read, and
	// its payload materialised, while the peer is still anonymous. Every read on this
	// socket from here on is bounded; gorilla answers an over-limit message with a
	// 1009 (message too big) close frame and ends the connection.
	conn.SetReadLimit(h.readLimit())

	// Reject a client that did not negotiate our subprotocol: a mismatched peer
	// cannot frame messages we understand.
	if conn.Subprotocol() != wsSubprotocol {
		closeConn(conn, closeBadRequest, "subprotocol not acceptable")
		return
	}

	// Base context carries the injected providers (rdb, api) so resolvers reach
	// the service API exactly as they do over HTTP.
	baseCtx := r.Context()
	for key, value := range h.ContextProviders {
		baseCtx = context.WithValue(baseCtx, key, value)
	}

	c := newConnection(conn, h)
	h.register(c)
	defer h.unregister(c)
	c.run(baseCtx)
}

// register adds a live connection to the shutdown registry.
func (h *SubscriptionHandler) register(c *wsConnection) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conns == nil {
		h.conns = make(map[*wsConnection]struct{})
	}
	h.conns[c] = struct{}{}
}

// unregister drops a connection whose run loop has returned, and signals its done
// channel so a Shutdown waiting on it can stop waiting.
func (h *SubscriptionHandler) unregister(c *wsConnection) {
	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
	close(c.done)
}

// liveConnections is how many upgraded connections have not yet finished.
//
// Unexported on purpose: nothing in the serving path consults it, and its only
// callers are this package's own tests. Exporting it would promise to keep stable a
// number that exists to be measured, not consumed.
func (h *SubscriptionHandler) liveConnections() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns)
}

// Shutdown ends every live subscription connection and waits, bounded by ctx, for
// their goroutines to unwind.
//
// 🔴 IT EXISTS BECAUSE net/http'S Shutdown CANNOT DO IT. That method neither closes
// nor waits for hijacked connections, and gorilla's Upgrader hijacks — so before
// this, an http.Server.Shutdown returned, the service logged an orderly stop, and
// every live WebSocket kept its read loop, its ping ticker and one pump goroutine per
// active operation, each still holding a tenant-scoped subscription on a pod that had
// been declared drained. They ended at process exit, which reaches the client as a
// torn TCP connection rather than as a close frame.
//
// 🔴 IT DOES NOT LATCH, AND THAT IS DELIBERATE. ExecuteStart is entered again by any
// start retried after a failed one, so a handler that recorded itself as closed would
// leave the retried server unable to serve subscriptions at all — the exact failure
// http.Server.Shutdown's permanent shuttingDown flag produces, which is why the manager
// builds a fresh HttpServer per start. There is nothing to reset
// here: Shutdown acts on the connections that exist when it is called and leaves the
// handler as it found it. New connections are refused by the listener closing a
// moment later, and by readiness having reported 503 since the drain began.
//
// The close code is 1001 (going away) rather than a `complete` for each operation.
// `complete` means the stream ENDED — a client would treat it as a clean, final
// result and not reconnect — whereas a drained pod is precisely the case where a
// client should come back to another one.
func (h *SubscriptionHandler) Shutdown(ctx context.Context) error {
	h.mu.Lock()
	live := make([]*wsConnection, 0, len(h.conns))
	for c := range h.conns {
		live = append(live, c)
	}
	h.mu.Unlock()
	if len(live) == 0 {
		return nil
	}

	// Close first, wait second. Closing the socket is what unblocks each run loop, so
	// doing it before any waiting means even an already-expired ctx still tears every
	// connection down — it only shortens how long we watch them unwind.
	//
	// Concurrently, because the close frame's write can be delayed by a peer that has
	// stopped reading, and N such peers must not serialise into N × shutdownCloseWait.
	for _, c := range live {
		go c.closeForShutdown()
	}

	for i, c := range live {
		select {
		case <-c.done:
		case <-ctx.Done():
			return fmt.Errorf("graphql subscriptions: up to %d of %d connections had not finished unwinding: %w",
				len(live)-i, len(live), ctx.Err())
		}
	}
	log.Info().Int("connections", len(live)).Msg("Closed live GraphQL subscription connections.")
	return nil
}

// wsConnection is the per-socket state machine.
type wsConnection struct {
	conn    *websocket.Conn
	handler *SubscriptionHandler

	writeMu sync.Mutex // gorilla permits only one concurrent writer

	mu  sync.Mutex                    // guards ops
	ops map[string]context.CancelFunc // active operation id -> cancel

	// closing is set once the server has decided to end the connection, before its
	// close frame is written; from then on no pump reports `complete` (see terminate).
	closing atomic.Bool

	// wg counts the goroutines this connection owns — the ping ticker and one pump
	// per active operation. run waits on it before returning, so "the connection has
	// finished" means all of them have, not merely that the read loop stopped.
	wg sync.WaitGroup

	// done is closed by the handler's unregister once run has returned, i.e. once the
	// socket is closed, the connection context is cancelled and every goroutine above
	// has unwound. Shutdown waits on it.
	done chan struct{}
}

func newConnection(conn *websocket.Conn, h *SubscriptionHandler) *wsConnection {
	return &wsConnection{
		conn:    conn,
		handler: h,
		ops:     make(map[string]context.CancelFunc),
		done:    make(chan struct{}),
	}
}

// closeForShutdown tells the peer the server is going away and closes the socket,
// which is what unblocks run's ReadJSON.
func (c *wsConnection) closeForShutdown() {
	c.terminate(websocket.CloseGoingAway, "server shutting down")
}

// terminate ends the connection from the server side: it marks it closing, sends a
// close frame with code and reason, and closes the socket, which is what unblocks
// run's ReadJSON and so, through run's deferred cancel, every pump.
//
// 🔴 IT DOES NOT TAKE writeMu, UNLIKE EVERY OTHER WRITE PATH HERE. gorilla documents
// Close and WriteControl as the two methods callable concurrently with all the
// others, and that exemption is the point: a pump streaming to a peer that has
// stopped reading holds writeMu for up to writeWait, and a drain or an expiry that
// queued behind it would be bounded by the very connection it is trying to end.
//
// 🔴 THE closing MARK COMES FIRST, SO THE TEARDOWN ITSELF NEVER PRODUCES A `complete`.
// A pump whose operation ends because the connection is being torn down would
// otherwise report `complete` — which a graphql-transport-ws client reads as the
// stream having FINISHED, cleanly and for good. It would then never act on the close
// code that says why: a 4401 is how a client learns to come back with a fresh token,
// and a 1001 how it learns to come back to another pod. Today the pumps are only
// cancelled after the close frame is out (by run's deferred cancel), and gorilla
// refuses every write after a close frame, so the ORDER already prevents it. The mark
// makes that hold without relying on the order: a future path that cancels first
// stays silent too. TestTerminateMarksTheConnectionClosing pins that terminate sets
// the mark; TestClosingConnectionSendsNoComplete pins that a pump honours it.
//
// What the mark does NOT rule out is a stream that ends ON ITS OWN in the same instant:
// a pump that read closing just before it was set can still write its `complete` ahead
// of the close frame. That `complete` is true — the stream really did end — so it is
// not the misreport this guards against.
func (c *wsConnection) terminate(code int, reason string) {
	c.closing.Store(true)
	_ = c.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason),
		time.Now().Add(terminateCloseWait))
	_ = c.conn.Close()
}

// run drives the connection: it waits for connection_init, then reads messages
// until the socket closes or the client/ server tears it down. connCtx is
// cancelled on return so every in-flight operation goroutine unwinds.
func (c *wsConnection) run(baseCtx context.Context) {
	connCtx, cancel := context.WithCancel(baseCtx)
	// Deferred FIRST so it runs LAST: the socket is closed and the connection context
	// cancelled before anything waits on the goroutines those two release. Waiting
	// first would deadlock against the very pumps the cancel is there to unwind.
	defer c.wg.Wait()
	defer cancel()
	defer c.conn.Close()

	// The client must send connection_init first, within the init window.
	authedCtx, expiry, ok := c.awaitInit(connCtx)
	if !ok {
		return
	}

	// The connection lives exactly as long as the token it authenticated with. An
	// anonymous connection (expiry zero) has no token to outlive, and no tenant either,
	// so every tenant-scoped resolver refuses it already; its channel stays nil and
	// never fires. A token at its very last second gives a timer of zero or less,
	// which fires at once: Parse has already refused every token past its exp.
	var lifetime <-chan time.Time
	if !expiry.IsZero() {
		timer := time.NewTimer(time.Until(expiry))
		defer timer.Stop()
		lifetime = timer.C
	}

	// Keepalive: ping the client periodically; its pong (or any message) refreshes
	// the read deadline set in the read loop.
	pinger := time.NewTicker(pingPeriod)
	defer pinger.Stop()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		for {
			select {
			case <-connCtx.Done():
				return
			case <-pinger.C:
				c.write(wsMessage{Type: msgPing})
			case <-lifetime:
				// No explicit cancel: closing the socket fails the read loop, and run's
				// deferred cancel then unwinds every pump — AFTER the close frame, which
				// is the order a client needs (see terminate).
				c.terminate(closeUnauthorized, "token expired")
				return
			}
		}
	}()

	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	for {
		var msg wsMessage
		if err := c.conn.ReadJSON(&msg); err != nil {
			return
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))

		switch msg.Type {
		case msgConnectionInit:
			// A second connection_init is a protocol violation.
			c.closeCode(closeTooManyInit, "too many initialisation requests")
			return
		case msgPing:
			c.write(wsMessage{Type: msgPong, Payload: msg.Payload})
		case msgPong:
			// Liveness only; the deadline refresh above is the effect.
		case msgSubscribe:
			c.startOperation(authedCtx, msg)
		case msgComplete:
			c.stopOperation(msg.ID)
		default:
			c.closeCode(closeBadRequest, "unexpected message type "+msg.Type)
			return
		}
	}
}

// awaitInit waits for the connection_init frame, authenticates it, and replies
// connection_ack. It returns the authenticated context (tenant + claims stamped
// when a token was presented), the token's expiry (zero for an anonymous
// connection) and true on success; on any violation it closes the socket with the
// spec code and returns false.
func (c *wsConnection) awaitInit(connCtx context.Context) (context.Context, time.Time, bool) {
	_ = c.conn.SetReadDeadline(time.Now().Add(connectionInitWait))
	var msg wsMessage
	if err := c.conn.ReadJSON(&msg); err != nil {
		// Timeout or malformed first frame.
		c.closeCode(closeInitTimeout, "connection initialisation timeout")
		return nil, time.Time{}, false
	}
	if msg.Type != msgConnectionInit {
		c.closeCode(closeUnauthorized, "expected connection_init")
		return nil, time.Time{}, false
	}

	ctx, expiry, err := c.authenticate(connCtx, msg.Payload)
	if err != nil {
		c.closeCode(closeUnauthorized, err.Error())
		return nil, time.Time{}, false
	}

	c.write(wsMessage{Type: msgConnectionAck})
	return ctx, expiry, true
}

// authenticate applies the handler's auth policy to the connection_init payload.
// The token is read from connectionParams ("Authorization: Bearer <t>" or a bare
// "token"). With the data-plane policy a missing token is allowed (unauthenticated,
// no tenant, and a zero expiry); a present-but-invalid token is rejected.
func (c *wsConnection) authenticate(ctx context.Context, payload json.RawMessage) (context.Context, time.Time, error) {
	token := bearerFromParams(payload)
	if token == "" {
		if c.handler.policy.required {
			return nil, time.Time{}, errors.New("authentication required")
		}
		return ctx, time.Time{}, nil
	}
	validator := c.handler.validator()
	if validator == nil {
		return nil, time.Time{}, errors.New("authentication is not available")
	}
	// The WS transport carries no HTTP headers, so service tokens (whose tenant
	// rides a header) are inapplicable here; the empty resolver makes them resolve
	// to no tenant and be rejected, leaving access/identity tokens working.
	claims, tenant, err := c.handler.policy.authenticate(validator, token, func(string) string { return "" })
	if err != nil {
		return nil, time.Time{}, errors.New("invalid or expired token")
	}
	// auth.Validator's parser is built with jwt.WithExpirationRequired()
	// (auth/validator.go), so a token with no exp is refused above and this cannot
	// happen today — no test can reach it. It is refused rather than trusted because
	// the alternative is a connection with no end: a validator that ever drops that
	// option reopens exactly the unbounded socket the lifetime timer exists to close.
	if claims.ExpiresAt == nil {
		return nil, time.Time{}, errors.New("token has no expiry")
	}
	if tenant != "" {
		ctx = core.WithTenant(ctx, tenant)
	}
	ctx = auth.WithClaims(ctx, claims)
	return ctx, claims.ExpiresAt.Time, nil
}

// startOperation begins a subscribe request: it refuses anything but a
// subscription operation, guards against a duplicate id, starts the schema's
// native Subscribe, and pumps each response to a `next` frame until the source
// closes (then `complete`).
func (c *wsConnection) startOperation(authedCtx context.Context, msg wsMessage) {
	if msg.ID == "" {
		c.closeCode(closeBadRequest, "subscribe missing id")
		return
	}
	var payload subscribePayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		c.writeError(msg.ID, "invalid subscribe payload")
		return
	}
	// 🔴 BEFORE Subscribe, BECAUSE Subscribe IS WHERE A QUERY OR MUTATION RUNS. For
	// those graphql-go executes the operation to completion inside the call and hands
	// back a channel holding the finished result, so a check on anything it returns
	// would come after the write it exists to prevent. A document that cannot be
	// read, or names no single operation, is refused too, with the reason why rather
	// than the subscriptions-only message (whose advice to use HTTP would be false for
	// an unreadable subscription). Either way it is an operation-level error, and the
	// socket stays open.
	kind, qerr := operationType(payload.Query, payload.OperationName, c.handler.Schema.maxQueryLength)
	if qerr != nil {
		c.writeError(msg.ID, qerr.Message)
		return
	}
	if kind != opSubscription {
		c.writeError(msg.ID, refusedOperationMessage)
		return
	}

	c.mu.Lock()
	if _, exists := c.ops[msg.ID]; exists {
		c.mu.Unlock()
		c.closeCode(closeSubscriberExists, "subscriber for "+msg.ID+" already exists")
		return
	}
	opCtx, cancel := context.WithCancel(authedCtx)
	c.ops[msg.ID] = cancel
	c.mu.Unlock()

	responses, err := c.handler.Schema.Subscribe(opCtx, payload.Query, payload.OperationName, payload.Variables)
	if err != nil {
		// Raised only when the schema offers no subscriptions or has no resolver;
		// surface it as an operation error and clear the slot.
		cancel()
		c.removeOp(msg.ID)
		c.writeError(msg.ID, err.Error())
		return
	}

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		if hook := c.handler.pumpExited; hook != nil {
			defer hook(msg.ID)
		}
		defer cancel()
		for resp := range responses {
			r, ok := resp.(*graphql.Response)
			if !ok {
				continue
			}
			// Only forward while this operation is still registered; a client
			// `complete` cancels opCtx (closing this channel) and removes the id,
			// after which we must stay silent for it.
			if !c.opActive(msg.ID) {
				return
			}
			// The same conflict answer Schema.Exec gives, applied here because a
			// subscription's responses are typed only on this side of the channel.
			answerConflicts(r.Errors)
			raw, mErr := json.Marshal(r)
			if mErr != nil {
				continue
			}
			c.write(wsMessage{ID: msg.ID, Type: msgNext, Payload: raw})
		}
		// Source exhausted (or opCtx cancelled). If the client cancelled we already
		// dropped the id and must not send complete. Nor when the server is ending the
		// connection: the close code, not a `complete`, is the peer's answer then
		// (see terminate). Otherwise signal completion.
		if c.removeOp(msg.ID) && !c.closing.Load() {
			c.write(wsMessage{ID: msg.ID, Type: msgComplete})
		}
	}()
}

// stopOperation handles a client `complete`: cancel the operation and drop its
// id so the pump goroutine stays silent and exits.
func (c *wsConnection) stopOperation(id string) {
	c.mu.Lock()
	cancel, ok := c.ops[id]
	if ok {
		delete(c.ops, id)
	}
	c.mu.Unlock()
	if ok {
		cancel()
	}
}

// opActive reports whether the operation id is still registered.
func (c *wsConnection) opActive(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.ops[id]
	return ok
}

// removeOp deletes the operation id, returning true if it was still present (so
// the caller knows whether it, rather than a client complete, owns teardown).
func (c *wsConnection) removeOp(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.ops[id]; ok {
		delete(c.ops, id)
		return true
	}
	return false
}

// write serializes a message to the socket under the write mutex.
func (c *wsConnection) write(msg wsMessage) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
	if err := c.conn.WriteJSON(msg); err != nil {
		log.Debug().Err(err).Str("type", msg.Type).Msg("subscription socket write failed")
	}
}

// writeError sends an operation-level `error` frame carrying a single GraphQL
// error message.
func (c *wsConnection) writeError(id, message string) {
	payload, _ := json.Marshal([]struct {
		Message string `json:"message"`
	}{{Message: message}})
	c.write(wsMessage{ID: id, Type: msgError, Payload: payload})
}

// closeCode sends a WebSocket close frame with a graphql-transport-ws code. It
// marks the connection closing first, as terminate does, so a protocol-violation
// close is not preceded by a `complete` either.
func (c *wsConnection) closeCode(code int, reason string) {
	c.closing.Store(true)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
	_ = c.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason), time.Now().Add(writeWait))
	_ = c.conn.Close()
}

// closeConn is closeCode without a live wsConnection (used before the state
// machine starts).
func closeConn(conn *websocket.Conn, code int, reason string) {
	_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(code, reason), time.Now().Add(writeWait))
	_ = conn.Close()
}

// bearerFromParams extracts a token from connection_init connectionParams,
// accepting either {"Authorization":"Bearer <t>"} (case-insensitive scheme) or a
// bare {"token":"<t>"}. Returns "" when no credential is present.
func bearerFromParams(payload json.RawMessage) string {
	if len(payload) == 0 {
		return ""
	}
	var params map[string]interface{}
	if err := json.Unmarshal(payload, &params); err != nil {
		return ""
	}
	for k, v := range params {
		s, ok := v.(string)
		if !ok || s == "" {
			continue
		}
		switch {
		case strings.EqualFold(k, "authorization"):
			if len(s) > len(bearerPrefix) && strings.EqualFold(s[:len(bearerPrefix)], bearerPrefix) {
				return s[len(bearerPrefix):]
			}
		case strings.EqualFold(k, "token"):
			return s
		}
	}
	return ""
}
