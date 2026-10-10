// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const deadlineSDL = `
	schema { query: Query mutation: Mutation subscription: Subscription }
	type Query { hello: String! blocked: String! }
	type Mutation { blockedMutation: String! }
	type Subscription { late: Int! }
`

type deadlineRoot struct{}

func (*deadlineRoot) Hello() string { return "hi" }

// Blocked waits for its context, as a resolver stuck on a slow query does; it returns
// only when the request's context ends.
func (*deadlineRoot) Blocked(ctx context.Context) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func (*deadlineRoot) BlockedMutation(ctx context.Context) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// Late emits once, after longer than the execution deadline used below.
func (*deadlineRoot) Late(ctx context.Context) <-chan int32 {
	ch := make(chan int32)
	go func() {
		defer close(ch)
		select {
		case <-time.After(1500 * time.Millisecond):
			select {
			case ch <- 7:
			case <-ctx.Done():
			}
		case <-ctx.Done():
		}
	}()
	return ch
}

func postGraphQL(t *testing.T, url, query string) (*http.Response, []byte, error) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"query": query})
	c := http.Client{Timeout: 8 * time.Second}
	resp, err := c.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b, nil
}

// A query or mutation whose resolver blocks is ended at the execution deadline: the
// resolver sees its context cancelled and the request answers with an error instead of
// holding the connection until the client gives up.
func TestExecDeadlineEndsABlockedResolver(t *testing.T) {
	t.Setenv(EnvGraphQLExecTimeout, "1")
	schema := MustParseSchema(deadlineSDL, &deadlineRoot{})
	srv := httptest.NewServer(NewHttpHandler(schema, nil, nil))
	defer srv.Close()

	for _, q := range []string{`{ blocked }`, `mutation { blockedMutation }`} {
		start := time.Now()
		resp, body, err := postGraphQL(t, srv.URL, q)
		require.NoError(t, err, "%s: the request must be answered at the deadline", q)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Contains(t, string(body), "context deadline exceeded")
		assert.Less(t, time.Since(start), 4*time.Second, q)
	}
}

// The handler's own ExecTimeout (a service whose operations run long) wins over the
// platform default, and a zero value keeps the default.
func TestExecDeadlineHandlerOverride(t *testing.T) {
	t.Setenv(EnvGraphQLExecTimeout, "30")
	schema := MustParseSchema(deadlineSDL, &deadlineRoot{})
	h := NewHttpHandler(schema, nil, nil)
	h.ExecTimeout = 200 * time.Millisecond
	srv := httptest.NewServer(h)
	defer srv.Close()

	start := time.Now()
	_, body, err := postGraphQL(t, srv.URL, `{ blocked }`)
	require.NoError(t, err)
	assert.Contains(t, string(body), "context deadline exceeded")
	assert.Less(t, time.Since(start), 3*time.Second)
}

func TestExecTimeoutIsNeverUnlimited(t *testing.T) {
	for _, v := range []string{"", "0", "-5", "abc"} {
		t.Setenv(EnvGraphQLExecTimeout, v)
		assert.Equal(t, time.Duration(DefaultGraphQLExecTimeoutSeconds)*time.Second, execTimeout(), "value %q", v)
	}
	t.Setenv(EnvGraphQLExecTimeout, "5")
	assert.Equal(t, 5*time.Second, execTimeout())
}

// A client that sends headers and then stops delivering the body is cut off at the body
// read deadline rather than holding the connection.
func TestSlowBodyIsCutOffAtTheReadDeadline(t *testing.T) {
	old := bodyReadTimeout
	bodyReadTimeout = 300 * time.Millisecond
	defer func() { bodyReadTimeout = old }()

	schema := MustParseSchema(deadlineSDL, &deadlineRoot{})
	srv := httptest.NewServer(NewHttpHandler(schema, nil, nil))
	defer srv.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.Write([]byte("POST / HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 200\r\n\r\n{\"query\":"))
	require.NoError(t, err)

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	buf := make([]byte, 512)
	n, _ := conn.Read(buf)
	assert.Less(t, time.Since(start), 3*time.Second, "the server must answer or close at the body deadline")
	assert.Contains(t, string(buf[:n]), "400", "a body that never arrives is a bad request")
}

// The execution deadline belongs to query/mutation requests: a subscription served
// over the WebSocket on the same path lives on past it.
func TestExecDeadlineDoesNotApplyToSubscriptions(t *testing.T) {
	t.Setenv(EnvGraphQLExecTimeout, "1")
	schema := MustParseSchema(deadlineSDL, &deadlineRoot{})
	srv := httptest.NewServer(graphqlDispatcher(
		NewHttpHandler(schema, nil, nil),
		NewSubscriptionHandler(schema, map[ContextKey]interface{}{}, nil),
	))
	defer srv.Close()

	dialer := websocket.Dialer{Subprotocols: []string{wsSubprotocol}}
	conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(6 * time.Second))
	writeMsg(t, conn, wsMessage{Type: msgConnectionInit})
	require.Equal(t, msgConnectionAck, readMsg(t, conn).Type)
	writeMsg(t, conn, subscribeMsg("1", "subscription { late }", nil))
	msg := readMsg(t, conn)
	require.Equal(t, msgNext, msg.Type, "payload: %s", msg.Payload)
	assert.EqualValues(t, 7, nextData(t, msg)["late"])
}

// A client that sends a complete JSON value, declares a longer body and stops is cut off
// at the body deadline too: the server does not wait for the rest with no deadline.
func TestStalledBodyAfterACompleteValueIsCutOffAtTheReadDeadline(t *testing.T) {
	old := bodyReadTimeout
	bodyReadTimeout = 300 * time.Millisecond
	defer func() { bodyReadTimeout = old }()

	schema := MustParseSchema(deadlineSDL, &deadlineRoot{})
	srv := httptest.NewServer(NewHttpHandler(schema, nil, nil))
	defer srv.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	defer conn.Close()
	body := `{"query":"{ hello }"}`
	_, err = conn.Write([]byte("POST / HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 500\r\n\r\n" + body))
	require.NoError(t, err)

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	buf := make([]byte, 512)
	n, _ := conn.Read(buf)
	assert.Less(t, time.Since(start), 3*time.Second, "the server must answer or close at the body deadline")
	assert.Contains(t, string(buf[:n]), "400")
}
