// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func requireSessionBatchRefusal(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	var rpc struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if w.Code != http.StatusBadRequest || w.Header().Get("Mcp-Session-Id") != "" ||
		json.Unmarshal(w.Body.Bytes(), &rpc) != nil || rpc.JSONRPC != "2.0" || string(rpc.ID) != "null" ||
		rpc.Error.Code != rpcInvalidRequest || !strings.Contains(rpc.Error.Message, "initialize") {
		t.Fatalf("want JSON-RPC invalid-request batch refusal, got %d %v %s", w.Code, w.Header(), w.Body.String())
	}
}

func TestSessionBatchCannotRunToolOrBlockInitialize(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		entered <- struct{}{}
		<-release
		_, _ = io.WriteString(w, `{"data":{"devices":{"results":[],"pagination":{"totalRecords":0}}}}`)
	}))
	defer up.Close()
	defer close(release)
	iss, validator := mustIssuerValidator(t)
	h, _ := New(testResource, "https://as.example.com", validator, testClient(up.URL), DefaultLimits())
	tok, err := iss.IssueOAuthAccess("acme", "batch@b.c", []string{"viewer"}, []string{"device:read"},
		coreauth.ScopeReadOnly, []string{testResource}, false, "mcp", "j-batch")
	if err != nil {
		t.Fatal(err)
	}
	wire := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, testResource, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+tok.Token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	batchDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		batchDone <- wire("[" + sessionInitialize + `,{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_devices","arguments":{}}}]`)
	}()
	var batch *httptest.ResponseRecorder
	select {
	case batch = <-batchDone:
	case <-entered:
		t.Error("session-creating batch invoked the GraphQL tool")
	case <-time.After(2 * time.Second):
		t.Fatal("batch neither refused nor dispatched")
	}
	initializeDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { initializeDone <- wire(sessionInitialize) }()
	select {
	case w := <-initializeDone:
		requireSession(t, w)
	case <-time.After(2 * time.Second):
		t.Fatal("ordinary initialize blocked behind the batch's slow GraphQL tool")
	}
	if batch != nil {
		requireSessionBatchRefusal(t, batch)
	}
	select {
	case <-entered:
		t.Fatal("refused batch invoked the GraphQL tool")
	default:
	}
	for _, body := range []string{"[" + sessionInitialize + "]", "[]", " \n[" + sessionInitialize + "] trailing"} {
		requireSessionBatchRefusal(t, wire(body))
	}
}

func TestSessionBatchRefusedBeforeAdmissionLock(t *testing.T) {
	s, _ := newServer(testClient("http://unused.invalid"))
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	defer close(release)
	h := limitSessions(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		entered <- struct{}{}
		<-release
	}), s, sessionCaps{total: 2, caller: 2})
	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, testResource, strings.NewReader(sessionInitialize)))
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("initialize did not enter admission")
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, testResource, strings.NewReader("["+sessionInitialize+"]")))
		done <- w
	}()
	select {
	case w := <-done:
		requireSessionBatchRefusal(t, w)
	case <-time.After(2 * time.Second):
		t.Fatal("batch refusal waited for admission lock")
	}
}

// Pin the SDK premise that makes holding the admission lock safe for one message.
func TestSessionlessSingleToolCannotRun(t *testing.T) {
	hits := make(chan struct{}, 1)
	s, _ := newServer(testClient("http://unused.invalid"))
	s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				hits <- struct{}{}
			}
			return next(ctx, method, req)
		}
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	r := httptest.NewRequest(http.MethodPost, testResource, strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_devices","arguments":{}}}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	select {
	case <-hits:
		t.Fatal("single sessionless tool reached receiving middleware")
	default:
	}
	if !strings.Contains(w.Body.String(), "invalid during session initialization") {
		t.Fatalf("sessionless tool: %d %s", w.Code, w.Body.String())
	}
}
