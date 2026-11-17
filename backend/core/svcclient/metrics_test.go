// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package svcclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

// Snapshot the real registry after package initialization, before any test calls.
var initialMetrics []*dto.MetricFamily

func init() {
	initialMetrics, _ = prometheus.DefaultGatherer.Gather()
}

func TestMetricsStartAtZero(t *testing.T) {
	want := map[string]int{}
	want["devicechain_svcclient_queries_total"] = 2
	want["devicechain_svcclient_query_duration_seconds"] = 1
	want["devicechain_svcclient_service_token_mint_duration_seconds"] = 1
	for _, family := range initialMetrics {
		if count, ok := want[family.GetName()]; ok {
			if len(family.Metric) != count {
				t.Fatalf("%s: got %d series, want %d", family.GetName(), len(family.Metric), count)
			}
			for _, metric := range family.Metric {
				if metric.GetCounter().GetValue() != 0 || metric.GetHistogram().GetSampleCount() != 0 {
					t.Fatalf("%s did not start at zero", family.GetName())
				}
			}
			delete(want, family.GetName())
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing zero series: %v", want)
	}
}

func histogramCount(t *testing.T, h prometheus.Histogram) uint64 {
	t.Helper()
	var m dto.Metric
	if err := h.Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetHistogram().GetSampleCount()
}

func TestQueryMetricsAndMintCache(t *testing.T) {
	var mints int32
	mint := mintServer(t, "secret", &mints)
	defer mint.Close()
	c := clientFor(mint, "secret")
	status, response := http.StatusOK, `{"data":{"ok":true}}`
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, response)
	}))
	defer peer.Close()
	success := testutil.ToFloat64(queryResults.WithLabelValues("ok"))
	failure := testutil.ToFloat64(queryResults.WithLabelValues("error"))
	queries, mintCount := histogramCount(t, queryDuration), histogramCount(t, mintDuration)
	call := func(wantError bool) {
		t.Helper()
		var out map[string]any
		err := c.Query(context.Background(), peer.URL, "tenant", "query{ok}", nil, &out)
		if (err != nil) != wantError {
			t.Fatalf("Query error = %v, want error %v", err, wantError)
		}
	}
	call(false)
	call(false) // cached token must not record another mint
	status = http.StatusServiceUnavailable
	call(true)
	status, response = http.StatusOK, `{"errors":[{"message":"denied"}]}`
	call(true)
	response = `invalid JSON`
	call(true)
	if testutil.ToFloat64(queryResults.WithLabelValues("ok"))-success != 2 ||
		testutil.ToFloat64(queryResults.WithLabelValues("error"))-failure != 3 ||
		histogramCount(t, queryDuration)-queries != 5 || histogramCount(t, mintDuration)-mintCount != 1 {
		t.Fatal("query results, durations, or cached mint accounting mismatch")
	}

	// Local validation and mint failures are errors too, with one duration each.
	c.secret, c.token, c.expiresAt = "wrong", "", time.Time{}
	call(true)
	if err := c.QueryOnBehalfOfScopedCaller(context.Background(), peer.URL, "", "", nil, nil); err == nil {
		t.Fatal("missing tenant accepted")
	}
	if testutil.ToFloat64(queryResults.WithLabelValues("error"))-failure != 5 ||
		histogramCount(t, queryDuration)-queries != 7 || histogramCount(t, mintDuration)-mintCount != 2 {
		t.Fatal("local or mint failures missing from metrics")
	}
}
