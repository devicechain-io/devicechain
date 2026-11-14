// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The SDK can continue tool work on the session context after its HTTP request
// times out. Keep a separate execution reservation until that work actually ends;
// the HTTP message reservation independently bounds retained batch responses.
var activeToolCalls = &messageBudget{callers: make(map[string]int)}

func limitToolExecution(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method != "tools/call" {
			return next(ctx, method, req)
		}
		extra := req.GetExtra()
		if extra == nil || extra.TokenInfo == nil || extra.TokenInfo.UserID == "" {
			return nil, fmt.Errorf("unauthenticated: no verified caller")
		}
		caller := extra.TokenInfo.UserID
		if !activeToolCalls.acquire(caller, 1) {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{
				&mcp.TextContent{Text: "too many in-flight tool calls; retry shortly"},
			}}, nil
		}
		defer activeToolCalls.release(caller, 1)
		return next(ctx, method, req)
	}
}
