// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// workSDL has a query, a mutation and a subscription, each backed by a resolver that
// COUNTS its calls. The counter is what turns "rejected before execution" from a claim
// about an error message into an observed fact: a document can carry an error AND
// have run its resolvers.
const workSDL = `
	schema { query: Query mutation: Mutation subscription: Subscription }
	type Query { ping: Int! }
	type Mutation { bump: Int! }
	type Subscription { tick: Int! }
`

type workRoot struct {
	queries   atomic.Int32
	mutations atomic.Int32
}

func (r *workRoot) Ping() int32 { return r.queries.Add(1) }
func (r *workRoot) Bump() int32 { return r.mutations.Add(1) }
func (r *workRoot) Tick(ctx context.Context) <-chan int32 {
	ch := make(chan int32, 1)
	ch <- 1
	close(ch)
	return ch
}

// aliased builds `<op> { a1: <field> a2: <field> … }` with n distinct aliases.
func aliased(op, field string, n int) string {
	var b strings.Builder
	b.WriteString(op + " {")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, " a%d: %s", i, field)
	}
	b.WriteString(" }")
	return b.String()
}

// errorCode returns the extensions code of the first error, or "".
func errorCode(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var body struct {
		Errors []struct {
			Extensions map[string]any `json:"extensions"`
		} `json:"errors"`
	}
	require.NoError(t, json.Unmarshal(raw, &body))
	if len(body.Errors) == 0 || body.Errors[0].Extensions == nil {
		return ""
	}
	code, _ := body.Errors[0].Extensions["code"].(string)
	return code
}

// The mutation ceiling is the one that bounds serial work: at the cap every alias
// runs; one past it NOTHING runs.
func TestMutationRootFieldCap(t *testing.T) {
	root := &workRoot{}
	schema := MustParseSchema(workSDL, root)

	resp := schema.Exec(context.Background(), aliased("mutation", "bump", DefaultGraphQLMaxMutationRootFields), "", nil)
	require.Empty(t, resp.Errors, "a mutation AT the cap must execute")
	// The positive control for every zero below: the counter does move when aliases run.
	require.Equal(t, int32(DefaultGraphQLMaxMutationRootFields), root.mutations.Load())

	root.mutations.Store(0)
	resp = schema.Exec(context.Background(), aliased("mutation", "bump", DefaultGraphQLMaxMutationRootFields+1), "", nil)
	require.Len(t, resp.Errors, 1)
	assert.Equal(t, fmt.Sprintf("mutation (anonymous) selects %d root fields; the maximum is %d",
		DefaultGraphQLMaxMutationRootFields+1, DefaultGraphQLMaxMutationRootFields), resp.Errors[0].Message)
	assert.Equal(t, workLimitCode, resp.Errors[0].Extensions["code"])
	assert.Nil(t, resp.Data, "a refused document carries no data")
	assert.Equal(t, int32(0), root.mutations.Load(), "no resolver may run for a refused document")
}

func TestQueryRootFieldCap(t *testing.T) {
	root := &workRoot{}
	schema := MustParseSchema(workSDL, root)

	resp := schema.Exec(context.Background(), aliased("query", "ping", DefaultGraphQLMaxQueryRootFields), "", nil)
	require.Empty(t, resp.Errors)
	require.Equal(t, int32(DefaultGraphQLMaxQueryRootFields), root.queries.Load())

	root.queries.Store(0)
	resp = schema.Exec(context.Background(), aliased("query", "ping", DefaultGraphQLMaxQueryRootFields+1), "", nil)
	require.Len(t, resp.Errors, 1)
	assert.Contains(t, resp.Errors[0].Message, "query (anonymous) selects 21 root fields; the maximum is 20")
	assert.Equal(t, int32(0), root.queries.Load())
}

// Aliases hidden behind a fragment are counted as though written inline — through a
// named spread, an inline fragment, a directive-only inline fragment, a nested spread,
// and a mix of all of them. graphql-go expands each of these into the root set it
// executes, so a counter that stopped at the first level would let every one through.
func TestRootFieldCapSeesThroughFragments(t *testing.T) {
	over := DefaultGraphQLMaxMutationRootFields + 1
	fields := func(from, to int) string {
		var b strings.Builder
		for i := from; i <= to; i++ {
			fmt.Fprintf(&b, " a%d: bump", i)
		}
		return b.String()
	}
	cases := map[string]string{
		"named spread":   "mutation { ...F } fragment F on Mutation {" + fields(1, over) + " }",
		"inline typed":   "mutation { ... on Mutation {" + fields(1, over) + " } }",
		"inline untyped": "mutation { ... @include(if: true) {" + fields(1, over) + " } }",
		"nested spread":  "mutation { ...F } fragment F on Mutation { ...G } fragment G on Mutation {" + fields(1, over) + " }",
		"mixed": "mutation { a1: bump ... on Mutation { a2: bump ...F } } " +
			"fragment F on Mutation {" + fields(3, over) + " }",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			root := &workRoot{}
			schema := MustParseSchema(workSDL, root)
			resp := schema.Exec(context.Background(), doc, "", nil)
			require.Len(t, resp.Errors, 1, "%s", doc)
			assert.Contains(t, resp.Errors[0].Message, fmt.Sprintf("selects %d root fields", over))
			assert.Equal(t, int32(0), root.mutations.Load())
		})
	}

	// The control: the same shapes AT the cap execute, so the rejections above are
	// about the count and not about the fragment syntax.
	root := &workRoot{}
	schema := MustParseSchema(workSDL, root)
	resp := schema.Exec(context.Background(),
		"mutation { ...F } fragment F on Mutation {"+fields(1, DefaultGraphQLMaxMutationRootFields)+" }", "", nil)
	require.Empty(t, resp.Errors)
	assert.Equal(t, int32(DefaultGraphQLMaxMutationRootFields), root.mutations.Load())
}

// A response key repeated is ONE field — graphql-go merges it into one resolver call —
// so it is counted once. Counting occurrences instead would refuse a legitimate
// document the server would have run as a single field.
func TestRepeatedKeyCountsOnce(t *testing.T) {
	root := &workRoot{}
	schema := MustParseSchema(workSDL, root)
	doc := "mutation {" + strings.Repeat(" bump", 20) + " ...F } fragment F on Mutation { bump }"
	resp := schema.Exec(context.Background(), doc, "", nil)
	require.Empty(t, resp.Errors)
	assert.Equal(t, int32(1), root.mutations.Load(), "graphql-go runs a repeated key once")
}

// Every operation in the document is counted, not only the one selected — a
// document cannot smuggle an oversized operation past the check by naming another.
func TestEveryOperationIsCounted(t *testing.T) {
	root := &workRoot{}
	schema := MustParseSchema(workSDL, root)
	doc := "mutation Small { bump } " +
		strings.Replace(aliased("mutation", "bump", DefaultGraphQLMaxMutationRootFields+1), "mutation", "mutation Big", 1)
	resp := schema.Exec(context.Background(), doc, "Small", nil)
	require.Len(t, resp.Errors, 1)
	assert.Contains(t, resp.Errors[0].Message, "mutation Big selects 6 root fields")
	assert.Equal(t, int32(0), root.mutations.Load())
}

// 🔴 THE LENGTH CEILING IS APPLIED BEFORE THE DOCUMENT IS PARSED. The document here is
// both over the length ceiling AND unparseable, so the message says which check ran
// first: had it been parsed, the refusal would be the parse error. This is what keeps
// the work limit from being a new amplifier — a body-sized document is refused by its
// length, as it was before, rather than being fully parsed by a second parser.
func TestLengthIsCheckedBeforeParsing(t *testing.T) {
	t.Setenv(EnvGraphQLMaxQueryLength, "64")
	root := &workRoot{}
	schema := MustParseSchema(workSDL, root)

	doc := strings.Repeat("{a", 200) // unparseable, and 400 bytes
	resp := schema.Exec(context.Background(), doc, "", nil)
	require.Len(t, resp.Errors, 1)
	assert.Equal(t, "query length 400 exceeds the maximum allowed query length of 64 bytes", resp.Errors[0].Message)

	// The control: the same unparseable text UNDER the ceiling reaches the parser, and
	// is refused by it — so the message above is genuinely the order, not a coincidence.
	resp = schema.Exec(context.Background(), strings.Repeat("{a", 10), "", nil)
	require.Len(t, resp.Errors, 1)
	assert.Contains(t, resp.Errors[0].Message, "the document could not be parsed")
}

// A document the counter cannot parse is refused, not waved through uncounted.
func TestUnparseableDocumentIsRefused(t *testing.T) {
	root := &workRoot{}
	schema := MustParseSchema(workSDL, root)
	resp := schema.Exec(context.Background(), "mutation { bump", "", nil)
	require.Len(t, resp.Errors, 1)
	assert.Contains(t, resp.Errors[0].Message, "the document could not be parsed")
	assert.Equal(t, int32(0), root.mutations.Load())
}

// The ceilings are env-tunable in either direction and can never be switched off.
func TestRootFieldCapEnvOverrides(t *testing.T) {
	for _, tc := range []struct {
		val       string
		wantQuery int
		wantMut   int
	}{
		{"", DefaultGraphQLMaxQueryRootFields, DefaultGraphQLMaxMutationRootFields},
		{"2", 2, 2},
		{"50", 50, 50},
		{"0", DefaultGraphQLMaxQueryRootFields, DefaultGraphQLMaxMutationRootFields},
		{"-3", DefaultGraphQLMaxQueryRootFields, DefaultGraphQLMaxMutationRootFields},
		{"garbage", DefaultGraphQLMaxQueryRootFields, DefaultGraphQLMaxMutationRootFields},
	} {
		t.Setenv(EnvGraphQLMaxQueryRootFields, tc.val)
		t.Setenv(EnvGraphQLMaxMutationRootFields, tc.val)
		assert.Equal(t, tc.wantQuery, maxQueryRootFields(), "query cap for %q", tc.val)
		assert.Equal(t, tc.wantMut, maxMutationRootFields(), "mutation cap for %q", tc.val)
	}

	// And the override reaches the schema MustParseSchema builds, not just the resolver
	// function: with the mutation cap at 2, three aliases are refused.
	t.Setenv(EnvGraphQLMaxMutationRootFields, "2")
	root := &workRoot{}
	schema := MustParseSchema(workSDL, root)
	resp := schema.Exec(context.Background(), aliased("mutation", "bump", 3), "", nil)
	require.Len(t, resp.Errors, 1)
	assert.Contains(t, resp.Errors[0].Message, "the maximum is 2")
	assert.Equal(t, int32(0), root.mutations.Load())
}

func TestCheckWorkExported(t *testing.T) {
	require.NoError(t, CheckWork(aliased("mutation", "bump", DefaultGraphQLMaxMutationRootFields)))
	require.Error(t, CheckWork(aliased("mutation", "bump", DefaultGraphQLMaxMutationRootFields+1)))
	// A subscription is left to graphql-go's own one-field rule.
	require.NoError(t, CheckWork("subscription { tick }"))
}

// ── the CALLERS: the limit is only worth anything if the handlers use it ──────────────

// The HTTP handler — the unauthenticated data-plane path login is reached through —
// refuses an over-cap mutation before any resolver runs, and keeps the wire contract
// the replaced relay handler had: HTTP 200, application/json, an `errors` array.
func TestHttpHandlerEnforcesRootFieldCap(t *testing.T) {
	root := &workRoot{}
	srv := httptest.NewServer(NewHttpHandler(MustParseSchema(workSDL, root), map[ContextKey]interface{}{}, nil))
	defer srv.Close()

	post := func(doc string) (*http.Response, json.RawMessage) {
		body, _ := json.Marshal(map[string]string{"query": doc})
		resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		defer resp.Body.Close()
		var raw json.RawMessage
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&raw))
		return resp, raw
	}

	resp, raw := post(aliased("mutation", "bump", DefaultGraphQLMaxMutationRootFields))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "", errorCode(t, raw), "at the cap: %s", raw)
	require.Equal(t, int32(DefaultGraphQLMaxMutationRootFields), root.mutations.Load())

	root.mutations.Store(0)
	resp, raw = post(aliased("mutation", "bump", DefaultGraphQLMaxMutationRootFields+1))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	assert.Equal(t, workLimitCode, errorCode(t, raw), "over the cap: %s", raw)
	assert.Equal(t, int32(0), root.mutations.Load())
}

// The replaced relay handler's contract, pinned so the replacement cannot drift from
// it: a GraphQL error is a 200 with an errors array (not an HTTP status), and a body
// that does not decode is a 400.
func TestHttpHandlerWireContract(t *testing.T) {
	srv := httptest.NewServer(NewHttpHandler(MustParseSchema(workSDL, &workRoot{}), map[ContextKey]interface{}{}, nil))
	defer srv.Close()

	body, _ := json.Marshal(map[string]string{"query": "{ nope }"})
	resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	var out struct {
		Errors []struct{ Message string } `json:"errors"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	require.NotEmpty(t, out.Errors)
	assert.Contains(t, out.Errors[0].Message, "nope")

	resp, err = http.Post(srv.URL, "application/json", strings.NewReader("{not json"))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// The WebSocket transport runs queries and MUTATIONS through Subscribe, so the limit
// must be decided by the operation type in the document and not by the entry point.
// An over-cap mutation sent over the socket gets one error `next` and a `complete`,
// and runs nothing.
func TestWebSocketEnforcesRootFieldCap(t *testing.T) {
	root := &workRoot{}
	h := NewSubscriptionHandler(MustParseSchema(workSDL, root), map[ContextKey]interface{}{}, nil)
	srv := httptest.NewServer(h)
	defer srv.Close()

	dialer := websocket.Dialer{Subprotocols: []string{wsSubprotocol}}
	conn, _, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	writeMsg(t, conn, wsMessage{Type: msgConnectionInit})
	require.Equal(t, msgConnectionAck, readMsg(t, conn).Type)

	// The control first: an at-cap mutation over the socket does run.
	writeMsg(t, conn, subscribeMsg("ok", aliased("mutation", "bump", DefaultGraphQLMaxMutationRootFields), nil))
	msg := readMsg(t, conn)
	require.Equal(t, msgNext, msg.Type)
	require.Equal(t, "", errorCode(t, msg.Payload), "%s", msg.Payload)
	require.Equal(t, msgComplete, readMsg(t, conn).Type)
	require.Equal(t, int32(DefaultGraphQLMaxMutationRootFields), root.mutations.Load())

	root.mutations.Store(0)
	writeMsg(t, conn, subscribeMsg("big", aliased("mutation", "bump", DefaultGraphQLMaxMutationRootFields+1), nil))
	msg = readMsg(t, conn)
	require.Equal(t, msgNext, msg.Type)
	assert.Equal(t, "big", msg.ID)
	assert.Equal(t, workLimitCode, errorCode(t, msg.Payload), "%s", msg.Payload)
	done := readMsg(t, conn)
	assert.Equal(t, msgComplete, done.Type)
	assert.Equal(t, int32(0), root.mutations.Load())
}
