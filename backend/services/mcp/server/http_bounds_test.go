// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
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

// A body that stops arriving is answered 400 at the read deadline, with the connection
// closed, on the endpoint and on the metadata document — through the mux Routes builds.
func TestStalledBodyIsAnsweredAtTheDeadline(t *testing.T) {
	shortenDeadlines(t)
	ts := httptest.NewServer(routesMux(t, routesResource))
	t.Cleanup(ts.Close)

	for _, tc := range []struct{ name, method, path string }{
		{"endpoint", http.MethodPost, "/"},
		{"metadata", http.MethodGet, ProtectedResourceMetadataPathFor(routesResource)},
		{"metadata-suffix", http.MethodGet, ProtectedResourceMetadataPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := net.Dial("tcp", ts.Listener.Addr().String())
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.Close()
			// 100 bytes declared, 5 sent.
			raw := tc.method + " " + tc.path + " HTTP/1.1\r\nHost: t\r\nContent-Type: application/json\r\n" +
				"Content-Length: 100\r\n\r\n{\"jso"
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
			if resp.StatusCode != http.StatusBadRequest || !resp.Close {
				t.Errorf("status = %d, connection closed = %v; want 400 with the connection closed", resp.StatusCode, resp.Close)
			}
			if elapsed > 2*time.Second {
				t.Errorf("answered after %v, want about the %v deadline", elapsed, testDeadline)
			}
		})
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

	iss, validator := mustIssuerValidator(t)
	mux := http.NewServeMux()
	Routes(mux, testResource, "https://as.example.com", validator, testClient(upstream.URL))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	tok, err := iss.IssueOAuthAccess("acme", "a@b.c", []string{"viewer"}, []string{"device:read"},
		coreauth.ScopeReadOnly, []string{testResource}, false, "mcp", "j-bound")
	if err != nil {
		t.Fatalf("issuing a token: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint:             ts.URL,
			HTTPClient:           &http.Client{Transport: bearerTransport{token: tok.Token}},
			MaxRetries:           -1,
			DisableStandaloneSSE: true,
		}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
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
