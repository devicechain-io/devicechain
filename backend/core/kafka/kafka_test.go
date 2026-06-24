/**
 * Copyright © 2022 DeviceChain
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

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
