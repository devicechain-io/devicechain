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

		n, id, ok := countMessages(body)
		if !ok {
			writeRPCError(w, http.StatusBadRequest, rpcInvalidRequest, nil,
				"the body opens a JSON-RPC batch that could not be parsed")
			return
		}
		if n > l.MaxBatchEntries {
			writeRPCError(w, http.StatusBadRequest, rpcInvalidRequest, nil,
				fmt.Sprintf("a JSON-RPC batch may carry at most %d messages; this one carries %d. Send them as separate requests.",
					l.MaxBatchEntries, n))
			return
		}
		if !limiter.AllowN(callerKey(r), n) {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(l, n)))
			writeRPCError(w, http.StatusTooManyRequests, rpcRateLimited, id,
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

// countMessages is how many JSON-RPC messages body carries, as the SDK will see them.
//
// A body whose first non-whitespace byte is '[' is a batch. It is read the way the SDK
// reads it: with a streaming decoder, which takes the FIRST JSON value and ignores what
// follows. A strict whole-body parse would reject `[...batch...] x`, and charging that
// as one message would let one trailing byte carry a batch of any size past both the cap
// and the rate limit, because the SDK still runs every entry. A body that opens a batch
// but cannot be decoded is reported !ok and refused rather than charged as one: where
// this parser and the SDK's disagree, the caller must not get the cheaper count.
//
// Anything else is one message, and id is its "id" member (matched exactly, as the SDK
// matches it), so a refusal can answer the request it refuses. A notification, or a
// body that is not an object, has no id and is answered with null.
func countMessages(body []byte) (n int, id json.RawMessage, ok bool) {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var batch []json.RawMessage
		if err := json.NewDecoder(bytes.NewReader(trimmed)).Decode(&batch); err != nil {
			return 0, nil, false
		}
		return len(batch), nil, true
	}
	var msg map[string]json.RawMessage
	if json.Unmarshal(body, &msg) == nil {
		id = msg["id"]
	}
	return 1, id, true
}

// writeRPCError answers with a JSON-RPC error. id is the refused request's id; it is
// null (nil) for a batch, or when the id cannot be determined, as the specification
// requires.
func writeRPCError(w http.ResponseWriter, status, code int, id json.RawMessage, msg string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": msg},
	})
}
