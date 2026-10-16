// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const scopeSubSDL = `
	schema { query: Query subscription: Subscription }
	type Query { hello: String! }
	type Subscription {
		gatedStream: String!
		optedStream: String!
	}
`

// scopeSubRoot has one subscription behind Authorize and one that opted in, which reports
// the tenant and scope it was handed — the shape alarmStream and measurementStream take.
type scopeSubRoot struct{}

func (*scopeSubRoot) Hello() string { return "hi" }

func oneValue(v string) <-chan string {
	ch := make(chan string, 1)
	ch <- v
	close(ch)
	return ch
}

func (*scopeSubRoot) GatedStream(ctx context.Context) (<-chan string, error) {
	if err := auth.Authorize(ctx, auth.AlarmRead); err != nil {
		return nil, err
	}
	return oneValue("all of the tenant"), nil
}

func (*scopeSubRoot) OptedStream(ctx context.Context) (<-chan string, error) {
	s, err := auth.AuthorizeScoped(ctx, auth.AlarmRead)
	if err != nil {
		return nil, err
	}
	tenant, _ := core.TenantFromContext(ctx)
	return oneValue(fmt.Sprintf("%s:%v:%s", tenant, s.IsScoped(), strings.Join(s.Anchors(), ","))), nil
}

// subscribeOnce opens a graphql-transport-ws connection authenticated with token, runs one
// subscription and returns the first payload frame for it.
func subscribeOnce(t *testing.T, url, token, query string) (string, json.RawMessage) {
	t.Helper()
	dialer := websocket.Dialer{Subprotocols: []string{wsSubprotocol}}
	conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(url, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	params, _ := json.Marshal(map[string]string{"Authorization": "Bearer " + token})
	writeMsg(t, conn, wsMessage{Type: msgConnectionInit, Payload: params})
	require.Equal(t, msgConnectionAck, readMsg(t, conn).Type)
	writeMsg(t, conn, subscribeMsg("1", query, nil))
	for {
		msg := readMsg(t, conn)
		if msg.ID == "1" && (msg.Type == msgNext || msg.Type == msgError) {
			return msg.Type, msg.Payload
		}
	}
}

// End to end over the WebSocket transport, the one the live alarm and measurement streams
// are served on: a scoped member's token reaches a subscription behind Authorize as
// FORBIDDEN with the reason, and an opted-in subscription with its tenant and scope —
// read from the claims the connection_init attached, exactly as the HTTP transport does.
// A transport that attached the claims but lost the scope would show here as an opted-in
// stream handed an unscoped caller.
func TestAScopedTokenOnTheSubscriptionTransport(t *testing.T) {
	iss, v := testIssuerValidator(t)
	gate := core.NewReadinessGate()
	gate.MarkReady(v)
	srv := httptest.NewServer(NewSubscriptionHandler(MustParseSchema(scopeSubSDL, &scopeSubRoot{}),
		map[ContextKey]interface{}{}, gate))
	defer srv.Close()

	scope, err := auth.NewScope([]string{"customer:acme-corp"})
	require.NoError(t, err)
	scoped, err := iss.IssueScopedTenantAccess("acme", "c@acme-corp.example", nil,
		[]string{string(auth.AuthorityAll)}, scope, "j-scoped")
	require.NoError(t, err)

	kind, payload := subscribeOnce(t, srv.URL, scoped.Token, "subscription { gatedStream }")
	require.Equal(t, msgNext, kind, "payload=%s", payload)
	var gated gqlAnswer
	require.NoError(t, json.Unmarshal(payload, &gated))
	require.Len(t, gated.Errors, 1, "a scoped member subscribed to a gated stream: %s", payload)
	assert.Equal(t, "FORBIDDEN", gated.Errors[0].Extensions["code"])
	assert.Equal(t, "SCOPE_UNSUPPORTED", gated.Errors[0].Extensions["reason"])

	kind, payload = subscribeOnce(t, srv.URL, scoped.Token, "subscription { optedStream }")
	require.Equal(t, msgNext, kind)
	var opted gqlAnswer
	require.NoError(t, json.Unmarshal(payload, &opted))
	require.Empty(t, opted.Errors)
	assert.Equal(t, "acme:true:customer:acme-corp", opted.Data["optedStream"])

	// Negative control: an unscoped token passes the gate, and reaches the opted-in
	// stream unscoped.
	plain, err := iss.IssueTenantAccess("acme", "a@acme.example", nil, []string{string(auth.AuthorityAll)}, false, "j-plain")
	require.NoError(t, err)
	_, payload = subscribeOnce(t, srv.URL, plain.Token, "subscription { gatedStream }")
	var all gqlAnswer
	require.NoError(t, json.Unmarshal(payload, &all))
	require.Empty(t, all.Errors)
	assert.Equal(t, "all of the tenant", all.Data["gatedStream"])
	_, payload = subscribeOnce(t, srv.URL, plain.Token, "subscription { optedStream }")
	require.NoError(t, json.Unmarshal(payload, &opted))
	assert.Equal(t, "acme:false:", opted.Data["optedStream"])
}
