// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coreauth "github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Probe the real shared budget while a served tool is still executing. Reserving
// all 64 MiB needs only a two-byte body; no large test allocation is necessary.
func TestServedToolReleasesBodyReadBudgetBeforeExecution(t *testing.T) {
	probeStatus := make(chan int, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		rr := httptest.NewRecorder()
		core.BufferBody(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}), core.BodyBufferBudget-1).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}")))
		probeStatus <- rr.Code
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	t.Cleanup(up.Close)
	cs, _ := servedToolSession(t, up.URL)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_devices", Arguments: map[string]any{}})
	mustSucceed(t, res, err)
	if got := <-probeStatus; got != http.StatusNoContent {
		t.Fatalf("body-read budget still reserved during tool execution: probe status %d", got)
	}
}

// Use literal limits in fixtures so increasing a safety bound cannot silently
// enlarge the test's expectations along with it.
func TestServedAggregateMemoryBounds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rows    int
		metric  string
		refused bool
	}{
		{"at row cap", 1000, "m", false},
		{"compact rows over cap", 1001, "m", true},
		{"bytes over cap", 1, strings.Repeat("m", 256<<10), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := fmt.Sprintf(`{"bucketStart":"2026-01-01T00:00:00Z","name":%q,"count":1}`, tc.metric)
			body := `{"data":{"bucketedMeasurements":[` + strings.TrimSuffix(strings.Repeat(row+",", tc.rows), ",") + `]}}`
			_, url := newRecordingUpstream(t, body)
			cs, _ := servedToolSession(t, url)
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
				Name:      "aggregate_measurements",
				Arguments: map[string]any{"deviceToken": "d", "startTime": "2026-01-01T00:00:00Z", "intervalSeconds": 60},
			})
			if tc.refused {
				if err != nil || !res.IsError || !strings.Contains(contentText(res), "narrow the request") {
					t.Fatalf("want whole-result narrow-request refusal, got result=%v err=%v", res, err)
				}
				if res.StructuredContent != nil {
					t.Fatal("refused aggregate returned partial structured data")
				}
			} else {
				mustSucceed(t, res, err)
				var out AggregateMeasurementsOutput
				decodeStructured(t, res, &out)
				if len(out.Buckets) != tc.rows {
					t.Fatalf("got %d rows, want %d", len(out.Buckets), tc.rows)
				}
			}
		})
	}
}

type heldAdmissionBody struct {
	entered chan<- struct{}
	release <-chan struct{}
}

func (b *heldAdmissionBody) Read([]byte) (int, error) {
	b.entered <- struct{}{}
	<-b.release
	return 0, io.EOF
}

func TestBodyReadsHavePerCallerAdmission(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		t.Run(method, func(t *testing.T) {
			entered := make(chan struct{}, 4)
			release := make(chan struct{})
			done := make(chan int, 4)
			started := 0
			t.Cleanup(func() {
				close(release)
				for i := 0; i < started; i++ {
					select {
					case status := <-done:
						if status != http.StatusNoContent {
							t.Errorf("admitted request status %d", status)
						}
					case <-time.After(3 * time.Second):
						t.Error("body reader did not finish")
					}
				}
			})
			h := bufferRequestBody(next)
			for i := 0; i < 4; i++ {
				started++
				go func() {
					rr := httptest.NewRecorder()
					h.ServeHTTP(rr, httptest.NewRequest(method, "/", &heldAdmissionBody{entered: entered, release: release}))
					done <- rr.Code
				}()
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("in-budget body read was not admitted")
				}
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(method, "/", strings.NewReader("{}")))
			if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") != "1" {
				t.Fatalf("body read bypassed caller admission: status %d, Retry-After %q", rr.Code, rr.Header().Get("Retry-After"))
			}
		})
	}
}

// Keep a real HTTP response open, so admission must cover serialization as well
// as downstream work. Two separately built handlers share the process budget.
func TestInFlightMessagesBoundCallersBatchesAndProcess(t *testing.T) {
	iss, validator := mustIssuerValidator(t)
	entered := make(chan struct{}, 16)
	release := make(chan struct{})
	done := make(chan int, 16)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "quick" {
			entered <- struct{}{}
			<-release
		}
		w.WriteHeader(http.StatusNoContent)
	})
	makeServer := func() *httptest.Server {
		h := limitMessages(next, Limits{MaxBatchEntries: 16, CallsPerSecond: 1000, CallsBurst: 1000})
		h = sdkauth.RequireBearerToken(NewTokenVerifier(validator, testResource),
			&sdkauth.RequireBearerTokenOptions{Scopes: []string{coreauth.ScopeReadOnly}})(bufferRequestBody(h))
		ts := httptest.NewServer(h)
		t.Cleanup(ts.Close)
		return ts
	}
	one, two := makeServer(), makeServer()
	tokenFor := func(email string) string {
		tok, err := iss.IssueOAuthAccess("acme", email, nil, []string{"device:read"},
			coreauth.ScopeReadOnly, []string{testResource}, false, "mcp", "j-"+email)
		if err != nil {
			t.Fatal(err)
		}
		return tok.Token
	}
	alice, bob, charlie := tokenFor("alice@b.c"), tokenFor("bob@b.c"), tokenFor("charlie@b.c")
	request := func(url, token, body string) int {
		req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
		if err != nil {
			return -1
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode == http.StatusTooManyRequests && resp.Header.Get("Retry-After") == "" {
			return -2
		}
		return resp.StatusCode
	}
	started := 0
	closed := false
	t.Cleanup(func() {
		if !closed {
			close(release)
		}
		for i := 0; i < started; i++ {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("admitted request did not finish")
			}
		}
	})
	start := func(ts *httptest.Server, token string) {
		started++
		go func() { done <- request(ts.URL, token, batchOf(4)) }()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("in-budget batch was not admitted")
		}
	}
	start(one, alice)
	if got := request(two.URL+"?quick", alice, batchOf(1)); got != http.StatusTooManyRequests {
		t.Fatalf("caller exceeded four slots across handlers: status %d", got)
	}
	if got := request(two.URL+"?quick", bob, batchOf(1)); got != http.StatusNoContent {
		t.Fatalf("alice consumed bob's allowance: status %d", got)
	}
	start(two, bob)
	if got := request(one.URL+"?quick", charlie, batchOf(1)); got != http.StatusTooManyRequests {
		t.Fatalf("process exceeded eight slots across callers: status %d", got)
	}
	close(release)
	closed = true
	for i := 0; i < started; i++ {
		if got := <-done; got != http.StatusNoContent {
			t.Errorf("admitted batch status %d", got)
		}
	}
	started = 0
	if got := request(one.URL+"?quick", alice, batchOf(4)); got != http.StatusNoContent {
		t.Fatalf("completed calls leaked slots: status %d", got)
	}
}
