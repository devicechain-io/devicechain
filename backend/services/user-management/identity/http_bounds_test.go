// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/iam"
)

// The deadlines these tests run under, and how long a client waits before deciding
// the server is never going to answer. The gap is what makes a server with no
// deadline FAIL here, by value, instead of hanging the test.
const (
	testBodyDeadline = 200 * time.Millisecond
	testExecDeadline = 200 * time.Millisecond
	testGiveUp       = 3 * time.Second
)

func shortenDeadlines(t *testing.T) {
	t.Helper()
	prevBody, prevExec := bodyReadTimeout, execTimeout
	bodyReadTimeout, execTimeout = testBodyDeadline, testExecDeadline
	t.Cleanup(func() { bodyReadTimeout, execTimeout = prevBody, prevExec })
}

// exchange sends a raw request to h over a real connection and returns the status
// and whether the server closed the connection, failing if no answer arrives.
func exchange(t *testing.T, h http.Handler, raw string) (int, bool, time.Duration) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
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
			t.Fatalf("no response after %v: the handler is still waiting", elapsed)
		}
		t.Fatalf("reading the response: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode, resp.Close, elapsed
}

// stalled declares a 100-byte body and sends 5 bytes of it.
func stalled(method, path string, header string) string {
	return fmt.Sprintf("%s %s HTTP/1.1\r\nHost: t\r\n%sContent-Type: application/x-www-form-urlencoded\r\nContent-Length: 100\r\n\r\na=b&c", method, path, header)
}

func blockingTokenHandler() http.Handler {
	block := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	return TokenHandler(
		func(ctx context.Context, _, _ string, _ bool) error { return block(ctx) },
		func(ctx context.Context, _, _, _, _ string) (*OAuthTokens, error) { return nil, block(ctx) },
		func(ctx context.Context, _, _, _ string) (*OAuthTokens, error) { return nil, block(ctx) },
	)
}

// blockingAuthorizeSvc blocks resolving the client until the request context ends —
// a database that has stopped answering.
type blockingAuthorizeSvc struct{ fakeAuthorizeSvc }

func (*blockingAuthorizeSvc) ResolveAuthorizeClient(ctx context.Context, _, _ string) (*iam.OAuthClient, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// Every endpoint this package serves answers a stalled body at the read deadline, 400
// with the connection closed, rather than waiting on the client for ever.
func TestStalledBodyIsAnsweredAtTheDeadline(t *testing.T) {
	mux := http.NewServeMux()
	shortenDeadlines(t)
	RegisterMetadataHandlers(mux, "https://as.example.com/api/user-management")

	for _, tc := range []struct {
		name string
		h    http.Handler
		raw  string
	}{
		{"token", blockingTokenHandler(), stalled(http.MethodPost, TokenPath, "")},
		{"authorize", AuthorizeHandler(&fakeAuthorizeSvc{}), stalled(http.MethodPost, AuthorizePath, "")},
		{"userinfo", UserinfoHandler(func(string) (*auth.Claims, error) { return claimsFor("a@b.c", "t", false), nil }),
			stalled(http.MethodPost, UserinfoPath, "Authorization: Bearer x\r\n")},
		{"service-token", ServiceTokenHandler(func() string { return "s" }, func(string, []string) (auth.IssuedToken, error) {
			return auth.IssuedToken{}, nil
		}), stalled(http.MethodPost, auth.ServiceTokenPath, auth.ServiceSecretHeader+": s\r\n")},
		{"metadata", mux, stalled(http.MethodGet, MetadataPath, "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, closed, elapsed := exchange(t, tc.h, tc.raw)
			if status != http.StatusBadRequest || !closed {
				t.Errorf("status = %d, connection closed = %v; want 400 with the connection closed", status, closed)
			}
			if elapsed > 2*time.Second {
				t.Errorf("answered after %v, want about the %v deadline", elapsed, testBodyDeadline)
			}
		})
	}
}

// A handler blocked on a dependency that has stopped answering returns at the
// execution deadline, because the request context ends there.
func TestBlockedHandlerReturnsAtTheDeadline(t *testing.T) {
	shortenDeadlines(t)
	tokenForm := "grant_type=refresh_token&refresh_token=r&client_id=c"
	q := validParams()

	for _, tc := range []struct {
		name string
		h    http.Handler
		raw  string
	}{
		{"token", blockingTokenHandler(), fmt.Sprintf(
			"POST %s HTTP/1.1\r\nHost: t\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: %d\r\n\r\n%s",
			TokenPath, len(tokenForm), tokenForm)},
		{"authorize", AuthorizeHandler(&blockingAuthorizeSvc{}), fmt.Sprintf(
			"GET %s?%s HTTP/1.1\r\nHost: t\r\n\r\n", AuthorizePath, q.Encode())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, elapsed := exchange(t, tc.h, tc.raw)
			if status < 400 {
				t.Errorf("status = %d, want an error status once the deadline ended the request", status)
			}
			if elapsed > 2*time.Second {
				t.Errorf("answered after %v, want about the %v deadline", elapsed, testExecDeadline)
			}
		})
	}
}
