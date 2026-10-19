// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
)

// limitedServer serves the mux Routes builds under the given limits, against an upstream
// that counts the GraphQL reads it receives, and returns tokens for two different users.
func limitedServer(t *testing.T, l Limits) (ts *httptest.Server, tokenFor func(email string) string, upstreamHits *atomic.Int64) {
	t.Helper()
	upstreamHits = &atomic.Int64{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	t.Cleanup(up.Close)
	iss, validator := mustIssuerValidator(t)
	mux := http.NewServeMux()
	Routes(mux, testResource, "https://as.example.com", validator, testClient(up.URL), l)
	ts = httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	tokenFor = func(email string) string {
		tok, err := iss.IssueOAuthAccess("acme", email, []string{"viewer"}, []string{"device:read"},
			coreauth.ScopeReadOnly, []string{testResource}, false, "mcp", "j-"+email)
		if err != nil {
			t.Fatalf("issuing a token: %v", err)
		}
		return tok.Token
	}
	return ts, tokenFor, upstreamHits
}

func post(t *testing.T, ts *httptest.Server, token, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

// batchOf builds a JSON-RPC batch of n tools/call entries.
func batchOf(n int) string {
	entries := make([]string, n)
	for i := range entries {
		entries[i] = `{"jsonrpc":"2.0","id":` + string(rune('0'+i%10)) + `,"method":"tools/call","params":{"name":"list_devices","arguments":{}}}`
	}
	return "[" + strings.Join(entries, ",") + "]"
}

func rpcError(t *testing.T, raw string) (code int, msg string) {
	t.Helper()
	var out struct {
		ID    json.RawMessage `json:"id"`
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("response is not a JSON-RPC error: %q (%v)", raw, err)
	}
	if string(out.ID) != "null" {
		t.Errorf("error id = %s, want null", out.ID)
	}
	return out.Error.Code, out.Error.Message
}

// A batch over the cap is refused WHOLE with a JSON-RPC error: no entry runs, and the
// refusal names the cap rather than silently dropping the tail.
func TestOverCapBatchIsRefusedWhole(t *testing.T) {
	ts, tokenFor, hits := limitedServer(t, Limits{MaxBatchEntries: 4, CallsPerSecond: 1000, CallsBurst: 1000})
	resp, raw := post(t, ts, tokenFor("a@b.c"), batchOf(5))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d (%s), want 400", resp.StatusCode, raw)
	}
	code, msg := rpcError(t, raw)
	if code != rpcInvalidRequest || !strings.Contains(msg, "at most 4") || !strings.Contains(msg, "carries 5") {
		t.Errorf("error = %d %q, want invalid-request naming the cap and the size", code, msg)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("%d upstream reads ran for a refused batch, want 0", n)
	}
}

// The cap is on entries, so a batch AT the cap is not refused by it.
func TestBatchAtCapIsNotRefusedByTheCap(t *testing.T) {
	ts, tokenFor, _ := limitedServer(t, Limits{MaxBatchEntries: 4, CallsPerSecond: 1000, CallsBurst: 1000})
	resp, raw := post(t, ts, tokenFor("a@b.c"), batchOf(4))
	if resp.StatusCode == http.StatusBadRequest && strings.Contains(raw, "at most") {
		t.Errorf("a batch of exactly the cap was refused by the cap: %s", raw)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		t.Errorf("a batch of exactly the cap was rate limited: %s", raw)
	}
}

// The rate limit is per caller and counts messages: a batch spends one token per entry,
// the next request is refused 429 with Retry-After, and another user is unaffected.
func TestRateLimitIsPerCallerAndCountsBatchEntries(t *testing.T) {
	ts, tokenFor, _ := limitedServer(t, Limits{MaxBatchEntries: 4, CallsPerSecond: 0.001, CallsBurst: 4})
	alice, bob := tokenFor("alice@b.c"), tokenFor("bob@b.c")

	if resp, raw := post(t, ts, alice, batchOf(4)); resp.StatusCode == http.StatusTooManyRequests {
		t.Fatalf("the first batch, within burst, was limited: %s", raw)
	}
	resp, raw := post(t, ts, alice, batchOf(1))
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d (%s), want 429 once the burst is spent by a batch", resp.StatusCode, raw)
	}
	// At 0.001/s one token takes 1,000 s to refill; a Retry-After of 0 (or absent) would
	// send the client straight back into the same refusal.
	if ra, err := strconv.Atoi(resp.Header.Get("Retry-After")); err != nil || ra < 1000 {
		t.Errorf("Retry-After = %q, want the refill interval (>= 1000 s)", resp.Header.Get("Retry-After"))
	}
	if code, _ := rpcError(t, raw); code != rpcRateLimited {
		t.Errorf("code = %d, want %d", code, rpcRateLimited)
	}
	if resp, raw := post(t, ts, bob, batchOf(1)); resp.StatusCode == http.StatusTooManyRequests {
		t.Errorf("another caller was limited by alice's flood: %s", raw)
	}
}

// A caller with no valid token is still answered by the bearer check, before any limit.
func TestUnauthenticatedCallerGetsTheChallengeNotALimit(t *testing.T) {
	ts, _, _ := limitedServer(t, Limits{MaxBatchEntries: 1, CallsPerSecond: 1, CallsBurst: 1})
	resp, _ := post(t, ts, "", batchOf(5))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// The SDK reads a batch with a streaming decoder, which takes the FIRST JSON value in the
// body and ignores whatever follows it. A strict whole-body parse rejects that body, and
// if the limiter then charged it as one message, appending one byte after a batch of
// any size would carry the whole batch past both the cap and the rate limit.
func TestTrailingDataDoesNotHideABatch(t *testing.T) {
	ts, tokenFor, hits := limitedServer(t, Limits{MaxBatchEntries: 4, CallsPerSecond: 1000, CallsBurst: 1000})
	for _, suffix := range []string{" x", "[]", "{}", " ]"} {
		resp, raw := post(t, ts, tokenFor("a@b.c"), batchOf(5)+suffix)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("suffix %q: status = %d (%s), want 400", suffix, resp.StatusCode, raw)
			continue
		}
		if code, _ := rpcError(t, raw); code != rpcInvalidRequest {
			t.Errorf("suffix %q: code = %d, want %d", suffix, code, rpcInvalidRequest)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("%d upstream reads ran for batches over the cap, want 0", n)
	}
}

// JSON allows whitespace before the value, and the SDK accepts a batch behind it, so the
// limiter must too or leading whitespace would be a free pass.
func TestLeadingWhitespaceDoesNotHideABatch(t *testing.T) {
	ts, tokenFor, hits := limitedServer(t, Limits{MaxBatchEntries: 4, CallsPerSecond: 1000, CallsBurst: 1000})
	for _, prefix := range []string{" ", "\t", "\r\n", " \n\t "} {
		resp, raw := post(t, ts, tokenFor("a@b.c"), prefix+batchOf(5))
		if resp.StatusCode != http.StatusBadRequest || !strings.Contains(raw, "at most 4") {
			t.Errorf("prefix %q: status = %d (%s), want the cap's 400", prefix, resp.StatusCode, raw)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("%d upstream reads ran for batches over the cap, want 0", n)
	}
}

// A body that opens like a batch but cannot be counted is refused here rather than
// handed on as one message: if this parser and the SDK's ever disagree about where a
// batch ends, the disagreement must not be the caller's discount.
func TestUncountableBatchIsRefused(t *testing.T) {
	ts, tokenFor, hits := limitedServer(t, Limits{MaxBatchEntries: 4, CallsPerSecond: 1000, CallsBurst: 1000})
	resp, raw := post(t, ts, tokenFor("a@b.c"), `[{"jsonrpc":"2.0","id":1,"method":"tools/call"`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d (%s), want 400", resp.StatusCode, raw)
	}
	if code, _ := rpcError(t, raw); code != rpcInvalidRequest {
		t.Errorf("code = %d, want %d", code, rpcInvalidRequest)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("%d upstream reads ran, want 0", n)
	}
}

// A refused SINGLE request answers with that request's own id, so a client correlating
// by id can tell which call was refused; null is only for a batch, which has no one id.
func TestRateLimitedSingleRequestEchoesItsID(t *testing.T) {
	ts, tokenFor, _ := limitedServer(t, Limits{MaxBatchEntries: 1, CallsPerSecond: 0.001, CallsBurst: 1})
	tok := tokenFor("a@b.c")
	single := `{"jsonrpc":"2.0","id":"call-7","method":"tools/call","params":{"name":"list_devices","arguments":{}}}`
	if resp, raw := post(t, ts, tok, single); resp.StatusCode == http.StatusTooManyRequests {
		t.Fatalf("the first request was limited: %s", raw)
	}
	resp, raw := post(t, ts, tok, single)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d (%s), want 429", resp.StatusCode, raw)
	}
	var out struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("not JSON: %q", raw)
	}
	if string(out.ID) != `"call-7"` {
		t.Errorf("id = %s, want \"call-7\"", out.ID)
	}
}
