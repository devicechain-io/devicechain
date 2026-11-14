// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTimedOutHTTPCallsKeepExecutionReservations(t *testing.T) {
	shortenDeadlines(t)
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	t.Cleanup(up.Close)
	ts, token := boundedServer(t, up.URL)
	cs := connect(t, ts, token)
	// Release before closing the session and servers, including on test failure.
	t.Cleanup(func() { close(release) })
	for i := 0; i < 4; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_devices", Arguments: map[string]any{}})
		cancel()
		if err == nil && !res.IsError {
			t.Fatal("blocked upstream unexpectedly succeeded")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_devices", Arguments: map[string]any{}})
	if err != nil || !res.IsError || !strings.Contains(contentText(res), "too many in-flight tool calls") {
		t.Fatalf("timed-out calls lost their execution reservations: result=%v err=%v", res, err)
	}
}
