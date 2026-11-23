// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package scenarioverify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestS7MCPUsesBearerSessionAndStructuredSSEToolAnswers(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tenant-member-token" {
			t.Error("MCP did not receive caller's user bearer")
			w.WriteHeader(401)
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(200)
			return
		}
		var request struct {
			ID     int
			Method string
			Params map[string]any
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		calls++
		switch request.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "real-session")
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": "2025-06-18"}})
		case "notifications/initialized":
			if r.Header.Get("Mcp-Session-Id") != "real-session" || request.ID != 0 {
				t.Error("invalid initialized notification")
			}
			w.WriteHeader(202)
		case "tools/call":
			if r.Header.Get("Mcp-Session-Id") != "real-session" || r.Header.Get("Mcp-Protocol-Version") != "2025-06-18" {
				t.Error("session binding lost")
			}
			if request.Params["name"] != "list_devices" {
				t.Error("wrong tool")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			result := map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"structuredContent": map[string]any{
				"totalRecords": 1, "devices": []any{map[string]any{"token": "same-device", "untrusted": map[string]any{"description": map[string]any{"value": "tenant-b", "source": "device-reported"}}}},
			}}}
			raw, _ := json.Marshal(result)
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", raw)
		default:
			t.Error("unsupported protocol operation")
		}
	}))
	defer server.Close()
	m := &s7MCP{http: server.Client(), endpoint: server.URL, token: "tenant-member-token"}
	if err := m.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer m.close()
	rows, err := s7ToolRows[S7Device](context.Background(), m, "list_devices", "devices", nil)
	if err != nil || len(rows) != 1 || rows[0].Description != "tenant-b" || calls != 3 {
		t.Fatalf("rows=%v calls=%d err=%v", rows, calls, err)
	}
}

func TestS7MCPDoesNotWaitForSSEStreamEOF(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ ID int }
		json.NewDecoder(r.Body).Decode(&request)
		w.Header().Set("Content-Type", "text/event-stream")
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"answer": "fleet"}})
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", raw)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 2 * time.Second
	m := &s7MCP{http: client, endpoint: server.URL, token: "member"}
	var out struct{ Answer string }
	if err := m.rpc(context.Background(), "tools/call", map[string]any{"name": "list_devices"}, &out); err != nil || out.Answer != "fleet" {
		t.Fatalf("complete SSE result was not returned: %v", err)
	}
}

func TestS7MCPRefusesBearerRedirects(t *testing.T) {
	leaked := make(chan struct{}, 1)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked <- struct{}{} }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", other.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	m := &s7MCP{http: server.Client(), endpoint: server.URL, token: "member-user-token"}
	if err := m.initialize(context.Background()); err == nil {
		t.Fatal("redirected MCP endpoint passed")
	}
	select {
	case <-leaked:
		t.Fatal("MCP bearer followed a redirect")
	default:
	}
}

func TestS7MCPPaginationRefusesTruncation(t *testing.T) {
	page := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ ID int }
		json.NewDecoder(r.Body).Decode(&request)
		page++
		rows := []any{}
		if page == 1 {
			rows = append(rows, map[string]any{"token": "first"})
		}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"structuredContent": map[string]any{"totalRecords": 2, "devices": rows}}})
	}))
	defer server.Close()
	m := &s7MCP{http: server.Client(), endpoint: server.URL, token: "member"}
	if _, err := s7ToolRows[S7Device](context.Background(), m, "list_devices", "devices", nil); err == nil {
		t.Fatal("incomplete platform enumeration passed")
	}
}
