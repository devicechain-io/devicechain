// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/devicechain-io/dc-microservice/core"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
)

// Limits bounds how much work one caller can ask of this server in one request and over
// time. Both bounds are counted in JSON-RPC MESSAGES, not HTTP requests, because a
// protocol version before 2025-06-18 lets one POST carry a batch: a 1 MiB body of
// minimal tools/call entries is ~15,000 tool calls and as many audit lines, which a
// per-request limit would count as one.
type Limits struct {
	// MaxBatchEntries is the most messages one JSON-RPC batch may carry. A batch over it is
	// refused whole with a JSON-RPC error; nothing is truncated or partly run.
	MaxBatchEntries int
	// CallsPerSecond and CallsBurst are the per-caller token bucket every message is
	// charged against. The caller is the verified token's subject (tenant + email), so
	// one user's flood spends only that user's allowance. CallsBurst must be at least
	// MaxBatchEntries or a full batch could never be admitted.
	CallsPerSecond float64
	CallsBurst     int
}

// Defaults for Limits. A batch of 16 is far above anything a real client sends; 20/s
// sustained with a burst of 40 is several times what an interactive agent issues.
const (
	DefaultMaxBatchEntries = 16
	DefaultCallsPerSecond  = 20.0
	DefaultCallsBurst      = 40
)

// DefaultLimits are the limits a service with nothing configured runs under.
func DefaultLimits() Limits {
	return Limits{MaxBatchEntries: DefaultMaxBatchEntries, CallsPerSecond: DefaultCallsPerSecond, CallsBurst: DefaultCallsBurst}
}

// JSON-RPC error codes used here (JSON-RPC 2.0 reserves -32600 for an invalid request;
// -32000..-32099 is the implementation-defined server-error range).
const (
	rpcInvalidRequest = -32600
	rpcRateLimited    = -32029
)

// limitMessages is the handler that applies Limits in front of the MCP handler. It must
// sit INSIDE the bearer check (it reads the verified subject from the request context)
// and AFTER the body is buffered (it reads the body and puts it back).
func limitMessages(next http.Handler, l Limits) http.Handler {
	limiter := core.NewTenantRateLimiter(core.StaticCeiling(l.CallsPerSecond, l.CallsBurst))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only a POST carries messages. GET opens the SSE stream and DELETE ends a session.
		if r.Method != http.MethodPost || r.Body == nil || r.Body == http.NoBody {
			next.ServeHTTP(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "the request body was not received", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		n := 1
		if trimmed := bytes.TrimLeft(body, " \t\r\n"); len(trimmed) > 0 && trimmed[0] == '[' {
			var batch []json.RawMessage
			// A body that does not parse is left for the SDK to refuse as it always has;
			// it is charged as one message.
			if json.Unmarshal(trimmed, &batch) == nil {
				n = len(batch)
			}
		}
		if n > l.MaxBatchEntries {
			writeRPCError(w, http.StatusBadRequest, rpcInvalidRequest,
				fmt.Sprintf("a JSON-RPC batch may carry at most %d messages; this one carries %d. Send them as separate requests.",
					l.MaxBatchEntries, n))
			return
		}
		if !limiter.AllowN(callerKey(r), n) {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(l, n)))
			writeRPCError(w, http.StatusTooManyRequests, rpcRateLimited,
				"rate limit exceeded for this caller; retry after the interval in Retry-After")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// callerKey is the verified token's subject. The bearer check has run by the time this
// is reached; the empty fallback only keeps a misordered chain on one shared bucket
// rather than unbounded.
func callerKey(r *http.Request) string {
	if info := sdkauth.TokenInfoFromContext(r.Context()); info != nil && info.UserID != "" {
		return info.UserID
	}
	return ""
}

// retryAfterSeconds is how long until n tokens have refilled, rounded up, at least 1.
func retryAfterSeconds(l Limits, n int) int {
	s := int(float64(n)/l.CallsPerSecond) + 1
	if s < 1 {
		s = 1
	}
	return s
}

// writeRPCError answers with a JSON-RPC error whose id is null, as the specification
// requires when the id of the request cannot be determined.
func writeRPCError(w http.ResponseWriter, status, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      nil,
		"error":   map[string]any{"code": code, "message": msg},
	})
}
