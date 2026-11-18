// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package svcclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Gather without WithLabelValues: creating a child here would hide missing seeds.
func mintCounts(t *testing.T, gatherer prometheus.Gatherer) map[string]float64 {
	t.Helper()
	families, err := gatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]float64{}
	for _, family := range families {
		if family.GetName() != "devicechain_svcclient_service_token_mints_total" {
			continue
		}
		for _, metric := range family.Metric {
			if len(metric.Label) != 1 || metric.Label[0].GetName() != "result" {
				t.Fatalf("unexpected mint labels: %v", metric.Label)
			}
			counts[metric.Label[0].GetValue()] = metric.GetCounter().GetValue()
		}
	}
	if len(counts) != 2 {
		t.Fatalf("want both fixed result series before any mint, got %v", counts)
	}
	for _, result := range []string{"ok", "error"} {
		if _, ok := counts[result]; !ok {
			t.Fatalf("missing seeded %s result", result)
		}
	}
	return counts
}

func TestServiceTokenMintMetricsSeededAtZero(t *testing.T) {
	reg := prometheus.NewRegistry()
	newMintCounter(reg)
	for result, value := range mintCounts(t, reg) {
		if value != 0 {
			t.Errorf("initial %s = %v, want zero", result, value)
		}
	}
}

// Done is consulted only after this caller has joined the shared mint. This lets
// the test hold the response until every caller is waiting, without sleeps.
type mintWaitingContext struct {
	context.Context
	waiting chan<- struct{}
}

func (ctx mintWaitingContext) Done() <-chan struct{} {
	ctx.waiting <- struct{}{}
	return ctx.Context.Done()
}

func TestServiceTokenMintMetricsCountSharedOperationOnce(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
			_, _ = w.Write([]byte(`{"token":"shared","expiresAt":1099511627776}`))
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := clientFor(srv, "secret")
	before := mintCounts(t, prometheus.DefaultGatherer)
	const callers = 8
	waiting := make(chan struct{}, callers)
	results := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() {
			_, err := c.serviceToken(mintWaitingContext{Context: ctx, waiting: waiting})
			results <- err
		}()
	}
	for i := 0; i < callers; i++ {
		select {
		case <-waiting:
		case <-ctx.Done():
			t.Fatal("callers did not join the shared mint")
		}
	}
	// One send releases the one HTTP handler; deferred close handles failure paths.
	release <- struct{}{}
	for i := 0; i < callers; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	after := mintCounts(t, prometheus.DefaultGatherer)
	if after["ok"]-before["ok"] != 1 || after["error"] != before["error"] {
		t.Fatalf("shared mint counts: before %v, after %v", before, after)
	}
}

func TestServiceTokenMintMetrics(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		secret string
		badURL bool
		result string
	}{
		{"success_cached", 200, `{"token":"secret-token","expiresAt":1099511627776}`, "secret", false, "ok"},
		{"http_error", 503, `unavailable`, "secret", false, "error"},
		{"decode_error", 200, `{`, "secret", false, "error"},
		{"empty_token", 200, `{}`, "secret", false, "error"},
		{"oversized", 200, strings.Repeat("x", MaxResponseBytes+1), "secret", false, "error"},
		{"transport_error", 200, `{}`, "secret", true, "error"},
		{"not_configured", 200, `{}`, "", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := clientFor(srv, tc.secret)
			if tc.badURL {
				c.mintURL = "unsupported://mint"
			}
			before := mintCounts(t, prometheus.DefaultGatherer)
			_, err := c.serviceToken(context.Background())
			if (err == nil) != (tc.result == "ok") {
				t.Fatalf("unexpected mint outcome: %v", err)
			}
			if tc.result == "ok" {
				// A cache hit must not count as another mint.
				if _, err := c.serviceToken(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			after := mintCounts(t, prometheus.DefaultGatherer)
			for _, result := range []string{"ok", "error"} {
				want := float64(0)
				if result == tc.result {
					want = 1
				}
				if delta := after[result] - before[result]; delta != want {
					t.Errorf("%s mint delta = %v, want %v", result, delta, want)
				}
			}
		})
	}
}
