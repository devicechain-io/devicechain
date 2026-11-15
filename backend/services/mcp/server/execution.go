// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The SDK uses a session context; bind tool work and reservations to HTTP too.
var activeToolCalls = &messageBudget{callers: make(map[string]int)}

const executionKey = "devicechain.mcp.requestExecution"

type requestExecution struct {
	ctx   context.Context
	slots chan struct{}
}

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
		if execution, ok := extra.TokenInfo.Extra[executionKey].(*requestExecution); ok {
			select {
			case execution.slots <- struct{}{}:
				defer func() { <-execution.slots }()
			case <-execution.ctx.Done():
				return nil, execution.ctx.Err()
			}
			toolCtx, cancel := context.WithCancel(ctx)
			stop := context.AfterFunc(execution.ctx, cancel)
			defer stop()
			defer cancel()
			if execution.ctx.Err() != nil {
				cancel()
			}
			ctx = toolCtx
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !activeToolCalls.acquire(caller, 1) {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{
				&mcp.TextContent{Text: "too many in-flight tool calls; retry shortly"},
			}}, nil
		}
		// Release exactly once even if a downstream handler ignores cancellation.
		var once sync.Once
		release := func() { once.Do(func() { activeToolCalls.release(caller, 1) }) }
		stop := context.AfterFunc(ctx, release)
		defer stop()
		defer release()
		return next(ctx, method, req)
	}
}
