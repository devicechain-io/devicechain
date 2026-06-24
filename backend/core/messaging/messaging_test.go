// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import "testing"

func TestParseTenantFromSubject(t *testing.T) {
	cases := []struct {
		subject string
		tenant  string
		ok      bool
	}{
		{"instance1.tenant1.inbound-events", "tenant1", true},
		{"dc.acme.devices.token.events", "acme", true}, // ADR-006 MQTT mapping
		{"instance1.tenant1.a.b.c", "tenant1", true},
		{"instance1..inbound-events", "", false}, // empty tenant
		{"instance1.tenant1", "", false},         // too few segments
		{"instance1", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		tenant, ok := ParseTenantFromSubject(tc.subject)
		if ok != tc.ok || tenant != tc.tenant {
			t.Errorf("ParseTenantFromSubject(%q) = (%q, %v); want (%q, %v)",
				tc.subject, tenant, ok, tc.tenant, tc.ok)
		}
	}
}

func TestSubjectHelpers(t *testing.T) {
	if got := ScopedSubject("inst", "ten", "inbound-events"); got != "inst.ten.inbound-events" {
		t.Errorf("ScopedSubject = %q", got)
	}
	if got := WildcardSubject("inst", "inbound-events"); got != "inst.*.inbound-events" {
		t.Errorf("WildcardSubject = %q", got)
	}
	// Names must not contain JetStream-illegal characters.
	if got := StreamName("inst.x", "inbound-events"); got != "inst_x_inbound-events" {
		t.Errorf("StreamName = %q", got)
	}
	if got := DurableName("inst", "device-management", "resolved-events"); got != "inst_device-management_resolved-events" {
		t.Errorf("DurableName = %q", got)
	}
}
