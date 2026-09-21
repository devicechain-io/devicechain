// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 🔴 WHAT THIS FILE EXISTS FOR: tools/call had NO WIRE COVERAGE AT ALL.
//
// Every other test of a tool's behaviour in this package invokes the tool's Go method
// directly and hands it authedReq — a CallToolRequest this package builds, carrying a
// RequestExtra{TokenInfo:{Extra:...}} that the test writes itself. That is a fixture of
// the very handoff the tool depends on: callerToken reads keys that, until this file,
// nothing but a test had ever written. The served session tests next door (risk_test.go)
// do drive a real client through the real middleware, and they stop at tools/list.
//
// So the three ingredients existed in three separate tests and had never been combined:
// production wiring, a token that survives the verifier, and an assertion about what a
// tool actually returned. Between them sat the whole of a tool call — argument
// unmarshalling into the typed Input, the SDK's delivery of TokenInfo to the handler,
// and the forwarding of the caller's bearer to the upstream, which is the ADR-047
// confused-deputy red line.
//
// 🔴 AND THE FIRST ASSERTION ANYONE WOULD WRITE HERE CANNOT FAIL. CallTool returns
// (result, error), and a tool handler that returns an error does NOT produce that error:
// the SDK reports it as a RESULT with IsError set and the message in Content, precisely
// so a model can see it and self-correct. `if err != nil { t.Fatal }` therefore passes
// for a server whose every tool call fails closed with "unauthenticated: no verified
// token" — the exact defect this file was written to detect. So no test below rests on
// err alone: the round trip goes through mustSucceed, which checks IsError and quotes the
// tool's own message into the failure, and the refusal test asserts IsError is SET.

// servedToolSession connects a real MCP client to the handler New serves, through the
// bearer middleware, with a read-only token bound to this resource — and points the
// tools' GraphQL client at upstream instead of at in-cluster Service DNS.
//
// It returns the session and the RAW TOKEN STRING the client presents, because the
// contract worth asserting is not "some credential reached the upstream" but "the
// caller's own did". Only the caller's token proves the server cannot exceed the user.
func servedToolSession(t *testing.T, upstreamURL string) (*mcp.ClientSession, string) {
	t.Helper()
	iss, validator := mustIssuerValidator(t)
	mcpHandler, _ := New(testResource, "https://as.example.com", validator, testClient(upstreamURL))
	ts := httptest.NewServer(mcpHandler)
	t.Cleanup(ts.Close)

	tok, err := iss.IssueOAuthAccess("acme", "a@b.c", []string{"viewer"}, []string{"device:read"},
		coreauth.ScopeReadOnly, []string{testResource}, false, "mcp", "j-call")
	if err != nil {
		t.Fatalf("issuing a read-only token: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint:   ts.URL,
			HTTPClient: &http.Client{Transport: bearerTransport{token: tok.Token}},
			// Nothing here is worth reconnecting for: a failure is the answer.
			MaxRetries:           -1,
			DisableStandaloneSSE: true,
		}, nil)
	if err != nil {
		t.Fatalf("client connect through the served handler: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, tok.Token
}

// recordingUpstream stands in for device-management's /graphql, recording the single
// call made to it and answering with body.
type recordingUpstream struct {
	auth string
	vars map[string]any
	hits int
}

func newRecordingUpstream(t *testing.T, body string) (*recordingUpstream, string) {
	t.Helper()
	up := &recordingUpstream{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up.hits++
		up.auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		var envelope struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(raw, &envelope)
		up.vars = envelope.Variables
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return up, ts.URL
}

// mustSucceed fails the test unless the call came back as a successful tool result,
// quoting the tool's own error text when it did not — see the IsError note above.
func mustSucceed(t *testing.T, res *mcp.CallToolResult, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("tools/call returned a protocol error: %v", err)
	}
	if res.IsError {
		t.Fatalf("tools/call came back as a tool error: %s", contentText(res))
	}
}

// contentText flattens a result's text content so a failure message can quote it.
func contentText(res *mcp.CallToolResult) string {
	out := ""
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			out += tc.Text
		}
	}
	if out == "" {
		return "(no text content)"
	}
	return out
}

// decodeStructured re-marshals a result's structured content into the tool's own output
// type. It goes through JSON deliberately: StructuredContent arrives at the client as
// whatever the wire produced, so decoding it this way asserts the output schema survived
// the round trip rather than reading back a Go value that never left the process.
func decodeStructured(t *testing.T, res *mcp.CallToolResult, out any) {
	t.Helper()
	if res.StructuredContent == nil {
		t.Fatalf("tool result carried no structured content; text was: %s", contentText(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("re-marshaling structured content: %v", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decoding structured content %s: %v", raw, err)
	}
}

// TestAToolCalledOverTheWireReachesTheUpstreamAsTheCaller is the round trip: a real
// tools/call, through the production handler and its bearer middleware, out to a fake
// upstream, and back as structured content.
//
// The four assertions are four separate handoffs, and each one is somewhere a defect
// could live that every existing test is blind to:
//
//   - the call succeeds at all ⇒ the SDK delivered TokenInfo to the handler and
//     callerToken found the token there. Nothing else in the tree shows this; the unit
//     tests supply that structure themselves.
//   - the upstream saw the CALLER'S token, byte for byte ⇒ the anti-confused-deputy
//     contract, asserted against the issued string rather than against "non-empty", so a
//     server that forwarded some other credential fails here.
//   - pageSize and deviceType arrived in the GraphQL criteria ⇒ the JSON-RPC arguments
//     were unmarshalled into the typed Input. A tool that ignored its arguments entirely
//     would return a perfectly good page of devices and satisfy every other check.
//   - the devices came back ⇒ the output schema survived the round trip.
//
// The hit count is checked alongside them so the two assertions about what the upstream
// SAW describe the only call that was made, rather than the last of several.
func TestAToolCalledOverTheWireReachesTheUpstreamAsTheCaller(t *testing.T) {
	up, url := newRecordingUpstream(t, `{"data":{"devices":{`+
		`"results":[{"token":"d1","name":"D1","description":"first","externalId":"VIN1",`+
		`"deviceType":{"token":"truck"}}],"pagination":{"totalRecords":1}}}}`)

	cs, token := servedToolSession(t, url)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "list_devices",
		// Both arguments are non-default on purpose: pageSize 7 is neither the default
		// (25) nor the cap (100), so a tool that dropped it lands on a value this test
		// can tell apart from the one it sent.
		Arguments: map[string]any{"pageSize": 7, "deviceType": "truck"},
	})
	mustSucceed(t, res, err)

	if up.hits != 1 {
		t.Errorf("expected exactly one upstream call, got %d", up.hits)
	}
	if want := "Bearer " + token; up.auth != want {
		t.Errorf("upstream saw %q, want the caller's own token %q", up.auth, want)
	}

	criteria, _ := up.vars["criteria"].(map[string]any)
	if criteria == nil {
		t.Fatalf("upstream saw no criteria variable; variables were %+v", up.vars)
	}
	if got := criteria["pageSize"]; got != float64(7) {
		t.Errorf("criteria.pageSize = %v, want 7 — the tool's arguments did not survive "+
			"the JSON-RPC round trip", got)
	}
	if got := criteria["deviceType"]; got != "truck" {
		t.Errorf("criteria.deviceType = %v, want %q", got, "truck")
	}

	var out ListDevicesOutput
	decodeStructured(t, res, &out)
	if out.TotalRecords != 1 || len(out.Devices) != 1 {
		t.Fatalf("unexpected result: %+v", out)
	}
	if d := out.Devices[0]; d.Token != "d1" || d.DeviceType != "truck" || d.ExternalId != "VIN1" {
		t.Errorf("unexpected device: %+v", d)
	}
}

// TestAToolCallThatFailsValidationNeverReachesTheUpstream is the counterweight, and the
// assertion that carries it is the HIT COUNT, not the error.
//
// get_device refuses an empty token list before it builds a query. Seen from the client,
// that refusal is indistinguishable from one made after a round trip that returned
// nothing — same IsError, same message — so an error assertion alone would be satisfied
// by a tool that asked the upstream first and refused afterwards. Only the upstream's own
// record separates them, which is why it is checked first here.
//
// 🔴 THIS IS DELIBERATELY NOT THE UNAUTHENTICATED CASE, and the first draft of this file
// got that wrong. A tools/call with no bearer never reaches a tool at all: the SDK
// requires an initialized session, so the upstream stays untouched even with the bearer
// middleware removed AND callerToken made to accept an absent token — both mutants were
// killed by the status code while the hit count sat at zero, which made "never reached
// the upstream" a claim the test could not actually see. The 401 itself is pinned next
// door in TestServerNew_AuthMiddlewareGatesRequests. Here the session is real and
// authenticated, so the tool genuinely runs, and the count means what it says.
func TestAToolCallThatFailsValidationNeverReachesTheUpstream(t *testing.T) {
	up, url := newRecordingUpstream(t, `{"data":{"devicesByToken":[]}}`)
	cs, _ := servedToolSession(t, url)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "get_device",
		Arguments: map[string]any{"tokens": []string{}},
	})
	if err != nil {
		t.Fatalf("tools/call returned a protocol error: %v", err)
	}

	if up.hits != 0 {
		t.Errorf("get_device queried the upstream %d time(s) before refusing an empty "+
			"token list; the refusal must come first", up.hits)
	}
	if !res.IsError {
		t.Errorf("get_device with no tokens came back as a success: %s", contentText(res))
	}
}
