// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The deadlines these tests run under, and how long a client waits before deciding the
// server is never going to answer — the gap is what makes a server with no deadline
// FAIL here rather than hang.
const (
	testDeadline = 300 * time.Millisecond
	testGiveUp   = 4 * time.Second
)

func shortenDeadlines(t *testing.T) {
	t.Helper()
	prevBody, prevExec := bodyReadTimeout, execTimeout
	bodyReadTimeout, execTimeout = testDeadline, testDeadline
	t.Cleanup(func() { bodyReadTimeout, execTimeout = prevBody, prevExec })
}

// rawExchange sends raw on a fresh connection to ts and returns the response and how
// long it took, failing if none arrives within testGiveUp.
func rawExchange(t *testing.T, ts *httptest.Server, raw string) (*http.Response, time.Duration) {
	t.Helper()
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Write([]byte(raw)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(testGiveUp))
	started := time.Now()
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	elapsed := time.Since(started)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Fatalf("no response after %v: the server is still waiting on the body", elapsed)
		}
		t.Fatalf("reading the response: %v", err)
	}
	_ = resp.Body.Close()
	return resp, elapsed
}

// stalledRequest declares a 100-byte body and sends 5 bytes of it.
func stalledRequest(method, path, header string) string {
	return method + " " + path + " HTTP/1.1\r\nHost: t\r\nContent-Type: application/json\r\n" + header +
		"Content-Length: 100\r\n\r\n{\"jso"
}

// boundedServer serves the mux Routes builds, against upstreamURL, and returns it with
// a valid read-only token for this resource.
func boundedServer(t *testing.T, upstreamURL string) (*httptest.Server, string) {
	t.Helper()
	iss, validator := mustIssuerValidator(t)
	mux := http.NewServeMux()
	Routes(mux, testResource, "https://as.example.com", validator, testClient(upstreamURL))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	tok, err := iss.IssueOAuthAccess("acme", "a@b.c", []string{"viewer"}, []string{"device:read"},
		coreauth.ScopeReadOnly, []string{testResource}, false, "mcp", "j-bound")
	if err != nil {
		t.Fatalf("issuing a token: %v", err)
	}
	return ts, tok.Token
}

func connect(t *testing.T, ts *httptest.Server, token string) *mcp.ClientSession {
	t.Helper()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint:             ts.URL,
			HTTPClient:           &http.Client{Transport: bearerTransport{token: token}},
			MaxRetries:           -1,
			DisableStandaloneSSE: true,
		}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// A body that stops arriving is answered 400 at the read deadline, with the connection
// closed, on the endpoint (for a caller with a valid token) and on the metadata
// documents — through the mux Routes builds.
func TestStalledBodyIsAnsweredAtTheDeadline(t *testing.T) {
	shortenDeadlines(t)
	ts, token := boundedServer(t, "http://127.0.0.1:1")

	for _, tc := range []struct{ name, raw string }{
		{"endpoint", stalledRequest(http.MethodPost, "/", "Authorization: Bearer "+token+"\r\n")},
		{"metadata", stalledRequest(http.MethodGet, ProtectedResourceMetadataPathFor(testResource), "")},
		{"metadata-suffix", stalledRequest(http.MethodGet, ProtectedResourceMetadataPath, "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, elapsed := rawExchange(t, ts, tc.raw)
			if resp.StatusCode != http.StatusBadRequest || !resp.Close {
				t.Errorf("status = %d, connection closed = %v; want 400 with the connection closed", resp.StatusCode, resp.Close)
			}
			if elapsed > 2*time.Second {
				t.Errorf("answered after %v, want about the %v deadline", elapsed, testDeadline)
			}
		})
	}
}

// A caller with no valid token is refused before any of its body is read: its stalled
// POST gets the 401 challenge at once, not a 400 at the read deadline (which is what a
// body buffered in front of the bearer check would produce) — and the connection is
// closed rather than left waiting on the rest.
func TestAnUnauthenticatedBodyIsNotBuffered(t *testing.T) {
	shortenDeadlines(t)
	ts, _ := boundedServer(t, "http://127.0.0.1:1")

	for name, header := range map[string]string{
		"no token":      "",
		"invalid token": "Authorization: Bearer not-a-token\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			resp, elapsed := rawExchange(t, ts, stalledRequest(http.MethodPost, "/", header))
			if resp.StatusCode != http.StatusUnauthorized || !resp.Close {
				t.Errorf("status = %d, connection closed = %v; want 401 with the connection closed", resp.StatusCode, resp.Close)
			}
			if elapsed >= testDeadline {
				t.Errorf("answered after %v, at or past the read deadline: the body was read before the token was checked", elapsed)
			}
		})
	}
}

// A body over the endpoint's ceiling is refused 413 by the SDK. The buffering cuts it one
// byte past the ceiling, so this holds only while the SDK is configured with the same
// ceiling: at its own default it would read the cut body as truncated JSON instead.
func TestAnOversizedCallIsRefusedAsTooLarge(t *testing.T) {
	ts, token := boundedServer(t, "http://127.0.0.1:1")

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"pad":"` +
		strings.Repeat("x", maxRequestBodyBytes) + `"}}`
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", resp.StatusCode)
	}
}

// A tool call whose upstream has stopped answering returns at the execution deadline,
// not at the GraphQL client's own 30-second timeout.
func TestBlockedToolCallReturnsAtTheDeadline(t *testing.T) {
	shortenDeadlines(t)

	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(upstream.Close)

	ts, token := boundedServer(t, upstream.URL)
	cs := connect(t, ts, token)
	// Registered last so it runs first: the session's tool goroutine is still waiting
	// on the upstream after the HTTP request has been answered (the SDK runs it on the
	// session's context, which the GraphQL client's own timeout bounds), and the
	// servers' Close waits for it.
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithTimeout(context.Background(), testGiveUp)
	defer cancel()
	started := time.Now()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_devices", Arguments: map[string]any{}})
	elapsed := time.Since(started)
	if ctx.Err() != nil {
		t.Fatalf("no answer after %v: the tool call is still waiting on its upstream", elapsed)
	}
	if err == nil && !res.IsError {
		t.Fatalf("the call succeeded against an upstream that never answered")
	}
	if elapsed > 2*time.Second {
		t.Errorf("answered after %v, want about the %v deadline", elapsed, testDeadline)
	}
}

// The session's server-to-client SSE stream (a GET to the endpoint) is long-lived by
// design and is exempt from the execution deadline: it is still open well after the
// deadline a tool call runs under has passed.
func TestTheSSEStreamOutlivesTheExecutionDeadline(t *testing.T) {
	shortenDeadlines(t)
	ts, token := boundedServer(t, "http://127.0.0.1:1")
	cs := connect(t, ts, token)

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", cs.ID())
	req.Header.Set("Mcp-Protocol-Version", cs.InitializeResult().ProtocolVersion)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("opening the stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 for the session's stream", resp.StatusCode)
	}

	ended := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, resp.Body)
		ended <- err
	}()
	select {
	case err := <-ended:
		t.Fatalf("the stream ended (%v) before %v; an execution deadline of %v was applied to it", err, 4*testDeadline, testDeadline)
	case <-time.After(4 * testDeadline):
	}
}
