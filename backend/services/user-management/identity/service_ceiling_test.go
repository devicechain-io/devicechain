// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetCounter().GetValue()
}

func seriesCount(t *testing.T, reg *prometheus.Registry) int {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == "devicechain_usermanagement_service_token_checks_total" {
			return len(mf.GetMetric())
		}
	}
	t.Fatal("the counter is not registered")
	return 0
}

// The ceiling is OBSERVED, not enforced: every verdict is counted and the token is minted anyway.
func TestServiceTokenHandlerObservesCeilingAndStillMints(t *testing.T) {
	key, _ := auth.GenerateKeyPair()
	iss := auth.NewIssuer(key, "test", time.Minute, time.Hour)
	issue := func(subject string, a []string) (auth.IssuedToken, error) { return iss.IssueService(subject, a, "j") }
	reg := prometheus.NewRegistry()
	obs := NewServiceCeilingObserver(reg, "usermanagement")
	h := ServiceTokenHandler(func() string { return "s3cret" }, issue, obs)

	mint := func(secret, subject string, authorities ...string) int {
		b, _ := json.Marshal(auth.ServiceTokenRequest{Subject: subject, Authorities: authorities})
		req := httptest.NewRequest(http.MethodPost, auth.ServiceTokenPath, bytes.NewReader(b))
		req.Header.Set(auth.ServiceSecretHeader, secret)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	cases := []struct {
		subject string
		auths   []string
		caller  string
		verdict auth.ServiceCeilingVerdict
	}{
		{"event-management", []string{"device:read"}, "event-management", auth.CeilingWithin},
		{"event-management", []string{"device:write"}, "event-management", auth.CeilingOver},
		{"event-management", []string{"*"}, "event-management", auth.CeilingWildcard},
		{"mallory", []string{"device:read"}, auth.UnknownCallerLabel, auth.CeilingUnknownCaller},
	}
	for _, tc := range cases {
		before := counterValue(t, obs.Counter(tc.caller, tc.verdict))
		if code := mint("s3cret", tc.subject, tc.auths...); code != http.StatusOK {
			t.Errorf("%s %v: observe mode must mint, got %d", tc.subject, tc.auths, code)
		}
		if got := counterValue(t, obs.Counter(tc.caller, tc.verdict)); got != before+1 {
			t.Errorf("%s %v: %s{%s} went %v -> %v", tc.subject, tc.auths, tc.verdict, tc.caller, before, got)
		}
	}

	// An unauthenticated sender, a bad body and an unknown authority never reach the observer.
	total := func() (n float64) {
		for _, c := range append(auth.ServiceCallerNames(), auth.UnknownCallerLabel) {
			for _, v := range auth.ServiceCeilingVerdicts {
				n += counterValue(t, obs.Counter(c, v))
			}
		}
		return
	}
	before := total()
	if mint("wrong", "event-management", "device:read") != http.StatusUnauthorized {
		t.Fatal("wrong secret must still be refused")
	}
	if mint("s3cret", "event-management", "device:raed") != http.StatusBadRequest {
		t.Fatal("an unknown authority must still be refused")
	}
	if mint("s3cret", "", "device:read") != http.StatusBadRequest {
		t.Fatal("an empty subject must still be refused")
	}
	if total() != before {
		t.Fatal("a refused request moved the ceiling counter")
	}
}

// Every series exists at zero, and a flood of distinct hostile subjects makes ONE series.
func TestServiceCeilingObserverCardinality(t *testing.T) {
	reg := prometheus.NewRegistry()
	obs := NewServiceCeilingObserver(reg, "usermanagement")
	want := (len(auth.ServiceCallerNames()) + 1) * len(auth.ServiceCeilingVerdicts)
	if got := seriesCount(t, reg); got != want {
		t.Fatalf("pre-created series = %d; want %d", got, want)
	}
	for i := 0; i < 1000; i++ {
		obs.Observe(fmt.Sprintf("hostile-%d", i), []string{fmt.Sprintf("x:%d", i)})
	}
	if got := seriesCount(t, reg); got != want {
		t.Fatalf("1000 hostile subjects grew the series from %d to %d", want, got)
	}
	if got := counterValue(t, obs.Counter(auth.UnknownCallerLabel, auth.CeilingUnknownCaller)); got != 1000 {
		t.Fatalf("other/unknown_caller = %v; want 1000", got)
	}
}

func TestServiceCeilingObserverNilStillJudges(t *testing.T) {
	var o *ServiceCeilingObserver
	if r := o.Observe("mallory", []string{"*"}); r.Verdict != auth.CeilingWildcard {
		t.Fatalf("a nil observer must still judge: %+v", r)
	}
}

func TestServiceCeilingLogIsRateLimited(t *testing.T) {
	obs := NewServiceCeilingObserver(nil, "usermanagement")
	now := time.Unix(1_000_000, 0)
	obs.now = func() time.Time { return now }
	allowed := 0
	for i := 0; i < 100; i++ {
		if obs.allowLog() {
			allowed++
		}
	}
	if allowed != ceilingLogBurst {
		t.Fatalf("allowed %d lines in one window; want %d", allowed, ceilingLogBurst)
	}
	now = now.Add(ceilingLogWindow)
	if !obs.allowLog() {
		t.Fatal("the limiter did not reopen after the window")
	}
}

func TestClipBoundsSenderText(t *testing.T) {
	if got := clip(strings.Repeat("a", 500)); len(got) != ceilingLogMaxLen+3+2 {
		t.Fatalf("clip returned %d bytes", len(got))
	}
	if got := clip("a\xffb\nc"); got != "\"a?b\\nc\"" {
		t.Fatalf("clip = %s", got)
	}
}

// What the warning carries of sender-chosen text is bounded, and the line names the verdict.
func TestServiceCeilingWarningIsBounded(t *testing.T) {
	logs := logSink.Capture(t)

	obs := NewServiceCeilingObserver(nil, "usermanagement")
	many := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		many = append(many, fmt.Sprintf("x:%d", i))
	}
	obs.Observe(strings.Repeat("s", 500), many)
	line := logs.String()
	if !strings.Contains(line, "unknown_caller") {
		t.Fatalf("the warning does not name the verdict: %s", line)
	}
	if strings.Contains(line, strings.Repeat("s", ceilingLogMaxLen+1)) {
		t.Fatalf("the subject was not clipped: %s", line)
	}
	if strings.Contains(line, "x:8") || !strings.Contains(line, "x:7") {
		t.Fatalf("offending authorities are not capped at %d: %s", ceilingLogMaxAuthorities, line)
	}

	before := len(logs.String())
	obs.Observe("event-management", []string{"device:read"})
	if after := logs.String(); len(after) != before {
		t.Fatalf("a within-ceiling request logged: %s", after[before:])
	}
}

// A hostile loop through Observe produces a bounded number of log lines, not one per request.
func TestServiceCeilingObserveLimitsItsOwnLogging(t *testing.T) {
	logs := logSink.Capture(t)

	obs := NewServiceCeilingObserver(nil, "usermanagement")
	for i := 0; i < 200; i++ {
		obs.Observe("mallory", []string{"*"})
	}
	if lines := strings.Count(logs.String(), "\n"); lines != ceilingLogBurst {
		t.Fatalf("200 refused-in-future requests wrote %d log lines; want %d", lines, ceilingLogBurst)
	}
}
