// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package userclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

var initialMetrics []*dto.MetricFamily

func init() {
	initialMetrics, _ = prometheus.DefaultGatherer.Gather()
}

func TestAuthMetricsStartAtZero(t *testing.T) {
	want := map[string]int{"devicechain_userclient_logins_total": 3, "devicechain_userclient_refreshes_total": 2}
	for _, family := range initialMetrics {
		if count, ok := want[family.GetName()]; ok {
			if len(family.Metric) != count {
				t.Fatalf("%s: got %d series, want %d", family.GetName(), len(family.Metric), count)
			}
			for _, metric := range family.Metric {
				if metric.GetCounter().GetValue() != 0 || len(metric.Label) != 1 || metric.Label[0].GetName() != "result" {
					t.Fatalf("%s: expected zero series with only result label", family.GetName())
				}
			}
			delete(want, family.GetName())
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing zero series: %v", want)
	}
}

func TestAuthOutcomeMetrics(t *testing.T) {
	for _, operation := range []string{"login", "refresh"} {
		for _, tc := range []struct {
			name, body, outcome string
		}{
			{"ok", `{"data":{"login":{"identityToken":"id"},"refresh":{"accessToken":"access"}}}`, "ok"},
			{"empty", `{"data":{}}`, "error"},
			{"decode", `broken`, "error"},
			{"refusal", `{"errors":[{"message":"denied"}]}`, "error"},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) }))
				defer srv.Close()
				counter := loginResults
				if operation == "refresh" {
					counter = refreshResults
				}
				before := testutil.ToFloat64(counter.WithLabelValues(tc.outcome))
				var err error
				if operation == "login" {
					_, err = Login(context.Background(), nil, srv.URL, "email", "password")
				} else {
					_, err = Refresh(context.Background(), nil, srv.URL, "refresh-secret")
				}
				if (err == nil) != (tc.outcome == "ok") {
					t.Fatalf("unexpected result: %v", err)
				}
				if testutil.ToFloat64(counter.WithLabelValues(tc.outcome))-before != 1 {
					t.Fatal("exchange outcome was not counted exactly once")
				}
			})
		}
	}
	srv := httptest.NewServer(http.HandlerFunc((&mfaStub{}).handler))
	defer srv.Close()
	challenge := testutil.ToFloat64(loginResults.WithLabelValues("mfa_challenge"))
	failure := testutil.ToFloat64(loginResults.WithLabelValues("error"))
	success := testutil.ToFloat64(loginResults.WithLabelValues("ok"))
	_, err := LoginWithOTP(context.Background(), nil, srv.URL, "email", "password", func(context.Context, *MfaRequiredError) (string, error) { return "123456", nil })
	if err != nil {
		t.Fatal(err)
	}
	if testutil.ToFloat64(loginResults.WithLabelValues("mfa_challenge"))-challenge != 1 ||
		testutil.ToFloat64(loginResults.WithLabelValues("error")) != failure ||
		testutil.ToFloat64(loginResults.WithLabelValues("ok")) != success {
		t.Fatal("MFA challenge must count once, separately from completed logins and errors")
	}
}
