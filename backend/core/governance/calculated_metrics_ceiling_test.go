// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestResolveCalculatedMetricsCeiling(t *testing.T) {
	i32 := func(v int32) *int32 { return &v }
	cases := []struct {
		name string
		in   *int32
		want int
	}{
		{"null inherits the platform default, which is off", nil, 0},
		{"an explicit zero is off", i32(0), 0},
		{"a negative value inherits the default", i32(-3), 0},
		{"a tier value is served", i32(8), 8},
		{"the maximum is served", i32(32), 32},
		{"above the maximum is clamped to it", i32(33), 32},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveCalculatedMetricsCeiling(c.in); got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
	if DefaultCalculatedMetricsPerProfile != 0 {
		t.Errorf("the platform default must stay 0 (calculated metrics off) until the cost gate passes, got %d",
			DefaultCalculatedMetricsPerProfile)
	}
}

// TestCalculatedMetricsCeilingQueryAndResponseAgree builds the wire body from the field
// constant and decodes it through the response type, so a json tag that drifted from the
// constant decodes to nil here (and would have served every tenant the default).
func TestCalculatedMetricsCeilingQueryAndResponseAgree(t *testing.T) {
	if !strings.Contains(calculatedMetricsCeilingQuery, "tenantGovernance { "+CalculatedMetricsPerProfileField+" }") {
		t.Fatalf("the query does not select %s off tenantGovernance: %q", CalculatedMetricsPerProfileField, calculatedMetricsCeilingQuery)
	}
	var out calculatedMetricsCeilingResponse
	body := `{"tenantGovernance":{"` + CalculatedMetricsPerProfileField + `":12}}`
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.TenantGovernance.CalculatedMetricsPerProfile == nil || *out.TenantGovernance.CalculatedMetricsPerProfile != 12 {
		t.Fatalf("decoded %v from %s; the json tag does not match the field", out.TenantGovernance.CalculatedMetricsPerProfile, body)
	}
}

func TestCalculatedMetricsCeilingResolverCarriesTheError(t *testing.T) {
	boom := errors.New("user-management unreachable")
	r := &CalculatedMetricsCeilingResolver{fetch: func(context.Context, string) (int, error) { return 0, boom }}
	if _, err := r.Resolve(context.Background(), "acme"); !errors.Is(err, boom) {
		t.Fatalf("a failed fetch must reach the caller, got %v", err)
	}
	if _, err := r.Resolve(context.Background(), ""); err == nil {
		t.Fatal("an empty tenant must be refused")
	}
}
