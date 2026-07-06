// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
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
// operation (subscription, or a single-result query/mutation).
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
// a `next` frame. One handler is registered alongside the HTTP handler on the
// same /graphql path (see the dispatcher in ExecuteStart); a schema without a
// Subscription root simply answers every subscribe with an error frame.
type SubscriptionHandler struct {
	Schema           *graphql.Schema
	ContextProviders map[ContextKey]interface{}
	// Gate supplies the late-bound JWT validator, exactly as for the HTTP handler.
	Gate     *core.ReadinessGate
	policy   authPolicy
	upgrader websocket.Upgrader
}

// NewSubscriptionHandler creates a data-plane subscription handler. It uses the
// same tenant policy as the HTTP data-plane handler: a token is optional, but a
// present token is verified and its tenant stamped into context (tenant-scoped
// operations still fail closed at the DB without one).
func NewSubscriptionHandler(schema *graphql.Schema, providers map[ContextKey]interface{}, gate *core.ReadinessGate) *SubscriptionHandler {
	return &SubscriptionHandler{
		Schema:           schema,
		ContextProviders: providers,
		Gate:             gate,
		policy:           tenantPolicy,
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
// share the single /graphql path.
func graphqlDispatcher(httpH http.Handler, wsH http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			wsH.ServeHTTP(w, r)
			return
		}
		httpH.ServeHTTP(w, r)
	})
}

// validator returns the live validator, or nil when the gate is absent or closed.
func (h *SubscriptionHandler) validator() *auth.Validator {
	if h.Gate == nil {
		return nil
	}
	return h.Gate.Validator()
}

// ServeHTTP upgrades the request to a WebSocket and runs the connection.
func (h *SubscriptionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote an error response.
		return
	}
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
	newConnection(conn, h).run(baseCtx)
}

// wsConnection is the per-socket state machine.
type wsConnection struct {
	conn    *websocket.Conn
	handler *SubscriptionHandler

	writeMu sync.Mutex // gorilla permits only one concurrent writer

	mu  sync.Mutex                    // guards ops
	ops map[string]context.CancelFunc // active operation id -> cancel
}

func newConnection(conn *websocket.Conn, h *SubscriptionHandler) *wsConnection {
	return &wsConnection{conn: conn, handler: h, ops: make(map[string]context.CancelFunc)}
}

// run drives the connection: it waits for connection_init, then reads messages
// until the socket closes or the client/ server tears it down. connCtx is
// cancelled on return so every in-flight operation goroutine unwinds.
func (c *wsConnection) run(baseCtx context.Context) {
	connCtx, cancel := context.WithCancel(baseCtx)
	defer cancel()
	defer c.conn.Close()

	// The client must send connection_init first, within the init window.
	authedCtx, ok := c.awaitInit(connCtx)
	if !ok {
		return
	}

	// Keepalive: ping the client periodically; its pong (or any message) refreshes
	// the read deadline set in the read loop.
	pinger := time.NewTicker(pingPeriod)
	defer pinger.Stop()
	go func() {
		for {
			select {
			case <-connCtx.Done():
				return
			case <-pinger.C:
				c.write(wsMessage{Type: msgPing})
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
// when a token was presented) and true on success; on any violation it closes
// the socket with the spec code and returns false.
func (c *wsConnection) awaitInit(connCtx context.Context) (context.Context, bool) {
	_ = c.conn.SetReadDeadline(time.Now().Add(connectionInitWait))
	var msg wsMessage
	if err := c.conn.ReadJSON(&msg); err != nil {
		// Timeout or malformed first frame.
		c.closeCode(closeInitTimeout, "connection initialisation timeout")
		return nil, false
	}
	if msg.Type != msgConnectionInit {
		c.closeCode(closeUnauthorized, "expected connection_init")
		return nil, false
	}

	ctx, err := c.authenticate(connCtx, msg.Payload)
	if err != nil {
		c.closeCode(closeUnauthorized, err.Error())
		return nil, false
	}

	c.write(wsMessage{Type: msgConnectionAck})
	return ctx, true
}

// authenticate applies the handler's auth policy to the connection_init payload.
// The token is read from connectionParams ("Authorization: Bearer <t>" or a bare
// "token"). With the data-plane policy a missing token is allowed (unauthenticated,
// no tenant); a present-but-invalid token is rejected.
func (c *wsConnection) authenticate(ctx context.Context, payload json.RawMessage) (context.Context, error) {
	token := bearerFromParams(payload)
	if token == "" {
		if c.handler.policy.required {
			return nil, errors.New("authentication required")
		}
		return ctx, nil
	}
	validator := c.handler.validator()
	if validator == nil {
		return nil, errors.New("authentication is not available")
	}
	// The WS transport carries no HTTP headers, so service tokens (whose tenant
	// rides a header) are inapplicable here; the empty resolver makes them resolve
	// to no tenant and be rejected, leaving access/identity tokens working.
	claims, tenant, err := c.handler.policy.authenticate(validator, token, func(string) string { return "" })
	if err != nil {
		return nil, errors.New("invalid or expired token")
	}
	if tenant != "" {
		ctx = core.WithTenant(ctx, tenant)
	}
	ctx = auth.WithClaims(ctx, claims)
	return ctx, nil
}

// startOperation begins a subscribe request: it guards against a duplicate id,
// starts the schema's native Subscribe, and pumps each response to a `next`
// frame until the source closes (then `complete`). A single-result query or
// mutation runs through the same path — one `next` then `complete`.
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

	go func() {
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
			raw, mErr := json.Marshal(r)
			if mErr != nil {
				continue
			}
			c.write(wsMessage{ID: msg.ID, Type: msgNext, Payload: raw})
		}
		// Source exhausted (or opCtx cancelled). If the client cancelled we already
		// dropped the id and must not send complete; otherwise signal completion.
		if c.removeOp(msg.ID) {
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

// closeCode sends a WebSocket close frame with a graphql-transport-ws code.
func (c *wsConnection) closeCode(code int, reason string) {
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
