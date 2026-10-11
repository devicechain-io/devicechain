// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// rawExchange writes req on a fresh connection to srv and returns the response it gets
// back, and how long that took. The client gives up after giveUp, which is what turns
// a server that hangs into a test that FAILS rather than one that hangs.
func rawExchange(t *testing.T, srv *httptest.Server, req string, giveUp time.Duration) (*http.Response, time.Duration) {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(giveUp))
	started := time.Now()
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	elapsed := time.Since(started)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Fatalf("no response after %v: the server is still waiting on the client", elapsed)
		}
		t.Fatalf("reading the response: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp, elapsed
}

// stalledPost declares a 100-byte body and sends 5 of them.
const stalledPost = "POST /x HTTP/1.1\r\nHost: t\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: 100\r\n\r\na=b&c"

// A body that stops arriving is answered 400 at the read deadline, and the connection
// is closed rather than kept for the rest.
func TestBoundRequestsAnswersAStalledBodyAtTheDeadline(t *testing.T) {
	var reached atomic.Bool
	srv := httptest.NewServer(BoundRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
		_, _ = io.ReadAll(r.Body)
	}), RequestBounds{MaxBodyBytes: 1 << 10, BodyReadTimeout: 200 * time.Millisecond}))
	t.Cleanup(srv.Close)

	resp, elapsed := rawExchange(t, srv, stalledPost, 3*time.Second)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if !resp.Close {
		t.Error("the connection is kept open after a stalled body; it must be closed")
	}
	if elapsed > 2*time.Second {
		t.Errorf("answered after %v, want about the 200ms deadline", elapsed)
	}
	if reached.Load() {
		t.Error("the handler ran on a body that was never delivered")
	}
}

// A client that sends a complete form, declares a longer body and stalls is cut off
// too: the body is read to its end under the deadline, not just as far as the handler
// happens to read.
func TestBoundRequestsReadsTheBodyToItsEnd(t *testing.T) {
	srv := httptest.NewServer(BoundRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reads nothing at all, the way a JWKS or metadata handler does.
		w.WriteHeader(http.StatusOK)
	}), RequestBounds{MaxBodyBytes: 1 << 10, BodyReadTimeout: 200 * time.Millisecond}))
	t.Cleanup(srv.Close)

	resp, elapsed := rawExchange(t, srv, stalledPost, 3*time.Second)
	if resp.StatusCode != http.StatusBadRequest || elapsed > 2*time.Second {
		t.Fatalf("status = %d after %v, want 400 at about the deadline", resp.StatusCode, elapsed)
	}
}

// A handler blocked on its context returns when the execution deadline ends it.
func TestBoundRequestsEndsABlockedHandlerAtTheDeadline(t *testing.T) {
	srv := httptest.NewServer(BoundRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		http.Error(w, "deadline", http.StatusServiceUnavailable)
	}), RequestBounds{MaxBodyBytes: 1 << 10, ExecTimeout: 200 * time.Millisecond}))
	t.Cleanup(srv.Close)

	resp, elapsed := rawExchange(t, srv, "GET /x HTTP/1.1\r\nHost: t\r\n\r\n", 3*time.Second)
	if resp.StatusCode != http.StatusServiceUnavailable || elapsed > 2*time.Second {
		t.Fatalf("status = %d after %v, want the handler's 503 at about the deadline", resp.StatusCode, elapsed)
	}
}

// The counterweight: a body delivered promptly reaches the handler whole, on a
// connection that stays open, and the defaults apply when nothing is set.
func TestBoundRequestsPassesAPromptBodyThrough(t *testing.T) {
	type seen struct {
		body     string
		deadline time.Time
	}
	ch := make(chan seen, 1)
	srv := httptest.NewServer(BoundRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		d, _ := r.Context().Deadline()
		ch <- seen{string(b), d}
		w.WriteHeader(http.StatusNoContent)
	}), RequestBounds{MaxBodyBytes: 1 << 10}))
	t.Cleanup(srv.Close)

	sent := time.Now()
	resp, _ := rawExchange(t, srv, "POST /x HTTP/1.1\r\nHost: t\r\nContent-Length: 7\r\n\r\na=b&c=d", 3*time.Second)
	s := <-ch
	if resp.StatusCode != http.StatusNoContent || s.body != "a=b&c=d" {
		t.Fatalf("status = %d, body = %q; want 204 and the whole body", resp.StatusCode, s.body)
	}
	if resp.Close {
		t.Error("a well-formed request had its connection closed")
	}
	if d := s.deadline.Sub(sent); d < DefaultRequestExecTimeout-5*time.Second || d > DefaultRequestExecTimeout+5*time.Second {
		t.Errorf("request deadline is %v away, want about the default %v", d, DefaultRequestExecTimeout)
	}
}

// A body over the ceiling reaches the handler cut at MaxBodyBytes+1 — so the handler
// sees it is too long — and the connection is closed instead of drained.
func TestBoundRequestsCutsAnOversizedBody(t *testing.T) {
	ch := make(chan int, 1)
	srv := httptest.NewServer(BoundRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ch <- len(b)
		w.WriteHeader(http.StatusRequestEntityTooLarge)
	}), RequestBounds{MaxBodyBytes: 4}))
	t.Cleanup(srv.Close)

	resp, _ := rawExchange(t, srv, "POST /x HTTP/1.1\r\nHost: t\r\nContent-Length: 10\r\n\r\n0123456789", 3*time.Second)
	if n := <-ch; n != 5 {
		t.Errorf("the handler saw %d bytes, want MaxBodyBytes+1 = 5", n)
	}
	if !resp.Close {
		t.Error("an oversized body left the connection open to be drained")
	}
}

// NoExecTimeout is for a long-lived stream: its context carries no deadline.
func TestBoundRequestsNoExecTimeoutLeavesTheContextOpen(t *testing.T) {
	var has bool
	h := BoundRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, has = r.Context().Deadline()
	}), RequestBounds{MaxBodyBytes: 1, NoExecTimeout: true})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil).WithContext(context.Background()))
	if has {
		t.Error("NoExecTimeout still put a deadline on the request context")
	}
}

// A wrapper with no body ceiling refuses to be built.
func TestBoundRequestsRequiresABodyCeiling(t *testing.T) {
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "MaxBodyBytes") {
			t.Fatalf("recovered %v, want a panic naming MaxBodyBytes", r)
		}
	}()
	BoundRequests(http.NotFoundHandler(), RequestBounds{})
}

// A body that runs past the ceiling and then stalls is answered at once. The handler
// refuses the cut body, and the unread rest is not waited for: net/http discards it
// after the handler returns, and it must not do that with no deadline in force.
// Both framings, since a chunked body has no declared length to give up on.
func TestBoundRequestsDoesNotWaitOnTheRestOfAnOversizedStalledBody(t *testing.T) {
	srv := httptest.NewServer(BoundRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusRequestEntityTooLarge)
	}), RequestBounds{MaxBodyBytes: 16, BodyReadTimeout: 10 * time.Second}))
	t.Cleanup(srv.Close)

	over := strings.Repeat("x", 20)
	for name, raw := range map[string]string{
		"content-length": "POST /x HTTP/1.1\r\nHost: t\r\nContent-Length: 1000\r\n\r\n" + over,
		"chunked":        "POST /x HTTP/1.1\r\nHost: t\r\nTransfer-Encoding: chunked\r\n\r\n14\r\n" + over + "\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			resp, elapsed := rawExchange(t, srv, raw, 3*time.Second)
			if resp.StatusCode != http.StatusRequestEntityTooLarge || !resp.Close {
				t.Errorf("status = %d, connection closed = %v; want the handler's 413 with the connection closed", resp.StatusCode, resp.Close)
			}
			if elapsed > time.Second {
				t.Errorf("answered after %v; the server waited on the rest of a body it had already refused", elapsed)
			}
		})
	}
}

// A handler that refuses without reading its body — an authentication failure in front
// of the read — is answered at once too, with the connection closed, not after the
// server has waited on the body it never wanted.
func TestRequestDeadlinesDoesNotWaitOnABodyTheHandlerNeverRead(t *testing.T) {
	srv := httptest.NewServer(RequestDeadlines(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
	}), RequestBounds{BodyReadTimeout: 10 * time.Second}))
	t.Cleanup(srv.Close)

	resp, elapsed := rawExchange(t, srv, stalledPost, 3*time.Second)
	if resp.StatusCode != http.StatusUnauthorized || !resp.Close {
		t.Errorf("status = %d, connection closed = %v; want 401 with the connection closed", resp.StatusCode, resp.Close)
	}
	if elapsed > time.Second {
		t.Errorf("answered after %v; the server waited on a body the handler never read", elapsed)
	}
}

// installBudget swaps in a budget of capacity bytes for one test.
func installBudget(t *testing.T, capacity int64) {
	t.Helper()
	prev := bodyBudget
	bodyBudget = newByteBudget(capacity)
	t.Cleanup(func() { bodyBudget = prev })
}

// When the process-wide buffer budget is spent, a request that would buffer is refused
// 503 with Retry-After and the connection closed, rather than buffered regardless; and
// the budget comes back when the request holding it finishes.
func TestBufferBodyRefusesWhenTheBudgetIsSpent(t *testing.T) {
	installBudget(t, 17) // exactly one request's reservation at MaxBodyBytes 16
	hold := make(chan struct{})
	entered := make(chan struct{}, 1)
	srv := httptest.NewServer(BoundRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hold" {
			entered <- struct{}{}
			<-hold
		}
		w.WriteHeader(http.StatusNoContent)
	}), RequestBounds{MaxBodyBytes: 16}))
	t.Cleanup(srv.Close)
	post := func(path string) string {
		return "POST " + path + " HTTP/1.1\r\nHost: t\r\nContent-Length: 3\r\n\r\nabc"
	}

	held := make(chan int, 1)
	go func() {
		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			held <- 0
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte(post("/hold")))
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			held <- 0
			return
		}
		held <- resp.StatusCode
	}()
	<-entered

	resp, _ := rawExchange(t, srv, post("/x"), 3*time.Second)
	if resp.StatusCode != http.StatusServiceUnavailable || !resp.Close || resp.Header.Get("Retry-After") == "" {
		t.Errorf("status = %d, closed = %v, Retry-After = %q; want 503, closed, with Retry-After",
			resp.StatusCode, resp.Close, resp.Header.Get("Retry-After"))
	}

	close(hold)
	if got := <-held; got != http.StatusNoContent {
		t.Fatalf("the request holding the budget got %d, want 204", got)
	}
	resp, _ = rawExchange(t, srv, post("/x"), 3*time.Second)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("after the holder finished, status = %d; the budget was not given back", resp.StatusCode)
	}
}

// A request with no body reserves nothing, so a spent budget does not refuse it.
func TestBufferBodyReservesNothingForABodilessRequest(t *testing.T) {
	installBudget(t, 0)
	h := BoundRequests(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), RequestBounds{MaxBodyBytes: 16})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}
