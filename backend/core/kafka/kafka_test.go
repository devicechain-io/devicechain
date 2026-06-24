// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package kafka

import "testing"

func TestParseTenantFromSubject(t *testing.T) {
	cases := []struct {
		name       string
		subject    string
		wantTenant string
		wantOk     bool
	}{
		{"valid", "instance1.tenantA.events", "tenantA", true},
		{"valid with extra dots in suffix", "inst.tenant.events.inbound", "tenant", true},
		{"too few segments", "instance1.tenantA", "", false},
		{"single segment", "events", "", false},
		{"empty instance", ".tenantA.events", "", false},
		{"empty tenant", "instance1..events", "", false},
		{"empty suffix", "instance1.tenantA.", "", false},
		{"empty string", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tenant, ok := ParseTenantFromSubject(tc.subject)
			if ok != tc.wantOk || tenant != tc.wantTenant {
				t.Fatalf("ParseTenantFromSubject(%q) = (%q, %v), want (%q, %v)",
					tc.subject, tenant, ok, tc.wantTenant, tc.wantOk)
			}
		})
	}
}
