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

func TestResolveIntegrationsCeiling(t *testing.T) {
	i32 := func(v int32) *int32 { return &v }
	cases := []struct {
		name string
		in   *int32
		want int
	}{
		{"null inherits the platform default", nil, DefaultIntegrationsPerTenant},
		{"a negative value inherits the default", i32(-3), DefaultIntegrationsPerTenant},
		{"an explicit zero is a tier that holds none", i32(0), 0},
		{"a tier value is served", i32(12), 12},
		{"the maximum is served", i32(MaxIntegrationsPerTenant), MaxIntegrationsPerTenant},
		{"above the maximum is clamped to it", i32(MaxIntegrationsPerTenant + 1), MaxIntegrationsPerTenant},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveIntegrationsCeiling(c.in); got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
	if DefaultIntegrationsPerTenant != 5 || MaxIntegrationsPerTenant != 100 {
		t.Errorf("default and maximum are 5 and 100, got %d and %d", DefaultIntegrationsPerTenant, MaxIntegrationsPerTenant)
	}
}

// TestIntegrationsCeilingQueryAndResponseAgree builds the wire body from the field constant
// and decodes it through the response type, so a json tag that drifted from the constant
// decodes to nil here (and would have served every tenant the default).
func TestIntegrationsCeilingQueryAndResponseAgree(t *testing.T) {
	if !strings.Contains(integrationsCeilingQuery, "tenantGovernance { "+IntegrationsPerTenantField+" }") {
		t.Fatalf("the query does not select %s off tenantGovernance: %q", IntegrationsPerTenantField, integrationsCeilingQuery)
	}
	var out integrationsCeilingResponse
	body := `{"tenantGovernance":{"` + IntegrationsPerTenantField + `":12}}`
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.TenantGovernance.IntegrationsPerTenant == nil || *out.TenantGovernance.IntegrationsPerTenant != 12 {
		t.Fatalf("decoded %v from %s; the json tag does not match the field", out.TenantGovernance.IntegrationsPerTenant, body)
	}
}

func TestIntegrationsCeilingResolverCarriesTheError(t *testing.T) {
	boom := errors.New("user-management unreachable")
	r := &IntegrationsCeilingResolver{fetch: func(context.Context, string) (int, error) { return 0, boom }}
	if _, err := r.Resolve(context.Background(), "acme"); !errors.Is(err, boom) {
		t.Fatalf("a failed fetch must reach the caller, got %v", err)
	}
	if _, err := r.Resolve(context.Background(), ""); err == nil {
		t.Fatal("an empty tenant must be refused")
	}
}
