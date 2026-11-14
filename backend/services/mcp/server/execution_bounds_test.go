// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHTTPDoneReleasesSlotsEvenIfToolIgnoresCancellation(t *testing.T) {
	httpCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	done := make(chan struct{}, 4)
	h := limitToolExecution(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		entered <- struct{}{}
		<-release // Deliberately ignore the tool context.
		return &mcp.CallToolResult{}, nil
	})
	started := 0
	t.Cleanup(func() {
		close(release)
		for i := 0; i < started; i++ {
			<-done
		}
	})
	for i := 0; i < 4; i++ {
		started++
		go func() {
			_, _ = h(context.Background(), "tools/call", &mcp.CallToolRequest{Extra: &mcp.RequestExtra{
				TokenInfo: &sdkauth.TokenInfo{UserID: "stubborn", Extra: map[string]any{
					executionKey: &requestExecution{ctx: httpCtx, slots: make(chan struct{}, 4)},
				}},
			}})
			done <- struct{}{}
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("tool not admitted within caller cap")
		}
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for !activeToolCalls.acquire("stubborn", 4) {
		if time.Now().After(deadline) {
			t.Fatal("HTTP cancellation leaked execution slots while tool kept running")
		}
		time.Sleep(time.Millisecond)
	}
	activeToolCalls.release("stubborn", 4)
}

func TestTimedOutHTTPCallsReleaseExecutionReservations(t *testing.T) {
	shortenDeadlines(t)
	release := make(chan struct{})
	cancelled := make(chan struct{}, 8)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the POST body so net/http can detect the peer disconnect.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
			cancelled <- struct{}{}
		case <-release:
		}
		<-release
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	t.Cleanup(up.Close)
	ts, token := boundedServer(t, up.URL)
	cs := connect(t, ts, token)
	// Release before closing the session and servers, including on test failure.
	t.Cleanup(func() { close(release) })
	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_devices", Arguments: map[string]any{}})
		cancel()
		if err == nil && !res.IsError {
			t.Fatal("blocked upstream unexpectedly succeeded")
		}
		select {
		case <-cancelled:
		case <-time.After(time.Second):
			t.Fatal("HTTP timeout did not cancel downstream work or permit the next call")
		}
	}
}
