// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const sessionInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"session-cap-test","version":"1"}}}`

func sessionCapOption(total, caller int) Option {
	return func(o *options) { o.sessionCaps = &sessionCaps{total: total, caller: caller} }
}

func sessionCapWire(t *testing.T, total, caller int) func(string, string, string, string) *httptest.ResponseRecorder {
	t.Helper()
	iss, validator := mustIssuerValidator(t)
	h, _ := New(testResource, "https://as.example.com", validator, testClient("http://unused.invalid"),
		Limits{MaxBatchEntries: 16, CallsPerSecond: 100000, CallsBurst: 100000}, sessionCapOption(total, caller))
	tokens := map[string]string{}
	ids := map[string]string{}
	wire := func(user, method, id, body string) *httptest.ResponseRecorder {
		t.Helper()
		if tokens[user] == "" {
			tok, err := iss.IssueOAuthAccess("acme", user, []string{"viewer"}, []string{"device:read"},
				coreauth.ScopeReadOnly, []string{testResource}, false, "mcp", "j-session")
			if err != nil {
				t.Fatal(err)
			}
			tokens[user] = tok.Token
		}
		r := httptest.NewRequest(method, testResource, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+tokens[user])
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("Mcp-Session-Id", id)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if got := w.Header().Get("Mcp-Session-Id"); got != "" {
			ids[got] = user
		}
		return w
	}
	t.Cleanup(func() {
		for id, user := range ids {
			wire(user, http.MethodDelete, id, "")
		}
	})
	return wire
}

func requireSession(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	id := w.Header().Get("Mcp-Session-Id")
	if w.Code != http.StatusOK || id == "" {
		t.Fatalf("initialize: status=%d session=%q body=%s", w.Code, id, w.Body.String())
	}
	return id
}

func requireSessionRefusal(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	var rpc struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" ||
		w.Header().Get("Mcp-Session-Id") != "" || w.Header().Get("Content-Type") != "application/json" ||
		json.Unmarshal(w.Body.Bytes(), &rpc) != nil || rpc.JSONRPC != "2.0" || rpc.ID != 1 ||
		rpc.Error.Code != rpcRateLimited || rpc.Error.Message == "" {
		t.Fatalf("want session refusal (429 + Retry-After + JSON-RPC), got %d %v %s", w.Code, w.Header(), w.Body.String())
	}
}

func TestSessionCapsCallerAndDelete(t *testing.T) {
	wire := sessionCapWire(t, 4, 2)
	id := requireSession(t, wire("a@b.c", http.MethodPost, "", sessionInitialize))
	requireSession(t, wire("a@b.c", http.MethodPost, "", sessionInitialize))
	requireSessionRefusal(t, wire("a@b.c", http.MethodPost, "", sessionInitialize))
	requireSession(t, wire("other@b.c", http.MethodPost, "", sessionInitialize))
	w := wire("a@b.c", http.MethodPost, id, `{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"result"`) {
		t.Fatalf("existing session refused: %d %s", w.Code, w.Body.String())
	}
	if w := wire("a@b.c", http.MethodDelete, id, ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d %s", w.Code, w.Body.String())
	}
	requireSession(t, wire("a@b.c", http.MethodPost, "", sessionInitialize))
}

func TestSessionCapsProcess(t *testing.T) {
	wire := sessionCapWire(t, 3, 2)
	for i := range 3 {
		requireSession(t, wire(fmt.Sprintf("user%d@b.c", i), http.MethodPost, "", sessionInitialize))
	}
	requireSessionRefusal(t, wire("fourth@b.c", http.MethodPost, "", sessionInitialize))
}

// Measure retained heap plus stack growth, rather than allocations per request:
// response buffers and token verification garbage are not idle session memory.
func TestSessionRetainedMemory(t *testing.T) {
	s, _ := newServer(testClient("http://unused.invalid"))
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{SessionTimeout: sessionTimeout, MaxRequestBodyBytes: maxRequestBodyBytes})
	ids := make([]string, 0, 512)
	t.Cleanup(func() {
		for ss := range s.Sessions() {
			_ = ss.Close()
		}
	})
	open := func() {
		r := httptest.NewRequest(http.MethodPost, testResource, strings.NewReader(sessionInitialize))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		ids = append(ids, requireSession(t, w))
		_, _ = io.Copy(io.Discard, w.Result().Body)
	}
	open() // warm shared SDK/schema caches
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 512 {
		open()
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	retained := int64(after.HeapAlloc) - int64(before.HeapAlloc) + int64(after.StackInuse) - int64(before.StackInuse)
	t.Logf("512 idle sessions retain %d bytes (%d bytes/session, heap + stacks)", retained, retained/512)
	if retained > 512*32*1024 {
		t.Fatalf("idle SDK sessions exceed the 32 KiB/session planning allowance: %d bytes", retained)
	}
	runtime.KeepAlive(ids)
}
