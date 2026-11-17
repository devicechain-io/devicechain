// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"io"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestSessionRetainedMemory measured ~7 KiB per idle SDK session (512 sessions,
// retained heap + stacks, Go 1.27/Linux, SDK v1.8.0). Budget 32 KiB each for
// authentication context, ownership metadata and growth: 1024 sessions use at
// most a planned 32 MiB of the chart's inherited 256Mi MCP memory limit
// (deploy/helm/devicechain/values.yaml). This leaves room for the existing
// 128 MiB message budget and service overhead. Sixteen per verified subject is
// comfortably above an agent's one or few sessions without letting one caller
// occupy the whole process. These constants introduce no rolling-upgrade keys.
const maxSessions = 1024
const maxCallerSessions = 16

type sessionCaps struct {
	total, caller int
}

// limitSessions runs inside authentication, body buffering and message admission.
// The service owns one SDK server per process. Only new-session POSTs take the
// lock or enumerate sessions; existing-session traffic (including DELETE) stays
// cheap and can free slots while an initialize is running.
func limitSessions(next http.Handler, server *mcp.Server, caps sessionCaps) http.Handler {
	var mu sync.Mutex
	owners := make(map[string]string)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Mcp-Session-Id") != "" {
			next.ServeHTTP(w, r)
			return
		}
		// Serialize admission through SDK creation and response-header capture.
		// Otherwise two initializes could both observe the final free slot.
		mu.Lock()
		defer mu.Unlock()

		live := make(map[string]bool)
		for session := range server.Sessions() {
			live[session.ID()] = true
		}
		// The SDK is authoritative: DELETE, idle reaping and failed initializes
		// all remove sessions. Ownership is metadata, never a lifetime counter.
		for id := range owners {
			if !live[id] {
				delete(owners, id)
			}
		}
		caller := callerKey(r)
		callerSessions := 0
		for _, owner := range owners {
			if owner == caller {
				callerSessions++
			}
		}
		// A live session with no recorded owner (none is expected: every session is
		// created here and its id read back below) still counts against the process
		// cap through len(live). It must not refuse every caller: that would turn one
		// unrecorded session into a process-wide lockout until it idles out.
		if len(live) >= caps.total || callerSessions >= caps.caller {
			body, _ := io.ReadAll(r.Body) // already buffered and bounded
			_, id, _ := countMessages(body)
			w.Header().Set("Retry-After", "1")
			writeRPCError(w, http.StatusTooManyRequests, rpcRateLimited, id,
				"too many live MCP sessions; reuse an existing session or close unused sessions and retry")
			return
		}
		next.ServeHTTP(w, r)
		if id := w.Header().Get("Mcp-Session-Id"); id != "" {
			owners[id] = caller
		}
	})
}
